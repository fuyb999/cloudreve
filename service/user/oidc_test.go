package user

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/request"
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

func TestResolveEffectiveOIDCSettingUsesRemoteCacheAndState(t *testing.T) {
	prevStore := globalOIDCRuntimeStore
	globalOIDCRuntimeStore = &oidcRuntimeStore{entries: make(map[string]*oidcRuntimeCacheEntry)}
	defer func() {
		globalOIDCRuntimeStore = prevStore
	}()

	baseCfg := &setting.OIDCSetting{
		Enabled:      true,
		ConfigMode:   setting.OIDCConfigModeRemote,
		BindingCode:  "cloudreve-main",
		WellKnownURL: "http://localhost:48080/.well-known/openid-configuration",
		ClientSecret: "secret",
	}
	requestCount := 0
	dep := oidcRuntimeTestDep{
		settings: setting.NewProvider(oidcRuntimeTestStore{values: map[string]any{
			"oidc_enabled":       "1",
			"oidc_config_mode":   string(setting.OIDCConfigModeRemote),
			"oidc_binding_code":  baseCfg.BindingCode,
			"oidc_wellknown_url": baseCfg.WellKnownURL,
			"oidc_client_secret": baseCfg.ClientSecret,
		}}),
		requestClient: oidcRuntimeTestClient{requestFunc: func(method, target string, body io.Reader, opts ...request.Option) *request.Response {
			requestCount++
			resp := `{"code":0,"data":{"bindingCode":"cloudreve-main","clientId":"cloudreve","scopeText":"openid profile","authProvider":{"discoveryUrl":"http://localhost:48080/.well-known/openid-configuration","ssoUrl":"http://localhost:5173/sso"}}}`
			return &request.Response{
				Response: &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(strings.NewReader(resp)),
				},
			}
		}},
	}

	effective, state := resolveEffectiveOIDCSetting(context.Background(), dep, baseCfg)
	if requestCount != 1 {
		t.Fatalf("expected first resolve to fetch remote config once, got %d", requestCount)
	}
	if effective.ClientID != "cloudreve" {
		t.Fatalf("effective.ClientID = %q, want %q", effective.ClientID, "cloudreve")
	}
	if state == nil || state.Status != setting.OIDCRuntimeStatusRemoteReady {
		t.Fatalf("state.Status = %v, want %v", state.Status, setting.OIDCRuntimeStatusRemoteReady)
	}

	effective, state = resolveEffectiveOIDCSetting(context.Background(), dep, baseCfg)
	if requestCount != 1 {
		t.Fatalf("expected second resolve to hit cache, got %d requests", requestCount)
	}
	if state == nil || state.Status != setting.OIDCRuntimeStatusRemoteCached {
		t.Fatalf("state.Status = %v, want %v", state.Status, setting.OIDCRuntimeStatusRemoteCached)
	}
	if effective.SSOURL != "http://localhost:5173/sso" {
		t.Fatalf("effective.SSOURL = %q, want remote sso url", effective.SSOURL)
	}
}

