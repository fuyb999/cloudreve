package admin

import (
	"testing"

	inventorytypes "github.com/cloudreve/Cloudreve/v4/inventory/types"
)

func TestParseNodeCapabilityCondition(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want *inventorytypes.NodeCapability
	}{
		{
			name: "content processing alias",
			raw:  contentProcessingFilterValue,
			want: capabilityPtr(inventorytypes.NodeCapabilityContentProcessing),
		},
		{
			name: "numeric fallback",
			raw:  "3",
			want: capabilityPtr(inventorytypes.NodeCapabilityRemoteDownload),
		},
		{
			name: "unknown capability",
			raw:  "unknown",
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseNodeCapabilityCondition(tc.raw)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("expected nil capability, got %v", *got)
				}
				return
			}

			if got == nil || *got != *tc.want {
				t.Fatalf("unexpected capability parse result: got %v want %v", got, tc.want)
			}
		})
	}
}

func capabilityPtr(capability inventorytypes.NodeCapability) *inventorytypes.NodeCapability {
	return &capability
}
