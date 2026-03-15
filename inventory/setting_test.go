package inventory

import (
	"context"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
)

func TestSettingClientSetUpsertsMissingSetting(t *testing.T) {
	t.Parallel()

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
