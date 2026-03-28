package inventory

import (
	"context"
	"strconv"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	entfile "github.com/cloudreve/Cloudreve/v4/ent/file"
	entsetting "github.com/cloudreve/Cloudreve/v4/ent/setting"
	entuser "github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

func TestEnsureSystemPublicRootSupportCreatesHiddenRoot(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:public-root-create?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}
	if err := migrateDefaultStoragePolicy(logging.NewConsoleLogger(logging.LevelError), client, ctx); err != nil {
		t.Fatalf("failed to migrate default storage policy: %v", err)
	}
	if err := migrateAdminGroup(logging.NewConsoleLogger(logging.LevelError), client, ctx); err != nil {
		t.Fatalf("failed to migrate admin group: %v", err)
	}

	if err := ensureSystemPublicRootSupport(ctx, logging.NewConsoleLogger(logging.LevelError), client, conf.SQLiteDB); err != nil {
		t.Fatalf("failed to ensure system public root: %v", err)
	}
	if err := ensureSystemPublicRootSupport(ctx, logging.NewConsoleLogger(logging.LevelError), client, conf.SQLiteDB); err != nil {
		t.Fatalf("failed to ensure system public root idempotently: %v", err)
	}

	settingModel, err := client.Setting.Query().Where(entsetting.Name(publicRootFileIDSettingKey)).Only(ctx)
	if err != nil {
		t.Fatalf("failed to query public root setting: %v", err)
	}

	rootID, err := strconv.Atoi(settingModel.Value)
	if err != nil || rootID <= 0 {
		t.Fatalf("unexpected public root setting value: %q (%v)", settingModel.Value, err)
	}

	systemOwner, err := client.User.Query().
		Where(entuser.Email(constants.PublicSystemOwnerEmail)).
		Only(ctx)
	if err != nil {
		t.Fatalf("failed to query public system owner: %v", err)
	}
	if systemOwner.Status != entuser.StatusInactive {
		t.Fatalf("unexpected public system owner status: %s", systemOwner.Status)
	}
	if systemOwner.ID != constants.PublicSystemOwnerID {
		t.Fatalf("unexpected public system owner id: got %d want %d", systemOwner.ID, constants.PublicSystemOwnerID)
	}
	if systemOwner.GroupUsers != 1 {
		t.Fatalf("unexpected public system owner group: %d", systemOwner.GroupUsers)
	}

	root, err := client.File.Query().Where(entfile.ID(rootID)).Only(ctx)
	if err != nil {
		t.Fatalf("failed to query hidden public root: %v", err)
	}
	if root.OwnerID != systemOwner.ID {
		t.Fatalf("unexpected hidden public root owner: got %d want %d", root.OwnerID, systemOwner.ID)
	}
	if root.Name != RootFolderName {
		t.Fatalf("unexpected hidden public root name: %q", root.Name)
	}
	if root.FileChildren != 0 {
		t.Fatalf("unexpected hidden public root parent id: %d", root.FileChildren)
	}
	if root.Type != int(types.FileTypeFolder) {
		t.Fatalf("unexpected hidden public root type: %d", root.Type)
	}

	count, err := client.File.Query().
		Where(
			entfile.OwnerIDEQ(systemOwner.ID),
			entfile.Not(entfile.HasParent()),
			entfile.Name(RootFolderName),
		).
		Count(ctx)
	if err != nil {
		t.Fatalf("failed to count hidden public roots: %v", err)
	}
	if count != 1 {
		t.Fatalf("unexpected hidden public root count: got %d want 1", count)
	}
}

