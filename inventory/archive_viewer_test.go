package inventory

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	entsetting "github.com/cloudreve/Cloudreve/v4/ent/setting"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

func TestInitializeDBClientEnsuresArchiveViewerExtsOnCurrentVersion(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:initdb-archive-viewer?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	kv := cache.NewMemoStore("", logging.NewConsoleLogger(logging.LevelError))
	if err := migrate(logging.NewConsoleLogger(logging.LevelError), client, ctx, kv, constants.BackendVersion); err != nil {
		t.Fatalf("failed to migrate sqlite client: %v", err)
	}

	raw, err := json.Marshal([]types.ViewerGroup{{
		Viewers: []types.Viewer{{
			ID:   "archive",
			Exts: []string{"zip", "7z"},
		}},
	}})
	if err != nil {
		t.Fatalf("failed to marshal file viewers: %v", err)
	}

	if _, err := client.Setting.Update().
		Where(entsetting.NameEQ("file_viewers")).
		SetValue(string(raw)).
		Save(ctx); err != nil {
		t.Fatalf("failed to reset file_viewers setting: %v", err)
	}
	if _, err := client.Setting.Delete().Where(entsetting.NameEQ(archiveViewerExtBackfillMarker)).Exec(ctx); err != nil {
		t.Fatalf("failed to delete archive viewer ext marker: %v", err)
	}
	if err := kv.Set(settingKVPrefix+"file_viewers", string(raw), 0); err != nil {
		t.Fatalf("failed to seed stale file_viewers cache: %v", err)
	}

	if _, err := InitializeDBClient(logging.NewConsoleLogger(logging.LevelError), client, kv, constants.BackendVersion, conf.SQLiteDB); err != nil {
		t.Fatalf("failed to initialize db client: %v", err)
	}

	fileViewersSetting, err := client.Setting.Query().Where(entsetting.NameEQ("file_viewers")).First(ctx)
	if err != nil {
		t.Fatalf("failed to query file_viewers setting: %v", err)
	}

	var fileViewers []types.ViewerGroup
	if err := json.Unmarshal([]byte(fileViewersSetting.Value), &fileViewers); err != nil {
		t.Fatalf("failed to unmarshal file_viewers setting: %v", err)
	}

	if len(fileViewers) == 0 || len(fileViewers[0].Viewers) == 0 {
		t.Fatalf("unexpected file viewers setting: %+v", fileViewers)
	}

	archiveViewer := fileViewers[0].Viewers[0]
	for _, ext := range defaultArchiveViewerExts {
		if !containsString(archiveViewer.Exts, ext) {
			t.Fatalf("expected archive viewer ext %q in %v", ext, archiveViewer.Exts)
		}
	}

	exists, err := client.Setting.Query().Where(entsetting.NameEQ(archiveViewerExtBackfillMarker)).Exist(ctx)
	if err != nil {
		t.Fatalf("failed to query archive viewer ext marker: %v", err)
	}
	if !exists {
		t.Fatalf("expected archive viewer ext marker %q to exist", archiveViewerExtBackfillMarker)
	}
	if _, ok := kv.Get(settingKVPrefix + "file_viewers"); ok {
		t.Fatalf("expected file_viewers cache to be invalidated")
	}
}
