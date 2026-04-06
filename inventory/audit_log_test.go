package inventory

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

func TestParseAuditLogTypeList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    []int
		wantErr bool
	}{
		{name: "empty", raw: "", want: nil},
		{name: "single", raw: "5", want: []int{5}},
		{name: "multiple", raw: "1,2,3", want: []int{1, 2, 3}},
		{name: "invalid", raw: "1,x", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseAuditLogTypeList(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("unexpected result: got %v want %v", got, tt.want)
			}
		})
	}
}

func TestDefaultAuditLogEnabledTypes(t *testing.T) {
	t.Parallel()

	var enabled []int
	if err := json.Unmarshal([]byte(defaultAuditLogEnabledTypes), &enabled); err != nil {
		t.Fatalf("failed to unmarshal defaults: %v", err)
	}

	if len(enabled) == 0 {
		t.Fatal("expected enabled audit log defaults")
	}
}

func TestAuditLogClientCreateKeepsInternalSystemUserID(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:audit-log-internal-user?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	if _, err := client.Group.Create().SetName("Admin").SetPermissions(&boolset.BooleanSet{}).Save(ctx); err != nil {
		t.Fatalf("failed to create admin group: %v", err)
	}
	if _, err := client.Group.Create().SetName("User").SetPermissions(&boolset.BooleanSet{}).Save(ctx); err != nil {
		t.Fatalf("failed to create user group: %v", err)
	}
	if _, err := createPublicSystemOwner(ctx, client, constants.PublicSystemOwnerID); err != nil {
		t.Fatalf("failed to create internal system user: %v", err)
	}

	logClient := NewAuditLogClient(client, "")
	created, err := logClient.Create(ctx, &CreateAuditLogArgs{
		Type:   0,
		UserID: constants.PublicSystemOwnerID,
	})
	if err != nil {
		t.Fatalf("failed to create audit log: %v", err)
	}

	stored, err := client.AuditLog.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("failed to reload audit log: %v", err)
	}
	if stored.UserID != constants.PublicSystemOwnerID {
		t.Fatalf("unexpected stored system user id: got %d want %d", stored.UserID, constants.PublicSystemOwnerID)
	}
}

func TestEnsureAuditLogSystemUserSupportBackfillsHistoricalStartupLogs(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:audit-log-backfill?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}
	if _, err := client.Group.Create().SetName("Admin").SetPermissions(&boolset.BooleanSet{}).Save(ctx); err != nil {
		t.Fatalf("failed to create admin group: %v", err)
	}
	if _, err := client.Group.Create().SetName("User").SetPermissions(&boolset.BooleanSet{}).Save(ctx); err != nil {
		t.Fatalf("failed to create user group: %v", err)
	}
	if _, err := createPublicSystemOwner(ctx, client, constants.PublicSystemOwnerID); err != nil {
		t.Fatalf("failed to create internal system user: %v", err)
	}

	stored, err := client.AuditLog.Create().SetType(0).Save(ctx)
	if err != nil {
		t.Fatalf("failed to create legacy startup audit log: %v", err)
	}

	if err := ensureAuditLogSystemUserSupport(ctx, logging.NewConsoleLogger(logging.LevelError), client); err != nil {
		t.Fatalf("failed to backfill startup audit log: %v", err)
	}

	reloaded, err := client.AuditLog.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("failed to reload startup audit log: %v", err)
	}
	if reloaded.UserID != constants.PublicSystemOwnerID {
		t.Fatalf("unexpected backfilled system user id: got %d want %d", reloaded.UserID, constants.PublicSystemOwnerID)
	}

	exists, err := client.Setting.Query().Where(setting.NameEQ(auditLogSystemUserBackfillMarker)).Exist(ctx)
	if err != nil {
		t.Fatalf("failed to query backfill marker: %v", err)
	}
	if !exists {
		t.Fatal("expected audit log backfill marker to be persisted")
	}
}
