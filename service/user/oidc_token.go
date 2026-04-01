package user

import (
	"context"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/externalidentity"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/auth"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const (
	oidcAccessTokenCachePrefix = "oidc_access_token_cache_"
	oidcAccessTokenRevokeKey   = "oidc_access_token_revoke_"
	oidcJWKSCachePrefix        = "oidc_jwks_cache_"
	oidcSubjectLogoutPrefix    = "oidc_subject_logout_"
	oidcUserLogoutPrefix       = "oidc_user_logout_"
	oidcRevokeCallbackTTL      = 48 * time.Hour
	oidcRevokeCallbackSkew     = 10 * time.Minute
	oidcJWKSCacheTTLSeconds    = 600
	oidcSubjectLogoutTTL       = 30 * 24 * time.Hour
	oidcBackChannelLogoutEvent = "http://schemas.openid.net/event/backchannel-logout"
)

type oidcIntrospectionPayload struct {
	Active      *bool    `json:"active,omitempty"`
	Subject     string   `json:"sub"`
	ClientID    string   `json:"client_id"`
	Scope       string   `json:"scope"`
	Scopes      []string `json:"scopes"`
	AccessToken string   `json:"access_token"`
	Exp         int64    `json:"exp"`
	Iat         int64    `json:"iat"`
	UserID      any      `json:"user_id"`
	UserType    any      `json:"user_type"`
	TenantID    any      `json:"tenant_id"`
	Username    string   `json:"username"`
}

type oidcAccessTokenCacheEntry struct {
	LocalUserID int      `json:"local_user_id"`
	Scopes      []string `json:"scopes"`
	ExpiresAt   int64    `json:"expires_at"`
	IssuedAt    int64    `json:"issued_at"`
	Issuer      string   `json:"issuer"`
	Subject     string   `json:"subject"`
}

type oidcRevokeCallbackPayload struct {
	Event      string `json:"event"`
	Token      string `json:"token"`
	ClientID   string `json:"client_id"`
	UserID     any    `json:"user_id"`
	UserType   any    `json:"user_type"`
	RevokeType string `json:"revoke_type"`
	RevokeTime string `json:"revoke_time"`
	TenantID   any    `json:"tenant_id"`
}

type oidcJWKSetPayload struct {
	Keys []oidcJWKPayload `json:"keys"`
}

type oidcJWKPayload struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type oidcBackChannelLogoutClaims struct {
	Events   map[string]map[string]any `json:"events"`
	SID      string                    `json:"sid,omitempty"`
	Nonce    string                    `json:"nonce,omitempty"`
	UserID   any                       `json:"user_id,omitempty"`
	UserType any                       `json:"user_type,omitempty"`
	jwt.RegisteredClaims
}

// TryVerifyOIDCAccessToken 在启用统一认证时验证第三方 access token。
// 第一次命中时会访问 Yudao/标准 OIDC 提供方做 introspection，并把结果缓存到本地。
func TryVerifyOIDCAccessToken(c *gin.Context) (bool, error) {
	dep := dependency.FromContext(c)
	if !dep.SettingProvider().OIDCEnabled(c) {
		return false, nil
	}

	token := extractBearerToken(c.GetHeader(auth.AuthorizationHeader))
	if token == "" {
		return false, nil
	}
	// 统一认证模式下，后续公共文件授权会复用这枚 access token 访问 Yudao 运行时接口。
	util.WithValue(c, inventory.OIDCAccessTokenCtx{}, token)

	if isOIDCAccessTokenRevoked(c, dep, token) {
		return false, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC access token has been revoked", nil)
	}

	entry, err := getOIDCAccessTokenCache(c, dep, token)
	if err != nil {
		return false, err
	}
	if entry != nil {
		if isOIDCAccessTokenCacheEntryLoggedOut(c, dep, entry) {
			_ = dep.KV().Delete("", oidcAccessTokenCacheKey(token))
			return false, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC access token has been logged out", nil)
		}
		// Cached entry still needs live introspection so cross-system logout/revoke
		// can take effect immediately.
		oidcCfg := loadEffectiveOIDCSetting(c, dep)
		discovery, err := fetchOIDCDiscovery(c, dep, oidcCfg)
		if err != nil {
			return false, err
		}
		introspection, err := introspectOIDCAccessToken(c, dep, oidcCfg, discovery, token)
		if err != nil {
			_ = dep.KV().Delete("", oidcAccessTokenCacheKey(token))
			return false, err
		}
		issuedAt := introspection.Iat
		if issuedAt == 0 {
			issuedAt = time.Now().Unix()
		}
		if isOIDCLogoutAfter(getOIDCSubjectLogoutAt(c, dep, entry.Issuer, entry.Subject), issuedAt) {
			_ = dep.KV().Delete("", oidcAccessTokenCacheKey(token))
			return false, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC access token has been logged out", nil)
		}
		entry.ExpiresAt = introspection.Exp
		entry.IssuedAt = issuedAt
		_ = cacheOIDCAccessToken(c, dep, token, entry)
		util.WithValue(c, inventory.UserIDCtx{}, entry.LocalUserID)
		return true, nil
	}

	oidcCfg := loadEffectiveOIDCSetting(c, dep)
	discovery, err := fetchOIDCDiscovery(c, dep, oidcCfg)
	if err != nil {
		return false, err
	}

	introspection, err := introspectOIDCAccessToken(c, dep, oidcCfg, discovery, token)
	if err != nil {
		return false, err
	}

	userinfo, err := fetchOIDCUserinfo(c, dep, discovery, token)
	if err != nil {
		// Some IdP-issued access tokens (for example, password-login tokens) may not
		// carry userinfo-read scopes. If introspection already succeeded, fall back to
		// introspection-only identity reconstruction instead of hard-failing.
		dep.Logger().Warning("Failed to load OIDC userinfo, fallback to introspection-only profile: %s", err)
		userinfo = &oidcUserinfoPayload{}
	}

	profile, err := buildOIDCIdentityProfileFromAccessToken(discovery, introspection, userinfo)
	if err != nil {
		return false, err
	}

	loginUser, err := syncOIDCShadowUser(c, dep, profile)
	if err != nil {
		return false, err
	}

	util.WithValue(c, inventory.UserIDCtx{}, loginUser.ID)
	issuedAt := introspection.Iat
	if issuedAt == 0 {
		issuedAt = time.Now().Unix()
	}
	if isOIDCLogoutAfter(getOIDCSubjectLogoutAt(c, dep, profile.Issuer, profile.Subject), issuedAt) {
		return false, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC access token has been logged out", nil)
	}
	if err := cacheOIDCAccessToken(c, dep, token, &oidcAccessTokenCacheEntry{
		LocalUserID: loginUser.ID,
		ExpiresAt:   introspection.Exp,
		IssuedAt:    issuedAt,
		Issuer:      profile.Issuer,
		Subject:     profile.Subject,
	}); err != nil {
		dep.Logger().Warning("Failed to cache OIDC access token: %s", err)
	}

	return true, nil
}

func refreshOIDCToken(c *gin.Context, service *RefreshTokenService) (*auth.Token, error) {
	dep := dependency.FromContext(c)
	cfg := loadEffectiveOIDCSetting(c, dep)
	discovery, err := fetchOIDCDiscovery(c, dep, cfg)
	if err != nil {
		return nil, err
	}

	payload, err := refreshProviderToken(c, dep, cfg, discovery, service.RefreshToken)
	if err != nil {
		return nil, err
	}
	if payload.RefreshToken == "" {
		payload.RefreshToken = service.RefreshToken
	}
	if payload.IDToken == "" {
		payload.IDToken = service.IDToken
	}

	token := buildProviderToken(payload)
	return &token, nil
}

func deleteOIDCToken(c *gin.Context, service *RefreshTokenService) (string, error) {
	dep := dependency.FromContext(c)
	cfg := loadEffectiveOIDCSetting(c, dep)
	discovery, err := fetchOIDCDiscovery(c, dep, cfg)
	if err != nil {
		return "", err
	}

	revokeTarget := firstNonEmptyString(service.AccessToken, service.RefreshToken)
	if revokeTarget != "" {
		if err := revokeProviderToken(c, dep, cfg, discovery, revokeTarget); err != nil {
			dep.Logger().Warning("Failed to revoke provider token: %s", err)
		}
		if service.AccessToken != "" {
			markOIDCAccessTokenRevoked(c, dep, service.AccessToken, oidcRevokeCallbackTTL)
		}
	}

	logoutURL := buildOIDCLogoutRedirectURL(discovery, dep.SettingProvider().SiteURL(c), cfg.ClientID, service.IDToken)
	return logoutURL, nil
}

// HandleOIDCRevokeCallback 接收 Yudao 发送的 token 失效通知，并使 Cloudreve 本地缓存立即失效。
func HandleOIDCRevokeCallback(c *gin.Context) error {
	dep := dependency.FromContext(c)
	cfg := loadEffectiveOIDCSetting(c, dep)
	if !cfg.Enabled {
		return serializer.NewError(serializer.CodeFeatureNotEnabled, "OIDC sign-in is disabled", nil)
	}

	body, err := c.GetRawData()
	if err != nil {
		return serializer.NewError(serializer.CodeParamErr, "Failed to read revoke callback body", err)
	}

	timestamp := c.GetHeader("X-Callback-Timestamp")
	signature := c.GetHeader("X-Callback-Signature")
	if !verifyOIDCRevokeCallbackSignature(cfg.ClientSecret, timestamp, body, signature) {
		return serializer.NewError(serializer.CodeCredentialInvalid, "Invalid revoke callback signature", nil)
	}
	if !verifyOIDCRevokeCallbackTimestamp(timestamp, time.Now()) {
		return serializer.NewError(serializer.CodeCredentialInvalid, "OIDC revoke callback timestamp is expired", nil)
	}

	payload := &oidcRevokeCallbackPayload{}
	if err := json.Unmarshal(body, payload); err != nil {
		return serializer.NewError(serializer.CodeParamErr, "Failed to parse revoke callback payload", err)
	}
	if payload.ClientID != "" && payload.ClientID != cfg.ClientID {
		return serializer.NewError(serializer.CodeCredentialInvalid, "Revoke callback client does not match current OIDC client", nil)
	}

	if payload.Token != "" {
		markOIDCAccessTokenRevoked(c, dep, payload.Token, oidcRevokeCallbackTTL)
	}

	return nil
}

// HandleOIDCBackChannelLogout 接收标准 OIDC Back-Channel Logout 请求。
// 这里不依赖业务自定义签名，而是校验 provider 签发的 logout_token。
func HandleOIDCBackChannelLogout(c *gin.Context) error {
	dep := dependency.FromContext(c)
	cfg := loadEffectiveOIDCSetting(c, dep)
	if !cfg.Enabled {
		return serializer.NewError(serializer.CodeFeatureNotEnabled, "OIDC sign-in is disabled", nil)
	}

	logoutToken, err := extractOIDCBackChannelLogoutToken(c)
	if err != nil {
		return err
	}

	discovery, err := fetchOIDCDiscovery(c, dep, cfg)
	if err != nil {
		return err
	}

	claims, err := verifyOIDCBackChannelLogoutToken(c, dep, discovery, cfg.ClientID, logoutToken)
	if err != nil {
		return err
	}

	if claims.Subject == "" {
		return serializer.NewError(serializer.CodeCredentialInvalid, "OIDC logout token subject is empty", nil)
	}

	logoutAt := time.Now().Unix()
	if claims.IssuedAt != nil {
		logoutAt = claims.IssuedAt.Unix()
	}
	markOIDCSubjectLoggedOut(c, dep, discovery.Issuer, claims.Subject, logoutAt, oidcSubjectLogoutTTL)

	identity, err := dep.DBClient().ExternalIdentity.Query().
		Where(
			externalidentity.ProviderEQ(oidcProviderName),
			externalidentity.IssuerEQ(discovery.Issuer),
			externalidentity.SubjectEQ(claims.Subject),
		).
		Only(c)
	if err == nil {
		markOIDCLocalUserLoggedOut(c, dep, identity.UserID, logoutAt, oidcSubjectLogoutTTL)
	} else if err != nil && !ent.IsNotFound(err) {
		return serializer.NewError(serializer.CodeDBError, "Failed to query OIDC external identity", err)
	}

	return nil
}

func introspectOIDCAccessToken(c *gin.Context, dep dependency.Dep, cfg *setting.OIDCSetting, discovery *oidcDiscovery, accessToken string) (*oidcIntrospectionPayload, error) {
	endpoint := discovery.IntrospectionEndpoint
	if endpoint == "" && strings.HasSuffix(discovery.TokenEndpoint, "/token") {
		endpoint = strings.TrimSuffix(discovery.TokenEndpoint, "/token") + "/check-token"
	}
	if endpoint == "" {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "OIDC introspection endpoint is empty", nil)
	}

	form := url.Values{}
	form.Set("token", accessToken)
	header := http.Header{}
	header.Set("Content-Type", "application/x-www-form-urlencoded")
	header.Set("Authorization", "Basic "+basicOIDCCredential(cfg.ClientID, cfg.ClientSecret))

	body, err := doOIDCRequest(c, dep, http.MethodPost, endpoint, strings.NewReader(form.Encode()), header)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "Failed to introspect OIDC access token", err)
	}

	payload, err := parseOIDCPayload[oidcIntrospectionPayload](body)
	if err != nil {
		var appErr serializer.AppError
		if errors.As(err, &appErr) {
			return nil, appErr
		}
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "Failed to parse OIDC introspection response", err)
	}
	if payload.Active != nil && !*payload.Active {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC access token is inactive", nil)
	}
	if payload.Exp != 0 && time.Unix(payload.Exp, 0).Before(time.Now()) {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC access token is expired", nil)
	}
	if payload.AccessToken == "" {
		payload.AccessToken = accessToken
	}
	return payload, nil
}

