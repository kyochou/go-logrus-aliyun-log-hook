package slsh

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/pierrec/lz4"

	"github.com/kyochou/go-logrus-aliyun-log-hook/api"
)

var (
	hContentType     = []string{"application/x-protobuf"}
	hApiVersion      = []string{"0.6.0"}
	hCompressType    = []string{"lz4"}
	hSignatureMethod = []string{"hmac-sha1"}
)

var loc = time.FixedZone("GMT", 0)

func gmtNow() string { return time.Now().In(loc).Format(time.RFC1123) }

type writer struct {
	client    *http.Client
	method    string
	appKey    string
	appSecret Secret
	uri       *url.URL
	hHost     []string
	topic     string
	source    string
}

func NewWriter(uri *url.URL, topic, source, accessKey string, accessSecret Secret, client *http.Client) *writer {
	return &writer{
		client:    client,
		method:    "POST",
		uri:       uri,
		hHost:     []string{uri.Host},
		topic:     topic,
		source:    source,
		appKey:    accessKey,
		appSecret: accessSecret,
	}
}

func (w *writer) WriteMessage(messages ...Message) error {
	if len(messages) == 0 {
		return nil
	}

	raw, err := w.encode(messages...)
	if err != nil {
		return err
	}

	data, err := w.compress(raw)
	if err != nil {
		return err
	}

	req, err := w.buildRequest(raw, data)
	if err != nil {
		return err
	}

	return w.fire(req)
}

func (w *writer) encode(messages ...Message) ([]byte, error) {
	group := &api.LogGroup{
		Topic:  &w.topic,
		Source: &w.source,
		Logs:   make([]*api.Log, len(messages)),
	}

	for i, message := range messages {
		contents := make([]*api.Log_Content, 0, len(message.Contents))
		for k, v := range message.Contents {
			contents = append(contents, &api.Log_Content{
				Key:   proto.String(k),
				Value: proto.String(v),
			})
		}
		group.Logs[i] = &api.Log{
			Time:     proto.Uint32(uint32(message.Time.Unix())),
			Contents: contents,
		}
	}
	return proto.Marshal(group)
}

func (w *writer) compress(data []byte) ([]byte, error) {
	out := make([]byte, lz4.CompressBlockBound(len(data)))
	var hashTable [1 << 16]int
	n, err := lz4.CompressBlock(data, out, hashTable[:])
	if err != nil {
		return nil, err
	}
	if n == 0 {
		if n, err = copyIncompressible(data, out); err != nil {
			return nil, err
		}
	}
	return out[:n], nil
}

func (w *writer) buildRequest(raw, data []byte) (*http.Request, error) {
	req, err := http.NewRequest(w.method, w.uri.String(), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	req.Header = http.Header{
		"Content-Type":          hContentType,
		"Content-Length":        []string{strconv.Itoa(len(data))},
		"Content-Md5":           []string{fmt.Sprintf("%X", md5.Sum(data))},
		"Date":                  []string{gmtNow()},
		"Host":                  w.hHost,
		"X-Log-Apiversion":      hApiVersion,
		"X-Log-Bodyrawsize":     []string{strconv.Itoa(len(raw))},
		"X-Log-Compresstype":    hCompressType,
		"X-Log-Signaturemethod": hSignatureMethod,
	}

	sign, err := signature(w.appSecret, req)
	if err != nil {
		return nil, err
	}

	req.Header["Authorization"] = []string{fmt.Sprintf("LOG %s:%s", w.appKey, sign)}
	return req, nil
}

func (w *writer) fire(req *http.Request) error {
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return w.validateResponse(resp)
}

func (w writer) validateResponse(resp *http.Response) error {
	if resp.StatusCode < http.StatusBadRequest {
		return nil
	}
	aErr := AliyunError{
		HTTPCode:  int32(resp.StatusCode),
		RequestID: resp.Header.Get("X-Log-Requestid"),
	}
	if err := json.NewDecoder(resp.Body).Decode(&aErr); err != nil {
		return err
	}
	return &aErr
}

func signature(secret Secret, req *http.Request) (string, error) {
	arr := make([]string, 0, 10)
	arr = append(arr,
		req.Method,
		req.Header.Get("Content-MD5"),
		req.Header.Get("Content-Type"),
		req.Header.Get("Date"),
	)

	// Calc CanonicalizedSLSHeaders
	sections := make([]string, 0, 4)
	for k, v := range req.Header {
		if len(v) > 0 && (strings.HasPrefix(k, "X-Log-") || strings.HasPrefix(k, "X-Acs-")) {
			str := fmt.Sprintf("%s:%s", strings.ToLower(k), strings.TrimSpace(strings.Join(v, ",")))
			sections = append(sections, str)
		}
	}
	sort.Strings(sections)
	arr = append(arr, sections...)

	// Calc CanonicalizedResource
	canoResource := req.URL.EscapedPath()

	// TODO: Enable GET support if necessary
	// if req.URL.RawQuery != "" {
	//	values := req.URL.Query()
	//	queries := make([]string, 0, len(values))
	//	for k, v := range values {
	//		queries = append(queries, fmt.Sprintf("%s=%s", k, v))
	//	}
	//	sort.Strings(queries)
	//
	//	canoResource = fmt.Sprintf("%s?%s", canoResource, strings.Join(queries, "&"))
	// }

	arr = append(arr, canoResource)

	signStr := strings.Join(arr, "\n")

	// Signature = base64(hmac-sha1(UTF8-Encoding-Of(SignString)，AccessKeySecret))
	mac := hmac.New(sha1.New, secret)
	if _, err := mac.Write([]byte(signStr)); err != nil {
		return "", err
	}

	digest := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return digest, nil
}

func copyIncompressible(src, dst []byte) (int, error) {
	lLen, dn := len(src), len(dst)

	di := 0
	if lLen < 0xF {
		dst[di] = byte(lLen << 4)
	} else {
		dst[di] = 0xF0
		if di++; di == dn {
			return di, nil
		}
		lLen -= 0xF
		for ; lLen >= 0xFF; lLen -= 0xFF {
			dst[di] = 0xFF
			if di++; di == dn {
				return di, nil
			}
		}
		dst[di] = byte(lLen)
	}
	if di++; di+len(src) > dn {
		return di, nil
	}
	di += copy(dst[di:], src)
	return di, nil
}
