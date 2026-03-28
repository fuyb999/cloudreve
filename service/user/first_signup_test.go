package user

import (
	"context"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

func TestDisableOpenRegistrationAfterFirstSignupClosesSettingAndInvalidatesCache(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:first-signup-register-switch?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	kv := cache.NewMemoStore("", logging.NewConsoleLogger(logging.LevelError))
	settingClient := inventory.NewSettingClient(client, kv)
	userClient := inventory.NewUserClient(client)
	if err := settingClient.Set(ctx, map[string]string{registerEnabledSettingName: "1"}); err != nil {
		t.Fatalf("failed to seed registration setting: %v", err)
	}
	if _, err := client.Group.Create().SetName("Admin").SetPermissions(&boolset.BooleanSet{}).Save(ctx); err != nil {
		t.Fatalf("failed to create admin group: %v", err)
	}
	if _, err := client.Group.Create().SetName("User").SetPermissions(&boolset.BooleanSet{}).Save(ctx); err != nil {
		t.Fatalf("failed to create user group: %v", err)
	}

	provider := setting.NewProvider(setting.NewKvSettingStore(kv, setting.NewDbSettingStore(settingClient, nil)))
	if !provider.RegisterEnabled(ctx) {
		t.Fatal("expected registration to be enabled before first signup")
	}

	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatalf("failed to start transaction: %v", err)
	}

	txSettingClient := inventory.NewSettingClient(tx.Client(), kv)
	txUserClient := inventory.NewUserClient(tx.Client())
	if _, err := tx.Client().User.Create().
		SetUsername("first-user").
		SetEmail("first@example.com").
		SetNick("First").
		SetStatus(user.StatusActive).
		SetGroupID(2).
		Save(ctx); err != nil {
		t.Fatalf("failed to create first visible user: %v", err)
	}

	disabled, err := disableOpenRegistrationAfterFirstSignup(ctx, txUserClient, txSettingClient, &ent.User{
		ID:    1,
		Email: "first@example.com",
	})
	if err != nil {
		t.Fatalf("failed to disable registration after first signup: %v", err)
	}
	if !disabled {
		t.Fatal("expected registration to be disabled for first visible user")
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("failed to commit registration update: %v", err)
	}

	if !provider.RegisterEnabled(ctx) {
		t.Fatal("expected cached registration setting to remain enabled before cache invalidation")
	}

	if err := invalidateOpenRegistrationCache(kv, true); err != nil {
		t.Fatalf("failed to invalidate registration setting cache: %v", err)
	}

	if provider.RegisterEnabled(ctx) {
		t.Fatal("expected registration to be disabled after first signup cache invalidation")
	}

	total, err := userClient.CountByTimeRange(ctx, nil, nil)
	if err != nil {
		t.Fatalf("failed to count visible users: %v", err)
	}
	if total != 1 {
		t.Fatalf("unexpected visible user count: got %d want 1", total)
	}
}

func TestDisableOpenRegistrationAfterFirstVisibleSignupIgnoresInternalSystemUser(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:first-visible-signup-with-internal-user?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	kv := cache.NewMemoStore("", logging.NewConsoleLogger(logging.LevelError))
	settingClient := inventory.NewSettingClient(client, kv)
	if err := settingClient.Set(ctx, map[string]string{registerEnabledSettingName: "1"}); err != nil {
		t.Fatalf("failed to seed registration setting: %v", err)
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

	realUser, err := client.User.Create().
		SetUsername("real-user").
		SetEmail("real-user@example.com").
		SetNick("Real User").
		SetStatus(user.StatusActive).
		SetGroupID(2).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create real user: %v", err)
	}

	disabled, err := disableOpenRegistrationAfterFirstSignup(ctx, inventory.NewUserClient(client), settingClient, realUser)
	if err != nil {
		t.Fatalf("failed to disable registration for first visible signup: %v", err)
	}
	if !disabled {
		t.Fatal("expected first visible signup to disable registration even with internal user present")
	}
}

func TestPromoteFirstOIDCIdentityUserToAdminWhenLocalUserIDIsOne(t *testing.T) {
	client, err := ent.Open("sqlite3", "file:first-oidc-admin?mode=memory&cache=shared&_fk=1")
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

	oidcUser, err := client.User.Create().
		SetRawID(1).
		SetEmail("oidc@example.com").
		SetNick("OIDC User").
		SetStatus(user.StatusActive).
		SetGroupID(2).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create oidc user: %v", err)
	}

	identity, err := client.ExternalIdentity.Create().
		SetProvider(oidcProviderName).
		SetIssuer("https://issuer.example.com").
		SetSubject("subject-1").
		SetUserID(oidcUser.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create oidc identity: %v", err)
	}

	promoted, err := promoteFirstOIDCIdentityUserToAdmin(ctx, oidcUser, identity)
	if err != nil {
		t.Fatalf("failed to promote first oidc identity user: %v", err)
	}

	if promoted.GroupUsers != 1 {
		t.Fatalf("expected first oidc user to become admin, got group_id=%d", promoted.GroupUsers)
	}

	stored, err := client.User.Get(ctx, oidcUser.ID)
	if err != nil {
		t.Fatalf("failed to reload oidc user: %v", err)
	}

	if stored.GroupUsers != 1 {
		t.Fatalf("expected stored oidc user to become admin, got group_id=%d", stored.GroupUsers)
	}
}
