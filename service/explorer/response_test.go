package explorer

import (
	"context"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
)

func TestBuildFileResponseNormalizesHistoricalPublicTopLevelName(t *testing.T) {
	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hasher: %v", err)
	}

	fileID := 24
	dirtyName := "omx-tika-pubtxt-20260501-142102.txt__" + hashid.EncodeFileID(hasher, fileID)
	publicURI, err := fs.NewUriFromString("cloudreve://public/" + dirtyName)
	if err != nil {
		t.Fatalf("failed to build uri: %v", err)
	}

	f := &dbfs.File{
		Model: &ent.File{
			ID:        fileID,
			Name:      dirtyName,
			Type:      int(types.FileTypeFile),
			OwnerID:   1,
			CreatedAt: time.Unix(1700000000, 0),
			UpdatedAt: time.Unix(1700000100, 0),
		},
		Path: [2]*fs.URI{publicURI, publicURI},
		OwnerModel: &ent.User{
			ID: 1,
		},
		CapabilitiesBs: &boolset.BooleanSet{},
	}

	resp := BuildFileResponse(context.Background(), &ent.User{ID: 1}, f, hasher, nil)
	if resp == nil {
		t.Fatal("expected response")
	}
	if got, want := resp.Name, "omx-tika-pubtxt-20260501-142102.txt"; got != want {
		t.Fatalf("unexpected normalized name: got %q want %q", got, want)
	}
	if got, want := resp.Path, constants.CloudreveScheme+"://public/omx-tika-pubtxt-20260501-142102.txt"; got != want {
		t.Fatalf("unexpected normalized path: got %q want %q", got, want)
	}
}
