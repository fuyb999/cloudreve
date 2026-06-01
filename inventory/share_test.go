package inventory

import (
	"context"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
)

func markShareEdgesLoaded(share *ent.Share) {
	edgesValue := reflect.ValueOf(&share.Edges).Elem()
	loadedTypes := edgesValue.FieldByName("loadedTypes")
	reflect.NewAt(loadedTypes.Type(), unsafe.Pointer(loadedTypes.UnsafeAddr())).Elem().Set(reflect.ValueOf([3]bool{true, true, false}))
}

func TestIsValidShare_AllowsPublicSourceOwnerMismatch(t *testing.T) {
	share := &ent.Share{
		Expires: nil,
		Props: &types.ShareProps{
			PublicSource: true,
		},
	}
	share.Edges.User = &ent.User{
		ID:     100,
		Status: user.StatusActive,
	}
	share.Edges.File = &ent.File{
		ID:           200,
		OwnerID:      999,
		FileChildren: 1,
	}
	markShareEdgesLoaded(share)

	if err := IsValidShare(share); err != nil {
		t.Fatalf("public source share should stay valid when source owner mismatches sharer: %v", err)
	}
}

func TestIsValidShare_RejectsRegularOwnerMismatch(t *testing.T) {
	share := &ent.Share{
		Expires: nil,
		Props:   &types.ShareProps{},
	}
	share.Edges.User = &ent.User{
		ID:     100,
		Status: user.StatusActive,
	}
	share.Edges.File = &ent.File{
		ID:           200,
		OwnerID:      999,
		FileChildren: 1,
	}
	markShareEdgesLoaded(share)

	if err := IsValidShare(share); err != ErrSourceFileInvalid {
		t.Fatalf("regular share with owner mismatch should be invalid, got: %v", err)
	}
}

func TestIsValidShare_RejectsExpiredShareFirst(t *testing.T) {
	expiredAt := time.Now().Add(-time.Minute)
	share := &ent.Share{
		Expires: &expiredAt,
		Props: &types.ShareProps{
			PublicSource: true,
		},
	}
	share.Edges.User = &ent.User{
		ID:     100,
		Status: user.StatusActive,
	}
	share.Edges.File = &ent.File{
		ID:           200,
		OwnerID:      999,
		FileChildren: 1,
	}
	markShareEdgesLoaded(share)

	if err := IsValidShare(share); err != ErrShareLinkExpired {
		t.Fatalf("expired share should return expiration error first, got: %v", err)
	}
}

func TestShareClientUpsertClearsPasswordWhenExistingShareBecomesPublic(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:share-upsert-clear-password?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	group, err := client.Group.Create().
		SetName("User").
		SetPermissions(&boolset.BooleanSet{}).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create group: %v", err)
	}
	owner, err := client.User.Create().
		SetUsername("share-owner").
		SetEmail("share-owner@example.com").
		SetNick("share-owner").
		SetGroupID(group.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	file, err := client.File.Create().
		SetType(int(types.FileTypeFile)).
		SetName("share.txt").
		SetOwnerID(owner.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	shares := NewShareClient(client, conf.SQLiteDB, nil)
	privateShare, err := shares.Upsert(ctx, &CreateShareParams{
		OwnerID:  owner.ID,
		FileID:   file.ID,
		Password: "abc123",
	})
	if err != nil {
		t.Fatalf("failed to create private share: %v", err)
	}
	if privateShare.Password != "abc123" {
		t.Fatalf("private share password not stored: %q", privateShare.Password)
	}

	updated, err := shares.Upsert(ctx, &CreateShareParams{
		Existed: privateShare,
		Props:   &types.ShareProps{ShowReadMe: true},
	})
	if err != nil {
		t.Fatalf("failed to update share: %v", err)
	}
	if updated.Password != "" {
		t.Fatalf("expected public share update to clear password, got %q", updated.Password)
	}
}
