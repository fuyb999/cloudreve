package dbfs

import (
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
)

func TestAllowCrossOwnerPublicFolderSummary(t *testing.T) {
	t.Run("public", func(t *testing.T) {
		uri, err := fs.NewUriFromString("cloudreve://public/team")
		if err != nil {
			t.Fatalf("failed to parse public uri: %v", err)
		}

		target := newFile(nil, &ent.File{ID: 1, Name: "team", Type: int(types.FileTypeFolder), OwnerID: -1})
		target.Path[pathIndexUser] = uri

		if !allowCrossOwnerPublicFolderSummary(target) {
			t.Fatalf("expected public folder summary to allow cross-owner access")
		}
	})

	t.Run("non-public", func(t *testing.T) {
		uri, err := fs.NewUriFromString("cloudreve://my/team")
		if err != nil {
			t.Fatalf("failed to parse my uri: %v", err)
		}

		target := newFile(nil, &ent.File{ID: 2, Name: "team", Type: int(types.FileTypeFolder), OwnerID: 99})
		target.Path[pathIndexUser] = uri

		if allowCrossOwnerPublicFolderSummary(target) {
			t.Fatalf("non-public folder summary should still require owner access")
		}
	})
}
