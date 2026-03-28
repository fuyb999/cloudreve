package publicshare

import (
	"context"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	entuser "github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

func TestCreateRootFolderUsesRequesterOwnerInsteadOfHiddenRootOwner(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:publicshare-create-root-folder-owner?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	logger := logging.NewConsoleLogger(logging.LevelError)
	if _, err := inventory.InitializeDBClient(
		logger,
		client,
		cache.NewMemoStore("", logger),
		"0.0.1",
		conf.SQLiteDB,
	); err != nil {
		t.Fatalf("failed to initialize db client: %v", err)
	}

	requester, err := client.User.Create().
		SetUsername("public-owner").
		SetEmail("public-owner@example.com").
		SetNick("public-owner").
		SetStatus(entuser.StatusActive).
		SetGroupID(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create requester: %v", err)
	}

	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hashid encoder: %v", err)
	}

	service := NewService(
		logging.NewConsoleLogger(logging.LevelError),
		inventory.NewFileClient(client, conf.SQLiteDB, nil),
		inventory.NewSettingClient(client, nil),
		hasher,
	)

	folder, err := service.CreateRootFolder(ctx, requester, "研发文档", &Rule{})
	if err != nil {
		t.Fatalf("failed to create public root folder: %v", err)
	}

	root, err := service.Root(ctx)
	if err != nil {
		t.Fatalf("failed to reload hidden public root: %v", err)
	}

	if root.OwnerID == requester.ID {
		t.Fatalf("hidden public root owner should remain system-owned, got requester %d", requester.ID)
	}
	if folder.OwnerID != requester.ID {
		t.Fatalf("public root folder should use requester owner: got %d want %d", folder.OwnerID, requester.ID)
	}
	if folder.FileChildren != root.ID {
		t.Fatalf("public root folder parent mismatch: got %d want %d", folder.FileChildren, root.ID)
	}
}
