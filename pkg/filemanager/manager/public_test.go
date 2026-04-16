package manager

import (
	"context"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
)

func TestWithUploadSessionPublicVisibilityRestoresOverride(t *testing.T) {
	override := &publicshare.VisibilityResult{
		RootGrants: []publicshare.RootGrant{
			{
				RootFileID:   20,
				RootOwnerID:  7,
				RootTreePath: "10.20",
				Actions: map[publicshare.Action]bool{
					publicshare.ActionList:   true,
					publicshare.ActionUpload: true,
				},
			},
		},
	}
	session := &fs.UploadSession{
		PublicVisibility: publicshare.EncodeVisibilityOverride(override),
	}

	ctx := withUploadSessionPublicVisibility(context.Background(), session)
	restored := publicshare.VisibilityOverrideFromContext(ctx)
	if restored == nil {
		t.Fatal("expected visibility override to be restored from upload session")
	}
	if len(restored.RootGrants) != 1 || restored.RootGrants[0].RootFileID != 20 {
		t.Fatalf("unexpected restored visibility: %+v", restored.RootGrants)
	}
}
