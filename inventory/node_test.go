package inventory

import (
	"context"
	"fmt"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/node"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
)

func TestListNodesFiltersByCapability(t *testing.T) {
	client := newTestNodeClient(t, "filter")
	ctx := context.Background()

	createTestNode(t, client, "content-active", node.StatusActive, capabilitiesWith(types.NodeCapabilityContentProcessing))
	createTestNode(t, client, "remote-active", node.StatusActive, capabilitiesWith(types.NodeCapabilityRemoteDownload))
	createTestNode(t, client, "content-suspended", node.StatusSuspended, capabilitiesWith(types.NodeCapabilityContentProcessing))

	capability := types.NodeCapabilityContentProcessing
	result, err := NewNodeClient(client).ListNodes(ctx, &ListNodeParameters{
		PaginationArgs: &PaginationArgs{
			Page:     0,
			PageSize: 10,
			OrderBy:  node.FieldID,
			Order:    OrderDirectionAsc,
		},
		Status:     node.StatusActive,
		Capability: &capability,
	})
	if err != nil {
		t.Fatalf("failed to list nodes: %v", err)
	}

	if result.TotalItems != 1 {
		t.Fatalf("unexpected total items: got %d want 1", result.TotalItems)
	}
	if len(result.Nodes) != 1 || result.Nodes[0].Name != "content-active" {
		t.Fatalf("unexpected nodes returned: %+v", result.Nodes)
	}
}

func TestListNodesFiltersByCapabilityBeforePagination(t *testing.T) {
	client := newTestNodeClient(t, "paginate")
	ctx := context.Background()

	for _, name := range []string{"content-1", "content-2", "remote-1", "content-3"} {
		capability := capabilitiesWith(types.NodeCapabilityContentProcessing)
		if name == "remote-1" {
			capability = capabilitiesWith(types.NodeCapabilityRemoteDownload)
		}
		createTestNode(t, client, name, node.StatusActive, capability)
	}

	filter := types.NodeCapabilityContentProcessing
	firstPage, err := NewNodeClient(client).ListNodes(ctx, &ListNodeParameters{
		PaginationArgs: &PaginationArgs{
			Page:     0,
			PageSize: 2,
			OrderBy:  node.FieldID,
			Order:    OrderDirectionAsc,
		},
		Capability: &filter,
	})
	if err != nil {
		t.Fatalf("failed to list first page: %v", err)
	}

	secondPage, err := NewNodeClient(client).ListNodes(ctx, &ListNodeParameters{
		PaginationArgs: &PaginationArgs{
			Page:     1,
			PageSize: 2,
			OrderBy:  node.FieldID,
			Order:    OrderDirectionAsc,
		},
		Capability: &filter,
	})
	if err != nil {
		t.Fatalf("failed to list second page: %v", err)
	}

	if firstPage.TotalItems != 3 || secondPage.TotalItems != 3 {
		t.Fatalf("unexpected total items: first=%d second=%d", firstPage.TotalItems, secondPage.TotalItems)
	}
	if len(firstPage.Nodes) != 2 || firstPage.Nodes[0].Name != "content-1" || firstPage.Nodes[1].Name != "content-2" {
		t.Fatalf("unexpected first page nodes: %+v", firstPage.Nodes)
	}
	if len(secondPage.Nodes) != 1 || secondPage.Nodes[0].Name != "content-3" {
		t.Fatalf("unexpected second page nodes: %+v", secondPage.Nodes)
	}
}

func newTestNodeClient(t *testing.T, suffix string) *ent.Client {
	t.Helper()

	client, err := ent.Open("sqlite3", fmt.Sprintf("file:test-node-%s?mode=memory&cache=shared&_fk=1", suffix))
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}

	if err := client.Schema.Create(context.Background()); err != nil {
		client.Close()
		t.Fatalf("failed to create schema: %v", err)
	}

	t.Cleanup(func() {
		client.Close()
	})

	return client
}

func createTestNode(t *testing.T, client *ent.Client, name string, status node.Status, capabilities *boolset.BooleanSet) {
	t.Helper()

	_, err := client.Node.Create().
		SetName(name).
		SetStatus(status).
		SetType(node.TypeSlave).
		SetCapabilities(capabilities).
		Save(context.Background())
	if err != nil {
		t.Fatalf("failed to create node %q: %v", name, err)
	}
}

func capabilitiesWith(capability types.NodeCapability) *boolset.BooleanSet {
	bs := &boolset.BooleanSet{}
	boolset.Set(capability, true, bs)
	return bs
}
