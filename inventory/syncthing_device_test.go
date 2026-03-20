package inventory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
)

func TestSyncthingDeviceClientRejectsBoundDeviceWithSameIP(t *testing.T) {
	client := newSyncthingDeviceTestClient(t)
	defer client.Close()

	ctx := context.Background()
	user := newSyncthingDeviceTestUser(t, ctx, client)
	deviceClient := NewSyncthingDeviceClient(client, "")

	if _, err := deviceClient.Upsert(ctx, &UpsertSyncthingDeviceArgs{
		UserID:     user.ID,
		DeviceID:   "OLD-DEVICE",
		ShortID:    "OLD123",
		LastIP:     "10.0.0.8",
		APIKey:     "api-old",
		JSONRaw:    map[string]any{"profile": "old"},
		BindURI:    "cloudreve://my/demo",
		LastSeenAt: time.Now(),
		Online:     true,
	}); err != nil {
		t.Fatalf("failed to create initial device: %v", err)
	}

	_, err := deviceClient.Upsert(ctx, &UpsertSyncthingDeviceArgs{
		UserID:     user.ID,
		DeviceID:   "NEW-DEVICE",
		ShortID:    "NEW123",
		LastIP:     "10.0.0.8",
		APIKey:     "api-new",
		JSONRaw:    map[string]any{"profile": "new"},
		LastSeenAt: time.Now(),
		Online:     true,
	})
	if !errors.Is(err, ErrSyncthingDeviceIPConflict) {
		t.Fatalf("expected ip conflict, got %v", err)
	}
}

func TestSyncthingDeviceClientUnbindAllowsMigrationAndRestoresPreviousConfig(t *testing.T) {
	client := newSyncthingDeviceTestClient(t)
	defer client.Close()

	ctx := context.Background()
	user := newSyncthingDeviceTestUser(t, ctx, client)
	deviceClient := NewSyncthingDeviceClient(client, "")

	created, err := deviceClient.Upsert(ctx, &UpsertSyncthingDeviceArgs{
		UserID:   user.ID,
		DeviceID: "OLD-DEVICE",
		ShortID:  "OLD123",
		LastIP:   "10.0.0.9",
		APIKey:   "api-old",
		JSONRaw: map[string]any{
			"profile": "old",
			"folders": []any{"docs"},
		},
		BindURI:    "cloudreve://my/docs",
		LastSeenAt: time.Now(),
		Online:     true,
	})
	if err != nil {
		t.Fatalf("failed to create device: %v", err)
	}

	if _, err := deviceClient.Unbind(ctx, user.ID, "OLD-DEVICE"); err != nil {
		t.Fatalf("failed to unbind device: %v", err)
	}

	migrated, err := deviceClient.Upsert(ctx, &UpsertSyncthingDeviceArgs{
		UserID:     user.ID,
		DeviceID:   "NEW-DEVICE",
		ShortID:    "NEW123",
		LastIP:     "10.0.0.9",
		APIKey:     "api-new",
		JSONRaw:    map[string]any{"profile": "new"},
		BindURI:    "cloudreve://my/docs",
		LastSeenAt: time.Now(),
		Online:     true,
	})
	if err != nil {
		t.Fatalf("failed to migrate binding: %v", err)
	}

	if migrated.Device == nil {
		t.Fatal("expected migrated device response")
	}
	if migrated.Device.ID != created.Device.ID {
		t.Fatalf("expected migration to reuse original row, got old=%d new=%d", created.Device.ID, migrated.Device.ID)
	}
	if migrated.Device.DeviceID != "NEW-DEVICE" {
		t.Fatalf("unexpected migrated device id: %q", migrated.Device.DeviceID)
	}
	if !migrated.Device.IsBound {
		t.Fatal("expected migrated device to be bound")
	}
	if migrated.RestoreFromDeviceID != "OLD-DEVICE" {
		t.Fatalf("unexpected restore source device: %q", migrated.RestoreFromDeviceID)
	}
	if got := migrated.RestoreConfig["profile"]; got != "old" {
		t.Fatalf("unexpected restore config payload: %#v", migrated.RestoreConfig)
	}

	devices, err := deviceClient.ListByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("failed to list devices: %v", err)
	}
	if len(devices) != 1 || devices[0].DeviceID != "NEW-DEVICE" {
		t.Fatalf("expected only migrated device row to remain, got %#v", devices)
	}
}

