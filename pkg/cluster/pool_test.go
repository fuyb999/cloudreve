package cluster

import (
	"testing"

	"github.com/cloudreve/Cloudreve/v4/inventory/types"
)

func TestSupportedCapabilitiesIncludesContentProcessing(t *testing.T) {
	found := false
	for _, capability := range supportedCapabilities {
		if capability == types.NodeCapabilityContentProcessing {
			found = true
			break
		}
	}

	if !found {
		t.Fatal("expected content processing capability to be registered in node pool")
	}
}
