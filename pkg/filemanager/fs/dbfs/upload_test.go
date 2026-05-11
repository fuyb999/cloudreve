package dbfs

import (
	"math"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
)

func TestUploadMaxVersionsDefaultsToOneWhenOwnerMissing(t *testing.T) {
	if got := uploadMaxVersions(nil, types.EntityTypeVersion, "demo.txt"); got != 1 {
		t.Fatalf("unexpected max versions: %d", got)
	}
}

func TestUploadMaxVersionsDefaultsToOneWhenSettingsMissing(t *testing.T) {
	if got := uploadMaxVersions(&ent.User{}, types.EntityTypeVersion, "demo.txt"); got != 1 {
		t.Fatalf("unexpected max versions: %d", got)
	}
}

func TestUploadMaxVersionsAppliesRetentionPolicy(t *testing.T) {
	owner := &ent.User{
		Settings: &types.UserSetting{
			VersionRetention:    true,
			VersionRetentionExt: []string{"txt"},
			VersionRetentionMax: 3,
		},
	}

	if got := uploadMaxVersions(owner, types.EntityTypeVersion, "demo.txt"); got != 3 {
		t.Fatalf("unexpected max versions: %d", got)
	}
}

func TestUploadMaxVersionsTreatsZeroAsUnlimited(t *testing.T) {
	owner := &ent.User{
		Settings: &types.UserSetting{
			VersionRetention:    true,
			VersionRetentionMax: 0,
		},
	}

	if got := uploadMaxVersions(owner, types.EntityTypeVersion, "demo.txt"); got != math.MaxInt32 {
		t.Fatalf("unexpected max versions: %d", got)
	}
}

func TestUploadMaxVersionsSkipsUnmatchedExtension(t *testing.T) {
	owner := &ent.User{
		Settings: &types.UserSetting{
			VersionRetention:    true,
			VersionRetentionExt: []string{"md"},
			VersionRetentionMax: 9,
		},
	}

	if got := uploadMaxVersions(owner, types.EntityTypeVersion, "demo.txt"); got != 1 {
		t.Fatalf("unexpected max versions: %d", got)
	}
}

func TestPreValidateUploadAllowsPublicTargetForCurrentUser(t *testing.T) {
	targetURI, err := fs.NewUriFromString("cloudreve://public/team-alpha")
	if err != nil {
		t.Fatalf("failed to parse public uri: %v", err)
	}

	target := &File{
		Model: &ent.File{
			ID:      1,
			Name:    "team-alpha",
			OwnerID: 7,
			Type:    int(types.FileTypeFolder),
		},
		Path: [2]*fs.URI{targetURI, targetURI},
	}

	if !isPublicFileMutationTarget(target) {
		t.Fatal("expected public target helper to report true")
	}
}