func TestSyncthingDeviceClientRequiresBoundRegistrationForHeartbeatActivityAndReport(t *testing.T) {
	client := newSyncthingDeviceTestClient(t)
	defer client.Close()

	ctx := context.Background()
	user := newSyncthingDeviceTestUser(t, ctx, client)
	deviceClient := NewSyncthingDeviceClient(client, "")

	if _, err := deviceClient.Upsert(ctx, &UpsertSyncthingDeviceArgs{
		UserID:     user.ID,
		DeviceID:   "OLD-DEVICE",
		ShortID:    "OLD123",
		LastIP:     "10.0.0.7",
		APIKey:     "api-old",
		JSONRaw:    map[string]any{"profile": "old"},
		LastSeenAt: time.Now(),
		Online:     true,
	}); err != nil {
		t.Fatalf("failed to create device: %v", err)
	}

	if _, err := deviceClient.Unbind(ctx, user.ID, "OLD-DEVICE"); err != nil {
		t.Fatalf("failed to unbind device: %v", err)
	}

	if _, err := deviceClient.Heartbeat(ctx, &SyncthingDeviceHeartbeatArgs{
		UserID:     user.ID,
		DeviceID:   "OLD-DEVICE",
		ShortID:    "OLD123",
		LastIP:     "10.0.0.7",
		LastSeenAt: time.Now(),
		Online:     true,
	}); !errors.Is(err, ErrSyncthingDeviceNotRegistered) {
		t.Fatalf("expected heartbeat to require registered binding, got %v", err)
	}

	if _, err := deviceClient.ReportActivity(ctx, &SyncthingDeviceActivityArgs{
		UserID:     user.ID,
		DeviceID:   "OLD-DEVICE",
		ShortID:    "OLD123",
		LastIP:     "10.0.0.7",
		LastSeenAt: time.Now(),
		LastSyncAt: time.Now(),
		Online:     true,
	}); !errors.Is(err, ErrSyncthingDeviceNotRegistered) {
		t.Fatalf("expected activity to require registered binding, got %v", err)
	}

	if _, err := deviceClient.Upsert(ctx, &UpsertSyncthingDeviceArgs{
		UserID:     user.ID,
		DeviceID:   "OLD-DEVICE",
		ShortID:    "OLD123",
		LastIP:     "10.0.0.7",
		APIKey:     "api-old",
		JSONRaw:    map[string]any{"profile": "old"},
		LastSeenAt: time.Now(),
		Online:     true,
	}); !errors.Is(err, ErrSyncthingDeviceNotRegistered) {
		t.Fatalf("expected report to keep unbound device retired, got %v", err)
	}
}

func TestSyncthingDeviceClientDeleteRemovesDevicePermanently(t *testing.T) {
	client := newSyncthingDeviceTestClient(t)
	defer client.Close()

	ctx := context.Background()
	user := newSyncthingDeviceTestUser(t, ctx, client)
	deviceClient := NewSyncthingDeviceClient(client, "")

	if _, err := deviceClient.Upsert(ctx, &UpsertSyncthingDeviceArgs{
		UserID:   user.ID,
		DeviceID: "OLD-DEVICE",
		ShortID:  "OLD123",
		LastIP:   "10.0.0.6",
		APIKey:   "api-old",
		JSONRaw: map[string]any{
			"profile": "old",
			"folders": []any{"docs"},
		},
		BindURI:    "cloudreve://my/docs",
		LastSeenAt: time.Now(),
		Online:     true,
	}); err != nil {
		t.Fatalf("failed to create device: %v", err)
	}

	deleted, err := deviceClient.Delete(ctx, user.ID, "OLD-DEVICE")
	if err != nil {
		t.Fatalf("failed to delete device: %v", err)
	}
	if !deleted {
		t.Fatal("expected device to be deleted")
	}

	devices, err := deviceClient.ListByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("failed to list devices: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("expected device list to be empty after delete, got %#v", devices)
	}

	recreated, err := deviceClient.Upsert(ctx, &UpsertSyncthingDeviceArgs{
		UserID:     user.ID,
		DeviceID:   "NEW-DEVICE",
		ShortID:    "NEW123",
		LastIP:     "10.0.0.6",
		APIKey:     "api-new",
		JSONRaw:    map[string]any{"profile": "new"},
		BindURI:    "cloudreve://my/docs",
		LastSeenAt: time.Now(),
		Online:     true,
	})
	if err != nil {
		t.Fatalf("failed to recreate device after delete: %v", err)
	}
	if recreated.RestoreConfig != nil {
		t.Fatalf("expected deleted device to leave no restore snapshot, got %#v", recreated.RestoreConfig)
	}
	if recreated.RestoreFromDeviceID != "" {
		t.Fatalf("expected no restore source after delete, got %q", recreated.RestoreFromDeviceID)
	}
}

func newSyncthingDeviceTestClient(t *testing.T) *ent.Client {
	t.Helper()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", strings.ReplaceAll(t.Name(), "/", "_"))
	client, err := ent.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}

	if err := client.Schema.Create(context.Background()); err != nil {
		client.Close()
		t.Fatalf("failed to create schema: %v", err)
	}

	return client
}

func newSyncthingDeviceTestUser(t *testing.T, ctx context.Context, client *ent.Client) *ent.User {
	t.Helper()

	permissions := boolset.BooleanSet{}
	group, err := client.Group.Create().
		SetName("default").
		SetPermissions(&permissions).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create group: %v", err)
	}

	user, err := client.User.Create().
		SetEmail("user@example.com").
		SetNick("user").
		SetGroupUsers(group.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	return user
}
