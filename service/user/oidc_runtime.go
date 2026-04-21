package user

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/gin-gonic/gin"
)

const (
	oidcRuntimeBindingConfigPath = "/app-api/authz/integration/runtime/binding-config"
	oidcWellKnownPath            = "/.well-known/openid-configuration"
	oidcRuntimeCacheTTL          = 5 * time.Minute
)

type oidcRuntimeBindingConfig struct {
	BindingCode  string                   `json:"bindingCode"`
	ClientID     string                   `json:"clientId"`
	ScopeText    string                   `json:"scopeText"`
	AuthProvider *oidcRuntimeAuthProvider `json:"authProvider"`
}

type oidcRuntimeAuthProvider struct {
	DiscoveryURL string `json:"discoveryUrl"`
	SsoURL       string `json:"ssoUrl"`
}

type oidcRuntimeCacheEntry struct {
	Config        *oidcRuntimeBindingConfig
	CachedAt      time.Time
	ExpiresAt     time.Time
	LastSuccessAt time.Time
	LastAttemptAt time.Time
	LastError     string
	LastErrorAt   time.Time
}

type oidcRuntimeStore struct {
	mu      sync.RWMutex
	entries map[string]*oidcRuntimeCacheEntry
}

var globalOIDCRuntimeStore = &oidcRuntimeStore{
	entries: make(map[string]*oidcRuntimeCacheEntry),
}

func loadEffectiveOIDCSetting(c *gin.Context, dep dependency.Dep) *setting.OIDCSetting {
	cfg := dep.SettingProvider().OIDC(c)
	effectiveCfg, _ := resolveEffectiveOIDCSetting(c, dep, cfg)
	return effectiveCfg
}

func LoadOIDCSettingForSiteConfig(ctx context.Context, dep dependency.Dep) *setting.OIDCSetting {
	cfg := dep.SettingProvider().OIDC(ctx)
	effectiveCfg, _ := resolveEffectiveOIDCSetting(ctx, dep, cfg)
	return effectiveCfg
}

func GetOIDCRuntimeState(ctx context.Context, dep dependency.Dep) *setting.OIDCRuntimeState {
	cfg := dep.SettingProvider().OIDC(ctx)
	_, state := resolveEffectiveOIDCSetting(ctx, dep, cfg)
	return state
}

