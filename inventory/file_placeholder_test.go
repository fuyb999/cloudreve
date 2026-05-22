package inventory

import (
	"context"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	entuser "github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/gofrs/uuid"
)

func TestUpgradePlaceholderReloadsStaleLoadedEntities(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:upgrade-placeholder-reload-stale?mode=memory&cache=shared&_fk=1")
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

	owner, err := client.User.Create().
		SetUsername("placeholder-owner").
		SetEmail("placeholder-owner@example.com").
		SetNick("placeholder-owner").
		SetStatus(entuser.StatusActive).
		SetGroupID(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create owner: %v", err)
	}

	fileClient := NewFileClient(client, conf.SQLiteDB, nil)
	root, err := fileClient.CreateFolder(ctx, nil, &CreateFolderParameters{
		Owner: owner.ID,
		Name:  RootFolderName,
	})
	if err != nil {
		t.Fatalf("failed to create root: %v", err)
	}

	target, _, _, err := fileClient.CreateFile(ctx, root, &CreateFileParameters{
		Owner:           owner.ID,
		FileType:        types.FileTypeFile,
		Name:            "新建文件.txt",
		StoragePolicyID: 1,
	})
	if err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}

	uploadSessionID := uuid.Must(uuid.NewV4())
	placeholder, _, err := fileClient.CreateEntity(ctx, target, &EntityParameters{
		OwnerID:         owner.ID,
		EntityType:      types.EntityTypeVersion,
		StoragePolicyID: 1,
		Source:          "data/uploads/1/new-file.txt",
		Size:            12,
		UploadSessionID: uploadSessionID,
	})
	if err != nil {
		t.Fatalf("failed to create placeholder entity: %v", err)
	}

	staleLoaded := *target
	staleLoaded.SetEntities([]*ent.Entity{})

	if err := fileClient.UpgradePlaceholder(ctx, &staleLoaded, nil, placeholder.ID, types.EntityTypeVersion); err != nil {
		t.Fatalf("expected stale loaded entities to be reloaded, got error: %v", err)
	}

	loadCtx := context.WithValue(ctx, LoadFileEntity{}, true)
	reloaded, err := fileClient.GetByID(loadCtx, target.ID)
	if err != nil {
		t.Fatalf("failed to reload target file: %v", err)
	}

	if reloaded.PrimaryEntity != placeholder.ID {
		t.Fatalf("unexpected primary entity: got %d want %d", reloaded.PrimaryEntity, placeholder.ID)
	}

	entities, err := reloaded.Edges.EntitiesOrErr()
	if err != nil {
		t.Fatalf("failed to load target entities: %v", err)
	}

	var upgraded *ent.Entity
	for _, entity := range entities {
		if entity.ID == placeholder.ID {
			upgraded = entity
			break
		}
	}
	if upgraded == nil {
		t.Fatalf("failed to find upgraded placeholder entity %d", placeholder.ID)
	}
	if upgraded.UploadSessionID != nil {
		t.Fatalf("expected upload session id to be cleared, got %v", *upgraded.UploadSessionID)
	}
}