func refreshProviderToken(c *gin.Context, dep dependency.Dep, cfg *setting.OIDCSetting, discovery *oidcDiscovery, refreshToken string) (*oidcTokenPayload, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	header := http.Header{}
	header.Set("Content-Type", "application/x-www-form-urlencoded")
	header.Set("Authorization", "Basic "+basicOIDCCredential(cfg.ClientID, cfg.ClientSecret))

	body, err := doOIDCRequest(c, dep, http.MethodPost, discovery.TokenEndpoint, strings.NewReader(form.Encode()), header)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "Failed to refresh OIDC access token", err)
	}

	payload, err := parseOIDCPayload[oidcTokenPayload](body)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "Failed to parse OIDC refresh response", err)
	}
	if payload.AccessToken == "" {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC access token is empty", nil)
	}
	return payload, nil
}

func revokeProviderToken(c *gin.Context, dep dependency.Dep, cfg *setting.OIDCSetting, discovery *oidcDiscovery, token string) error {
	header := http.Header{}
	header.Set("Authorization", "Basic "+basicOIDCCredential(cfg.ClientID, cfg.ClientSecret))
	header.Set("Content-Type", "application/x-www-form-urlencoded")

	if discovery.RevocationEndpoint != "" {
		form := url.Values{}
		form.Set("token", token)
		if _, err := doOIDCRequest(c, dep, http.MethodPost, discovery.RevocationEndpoint, strings.NewReader(form.Encode()), header); err == nil {
			return nil
		}
	}

	// Yudao 当前把 revocation_endpoint 暴露成 /token，但真正撤销使用 DELETE /token?token=xxx。
	if strings.HasSuffix(discovery.TokenEndpoint, "/token") {
		target := discovery.TokenEndpoint + "?token=" + url.QueryEscape(token)
		_, err := doOIDCRequest(c, dep, http.MethodDelete, target, nil, header)
		return err
	}

	return nil
}