func resolveEffectiveOIDCSetting(ctx context.Context, dep dependency.Dep, cfg *setting.OIDCSetting) (*setting.OIDCSetting, *setting.OIDCRuntimeState) {
	cfg = cloneOIDCSetting(cfg)
	if cfg == nil {
		return nil, nil
	}

	if !cfg.Enabled {
		cfg.RuntimeState = &setting.OIDCRuntimeState{
			Status: setting.OIDCRuntimeStatusDisabled,
			Source: setting.OIDCRuntimeConfigSourceLocal,
		}
		return cfg, cfg.RuntimeState
	}

	if cfg.ConfigMode != setting.OIDCConfigModeRemote {
		cfg.RuntimeState = buildOIDCRuntimeState(cfg, setting.OIDCRuntimeStatusStandard, setting.OIDCRuntimeConfigSourceLocal, nil)
		return cfg, cfg.RuntimeState
	}

	if strings.TrimSpace(cfg.WellKnownURL) == "" {
		cfg.RuntimeState = buildOIDCRuntimeState(cfg, setting.OIDCRuntimeStatusRemoteError, setting.OIDCRuntimeConfigSourceLocalFallback, &oidcRuntimeCacheEntry{
			LastError:   "OIDC remote mode requires well-known URL",
			LastErrorAt: time.Now(),
		})
		return cfg, cfg.RuntimeState
	}

	cacheKey := oidcRuntimeCacheKey(cfg)
	cachedEntry := globalOIDCRuntimeStore.get(cacheKey)
	now := time.Now()
	if cachedEntry != nil && cachedEntry.Config != nil && now.Before(cachedEntry.ExpiresAt) {
		applyOIDCRuntimeBindingConfig(cfg, cachedEntry.Config)
		cfg.RuntimeState = buildOIDCRuntimeState(cfg, setting.OIDCRuntimeStatusRemoteCached, setting.OIDCRuntimeConfigSourceRemoteCache, cachedEntry)
		return cfg, cfg.RuntimeState
	}

	entry := &oidcRuntimeCacheEntry{}
	if cachedEntry != nil {
		entry = cachedEntry.clone()
	}
	entry.LastAttemptAt = now

	runtimeCfg, err := fetchOIDCRuntimeBindingConfig(ctx, dep, cfg)
	if err == nil {
		entry.Config = runtimeCfg
		entry.CachedAt = now
		entry.ExpiresAt = now.Add(oidcRuntimeCacheTTL)
		entry.LastSuccessAt = now
		entry.LastError = ""
		entry.LastErrorAt = time.Time{}
		globalOIDCRuntimeStore.set(cacheKey, entry)
		applyOIDCRuntimeBindingConfig(cfg, runtimeCfg)
		cfg.RuntimeState = buildOIDCRuntimeState(cfg, setting.OIDCRuntimeStatusRemoteReady, setting.OIDCRuntimeConfigSourceRemote, entry)
		return cfg, cfg.RuntimeState
	}

	entry.LastError = err.Error()
	entry.LastErrorAt = now
	globalOIDCRuntimeStore.set(cacheKey, entry)
	dep.Logger().Debug("Failed to load OIDC runtime binding config, fallback to local settings: %s", err)

	if entry.Config != nil {
		applyOIDCRuntimeBindingConfig(cfg, entry.Config)
		cfg.RuntimeState = buildOIDCRuntimeState(cfg, setting.OIDCRuntimeStatusRemoteCached, setting.OIDCRuntimeConfigSourceRemoteCache, entry)
		cfg.RuntimeState.LastError = err.Error()
		cfg.RuntimeState.LastErrorAt = timePtr(entry.LastErrorAt)
		return cfg, cfg.RuntimeState
	}

	cfg.RuntimeState = buildOIDCRuntimeState(cfg, setting.OIDCRuntimeStatusLocalFallback, setting.OIDCRuntimeConfigSourceLocalFallback, entry)
	// 保留错误信息，便于管理员判断为什么当前只能回退本地设置。
	return cfg, cfg.RuntimeState
}

func shouldUseOIDCRuntimeConfig(cfg *setting.OIDCSetting) bool {
	if cfg == nil || !cfg.Enabled {
		return false
	}

	if cfg.ConfigMode != setting.OIDCConfigModeRemote {
		return false
	}

	return strings.TrimSpace(cfg.WellKnownURL) != ""
}

func fetchOIDCRuntimeBindingConfig(ctx context.Context, dep dependency.Dep, cfg *setting.OIDCSetting) (*oidcRuntimeBindingConfig, error) {
	target, err := oidcRuntimeBindingConfigURL(cfg.WellKnownURL)
	if err != nil {
		return nil, err
	}

	targetURL, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("failed to parse OIDC runtime config url: %w", err)
	}

	query := targetURL.Query()
	query.Set("bindingCode", firstNonEmptyString(strings.TrimSpace(cfg.BindingCode), "cloudreve-main"))
	if clientID := strings.TrimSpace(cfg.ClientID); clientID != "" {
		query.Set("clientId", clientID)
	}
	targetURL.RawQuery = query.Encode()

	body, err := doOIDCRequest(ctx, dep, http.MethodGet, targetURL.String(), nil, nil)
	if err != nil {
		return nil, err
	}

	payload, err := parseOIDCPayload[oidcRuntimeBindingConfig](body)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		return nil, fmt.Errorf("OIDC runtime binding config response is empty")
	}

	return payload, nil
}

