package dbfs

import (
	"context"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
)

func TestOwnerForNewChildUsesCurrentUserForPublicTargets(t *testing.T) {
	group := &ent.Group{ID: 9}
	currentUser := &ent.User{
		ID: 88,
		Edges: ent.UserEdges{
			Group: group,
		},
	}
	publicURI, err := fs.NewUriFromString("cloudreve://public/team-alpha")
	if err != nil {
		t.Fatalf("failed to parse public uri: %v", err)
	}

	parent := &File{
		Model: &ent.File{
			ID:      12,
			Name:    "team-alpha",
			OwnerID: 7,
			Type:    int(types.FileTypeFolder),
		},
		Path: [2]*fs.URI{
			publicURI,
			publicURI,
		},
	}

	dbfs := &DBFS{user: currentUser}
	owner, err := dbfs.ownerForNewChild(context.Background(), parent)
	if err != nil {
		t.Fatalf("unexpected owner resolution error: %v", err)
	}
	if owner != currentUser {
		t.Fatalf("expected current user to own new public child, got %+v", owner)
	}
}

func TestStoragePolicyForOwnerUsesGroupCache(t *testing.T) {
	group := &ent.Group{ID: 5}
	policy := &ent.StoragePolicy{ID: 17}
	dbfs := &DBFS{
		groupPolicyCache: map[int]*ent.StoragePolicy{
			group.ID: policy,
		},
	}

	owner := &ent.User{
		ID: 3,
		Edges: ent.UserEdges{
			Group: group,
		},
	}

	got, err := dbfs.storagePolicyForOwner(context.Background(), owner)
	if err != nil {
		t.Fatalf("unexpected policy error: %v", err)
	}
	if got != policy {
		t.Fatalf("expected cached policy, got %+v", got)
	}
}
