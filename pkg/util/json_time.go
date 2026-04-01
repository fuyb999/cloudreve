package util

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	DateTimeSecondFormat = "yyyy-MM-dd HH:mm:ss"
	DateTimeSecondLayout = "2006-01-02 15:04:05"
)

// DateTimeSecond serializes/deserializes time values using second precision.
type DateTimeSecond time.Time

func NewDateTimeSecond(t time.Time) DateTimeSecond {
	return DateTimeSecond(t)
}

func (t DateTimeSecond) MarshalJSON() ([]byte, error) {
	tt := time.Time(t)
	if tt.IsZero() {
		return []byte("null"), nil
	}

	return json.Marshal(tt.In(time.Local).Format(DateTimeSecondLayout))
}

func (t *DateTimeSecond) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*t = DateTimeSecond(time.Time{})
		return nil
	}

	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	raw = strings.TrimSpace(raw)
	if raw == "" {
		*t = DateTimeSecond(time.Time{})
		return nil
	}

	parsed, err := time.ParseInLocation(DateTimeSecondLayout, raw, time.Local)
	if err != nil {
		return fmt.Errorf("failed to parse second-precision datetime %q: %w", raw, err)
	}

	*t = DateTimeSecond(parsed)
	return nil
}

func (t DateTimeSecond) IsZero() bool {
	return time.Time(t).IsZero()
}

func (t DateTimeSecond) Time() time.Time {
	return time.Time(t)
}
