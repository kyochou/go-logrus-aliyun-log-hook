package slsh

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sls "github.com/aliyun/aliyun-log-go-sdk"
	"github.com/golang/protobuf/proto"
	"github.com/stretchr/testify/assert"
)

// {"errorCode":"ParameterInvalid","errorMessage":"http extend authorization : LOG :WL2xp3EYvKpsIGgwE3s5HHK7M/c= pair is invalid"}

const (
	DefaultAccessKey    = "123"
	DefaultTopic        = "test-topic"
	DefaultSource       = "127.0.0.1"
	DefaultErrorMessage = `{"errorCode":"ParameterInvalid","errorMessage":"http extend authorization : LOG :WL2xp3EYvKpsIGgwE3s5HHK7M/c= pair is invalid"}`
)

var (
	DefaultAccessSecret = Secret("321")
	ShortMessage        = Message{
		Time:     time.Date(2020, 1, 1, 0, 0, 0, 0, loc),
		Contents: map[string]string{"key": "value"},
	}
	LongMessage = Message{
		Time: time.Date(2020, 1, 1, 0, 0, 0, 0, loc),
		Contents: map[string]string{
			strings.Repeat("key1", 10): strings.Repeat("value2", 20),
			strings.Repeat("key2", 10): strings.Repeat("value2", 20),
		},
	}
	Messages = []Message{ShortMessage, LongMessage}
	Error    = &AliyunError{
		HTTPCode:  http.StatusUnauthorized,
		Code:      "ParameterInvalid",
		Message:   "http extend authorization : LOG :WL2xp3EYvKpsIGgwE3s5HHK7M/c= pair is invalid",
		RequestID: "123",
	}
)

func TestSecret(t *testing.T) {
	assert.Regexp(t, `\*+`, Secret("123").String())
}

func TestWriter(t *testing.T) {
	newNormalHandler := func(t *testing.T) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			hPrefix := "LOG " + DefaultAccessKey + ":"
			hAuth := req.Header.Get("Authorization")
			assert.True(t, strings.HasPrefix(hAuth, hPrefix))

			data, err := ioutil.ReadAll(req.Body)
			if assert.NoError(t, err) {
				assert.True(t, req.ContentLength > 0)
				assert.Equal(t, req.ContentLength, int64(len(data)))
			}
			w.WriteHeader(http.StatusOK)
		})
	}

	newErrorHandler := func(t *testing.T) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("X-Log-Requestid", Error.RequestID)
			w.WriteHeader(int(Error.HTTPCode))
			err := json.NewEncoder(w).Encode(Error)
			assert.NoError(t, err)
		})
	}

	newWriter := func(t *testing.T, uri string) *writer {
		u, err := url.Parse(uri)
		assert.NoError(t, err)
		return NewWriter(u, DefaultTopic, DefaultSource, DefaultAccessKey, DefaultAccessSecret)
	}

	t.Run("short message", func(t *testing.T) {
		srv := httptest.NewServer(newNormalHandler(t))
		defer srv.Close()

		writer := newWriter(t, srv.URL)

		err := writer.WriteMessage(ShortMessage)
		assert.NoError(t, err)
	})

	t.Run("long message", func(t *testing.T) {
		srv := httptest.NewServer(newNormalHandler(t))
		defer srv.Close()

		writer := newWriter(t, srv.URL)
		err := writer.WriteMessage(Messages...)
		assert.NoError(t, err)
	})

	t.Run("error message", func(t *testing.T) {
		srv := httptest.NewServer(newErrorHandler(t))
		defer srv.Close()

		writer := newWriter(t, srv.URL)
		err := writer.WriteMessage(ShortMessage)

		var aErr *AliyunError
		if assert.True(t, errors.As(err, &aErr)) {
			assert.Equal(t, Error, aErr)
			assert.JSONEq(t, DefaultErrorMessage, aErr.Error())
		}
	})
}

func TestSignature(t *testing.T) {
	uri := "http://test-project.regionid.example.com/logstores/test-logstore"
	req, err := http.NewRequest("POST", uri, nil)
	if !assert.NoError(t, err) {
		return
	}

	req.Header = http.Header{
		"Date":                  []string{"Mon, 09 Nov 2015 06:03:03 GMT"},
		"Host":                  []string{"test-project.regionid.example.com"},
		"X-Log-Apiversion":      []string{"0.6.0"},
		"X-Log-Signaturemethod": []string{"hmac-sha1"},
		"Content-Md5":           []string{"1DD45FA4A70A9300CC9FE7305AF2C494"},
		"Content-Length":        []string{"52"},
		"X-Log-Bodyrawsize":     []string{"50"},
		"X-Log-Compresstype":    []string{"lz4"},
	}

	sig, err := signature(Secret("321"), req)
	if assert.NoError(t, err) {
		assert.Equal(t, "v/969+iSsYwGFtAXAy1xaK9rNDI=", sig)
	}
}

func BenchmarkWriter(b *testing.B) {
	startServer := func(b *testing.B) *httptest.Server {
		handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			b.StopTimer()
			defer b.StartTimer()

			switch req.Method {
			case "GET":
				if strings.Contains(req.URL.Path, "logstores") {
					_, _ = w.Write([]byte(`{"logstoreName":""}`))
				} else {
					_, _ = w.Write([]byte(`{"projectName":"", "region":""}`))
				}
			case "POST":
				w.WriteHeader(http.StatusOK)
			}
		})
		return httptest.NewServer(handler)
	}

	b.Run("hook", func(b *testing.B) {
		srv := startServer(b)
		defer srv.Close()

		msg := Message{
			Time: time.Now(),
			Contents: map[string]string{
				"aaaaaaaaaaaa": "bbbbbbbbbbbb",
				"cccccccccccc": "dddddddddddd",
			},
		}

		uri, _ := url.Parse(srv.URL)
		writer := NewWriter(uri, "any", "any", "any", Secret("any"))

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := writer.WriteMessage(msg); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("sls", func(b *testing.B) {
		srv := startServer(b)
		defer srv.Close()

		group := &sls.LogGroup{
			Source: proto.String("any"),
			Topic:  proto.String("any"),
			Logs: []*sls.Log{{
				Time: proto.Uint32(uint32(time.Now().Unix())),
				Contents: []*sls.LogContent{{
					Key:   proto.String("aaaaaaaaaaaa"),
					Value: proto.String("bbbbbbbbbbbb"),
				}, {
					Key:   proto.String("cccccccccccc"),
					Value: proto.String("dddddddddddd"),
				}},
			}},
		}

		client := &sls.Client{
			Endpoint:        srv.URL,
			AccessKeyID:     "any",
			AccessKeySecret: "any",
		}
		defer func() { _ = client.Close() }()

		project, err := client.GetProject("any")
		if err != nil {
			b.Fatal(err)
		}

		store, err := project.GetLogStore("any")
		if err != nil {
			b.Fatal(err)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := store.PutLogs(group); err != nil {
				b.Fatal(err)
			}
		}
	})
}
