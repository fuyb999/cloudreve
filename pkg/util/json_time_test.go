package util

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDateTimeSecondMarshalJSON(t *testing.T) {
	raw, err := json.Marshal(NewDateTimeSecond(time.Date(2026, 4, 1, 20, 15, 16, 999, time.Local)))
	if err != nil {
		t.Fatalf("failed to marshal DateTimeSecond: %v", err)
	}

	if got, want := string(raw), `"2026-04-01 20:15:16"`; got != want {
		t.Fatalf("unexpected marshaled datetime: got %s want %s", got, want)
	}
}

func TestDateTimeSecondUnmarshalJSON(t *testing.T) {
	var value DateTimeSecond
	if err := json.Unmarshal([]byte(`"2026-04-01 20:15:16"`), &value); err != nil {
		t.Fatalf("failed to unmarshal DateTimeSecond: %v", err)
	}

	want := time.Date(2026, 4, 1, 20, 15, 16, 0, time.Local)
	if got := value.Time(); !got.Equal(want) {
		t.Fatalf("unexpected unmarshaled datetime: got %s want %s", got, want)
	}
}

func TestDateTimeSecondNullJSON(t *testing.T) {
	var value DateTimeSecond
	if err := json.Unmarshal([]byte(`null`), &value); err != nil {
		t.Fatalf("failed to unmarshal null DateTimeSecond: %v", err)
	}

	if !value.IsZero() {
		t.Fatal("expected null to unmarshal to zero DateTimeSecond")
	}
}