func oidcRuntimeBindingConfigURL(wellKnownURL string) (string, error) {
	raw := strings.TrimSpace(wellKnownURL)
	if raw == "" {
		return "", fmt.Errorf("oidc well-known url is empty")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid oidc well-known url: %w", err)
	}

	if !strings.HasSuffix(parsed.Path, oidcWellKnownPath) {
		return "", fmt.Errorf("failed to derive oidc runtime config url from %q", raw)
	}

	basePath := strings.TrimSuffix(parsed.Path, oidcWellKnownPath)
	if basePath == "" {
		parsed.Path = oidcRuntimeBindingConfigPath
	} else {
		parsed.Path = strings.TrimRight(basePath, "/") + oidcRuntimeBindingConfigPath
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return parsed.String(), nil
}

func applyOIDCRuntimeBindingConfig(cfg *setting.OIDCSetting, runtimeCfg *oidcRuntimeBindingConfig) {
	if cfg == nil || runtimeCfg == nil {
		return
	}

	cfg.BindingCode = firstNonEmptyString(strings.TrimSpace(runtimeCfg.BindingCode), strings.TrimSpace(cfg.BindingCode), "cloudreve-main")

	if clientID := strings.TrimSpace(runtimeCfg.ClientID); clientID != "" {
		cfg.ClientID = clientID
	}
	if scopeText := strings.TrimSpace(runtimeCfg.ScopeText); scopeText != "" {
		cfg.Scope = scopeText
	}
	if runtimeCfg.AuthProvider == nil {
		return
	}
	if discoveryURL := strings.TrimSpace(runtimeCfg.AuthProvider.DiscoveryURL); discoveryURL != "" {
		cfg.WellKnownURL = discoveryURL
	}
	if ssoURL := strings.TrimSpace(runtimeCfg.AuthProvider.SsoURL); ssoURL != "" {
		cfg.SSOURL = ssoURL
	}
}

func cloneOIDCSetting(cfg *setting.OIDCSetting) *setting.OIDCSetting {
	if cfg == nil {
		return nil
	}
	cloned := *cfg
	if cfg.RuntimeState != nil {
		state := *cfg.RuntimeState
		cloned.RuntimeState = &state
	}
	return &cloned
}

func buildOIDCRuntimeState(cfg *setting.OIDCSetting, status setting.OIDCRuntimeStatus, source setting.OIDCRuntimeConfigSource, entry *oidcRuntimeCacheEntry) *setting.OIDCRuntimeState {
	state := &setting.OIDCRuntimeState{
		Status:       status,
		Source:       source,
		BindingCode:  strings.TrimSpace(cfg.BindingCode),
		ClientID:     strings.TrimSpace(cfg.ClientID),
		Scope:        strings.TrimSpace(cfg.Scope),
		SSOURL:       strings.TrimSpace(cfg.SSOURL),
		WellKnownURL: strings.TrimSpace(cfg.WellKnownURL),
	}

	if entry == nil {
		return state
	}

	state.CacheExpiresAt = timePtr(entry.ExpiresAt)
	state.LastSuccessAt = timePtr(entry.LastSuccessAt)
	state.LastAttemptAt = timePtr(entry.LastAttemptAt)
	state.LastError = strings.TrimSpace(entry.LastError)
	state.LastErrorAt = timePtr(entry.LastErrorAt)
	return state
}

func oidcRuntimeCacheKey(cfg *setting.OIDCSetting) string {
	if cfg == nil {
		return ""
	}
	return fmt.Sprintf("%s|%s", strings.TrimSpace(cfg.WellKnownURL), firstNonEmptyString(strings.TrimSpace(cfg.BindingCode), "cloudreve-main"))
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	value := t
	return &value
}

func (e *oidcRuntimeCacheEntry) clone() *oidcRuntimeCacheEntry {
	if e == nil {
		return nil
	}
	cloned := *e
	if e.Config != nil {
		config := *e.Config
		if e.Config.AuthProvider != nil {
			authProvider := *e.Config.AuthProvider
			config.AuthProvider = &authProvider
		}
		cloned.Config = &config
	}
	return &cloned
}

func (s *oidcRuntimeStore) get(key string) *oidcRuntimeCacheEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.entries[key]
	if !ok {
		return nil
	}
	return entry.clone()
}

func (s *oidcRuntimeStore) set(key string, entry *oidcRuntimeCacheEntry) {
	if strings.TrimSpace(key) == "" || entry == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = entry.clone()
}
