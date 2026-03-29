package user

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/gin-gonic/gin"
)

func TestUpdateOIDCShadowUserOverwritesEmailFromProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)

	client, err := ent.Open("sqlite3", "file:oidc-shadow-user-email-sync?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	group, err := client.Group.Create().SetName("User").SetPermissions(&boolset.BooleanSet{}).Save(ctx)
	if err != nil {
		t.Fatalf("failed to create user group: %v", err)
	}

	localUser, err := client.User.Create().
		SetRawID(1001).
		SetUsername("legacy-user").
		SetEmail("legacy@example.com").
		SetNick("Legacy User").
		SetStatus(user.StatusActive).
		SetGroupID(group.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create local user: %v", err)
	}

	w := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(w)

	updatedUser, err := updateOIDCShadowUser(ginCtx, client, localUser, &oidcIdentityProfile{
		ExternalUserID: "1001",
		Email:          "auth-center@example.com",
		Username:       "legacy-user",
		Nickname:       "统一认证用户",
	})
	if err != nil {
		t.Fatalf("expected email mismatch to be synced instead of rejected, got error: %v", err)
	}

	if updatedUser.Email != "auth-center@example.com" {
		t.Fatalf("expected email to be overwritten by provider, got %q", updatedUser.Email)
	}
	if updatedUser.Nick != "统一认证用户" {
		t.Fatalf("expected nickname to be synced, got %q", updatedUser.Nick)
	}
}
