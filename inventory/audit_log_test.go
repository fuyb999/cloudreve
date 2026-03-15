package inventory

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseAuditLogTypeList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    []int
		wantErr bool
	}{
		{name: "empty", raw: "", want: nil},
		{name: "single", raw: "5", want: []int{5}},
		{name: "multiple", raw: "1,2,3", want: []int{1, 2, 3}},
		{name: "invalid", raw: "1,x", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseAuditLogTypeList(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("unexpected result: got %v want %v", got, tt.want)
			}
		})
	}
}

func TestDefaultAuditLogEnabledTypes(t *testing.T) {
	t.Parallel()

	var enabled []int
	if err := json.Unmarshal([]byte(defaultAuditLogEnabledTypes), &enabled); err != nil {
		t.Fatalf("failed to unmarshal defaults: %v", err)
	}

	if len(enabled) == 0 {
		t.Fatal("expected enabled audit log defaults")
	}
}