func buildOIDCIdentityProfileFromAccessToken(discovery *oidcDiscovery, introspection *oidcIntrospectionPayload, userinfo *oidcUserinfoPayload) (*oidcIdentityProfile, error) {
	subject := firstNonEmptyString(introspection.Subject, stringFromAny(userinfo.ID), stringFromAny(introspection.UserID))
	if subject == "" {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC subject is missing", nil)
	}

	departmentID := ""
	if userinfo.Dept != nil {
		departmentID = stringFromAny(userinfo.Dept.ID)
	}

	claims := map[string]any{
		"user_id":          stringFromAny(introspection.UserID),
		"user_type":        stringFromAny(introspection.UserType),
		"tenant_id":        stringFromAny(introspection.TenantID),
		"client_id":        introspection.ClientID,
		"scope":            strings.Join(normalizeOIDCScopes(introspection.Scope, introspection.Scopes), " "),
		"external_user_id": stringFromAny(introspection.UserID),
		"username":         firstNonEmptyString(userinfo.Username, introspection.Username),
		"nickname":         userinfo.Nickname,
		"email":            userinfo.Email,
		"avatar":           userinfo.Avatar,
	}
	if departmentID != "" {
		claims["department_id"] = departmentID
	}

	return &oidcIdentityProfile{
		Issuer:         discovery.Issuer,
		Subject:        subject,
		ExternalUserID: firstNonEmptyString(stringFromAny(userinfo.ID), stringFromAny(introspection.UserID)),
		TenantID:       stringFromAny(introspection.TenantID),
		DepartmentID:   departmentID,
		Email:          strings.TrimSpace(userinfo.Email),
		Username:       firstNonEmptyString(strings.TrimSpace(userinfo.Username), strings.TrimSpace(introspection.Username)),
		Nickname:       strings.TrimSpace(userinfo.Nickname),
		Avatar:         strings.TrimSpace(userinfo.Avatar),
		Claims:         claims,
	}, nil
}

