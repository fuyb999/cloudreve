package inventory

import (
	"context"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	entuser "github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

func TestCountByTimeRangeExcludesHiddenPublicRoot(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:file-count-hidden-public-root?mode=memory&cache=shared&_fk=1")
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
		t.Fatalf("failed to ensure hidden public root: %v", err)
	}

	visibleOwner, err := client.User.Create().
		SetUsername("visible-owner").
		SetEmail("visible-owner@example.com").
		SetNick("visible-owner").
		SetStatus(entuser.StatusActive).
		SetGroupID(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create visible owner: %v", err)
	}

	fileClient := NewFileClient(client, conf.SQLiteDB, nil)
	visibleRoot, err := fileClient.CreateFolder(ctx, nil, &CreateFolderParameters{
		Owner: visibleOwner.ID,
		Name:  RootFolderName,
	})
	if err != nil {
		t.Fatalf("failed to create visible root: %v", err)
	}
	if _, err := fileClient.CreateFolder(ctx, visibleRoot, &CreateFolderParameters{
		Owner: visibleOwner.ID,
		Name:  "文档",
	}); err != nil {
		t.Fatalf("failed to create visible child folder: %v", err)
	}

	rawCount, err := client.File.Query().Count(ctx)
	if err != nil {
		t.Fatalf("failed to count raw files: %v", err)
	}
	if rawCount != 3 {
		t.Fatalf("unexpected raw file count: got %d want 3", rawCount)
	}

	total, err := fileClient.CountByTimeRange(ctx, nil, nil)
	if err != nil {
		t.Fatalf("failed to count visible files: %v", err)
	}
	if total != 2 {
		t.Fatalf("unexpected visible file count: got %d want 2", total)
	}

	start := time.Now().Add(-time.Hour)
	end := time.Now().Add(time.Hour)
	rangedTotal, err := fileClient.CountByTimeRange(ctx, &start, &end)
	if err != nil {
		t.Fatalf("failed to count visible files by range: %v", err)
	}
	if rangedTotal != 2 {
		t.Fatalf("unexpected ranged visible file count: got %d want 2", rangedTotal)
	}
}
