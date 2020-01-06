package slsh

import (
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func TestConverter(t *testing.T) {
	c := NewConverter("m", "l",
		func(level logrus.Level) int { return int(level) },
		map[string]string{"e1": "v1", "e2": "v2"})

	entry := &logrus.Entry{
		Data:    logrus.Fields{"f1": "v1", "f2": 1, "f3": 2.0, "f4": true, "f5": errors.New("e")},
		Time:    time.Now(),
		Level:   logrus.InfoLevel,
		Message: "content",
	}

	msg := c.Message(entry)
	assert.Equal(t, entry.Time, msg.Time)
	assert.Equal(t, entry.Data["f1"], msg.Contents["f1"])
	assert.Equal(t, fmt.Sprintf("%v", entry.Data["f2"]), msg.Contents["f2"])
	assert.Equal(t, fmt.Sprintf("%f", entry.Data["f3"]), msg.Contents["f3"])
	assert.Equal(t, fmt.Sprintf("%v", entry.Data["f4"]), msg.Contents["f4"])
	assert.Equal(t, fmt.Sprintf("%v", entry.Data["f5"]), msg.Contents["f5"])
	assert.Equal(t, c.Extra["e1"], msg.Contents["e1"])
	assert.Equal(t, c.Extra["e2"], msg.Contents["e2"])
	assert.Equal(t, entry.Message, msg.Contents[c.MessageKey])
	assert.Equal(t, strconv.Itoa(int(entry.Level)), msg.Contents[c.LevelKey])
}
