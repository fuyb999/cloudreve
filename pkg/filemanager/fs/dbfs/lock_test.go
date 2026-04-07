package dbfs

import (
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
)

func TestDisplayOnlyPublicRootNameAlias(t *testing.T) {
	uri, err := fs.NewUriFromString("cloudreve://public")
	if err != nil {
		t.Fatalf("failed to parse public uri: %v", err)
	}

	cached := &File{
		Model: &ent.File{
			ID:   1,
			Name: publicshare.DefaultRootName,
			Type: int(types.FileTypeFolder),
		},
		Path: [2]*fs.URI{uri, uri},
	}

	latest := &ent.File{
		ID:           1,
		Name:         inventory.RootFolderName,
		Type:         int(types.FileTypeFolder),
		FileChildren: 0,
	}

	if !isDisplayOnlyPublicRootNameAlias(cached, latest) {
		t.Fatalf("expected hidden public root display alias to be treated as consistent")
	}
}

func TestDisplayOnlyPublicRootNameAliasRejectsRegularFolders(t *testing.T) {
	uri, err := fs.NewUriFromString("cloudreve://public/team")
	if err != nil {
		t.Fatalf("failed to parse public team uri: %v", err)
	}

	cached := &File{
		Model: &ent.File{
			ID:   2,
			Name: "team",
			Type: int(types.FileTypeFolder),
		},
		Path: [2]*fs.URI{uri, uri},
	}

	latest := &ent.File{
		ID:           2,
		Name:         inventory.RootFolderName,
		Type:         int(types.FileTypeFolder),
		FileChildren: 0,
	}

	if isDisplayOnlyPublicRootNameAlias(cached, latest) {
		t.Fatalf("regular public folders should not bypass consistency check")
	}
}
