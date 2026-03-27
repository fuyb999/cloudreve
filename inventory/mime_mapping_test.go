package inventory

import (
	"encoding/json"
	"testing"
)

func TestDefaultMimeMappingIsValidAndContainsUpdatedTextTypes(t *testing.T) {
	mapping := make(map[string]string)
	if err := json.Unmarshal([]byte(defaultMimeMapping), &mapping); err != nil {
		t.Fatalf("unexpected mime mapping json error: %v", err)
	}

	cases := map[string]string{
		".rar":  "application/x-rar-compressed",
		".7z":   "application/x-7z-compressed",
		".js":   "application/javascript",
		".ts":   "text/plain",
		".tsx":  "text/plain",
		".yaml": "text/plain",
		".vue":  "text/plain",
		".md":   "text/markdown",
		".csv":  "text/csv",
	}

	for ext, want := range cases {
		if got := mapping[ext]; got != want {
			t.Fatalf("%s: unexpected default mime mapping: got %q want %q", ext, got, want)
		}
	}
}
