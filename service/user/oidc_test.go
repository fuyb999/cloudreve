package user

import (
	"testing"

	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

func TestNormalizeOIDCLoginEntryURL_RewritesYudaoAuthorizeEndpoint(t *testing.T) {
	discovery := &oidcDiscovery{
		Issuer:                "http://localhost:48080",
		AuthorizationEndpoint: "http://localhost:48080/admin-api/system/oauth2/authorize",
	}

	got := normalizeOIDCLoginEntryURL("http://localhost:48080/admin-api/system/oauth2/authorize", discovery)
	want := "http://localhost:48080/sso"
	if got != want {
		t.Fatalf("normalizeOIDCLoginEntryURL() = %q, want %q", got, want)
	}
}

func TestNormalizeOIDCLoginEntryURL_RewritesRootURLToYudaoSSO(t *testing.T) {
	discovery := &oidcDiscovery{
		Issuer:                "http://localhost:5173",
		AuthorizationEndpoint: "http://localhost:48080/admin-api/system/oauth2/authorize",
	}

	got := normalizeOIDCLoginEntryURL("http://localhost:5173", discovery)
	want := "http://localhost:5173/sso"
	if got != want {
		t.Fatalf("normalizeOIDCLoginEntryURL() = %q, want %q", got, want)
	}
}

func TestResolveOIDCLoginEntryURL_FallsBackToYudaoSSO(t *testing.T) {
	discovery := &oidcDiscovery{
		Issuer:                "http://localhost:5173",
		AuthorizationEndpoint: "http://localhost:48080/admin-api/system/oauth2/authorize",
	}

	got := resolveOIDCLoginEntryURL(nil, discovery)
	want := "http://localhost:5173/sso"
	if got != want {
		t.Fatalf("resolveOIDCLoginEntryURL() = %q, want %q", got, want)
	}
}

func TestResolveOIDCLocalUserID(t *testing.T) {
	got, err := resolveOIDCLocalUserID(&oidcIdentityProfile{ExternalUserID: "1001"})
	if err != nil {
		t.Fatalf("resolveOIDCLocalUserID() returned error: %v", err)
	}
	if got != 1001 {
		t.Fatalf("resolveOIDCLocalUserID() = %d, want 1001", got)
	}
}

func TestResolveOIDCLocalUserIDRejectsInvalidValue(t *testing.T) {
	if _, err := resolveOIDCLocalUserID(&oidcIdentityProfile{ExternalUserID: "admin"}); err == nil {
		t.Fatal("expected invalid external_user_id to be rejected")
	}
	if _, err := resolveOIDCLocalUserID(&oidcIdentityProfile{ExternalUserID: "0"}); err == nil {
		t.Fatal("expected non-positive external_user_id to be rejected")
	}
}

func TestOIDCRuntimeBindingConfigURL(t *testing.T) {
	got, err := oidcRuntimeBindingConfigURL("http://localhost:48080/.well-known/openid-configuration")
	if err != nil {
		t.Fatalf("oidcRuntimeBindingConfigURL() returned error: %v", err)
	}

	want := "http://localhost:48080/app-api/authz/integration/runtime/binding-config"
	if got != want {
		t.Fatalf("oidcRuntimeBindingConfigURL() = %q, want %q", got, want)
	}
}

func TestApplyOIDCRuntimeBindingConfigOverridesLocalEndpoints(t *testing.T) {
	cfg := &setting.OIDCSetting{
		BindingCode:  "cloudreve-main",
		SSOURL:       "http://localhost:5173/sso-local",
		WellKnownURL: "http://localhost:48080/.well-known/openid-configuration",
		ClientID:     "cloudreve-local",
		ClientSecret: "cloudreve-secret",
		Scope:        "openid profile email user_info user.read UserInfo.Read Admin.Read Files.Read Files.Write Workflow.Read Workflow.Write Shares.Read Shares.Write",
	}

	applyOIDCRuntimeBindingConfig(cfg, &oidcRuntimeBindingConfig{
		BindingCode: "cloudreve-main",
		ClientID:    "cloudreve",
		ScopeText:   "openid profile email user_info user.read UserInfo.Read Admin.Read Files.Read Files.Write Workflow.Read Workflow.Write Shares.Read Shares.Write",
		AuthProvider: &oidcRuntimeAuthProvider{
			DiscoveryURL: "http://localhost:48080/.well-known/openid-configuration",
			SsoURL:       "http://localhost:5173/sso",
		},
	})

	if cfg.ClientID != "cloudreve" {
		t.Fatalf("cfg.ClientID = %q, want %q", cfg.ClientID, "cloudreve")
	}
	if cfg.Scope != "openid profile email user_info user.read UserInfo.Read Admin.Read Files.Read Files.Write Workflow.Read Workflow.Write Shares.Read Shares.Write" {
		t.Fatalf("cfg.Scope = %q, want remote scope", cfg.Scope)
	}
	if cfg.SSOURL != "http://localhost:5173/sso" {
		t.Fatalf("cfg.SSOURL = %q, want remote sso url", cfg.SSOURL)
	}
	if cfg.ClientSecret != "cloudreve-secret" {
		t.Fatalf("cfg.ClientSecret = %q, want local secret preserved", cfg.ClientSecret)
	}
}

func TestShouldUseOIDCRuntimeConfig(t *testing.T) {
	if shouldUseOIDCRuntimeConfig(&setting.OIDCSetting{
		Enabled:      true,
		ConfigMode:   setting.OIDCConfigModeRemote,
		WellKnownURL: "http://localhost:48080/.well-known/openid-configuration",
	}) != true {
		t.Fatal("expected remote mode with well-known url to enable runtime config")
	}

	if shouldUseOIDCRuntimeConfig(&setting.OIDCSetting{
		Enabled:      true,
		ConfigMode:   setting.OIDCConfigModeStandard,
		WellKnownURL: "http://localhost:48080/.well-known/openid-configuration",
	}) {
		t.Fatal("expected standard mode to disable runtime config")
	}
}
