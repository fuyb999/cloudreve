package publicsvc

import (
	"testing"

	acl "github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
)

func TestBuildVisibleRootURIUsesProjectedAliasForNonRootGrant(t *testing.T) {
	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hasher: %v", err)
	}

	grant := acl.RootGrant{
		RootFileID:   20,
		RootOwnerID:  9,
		RootName:     "部门空间",
		RootTreePath: "1.9.20",
	}

	got := buildVisibleRootURI(hasher, 9, grant)
	want := acl.BuildPublicURI().Join(acl.ProjectedRootAlias(hasher, grant)).String()
	if got != want {
		t.Fatalf("unexpected projected public uri: got %q want %q", got, want)
	}
}

func TestBuildVisibleRootURIReturnsPublicRootForRealRootGrant(t *testing.T) {
	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hasher: %v", err)
	}

	grant := acl.RootGrant{
		RootFileID:   9,
		RootOwnerID:  -1,
		RootName:     acl.DefaultRootName,
		RootTreePath: "1.9",
	}

	got := buildVisibleRootURI(hasher, 9, grant)
	want := acl.BuildPublicURI().String()
	if got != want {
		t.Fatalf("unexpected root public uri: got %q want %q", got, want)
	}
}
