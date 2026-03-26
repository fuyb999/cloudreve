package inventory

import (
	"context"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
)

func TestCreatePromotesFirstUserToAdminGroup(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:first-user-admin?mode=memory&cache=shared&_fk=1")
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

	userClient := NewUserClient(client)
	created, err := userClient.Create(ctx, &NewUserArgs{
		Username: "first-admin",
		Email:    "first-admin@example.com",
		Status:   user.StatusActive,
		GroupID:  2,
	})
	if err != nil {
		t.Fatalf("failed to create first user: %v", err)
	}

	if created.GroupUsers != 1 {
		t.Fatalf("expected returned first user to be promoted to admin group, got group_id=%d", created.GroupUsers)
	}

	stored, err := userClient.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("failed to reload stored user: %v", err)
	}

	if stored.GroupUsers != 1 {
		t.Fatalf("expected stored first user to be promoted to admin group, got group_id=%d", stored.GroupUsers)
	}
}
