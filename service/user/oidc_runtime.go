package user

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/gin-gonic/gin"
)

const (
	oidcRuntimeBindingConfigPath = "/app-api/authz/integration/runtime/binding-config"
	oidcWellKnownPath            = "/.well-known/openid-configuration"
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

func loadEffectiveOIDCSetting(c *gin.Context, dep dependency.Dep) *setting.OIDCSetting {
	cfg := dep.SettingProvider().OIDC(c)
	if !shouldUseOIDCRuntimeConfig(cfg) {
		return cfg
	}

	runtimeCfg, err := fetchOIDCRuntimeBindingConfig(c, dep, cfg)
	if err != nil {
		dep.Logger().Debug("Failed to load OIDC runtime binding config, fallback to local settings: %s", err)
		return cfg
	}

	applyOIDCRuntimeBindingConfig(cfg, runtimeCfg)
	return cfg
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

func fetchOIDCRuntimeBindingConfig(c *gin.Context, dep dependency.Dep, cfg *setting.OIDCSetting) (*oidcRuntimeBindingConfig, error) {
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
	targetURL.RawQuery = query.Encode()

	body, err := doOIDCRequest(c, dep, http.MethodGet, targetURL.String(), nil, nil)
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
