package inventory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
)

func TestUserClientGetActiveByDavAccountSupportsUsernameAndEmailFallback(t *testing.T) {
	client := newUserDAVTestClient(t)
	defer client.Close()

	ctx := context.Background()
	group := newUserDAVTestGroup(t, ctx, client)
	created := newUserDAVTestUser(t, ctx, client, group.ID, "alice", "alice@example.com", user.StatusActive)
	newUserDAVTestAccount(t, ctx, client, created.ID, "dav-password")

	userClient := NewUserClient(client)

	gotByUsername, err := userClient.GetActiveByDavAccount(ctx, "alice", "dav-password")
	if err != nil {
		t.Fatalf("expected username login to succeed, got %v", err)
	}
	if gotByUsername.ID != created.ID {
		t.Fatalf("unexpected user from username login: got %d want %d", gotByUsername.ID, created.ID)
	}

	gotByEmail, err := userClient.GetActiveByDavAccount(ctx, "alice@example.com", "dav-password")
	if err != nil {
		t.Fatalf("expected email fallback login to succeed, got %v", err)
	}
	if gotByEmail.ID != created.ID {
		t.Fatalf("unexpected user from email fallback login: got %d want %d", gotByEmail.ID, created.ID)
	}
}

func TestUserClientGetActiveByDavAccountRejectsInactiveUser(t *testing.T) {
	client := newUserDAVTestClient(t)
	defer client.Close()

	ctx := context.Background()
	group := newUserDAVTestGroup(t, ctx, client)
	created := newUserDAVTestUser(t, ctx, client, group.ID, "disabled", "disabled@example.com", user.StatusInactive)
	newUserDAVTestAccount(t, ctx, client, created.ID, "dav-password")

	userClient := NewUserClient(client)
	if _, err := userClient.GetActiveByDavAccount(ctx, "disabled", "dav-password"); err == nil {
		t.Fatal("expected inactive user to be rejected")
	}
}

func newUserDAVTestClient(t *testing.T) *ent.Client {
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

func newUserDAVTestGroup(t *testing.T, ctx context.Context, client *ent.Client) *ent.Group {
	t.Helper()

	permissions := boolset.BooleanSet{}
	group, err := client.Group.Create().
		SetName("default").
		SetPermissions(&permissions).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create group: %v", err)
	}

	return group
}

func newUserDAVTestUser(t *testing.T, ctx context.Context, client *ent.Client, groupID int, username, email string, status user.Status) *ent.User {
	t.Helper()

	created, err := client.User.Create().
		SetUsername(username).
		SetEmail(email).
		SetNick(username).
		SetStatus(status).
		SetGroupUsers(groupID).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	return created
}

func newUserDAVTestAccount(t *testing.T, ctx context.Context, client *ent.Client, ownerID int, password string) *ent.DavAccount {
	t.Helper()

	account, err := client.DavAccount.Create().
		SetName("default").
		SetURI("/").
		SetPassword(password).
		SetOptions(&boolset.BooleanSet{}).
		SetOwnerID(ownerID).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create dav account: %v", err)
	}

	return account
}