func TestEnsureSystemPublicRootSupportMigratesLegacyConfiguredRoot(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:public-root-migrate?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}
	if err := migrateDefaultStoragePolicy(logging.NewConsoleLogger(logging.LevelError), client, ctx); err != nil {
		t.Fatalf("failed to migrate default storage policy: %v", err)
	}
	if err := migrateAdminGroup(logging.NewConsoleLogger(logging.LevelError), client, ctx); err != nil {
		t.Fatalf("failed to migrate admin group: %v", err)
	}

	legacyOwner, err := client.User.Create().
		SetUsername("legacy-owner").
		SetEmail("legacy-owner@example.com").
		SetNick("legacy-owner").
		SetStatus(entuser.StatusActive).
		SetGroupID(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create legacy owner: %v", err)
	}

	fileClient := NewFileClient(client, conf.SQLiteDB, nil)
	legacyOwnerRoot, err := fileClient.CreateFolder(ctx, nil, &CreateFolderParameters{
		Owner: legacyOwner.ID,
		Name:  RootFolderName,
	})
	if err != nil {
		t.Fatalf("failed to create legacy owner root: %v", err)
	}
	legacyPublicRoot, err := fileClient.CreateFolder(ctx, legacyOwnerRoot, &CreateFolderParameters{
		Owner: legacyOwner.ID,
		Name:  "公共文件",
	})
	if err != nil {
		t.Fatalf("failed to create legacy public root: %v", err)
	}

	if err := NewSettingClient(client, nil).Set(ctx, map[string]string{
		publicRootFileIDSettingKey: strconv.Itoa(legacyPublicRoot.ID),
	}); err != nil {
		t.Fatalf("failed to persist legacy public root setting: %v", err)
	}

	if err := ensureSystemPublicRootSupport(ctx, logging.NewConsoleLogger(logging.LevelError), client, conf.SQLiteDB); err != nil {
		t.Fatalf("failed to migrate legacy public root: %v", err)
	}

	systemOwner, err := client.User.Query().
		Where(entuser.Email(constants.PublicSystemOwnerEmail)).
		Only(ctx)
	if err != nil {
		t.Fatalf("failed to query public system owner: %v", err)
	}

	migratedRoot, err := client.File.Query().Where(entfile.ID(legacyPublicRoot.ID)).Only(ctx)
	if err != nil {
		t.Fatalf("failed to query migrated public root: %v", err)
	}
	if migratedRoot.OwnerID != systemOwner.ID {
		t.Fatalf("unexpected migrated public root owner: got %d want %d", migratedRoot.OwnerID, systemOwner.ID)
	}
	if migratedRoot.Name != RootFolderName {
		t.Fatalf("unexpected migrated public root name: %q", migratedRoot.Name)
	}
	if migratedRoot.FileChildren != 0 {
		t.Fatalf("unexpected migrated public root parent id: %d", migratedRoot.FileChildren)
	}

	settingModel, err := client.Setting.Query().Where(entsetting.Name(publicRootFileIDSettingKey)).Only(ctx)
	if err != nil {
		t.Fatalf("failed to query public root setting: %v", err)
	}
	if settingModel.Value != strconv.Itoa(legacyPublicRoot.ID) {
		t.Fatalf("unexpected public root setting after migration: %s", settingModel.Value)
	}
}

func TestEnsureSystemPublicRootSupportMigratesLegacySystemOwnerID(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:public-root-migrate-system-owner-id?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}
	if err := migrateDefaultStoragePolicy(logging.NewConsoleLogger(logging.LevelError), client, ctx); err != nil {
		t.Fatalf("failed to migrate default storage policy: %v", err)
	}
	if err := migrateAdminGroup(logging.NewConsoleLogger(logging.LevelError), client, ctx); err != nil {
		t.Fatalf("failed to migrate admin group: %v", err)
	}

	legacyOwner, err := client.User.Create().
		SetUsername(constants.PublicSystemOwnerUsername).
		SetEmail(constants.PublicSystemOwnerEmail).
		SetNick(constants.PublicSystemOwnerNick).
		SetStatus(entuser.StatusInactive).
		SetGroupID(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create legacy system owner: %v", err)
	}

	fileClient := NewFileClient(client, conf.SQLiteDB, nil)
	legacyRoot, err := fileClient.CreateFolder(ctx, nil, &CreateFolderParameters{
		Owner: legacyOwner.ID,
		Name:  RootFolderName,
	})
	if err != nil {
		t.Fatalf("failed to create legacy hidden public root: %v", err)
	}
	if err := NewSettingClient(client, nil).Set(ctx, map[string]string{
		publicRootFileIDSettingKey: strconv.Itoa(legacyRoot.ID),
	}); err != nil {
		t.Fatalf("failed to persist legacy public root setting: %v", err)
	}

	if err := ensureSystemPublicRootSupport(ctx, logging.NewConsoleLogger(logging.LevelError), client, conf.SQLiteDB); err != nil {
		t.Fatalf("failed to migrate legacy system owner id: %v", err)
	}

	systemOwner, err := client.User.Query().
		Where(entuser.Email(constants.PublicSystemOwnerEmail)).
		Only(ctx)
	if err != nil {
		t.Fatalf("failed to query migrated system owner: %v", err)
	}
	if systemOwner.ID != constants.PublicSystemOwnerID {
		t.Fatalf("unexpected migrated system owner id: got %d want %d", systemOwner.ID, constants.PublicSystemOwnerID)
	}

	root, err := client.File.Query().Where(entfile.ID(legacyRoot.ID)).Only(ctx)
	if err != nil {
		t.Fatalf("failed to reload migrated hidden root: %v", err)
	}
	if root.OwnerID != constants.PublicSystemOwnerID {
		t.Fatalf("unexpected migrated hidden root owner: got %d want %d", root.OwnerID, constants.PublicSystemOwnerID)
	}

	existed, err := client.User.Query().Where(entuser.ID(legacyOwner.ID)).Exist(ctx)
	if err != nil {
		t.Fatalf("failed to query legacy owner existence: %v", err)
	}
	if existed {
		t.Fatalf("expected legacy public system owner %d to be removed", legacyOwner.ID)
	}
}
