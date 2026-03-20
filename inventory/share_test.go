package inventory

import (
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
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
