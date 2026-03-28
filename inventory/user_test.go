package inventory

import (
	"context"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
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

func TestCreatePromotesFirstVisibleUserToAdminGroupWhenInternalUserExists(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:first-visible-user-admin?mode=memory&cache=shared&_fk=1")
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

	if _, err := client.User.Create().
		SetUsername(constants.PublicSystemOwnerUsername).
		SetEmail(constants.PublicSystemOwnerEmail).
		SetNick(constants.PublicSystemOwnerNick).
		SetStatus(user.StatusInactive).
		SetGroupID(1).
		Save(ctx); err != nil {
		t.Fatalf("failed to create internal system user: %v", err)
	}

	userClient := NewUserClient(client)
	created, err := userClient.Create(ctx, &NewUserArgs{
		Username: "first-real-user",
		Email:    "first-real-user@example.com",
		Status:   user.StatusActive,
		GroupID:  2,
	})
	if err != nil {
		t.Fatalf("failed to create first visible user: %v", err)
	}

	if created.GroupUsers != 1 {
		t.Fatalf("expected first visible user to be promoted to admin group, got group_id=%d", created.GroupUsers)
	}

	stored, err := userClient.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("failed to reload stored user: %v", err)
	}

	if stored.GroupUsers != 1 {
		t.Fatalf("expected stored first visible user to be promoted to admin group, got group_id=%d", stored.GroupUsers)
	}
}

func TestCreateSupportsRawID(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:create-user-raw-id?mode=memory&cache=shared&_fk=1")
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
		RawID:    1001,
		Username: "oidc-shadow",
		Email:    "oidc-shadow@example.com",
		Status:   user.StatusActive,
		GroupID:  2,
	})
	if err != nil {
		t.Fatalf("failed to create raw-id user: %v", err)
	}

	if created.ID != 1001 {
		t.Fatalf("unexpected raw user id: got %d want 1001", created.ID)
	}
	if created.GroupUsers != 1 {
		t.Fatalf("expected first visible raw-id user to be promoted to admin, got group_id=%d", created.GroupUsers)
	}
}

func TestListUsersAndSearchActiveHideInternalSystemUser(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:hide-internal-user?mode=memory&cache=shared&_fk=1")
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
	normalActive, err := userClient.Create(ctx, &NewUserArgs{
		Username: "normal-user",
		Email:    "normal-user@example.com",
		Nick:     "Normal User",
		Status:   user.StatusActive,
		GroupID:  2,
	})
	if err != nil {
		t.Fatalf("failed to create normal active user: %v", err)
	}
	if _, err := userClient.Create(ctx, &NewUserArgs{
		Username: "inactive-user",
		Email:    "inactive-user@example.com",
		Nick:     "Inactive User",
		Status:   user.StatusInactive,
		GroupID:  2,
	}); err != nil {
		t.Fatalf("failed to create inactive user: %v", err)
	}
	internalUser, err := client.User.Create().
		SetUsername(constants.PublicSystemOwnerUsername).
		SetEmail(constants.PublicSystemOwnerEmail).
		SetNick(constants.PublicSystemOwnerNick).
		SetStatus(user.StatusActive).
		SetGroupID(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create internal system user: %v", err)
	}

	if !IsInternalSystemUser(internalUser) {
		t.Fatalf("expected internal system user to be detected")
	}
	if IsInternalSystemUser(normalActive) {
		t.Fatalf("normal user should not be treated as internal system user")
	}

	listed, err := userClient.ListUsers(ctx, &ListUserParameters{
		PaginationArgs: &PaginationArgs{
			Page:     0,
			PageSize: 10,
		},
	})
	if err != nil {
		t.Fatalf("failed to list users: %v", err)
	}
	if len(listed.Users) != 2 {
		t.Fatalf("unexpected visible user count: got %d want 2", len(listed.Users))
	}
	for _, listedUser := range listed.Users {
		if IsInternalSystemUser(listedUser) {
			t.Fatalf("internal system user should be hidden from list")
		}
	}

	found, err := userClient.SearchActive(ctx, 10, "user")
	if err != nil {
		t.Fatalf("failed to search users: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("unexpected active search result count: got %d want 1", len(found))
	}
	if found[0].ID != normalActive.ID {
		t.Fatalf("unexpected active search result: got %d want %d", found[0].ID, normalActive.ID)
	}

	totalCount, err := userClient.CountByTimeRange(ctx, nil, nil)
	if err != nil {
		t.Fatalf("failed to count all visible users: %v", err)
	}
	if totalCount != 2 {
		t.Fatalf("unexpected visible user total count: got %d want 2", totalCount)
	}

	start := time.Now().Add(-time.Hour)
	end := time.Now().Add(time.Hour)
	rangeCount, err := userClient.CountByTimeRange(ctx, &start, &end)
	if err != nil {
		t.Fatalf("failed to count visible users by time range: %v", err)
	}
	if rangeCount != 2 {
		t.Fatalf("unexpected visible user time-range count: got %d want 2", rangeCount)
	}

	groupClient := NewGroupClient(client, "", nil)
	adminCount, err := groupClient.CountUsers(ctx, 1)
	if err != nil {
		t.Fatalf("failed to count admin group users: %v", err)
	}
	if adminCount != 1 {
		t.Fatalf("unexpected admin group visible user count: got %d want 1", adminCount)
	}

	userCount, err := groupClient.CountUsers(ctx, 2)
	if err != nil {
		t.Fatalf("failed to count user group users: %v", err)
	}
	if userCount != 1 {
		t.Fatalf("unexpected normal group visible user count: got %d want 1", userCount)
	}
}