func TestResolveEffectiveOIDCSettingFallsBackToLocalWhenRemoteFails(t *testing.T) {
	prevStore := globalOIDCRuntimeStore
	globalOIDCRuntimeStore = &oidcRuntimeStore{entries: make(map[string]*oidcRuntimeCacheEntry)}
	defer func() {
		globalOIDCRuntimeStore = prevStore
	}()

	now := time.Now().Add(-time.Minute)
	cacheKey := oidcRuntimeCacheKey(&setting.OIDCSetting{
		BindingCode:  "cloudreve-main",
		WellKnownURL: "http://localhost:48080/.well-known/openid-configuration",
	})
	globalOIDCRuntimeStore.set(cacheKey, &oidcRuntimeCacheEntry{
		Config: &oidcRuntimeBindingConfig{
			BindingCode: "cloudreve-main",
			ClientID:    "cached-client",
			ScopeText:   "openid profile",
			AuthProvider: &oidcRuntimeAuthProvider{
				DiscoveryURL: "http://localhost:48080/.well-known/openid-configuration",
				SsoURL:       "http://localhost:5173/sso",
			},
		},
		CachedAt:      now.Add(-10 * time.Minute),
		ExpiresAt:     now.Add(-5 * time.Minute),
		LastSuccessAt: now,
	})

	baseCfg := &setting.OIDCSetting{
		Enabled:      true,
		ConfigMode:   setting.OIDCConfigModeRemote,
		BindingCode:  "cloudreve-main",
		WellKnownURL: "http://localhost:48080/.well-known/openid-configuration",
		ClientID:     "local-client",
		ClientSecret: "secret",
		Scope:        "openid",
	}
	dep := oidcRuntimeTestDep{
		settings: setting.NewProvider(oidcRuntimeTestStore{values: map[string]any{
			"oidc_enabled":       "1",
			"oidc_config_mode":   string(setting.OIDCConfigModeRemote),
			"oidc_binding_code":  baseCfg.BindingCode,
			"oidc_wellknown_url": baseCfg.WellKnownURL,
			"oidc_client_id":     baseCfg.ClientID,
			"oidc_client_secret": baseCfg.ClientSecret,
			"oidc_scope":         baseCfg.Scope,
		}}),
		requestClient: oidcRuntimeTestClient{requestFunc: func(method, target string, body io.Reader, opts ...request.Option) *request.Response {
			return &request.Response{Err: io.EOF}
		}},
	}

	effective, state := resolveEffectiveOIDCSetting(context.Background(), dep, baseCfg)
	if state == nil || state.Status != setting.OIDCRuntimeStatusRemoteCached {
		t.Fatalf("state.Status = %v, want %v", state.Status, setting.OIDCRuntimeStatusRemoteCached)
	}
	if state.LastError == "" {
		t.Fatal("expected last error to be recorded on cached fallback")
	}
	if effective.ClientID != "cached-client" {
		t.Fatalf("effective.ClientID = %q, want %q", effective.ClientID, "cached-client")
	}

	globalOIDCRuntimeStore = &oidcRuntimeStore{entries: make(map[string]*oidcRuntimeCacheEntry)}
	effective, state = resolveEffectiveOIDCSetting(context.Background(), dep, baseCfg)
	if state == nil || state.Status != setting.OIDCRuntimeStatusLocalFallback {
		t.Fatalf("state.Status = %v, want %v", state.Status, setting.OIDCRuntimeStatusLocalFallback)
	}
	if effective.ClientID != "local-client" {
		t.Fatalf("effective.ClientID = %q, want %q", effective.ClientID, "local-client")
	}
}

type oidcRuntimeTestDep struct {
	dependency.Dep
	settings      setting.Provider
	requestClient request.Client
}

func (d oidcRuntimeTestDep) SettingProvider() setting.Provider {
	return d.settings
}

func (d oidcRuntimeTestDep) RequestClient(opts ...request.Option) request.Client {
	return d.requestClient
}

func (d oidcRuntimeTestDep) Logger() logging.Logger {
	return logging.NewConsoleLogger(logging.LevelError)
}

type oidcRuntimeTestClient struct {
	requestFunc func(method, target string, body io.Reader, opts ...request.Option) *request.Response
}

func (c oidcRuntimeTestClient) Apply(opts ...request.Option) {}

func (c oidcRuntimeTestClient) Request(method, target string, body io.Reader, opts ...request.Option) *request.Response {
	return c.requestFunc(method, target, body, opts...)
}

type oidcRuntimeTestStore struct {
	values map[string]any
}

func (s oidcRuntimeTestStore) Get(_ context.Context, name string, defaultVal any) any {
	if value, ok := s.values[name]; ok {
		return value
	}
	return defaultVal
}