func buildOIDCLogoutRedirectURL(discovery *oidcDiscovery, siteURL *url.URL, clientID string, idToken string) string {
	if discovery.EndSessionEndpoint == "" {
		return ""
	}

	target, err := url.Parse(discovery.EndSessionEndpoint)
	if err != nil {
		return ""
	}

	postLogout := *siteURL
	postLogout.Path = strings.TrimRight(postLogout.Path, "/") + "/session"
	postLogout.RawQuery = ""
	postLogout.Fragment = ""

	query := target.Query()
	query.Set("client_id", clientID)
	query.Set("post_logout_redirect_uri", postLogout.String())
	if idToken != "" {
		query.Set("id_token_hint", idToken)
	}
	target.RawQuery = query.Encode()
	return target.String()
}

func basicOIDCCredential(clientID string, clientSecret string) string {
	return base64.StdEncoding.EncodeToString([]byte(clientID + ":" + clientSecret))
}

func normalizeOIDCScopes(scopeText string, scopes []string) []string {
	if len(scopes) > 0 {
		return scopes
	}
	if strings.TrimSpace(scopeText) == "" {
		return nil
	}
	return strings.Fields(scopeText)
}

func extractBearerToken(header string) string {
	if !strings.HasPrefix(header, auth.TokenHeaderPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, auth.TokenHeaderPrefix))
}

