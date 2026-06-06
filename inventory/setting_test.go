package inventory

import (
	"context"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/schema"
	entsetting "github.com/cloudreve/Cloudreve/v4/ent/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

func TestSettingClientSetUpsertsMissingSetting(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:setting-upsert?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	settingClient := NewSettingClient(client, nil)
	wantCreated := "https://example.com/linux"
	if err := settingClient.Set(ctx, map[string]string{
		"syncthing_download_linux_url": wantCreated,
	}); err != nil {
		t.Fatalf("failed to create missing setting: %v", err)
	}

	got, err := settingClient.Get(ctx, "syncthing_download_linux_url")
	if err != nil {
		t.Fatalf("failed to get created setting: %v", err)
	}
	if got != wantCreated {
		t.Fatalf("unexpected created setting value: got %q want %q", got, wantCreated)
	}

	wantUpdated := "https://example.com/linux-v2"
	if err := settingClient.Set(ctx, map[string]string{
		"syncthing_download_linux_url": wantUpdated,
	}); err != nil {
		t.Fatalf("failed to update existing setting: %v", err)
	}

	got, err = settingClient.Get(ctx, "syncthing_download_linux_url")
	if err != nil {
		t.Fatalf("failed to get updated setting: %v", err)
	}
	if got != wantUpdated {
		t.Fatalf("unexpected updated setting value: got %q want %q", got, wantUpdated)
	}
}

func TestDefaultSettingsIncludeContentProcessingQueueDefaults(t *testing.T) {
	expected := map[string]string{
		"queue_content_processing_worker_num":           "30",
		"queue_content_processing_max_execution":        "3600",
		"queue_content_processing_backoff_factor":       "2",
		"queue_content_processing_backoff_max_duration": "60",
		"queue_content_processing_max_retry":            "1",
		"queue_content_processing_retry_delay":          "0",
	}

	for key, want := range expected {
		got, ok := DefaultSettings[key]
		if !ok {
			t.Fatalf("expected default setting %q to exist", key)
		}
		if got != want {
			t.Fatalf("unexpected default setting %q: got %q want %q", key, got, want)
		}
	}
}

func TestSettingClientSetRevivesSoftDeletedSetting(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:setting-upsert-soft-delete?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	key := "oidc_display_name"
	if err := NewSettingClient(client, nil).Set(ctx, map[string]string{key: "before"}); err != nil {
		t.Fatalf("failed to create setting: %v", err)
	}
	if _, err := client.Setting.Delete().Where(entsetting.NameEQ(key)).Exec(ctx); err != nil {
		t.Fatalf("failed to soft-delete setting: %v", err)
	}
	if exists, err := client.Setting.Query().Where(entsetting.NameEQ(key)).Exist(ctx); err != nil {
		t.Fatalf("failed to query soft-deleted setting: %v", err)
	} else if exists {
		t.Fatalf("expected soft-deleted setting %q to be hidden", key)
	}

	if err := NewSettingClient(client, nil).Set(ctx, map[string]string{key: "after"}); err != nil {
		t.Fatalf("failed to upsert soft-deleted setting: %v", err)
	}

	got, err := client.Setting.Query().Where(entsetting.NameEQ(key)).Only(ctx)
	if err != nil {
		t.Fatalf("expected setting %q to be visible after upsert: %v", key, err)
	}
	if got.Value != "after" {
		t.Fatalf("unexpected revived setting value: got %q want %q", got.Value, "after")
	}
	if got.DeletedAt != nil {
		t.Fatalf("expected revived setting deleted_at to be cleared, got %v", got.DeletedAt)
	}
}

func TestInitializeDBClientRestoresMissingDefaultSettingsOnCurrentVersion(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:initdb-restore-default-settings?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	logger := logging.NewConsoleLogger(logging.LevelError)
	kv := cache.NewMemoStore("", logger)
	if err := migrate(logger, client, ctx, kv, constants.BackendVersion); err != nil {
		t.Fatalf("failed to migrate sqlite client: %v", err)
	}

	deletedKeys := []string{"siteURL", "secret_key", "hash_id_salt", "fts_enabled"}
	if _, err := client.Setting.Delete().
		Where(entsetting.NameIn(deletedKeys...)).
		Exec(schema.SkipSoftDelete(ctx)); err != nil {
		t.Fatalf("failed to physically delete settings: %v", err)
	}
	versionExists, err := client.Setting.Query().
		Where(entsetting.NameEQ(DBVersionPrefix + constants.BackendVersion)).
		Exist(ctx)
	if err != nil {
		t.Fatalf("failed to query db version marker: %v", err)
	}
	if !versionExists {
		t.Fatal("expected db version marker to remain")
	}

	if _, err := InitializeDBClient(logger, client, kv, constants.BackendVersion, conf.SQLiteDB); err != nil {
		t.Fatalf("failed to initialize db client: %v", err)
	}

	for _, key := range deletedKeys {
		exists, err := client.Setting.Query().Where(entsetting.NameEQ(key)).Exist(ctx)
		if err != nil {
			t.Fatalf("failed to query restored setting %q: %v", key, err)
		}
		if !exists {
			t.Fatalf("expected missing default setting %q to be restored", key)
		}
	}
}
