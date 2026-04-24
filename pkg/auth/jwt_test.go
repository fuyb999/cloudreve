package auth

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/gin-gonic/gin"
)

func TestOAuthClientTokenTTLs(t *testing.T) {
	syncthingTTL := time.Duration(inventory.SyncthingOAuthTokenTTLSeconds) * time.Second

	tests := []struct {
		name           string
		client         *ent.OAuthClient
		wantAccessTTL  time.Duration
		wantRefreshTTL time.Duration
	}{
		{
			name:           "syncthing legacy props fall back to long lived defaults",
			client:         &ent.OAuthClient{GUID: inventory.OAuthClientSyncthingGUID, Props: &types.OAuthClientProps{RefreshTokenTTL: inventory.LegacyOAuthTokenTTLSeconds}},
			wantAccessTTL:  syncthingTTL,
			wantRefreshTTL: syncthingTTL,
		},
		{
			name:           "syncthing nil props fall back to long lived defaults",
			client:         &ent.OAuthClient{GUID: inventory.OAuthClientSyncthingGUID},
			wantAccessTTL:  syncthingTTL,
			wantRefreshTTL: syncthingTTL,
		},
		{
			name: "syncthing custom props are preserved",
			client: &ent.OAuthClient{
				GUID: inventory.OAuthClientSyncthingGUID,
				Props: &types.OAuthClientProps{
					AccessTokenTTL:  7200,
					RefreshTokenTTL: 86400,
				},
			},
			wantAccessTTL:  2 * time.Hour,
			wantRefreshTTL: 24 * time.Hour,
		},
		{
			name: "regular oauth client uses explicit props only",
			client: &ent.OAuthClient{
				GUID: "custom-client",
				Props: &types.OAuthClientProps{
					AccessTokenTTL:  1800,
					RefreshTokenTTL: 3600,
				},
			},
			wantAccessTTL:  30 * time.Minute,
			wantRefreshTTL: time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAccessTTL, gotRefreshTTL := OAuthClientTokenTTLs(tt.client)
			if gotAccessTTL != tt.wantAccessTTL {
				t.Fatalf("unexpected access ttl: got %s, want %s", gotAccessTTL, tt.wantAccessTTL)
			}
			if gotRefreshTTL != tt.wantRefreshTTL {
				t.Fatalf("unexpected refresh ttl: got %s, want %s", gotRefreshTTL, tt.wantRefreshTTL)
			}
		})
	}
}

func TestVerifyAndRetrieveUserSkipsOpaqueBearerToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	engine := gin.New()
	engine.ContextWithFallback = true
	c := gin.CreateTestContextOnly(w, engine)
	c.Request = httptest.NewRequest("GET", "/", nil)
	c.Request.Header.Set(AuthorizationHeader, TokenHeaderPrefix+"opaque-token")

	auth := &tokenAuth{secret: []byte("local-secret")}
	shouldContinue, err := auth.VerifyAndRetrieveUser(c)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !shouldContinue {
		t.Fatalf("expected opaque bearer token to fall through to OIDC verification")
	}
}

func TestCheckScopeAllowsLocalAdminForAdminScopes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	engine := gin.New()
	engine.ContextWithFallback = true
	c := gin.CreateTestContextOnly(w, engine)
	permissions := &boolset.BooleanSet{}
	boolset.Sets(map[types.GroupPermission]bool{
		types.GroupPermissionIsAdmin: true,
	}, permissions)
	c.Request = httptest.NewRequest("GET", "/api/v4/admin/summary", nil)

	adminUser := &ent.User{
		ID: 1,
		Edges: ent.UserEdges{
			Group: &ent.Group{Permissions: permissions},
		},
	}
	util.WithValue(c, inventory.UserCtx{}, adminUser)
	util.WithValue(c, ScopeContextKey{}, []string{types.ScopeFilesRead})
	util.WithValue(c, inventory.OIDCAccessTokenCtx{}, "provider-token")

	if err := CheckScope(c, types.ScopeAdminRead); err != nil {
		t.Fatalf("expected local admin to satisfy Admin.Read without upstream scope, got %v", err)
	}
	if err := CheckScope(c, types.ScopeAdminWrite); err != nil {
		t.Fatalf("expected local admin to satisfy Admin.Write without upstream scope, got %v", err)
	}
}

func TestCheckScopeDoesNotBypassNonAdminScopesForLocalAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	engine := gin.New()
	engine.ContextWithFallback = true
	c := gin.CreateTestContextOnly(w, engine)
	permissions := &boolset.BooleanSet{}
	boolset.Sets(map[types.GroupPermission]bool{
		types.GroupPermissionIsAdmin: true,
	}, permissions)
	adminUser := &ent.User{
		ID: 1,
		Edges: ent.UserEdges{
			Group: &ent.Group{Permissions: permissions},
		},
	}

	c.Request = httptest.NewRequest("GET", "/api/v4/file/search", nil)
	util.WithValue(c, inventory.UserCtx{}, adminUser)
	util.WithValue(c, ScopeContextKey{}, []string{types.ScopeAdminRead})
	util.WithValue(c, inventory.OIDCAccessTokenCtx{}, "provider-token")

	if err := CheckScope(c, types.ScopeFilesRead); err == nil {
		t.Fatal("expected non-admin scope to remain enforced for local admin")
	}
}