func getOIDCAccessTokenCache(c *gin.Context, dep dependency.Dep, token string) (*oidcAccessTokenCacheEntry, error) {
	raw, ok := dep.KV().Get(oidcAccessTokenCacheKey(token))
	if !ok {
		return nil, nil
	}

	payloadStr, ok := raw.(string)
	if !ok || payloadStr == "" {
		return nil, nil
	}

	entry := &oidcAccessTokenCacheEntry{}
	if err := json.Unmarshal([]byte(payloadStr), entry); err != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "Failed to parse OIDC access token cache", err)
	}

	if entry.ExpiresAt != 0 && time.Unix(entry.ExpiresAt, 0).Before(time.Now()) {
		_ = dep.KV().Delete("", oidcAccessTokenCacheKey(token))
		return nil, nil
	}

	return entry, nil
}

func cacheOIDCAccessToken(c *gin.Context, dep dependency.Dep, token string, entry *oidcAccessTokenCacheEntry) error {
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	ttl := time.Minute
	if entry.ExpiresAt != 0 {
		ttl = time.Until(time.Unix(entry.ExpiresAt, 0))
		if ttl <= 0 {
			ttl = time.Minute
		}
	}
	seconds := int(ttl.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return dep.KV().Set(oidcAccessTokenCacheKey(token), string(payload), seconds)
}

func markOIDCAccessTokenRevoked(c context.Context, dep dependency.Dep, token string, ttl time.Duration) {
	if token == "" {
		return
	}
	_ = dep.KV().Set(oidcAccessTokenRevokedKey(token), true, int(ttl.Seconds()))
	_ = dep.KV().Delete("", oidcAccessTokenCacheKey(token))
}

func isOIDCAccessTokenRevoked(c context.Context, dep dependency.Dep, token string) bool {
	if token == "" {
		return false
	}
	_, ok := dep.KV().Get(oidcAccessTokenRevokedKey(token))
	return ok
}

func verifyOIDCRevokeCallbackSignature(secret string, timestamp string, body []byte, signature string) bool {
	if secret == "" || timestamp == "" || signature == "" {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("\n"))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(strings.ToLower(signature)))
}

func verifyOIDCRevokeCallbackTimestamp(timestamp string, now time.Time) bool {
	parsed, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return false
	}

	callbackTime := time.Unix(parsed, 0)
	if parsed > 1_000_000_000_000 {
		callbackTime = time.UnixMilli(parsed)
	}

	diff := now.Sub(callbackTime)
	if diff < 0 {
		diff = -diff
	}

	return diff <= oidcRevokeCallbackSkew
}

func oidcAccessTokenCacheKey(token string) string {
	return oidcAccessTokenCachePrefix + hashOIDCToken(token)
}

func oidcAccessTokenRevokedKey(token string) string {
	return oidcAccessTokenRevokeKey + hashOIDCToken(token)
}

func oidcJWKSCacheKey(jwksURI string) string {
	return oidcJWKSCachePrefix + hashOIDCToken(jwksURI)
}

func oidcSubjectLogoutKey(issuer string, subject string) string {
	return oidcSubjectLogoutPrefix + hashOIDCToken(issuer+"\n"+subject)
}

func oidcUserLogoutKey(userID int) string {
	return oidcUserLogoutPrefix + strconv.Itoa(userID)
}

func hashOIDCToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func extractOIDCBackChannelLogoutToken(c *gin.Context) (string, error) {
	if logoutToken := strings.TrimSpace(c.PostForm("logout_token")); logoutToken != "" {
		return logoutToken, nil
	}

	payload := struct {
		LogoutToken string `json:"logout_token"`
	}{}
	if err := c.ShouldBindJSON(&payload); err == nil && strings.TrimSpace(payload.LogoutToken) != "" {
		return strings.TrimSpace(payload.LogoutToken), nil
	}

	return "", serializer.NewError(serializer.CodeParamErr, "OIDC logout_token is required", nil)
}

func verifyOIDCBackChannelLogoutToken(c *gin.Context, dep dependency.Dep, discovery *oidcDiscovery, clientID string, logoutToken string) (*oidcBackChannelLogoutClaims, error) {
	if strings.TrimSpace(discovery.JWKSURI) == "" {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC jwks_uri is empty", nil)
	}

	jwks, err := fetchOIDCJWKSet(c, dep, discovery.JWKSURI, false)
	if err != nil {
		return nil, err
	}

	claims := &oidcBackChannelLogoutClaims{}
	token, err := jwt.ParseWithClaims(logoutToken, claims, func(token *jwt.Token) (any, error) {
		if token.Method == nil || token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, serializer.NewError(serializer.CodeCredentialInvalid, "Unsupported OIDC logout token algorithm", nil)
		}

		kid, _ := token.Header["kid"].(string)
		key, keyErr := findOIDCJWKPublicKey(jwks, kid)
		if keyErr == nil {
			return key, nil
		}

		jwks, keyErr = fetchOIDCJWKSet(c, dep, discovery.JWKSURI, true)
		if keyErr != nil {
			return nil, keyErr
		}

		return findOIDCJWKPublicKey(jwks, kid)
	}, jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}), jwt.WithAudience(clientID), jwt.WithIssuer(discovery.Issuer), jwt.WithIssuedAt(), jwt.WithLeeway(time.Minute))
	if err != nil || token == nil || !token.Valid {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "Failed to verify OIDC logout token", err)
	}

	if claims.ID == "" {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC logout token jti is empty", nil)
	}
	if claims.Nonce != "" {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC logout token must not contain nonce", nil)
	}
	if claims.Events == nil || claims.Events[oidcBackChannelLogoutEvent] == nil {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC logout token events claim is invalid", nil)
	}
	if claims.Subject == "" && claims.SID == "" {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC logout token subject and sid are both empty", nil)
	}

	return claims, nil
}

func fetchOIDCJWKSet(c *gin.Context, dep dependency.Dep, jwksURI string, forceRefresh bool) (*oidcJWKSetPayload, error) {
	cacheKey := oidcJWKSCacheKey(jwksURI)
	if !forceRefresh {
		if raw, ok := dep.KV().Get(cacheKey); ok {
			if payloadStr, ok := raw.(string); ok && payloadStr != "" {
				payload := &oidcJWKSetPayload{}
				if err := json.Unmarshal([]byte(payloadStr), payload); err == nil && len(payload.Keys) > 0 {
					return payload, nil
				}
			}
		}
	}

	body, err := doOIDCRequest(c, dep, http.MethodGet, jwksURI, nil, nil)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "Failed to load OIDC JWKS", err)
	}

	payload := &oidcJWKSetPayload{}
	if err := json.Unmarshal(body, payload); err != nil {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "Failed to parse OIDC JWKS", err)
	}
	if len(payload.Keys) == 0 {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC JWKS is empty", nil)
	}

	_ = dep.KV().Set(cacheKey, string(body), oidcJWKSCacheTTLSeconds)
	return payload, nil
}

func findOIDCJWKPublicKey(jwks *oidcJWKSetPayload, kid string) (*rsa.PublicKey, error) {
	if jwks == nil || len(jwks.Keys) == 0 {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC JWKS is empty", nil)
	}

	for _, key := range jwks.Keys {
		if kid != "" && key.Kid != "" && key.Kid != kid {
			continue
		}
		if key.Kty != "" && key.Kty != "RSA" {
			continue
		}
		publicKey, err := buildRSAPublicKeyFromJWK(&key)
		if err == nil {
			return publicKey, nil
		}
	}

	return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC JWKS key not found", nil)
}

func buildRSAPublicKeyFromJWK(jwk *oidcJWKPayload) (*rsa.PublicKey, error) {
	if jwk == nil || jwk.N == "" || jwk.E == "" {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC JWK is incomplete", nil)
	}

	modulusBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "Failed to decode OIDC JWK modulus", err)
	}
	exponentBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "Failed to decode OIDC JWK exponent", err)
	}

	exponent := new(big.Int).SetBytes(exponentBytes).Int64()
	if exponent <= 0 {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC JWK exponent is invalid", nil)
	}

	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(modulusBytes),
		E: int(exponent),
	}, nil
}

func markOIDCSubjectLoggedOut(c context.Context, dep dependency.Dep, issuer string, subject string, logoutAt int64, ttl time.Duration) {
	if issuer == "" || subject == "" {
		return
	}
	_ = dep.KV().Set(oidcSubjectLogoutKey(issuer, subject), strconv.FormatInt(logoutAt, 10), int(ttl.Seconds()))
}

func markOIDCLocalUserLoggedOut(c context.Context, dep dependency.Dep, userID int, logoutAt int64, ttl time.Duration) {
	if userID <= 0 {
		return
	}
	_ = dep.KV().Set(oidcUserLogoutKey(userID), strconv.FormatInt(logoutAt, 10), int(ttl.Seconds()))
}

func getOIDCSubjectLogoutAt(c context.Context, dep dependency.Dep, issuer string, subject string) int64 {
	if issuer == "" || subject == "" {
		return 0
	}
	return parseOIDCLogoutAt(dep, oidcSubjectLogoutKey(issuer, subject))
}

func getOIDCLocalUserLogoutAt(c context.Context, dep dependency.Dep, userID int) int64 {
	if userID <= 0 {
		return 0
	}
	return parseOIDCLogoutAt(dep, oidcUserLogoutKey(userID))
}

func parseOIDCLogoutAt(dep dependency.Dep, key string) int64 {
	raw, ok := dep.KV().Get(key)
	if !ok {
		return 0
	}

	switch value := raw.(type) {
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err == nil {
			return parsed
		}
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	}

	return 0
}

func isOIDCAccessTokenCacheEntryLoggedOut(c context.Context, dep dependency.Dep, entry *oidcAccessTokenCacheEntry) bool {
	if entry == nil {
		return false
	}

	if isOIDCLogoutAfter(getOIDCSubjectLogoutAt(c, dep, entry.Issuer, entry.Subject), entry.IssuedAt) {
		return true
	}

	return isOIDCLogoutAfter(getOIDCLocalUserLogoutAt(c, dep, entry.LocalUserID), entry.IssuedAt)
}

func isOIDCLogoutAfter(logoutAt int64, issuedAt int64) bool {
	return logoutAt > 0 && (issuedAt == 0 || issuedAt <= logoutAt)
}
