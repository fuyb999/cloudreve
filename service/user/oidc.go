package user

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/externalidentity"
	"github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/auth"
	crrequest "github.com/cloudreve/Cloudreve/v4/pkg/request"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const (
	oidcProviderName = "oidc"
	// state 只在 Cloudreve 本地短暂缓存，用来同时承载防 CSRF 校验和登录后跳转地址。
	oidcStatePrefix = "oidc_login_state_"
	oidcStateTTL    = 600
)

type (
	// OIDCPrepareParameterCtx 标记统一认证预处理接口的参数上下文。
	OIDCPrepareParameterCtx struct{}
	// OIDCPrepareService 负责生成统一认证入口地址，并把登录后的目标地址绑定到 state。
	OIDCPrepareService struct {
		Next string `form:"next"`
	}
	// OIDCPrepareResponse 返回前端需要跳转到的统一认证地址，以及本地生成的 state。
	OIDCPrepareResponse struct {
		RedirectURL string `json:"redirect_url"`
		State       string `json:"state"`
	}
	// OIDCExchangeParameterCtx 标记授权码换取本地会话接口的参数上下文。
	OIDCExchangeParameterCtx struct{}
	// OIDCExchangeService 接收前端回传的 code/state，完成远端换票和本地登录。
	OIDCExchangeService struct {
		Code  string `json:"code" binding:"required"`
		State string `json:"state" binding:"required"`
	}
	// OIDCExchangeResponse 返回 Cloudreve 本地影子用户和上游统一认证中心签发的 token。
	OIDCExchangeResponse struct {
		User       User       `json:"user"`
		Token      auth.Token `json:"token"`
		RedirectTo string     `json:"redirect_to,omitempty"`
	}
)

type oidcDiscovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
	IntrospectionEndpoint string `json:"introspection_endpoint"`
	EndSessionEndpoint    string `json:"end_session_endpoint"`
}

type oidcTokenPayload struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
	Scope            string `json:"scope"`
	IDToken          string `json:"id_token"`
}

type oidcUserinfoPayload struct {
	ID                any    `json:"id"`
	Sub               string `json:"sub"`
	Username          string `json:"username"`
	PreferredUsername string `json:"preferred_username"`
	Nickname          string `json:"nickname"`
	Name              string `json:"name"`
	Email             string `json:"email"`
	Mobile            string `json:"mobile"`
	Avatar            string `json:"avatar"`
	Picture           string `json:"picture"`
	Dept              *struct {
		ID   any    `json:"id"`
		Name string `json:"name"`
	} `json:"dept"`
}

type oidcEnvelope[T any] struct {
	Code int    `json:"code"`
	Data T      `json:"data"`
	Msg  string `json:"msg"`
}

type oidcStatePayload struct {
	Next         string `json:"next"`
	CodeVerifier string `json:"code_verifier"`
}

// oidcIdentityProfile 把第三方平台返回的身份字段归一化，后续仅围绕这个结构同步本地影子用户。
type oidcIdentityProfile struct {
	Issuer         string
	Subject        string
	ExternalUserID string
	TenantID       string
	DepartmentID   string
	Email          string
	Username       string
	Nickname       string
	Avatar         string
	Claims         map[string]any
}

// Prepare 读取 OIDC 配置和发现文档，生成前端登录入口地址。
func (service *OIDCPrepareService) Prepare(c *gin.Context) (*OIDCPrepareResponse, error) {
	dep := dependency.FromContext(c)
	oidcSetting := loadEffectiveOIDCSetting(c, dep)
	if !oidcSetting.Enabled {
		return nil, serializer.NewError(serializer.CodeFeatureNotEnabled, "OIDC sign-in is disabled", nil)
	}

	if oidcSetting.ClientID == "" || oidcSetting.ClientSecret == "" || oidcSetting.WellKnownURL == "" {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "OIDC settings are incomplete", nil)
	}

	// 先拉取发现文档，确保 token/userinfo 端点和 issuer 都可用。
	discovery, err := fetchOIDCDiscovery(c, dep, oidcSetting)
	if err != nil {
		return nil, err
	}

	state := util.RandStringRunesCrypto(32)
	next := sanitizeOIDCRedirectTarget(service.Next)
	codeVerifier := util.RandStringRunesCrypto(64)
	// state 只保存短期登录上下文，不把跳转目标直接暴露给前端拼接。
	statePayload, err := json.Marshal(&oidcStatePayload{
		Next:         next,
		CodeVerifier: codeVerifier,
	})
	if err != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "Failed to encode OIDC login session", err)
	}
	if err := dep.KV().Set(oidcStateKey(state), string(statePayload), oidcStateTTL); err != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "Failed to create OIDC login session", err)
	}

	redirectURL, err := buildOIDCRedirectURL(oidcSetting, discovery, state, codeVerifier, oidcSPACallbackURL(dep.SettingProvider().SiteURL(c)))
	if err != nil {
		_ = dep.KV().Delete("", oidcStateKey(state))
		return nil, err
	}

	return &OIDCPrepareResponse{
		RedirectURL: redirectURL,
		State:       state,
	}, nil
}

// Exchange 用授权码换取上游 access token，再同步/创建本地影子用户并把 provider token 直接返回给前端。
func (service *OIDCExchangeService) Exchange(c *gin.Context) (*OIDCExchangeResponse, error) {
	dep := dependency.FromContext(c)
	oidcSetting := loadEffectiveOIDCSetting(c, dep)
	if !oidcSetting.Enabled {
		return nil, serializer.NewError(serializer.CodeFeatureNotEnabled, "OIDC sign-in is disabled", nil)
	}

	stateRaw, ok := dep.KV().Get(oidcStateKey(service.State))
	if !ok {
		return nil, serializer.NewError(serializer.CodeLoginSessionNotExist, "OIDC login session not found or expired", nil)
	}
	// state 只允许消费一次，避免授权码回放。
	_ = dep.KV().Delete("", oidcStateKey(service.State))

	statePayload, err := parseOIDCStatePayload(stateRaw)
	if err != nil {
		return nil, err
	}

	discovery, err := fetchOIDCDiscovery(c, dep, oidcSetting)
	if err != nil {
		return nil, err
	}

	callbackURL := oidcSPACallbackURL(dep.SettingProvider().SiteURL(c))
	tokenPayload, err := exchangeOIDCCode(c, dep, oidcSetting, discovery, callbackURL, statePayload.CodeVerifier, service)
	if err != nil {
		return nil, err
	}

	userinfoPayload, err := fetchOIDCUserinfo(c, dep, discovery, tokenPayload.AccessToken)
	if err != nil {
		return nil, err
	}

	profile, err := buildOIDCIdentityProfile(discovery, tokenPayload, userinfoPayload)
	if err != nil {
		return nil, err
	}

	loginUser, err := syncOIDCShadowUser(c, dep, profile)
	if err != nil {
		return nil, err
	}

	accessExpiresAt := time.Now().Add(time.Duration(tokenPayload.ExpiresIn) * time.Second).Unix()
	issuedAt := extractJWTIssuedAt(tokenPayload.IDToken)
	if issuedAt == 0 {
		issuedAt = time.Now().Unix()
	}
	if isOIDCLogoutAfter(getOIDCSubjectLogoutAt(c, dep, profile.Issuer, profile.Subject), issuedAt) {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC access token has been logged out", nil)
	}
	if err := cacheOIDCAccessToken(c, dep, tokenPayload.AccessToken, &oidcAccessTokenCacheEntry{
		LocalUserID: loginUser.ID,
		Scopes:      normalizeOIDCScopes(tokenPayload.Scope, nil),
		ExpiresAt:   accessExpiresAt,
		IssuedAt:    issuedAt,
		Issuer:      profile.Issuer,
		Subject:     profile.Subject,
	}); err != nil {
		dep.Logger().Warning("Failed to warm OIDC access token cache: %s", err)
	}

	if err := afterLoginSuccess(c, loginUser); err != nil {
		return nil, err
	}

	return &OIDCExchangeResponse{
		User:       BuildUser(loginUser, dep.HashIDEncoder()),
		Token:      buildProviderToken(tokenPayload),
		RedirectTo: sanitizeOIDCRedirectTarget(statePayload.Next),
	}, nil
}

// fetchOIDCDiscovery 从 well-known 文档解析出后续换票和取用户信息所需的端点。
func fetchOIDCDiscovery(c *gin.Context, dep dependency.Dep, cfg *setting.OIDCSetting) (*oidcDiscovery, error) {
	if cfg.WellKnownURL == "" {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "OIDC discovery URL is empty", nil)
	}

	body, err := doOIDCRequest(c, dep, http.MethodGet, cfg.WellKnownURL, nil, nil)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "Failed to load OIDC discovery document", err)
	}

	var discovery oidcDiscovery
	if err := json.Unmarshal(body, &discovery); err != nil {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "Failed to parse OIDC discovery document", err)
	}

	if discovery.Issuer == "" || discovery.TokenEndpoint == "" || discovery.UserinfoEndpoint == "" {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC discovery document is incomplete", nil)
	}

	return &discovery, nil
}

// buildOIDCRedirectURL 生成跳到统一认证前端入口的地址。
// 兼容策略：
// 1. 管理员显式配置 oidc_sso_url 时优先使用；
// 2. 如果发现文档是 Yudao 风格的后端 authorize 端点，则改走前端 /sso 页面，避免未登录时直接返回 401；
// 3. 其它标准 OIDC 提供方继续走 authorization_endpoint。
func buildOIDCRedirectURL(cfg *setting.OIDCSetting, discovery *oidcDiscovery, state string, codeVerifier string, callbackURL string) (string, error) {
	redirectURL := resolveOIDCLoginEntryURL(cfg, discovery)
	if redirectURL == "" {
		return "", serializer.NewError(serializer.CodeInternalSetting, "OIDC authorization URL is empty", nil)
	}

	parsed, err := url.Parse(redirectURL)
	if err != nil {
		return "", serializer.NewError(serializer.CodeInternalSetting, "Invalid OIDC authorization URL", err)
	}

	scope := strings.TrimSpace(cfg.Scope)
	if scope == "" {
		scope = "openid user_info user.read"
	}

	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("client_id", cfg.ClientID)
	query.Set("redirect_uri", callbackURL)
	query.Set("state", state)
	query.Set("scope", scope)
	if codeVerifier != "" {
		query.Set("code_challenge", oidcCodeChallenge(codeVerifier))
		query.Set("code_challenge_method", "S256")
	}
	parsed.RawQuery = query.Encode()

	return parsed.String(), nil
}

func resolveOIDCLoginEntryURL(cfg *setting.OIDCSetting, discovery *oidcDiscovery) string {
	redirectURL := ""
	if cfg != nil {
		redirectURL = strings.TrimSpace(cfg.SSOURL)
		if redirectURL != "" {
			return normalizeOIDCLoginEntryURL(redirectURL, discovery)
		}
	}

	if inferred := inferYudaoSSOURL(discovery); inferred != "" {
		return inferred
	}

	redirectURL = strings.TrimSpace(discovery.AuthorizationEndpoint)
	if redirectURL != "" {
		return redirectURL
	}

	return normalizeOIDCLoginEntryURL(strings.TrimRight(discovery.Issuer, "/")+"/sso", discovery)
}

func normalizeOIDCLoginEntryURL(raw string, discovery *oidcDiscovery) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	// 如果管理员把 Yudao 的后端 authorize 端点填到了 SSO 地址中，
	// 这里统一改回前端 /sso 页面，避免浏览器直接看到 401 JSON。
	if isYudaoAuthorizePath(parsed.Path) {
		parsed.Path = "/sso"
		parsed.RawPath = ""
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String()
	}

	// 如果管理员误填了统一认证站点根地址，这里自动补成 /sso，避免直接命中后端根路径。
	if parsed.Path == "" || parsed.Path == "/" {
		if inferred := inferYudaoSSOURL(discovery); inferred != "" {
			return inferred
		}
		parsed.Path = "/sso"
		parsed.RawPath = ""
		return parsed.String()
	}

	return raw
}

func inferYudaoSSOURL(discovery *oidcDiscovery) string {
	authEndpoint := strings.TrimSpace(discovery.AuthorizationEndpoint)
	if authEndpoint == "" {
		return ""
	}

	parsed, err := url.Parse(authEndpoint)
	if err != nil {
		return ""
	}

	if !isYudaoAuthorizePath(parsed.Path) {
		return ""
	}

	if issuerURL := strings.TrimSpace(discovery.Issuer); issuerURL != "" {
		if issuerParsed, err := url.Parse(issuerURL); err == nil && issuerParsed.Scheme != "" && issuerParsed.Host != "" {
			issuerParsed.Path = strings.TrimRight(issuerParsed.Path, "/") + "/sso"
			issuerParsed.RawPath = ""
			issuerParsed.RawQuery = ""
			issuerParsed.Fragment = ""
			return issuerParsed.String()
		}
	}

	parsed.Path = "/sso"
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func isYudaoAuthorizePath(path string) bool {
	return strings.HasSuffix(strings.TrimSpace(path), "/admin-api/system/oauth2/authorize")
}

// exchangeOIDCCode 使用授权码换取 access token。兼容标准 OIDC 响应和 Yudao 的 CommonResult 包装。
func exchangeOIDCCode(c *gin.Context, dep dependency.Dep, cfg *setting.OIDCSetting, discovery *oidcDiscovery, callbackURL string, codeVerifier string, service *OIDCExchangeService) (*oidcTokenPayload, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", service.Code)
	form.Set("redirect_uri", callbackURL)
	form.Set("state", service.State)
	if codeVerifier != "" {
		form.Set("code_verifier", codeVerifier)
	}

	header := http.Header{}
	header.Set("Content-Type", "application/x-www-form-urlencoded")
	header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(cfg.ClientID+":"+cfg.ClientSecret)))

	body, err := doOIDCRequest(c, dep, http.MethodPost, discovery.TokenEndpoint, strings.NewReader(form.Encode()), header)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "Failed to exchange OIDC authorization code", err)
	}

	tokenPayload, err := parseOIDCPayload[oidcTokenPayload](body)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "Failed to parse OIDC token response", err)
	}

	if tokenPayload.AccessToken == "" {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC access token is empty", nil)
	}

	return tokenPayload, nil
}

// fetchOIDCUserinfo 使用 access token 拉取外部用户资料。
func fetchOIDCUserinfo(c *gin.Context, dep dependency.Dep, discovery *oidcDiscovery, accessToken string) (*oidcUserinfoPayload, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+accessToken)

	body, err := doOIDCRequest(c, dep, http.MethodGet, discovery.UserinfoEndpoint, nil, header)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "Failed to load OIDC userinfo", err)
	}

	payload, err := parseOIDCPayload[oidcUserinfoPayload](body)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "Failed to parse OIDC userinfo response", err)
	}

	return payload, nil
}

// buildOIDCIdentityProfile 把 ID Token 与 userinfo 中的字段整理成统一结构。
// 这里对 ID Token 只做“非校验解析”，目的仅是补齐 subject / issuer / tenant 等辅助字段，
// 真正用于登录准入的核心身份仍以成功换票后的会话链路为前提。
func buildOIDCIdentityProfile(discovery *oidcDiscovery, tokenPayload *oidcTokenPayload, userinfo *oidcUserinfoPayload) (*oidcIdentityProfile, error) {
	claims := map[string]any{}
	if tokenPayload.IDToken != "" {
		unverifiedClaims := jwt.MapClaims{}
		if _, _, err := jwt.NewParser().ParseUnverified(tokenPayload.IDToken, unverifiedClaims); err == nil {
			for k, v := range unverifiedClaims {
				claims[k] = v
			}
		}
	}

	subject := stringFromAny(claims["sub"])
	externalUserID := firstNonEmptyString(stringFromAny(userinfo.ID), strings.TrimSpace(userinfo.Sub))
	if externalUserID == "" {
		externalUserID = stringFromAny(claims["user_id"])
	}
	if subject == "" {
		subject = externalUserID
	}
	if subject == "" {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC subject is missing", nil)
	}

	issuer := strings.TrimSpace(discovery.Issuer)
	if issuer == "" {
		issuer = stringFromAny(claims["iss"])
	}
	if issuer == "" {
		return nil, serializer.NewError(serializer.CodeCredentialInvalid, "OIDC issuer is missing", nil)
	}

	departmentID := ""
	if userinfo.Dept != nil {
		departmentID = stringFromAny(userinfo.Dept.ID)
	}
	if departmentID == "" {
		departmentID = firstNonEmptyString(stringFromAny(claims["dept_id"]), stringFromAny(claims["deptId"]))
	}

	if len(claims) == 0 {
		claims = map[string]any{}
	}
	claims["external_user_id"] = externalUserID
	claims["username"] = firstNonEmptyString(userinfo.Username, userinfo.PreferredUsername)
	claims["nickname"] = firstNonEmptyString(userinfo.Nickname, userinfo.Name)
	claims["email"] = userinfo.Email
	claims["avatar"] = firstNonEmptyString(userinfo.Avatar, userinfo.Picture)
	if departmentID != "" {
		claims["department_id"] = departmentID
	}

	return &oidcIdentityProfile{
		Issuer:         issuer,
		Subject:        subject,
		ExternalUserID: externalUserID,
		TenantID:       firstNonEmptyString(stringFromAny(claims["tenant_id"]), stringFromAny(claims["tenantId"])),
		DepartmentID:   departmentID,
		Email:          strings.TrimSpace(userinfo.Email),
		Username:       firstNonEmptyString(strings.TrimSpace(userinfo.Username), strings.TrimSpace(userinfo.PreferredUsername)),
		Nickname:       firstNonEmptyString(strings.TrimSpace(userinfo.Nickname), strings.TrimSpace(userinfo.Name)),
		Avatar:         firstNonEmptyString(strings.TrimSpace(userinfo.Avatar), strings.TrimSpace(userinfo.Picture)),
		Claims:         claims,
	}, nil
}

// syncOIDCShadowUser 负责维护“外部身份 <-> Cloudreve 本地用户”的绑定关系。
// Cloudreve 仍保留本地 user_id，这样现有 owner_id、tree_path、配额、文件归属逻辑都无需重写。
func syncOIDCShadowUser(c *gin.Context, dep dependency.Dep, profile *oidcIdentityProfile) (*ent.User, error) {
	tx, err := dep.DBClient().Tx(c)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to start OIDC login transaction", err)
	}

	userClient := inventory.NewUserClient(tx.Client())
	var identity *ent.ExternalIdentity
	identity, err = tx.ExternalIdentity.Query().
		Where(
			externalidentity.ProviderEQ(oidcProviderName),
			externalidentity.IssuerEQ(profile.Issuer),
			externalidentity.SubjectEQ(profile.Subject),
		).
		Only(c)
	if err != nil && !ent.IsNotFound(err) {
		_ = tx.Rollback()
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to query external identity", err)
	}

	var currentUser *ent.User
	createdShadowUser := false
	targetUserID, err := resolveOIDCLocalUserID(profile)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if identity != nil {
		currentUser, err = userClient.GetByID(c, identity.UserID)
		if err != nil && !ent.IsNotFound(err) {
			_ = tx.Rollback()
			return nil, serializer.NewError(serializer.CodeDBError, "Failed to query linked user", err)
		}
		if currentUser != nil && currentUser.ID != targetUserID {
			_ = tx.Rollback()
			return nil, serializer.NewError(serializer.CodeDBError,
				fmt.Sprintf("OIDC local user id mismatch: mapped=%d external=%d", currentUser.ID, targetUserID), nil)
		}
	}

	if currentUser == nil {
		currentUser, err = userClient.GetByID(c, targetUserID)
		if err != nil && !ent.IsNotFound(err) {
			_ = tx.Rollback()
			return nil, serializer.NewError(serializer.CodeDBError, "Failed to query user by target id", err)
		}
	}

	if currentUser == nil {
		email := profile.Email
		if email == "" {
			// 外部身份没有邮箱时，生成一个稳定占位邮箱，避免破坏本地用户唯一键约束。
			email = oidcPlaceholderEmail(profile.Subject)
		}
		existedEmail, emailErr := tx.User.Query().
			Where(user.EmailEqualFold(email), user.IDNEQ(targetUserID)).
			Exist(c)
		if emailErr != nil {
			_ = tx.Rollback()
			return nil, serializer.NewError(serializer.CodeDBError, "Failed to check email collision", emailErr)
		}
		if existedEmail {
			_ = tx.Rollback()
			return nil, serializer.NewError(serializer.CodeDBError,
				fmt.Sprintf("OIDC email %q already exists on another local user, target id=%d", email, targetUserID), nil)
		}

		username := selectOIDCUsername(profile)
		username, err = inventory.NextAvailableUsername(c, tx.Client(), username, 0)
		if err != nil {
			_ = tx.Rollback()
			return nil, serializer.NewError(serializer.CodeDBError, "Failed to allocate shadow username", err)
		}

		currentUser, err = userClient.Create(c, &inventory.NewUserArgs{
			RawID:    targetUserID,
			Username: username,
			Email:    email,
			Nick:     selectOIDCNickname(profile),
			Status:   user.StatusActive,
			GroupID:  dep.SettingProvider().DefaultGroup(c),
			Avatar:   profile.Avatar,
		})
		if err != nil {
			_ = tx.Rollback()
			return nil, serializer.NewError(serializer.CodeDBError, "Failed to create shadow user", err)
		}
		createdShadowUser = true
		settingClient := dep.SettingClient().SetClient(tx.Client()).(inventory.SettingClient)
		registrationDisabled, err := disableOpenRegistrationAfterFirstSignup(c, userClient, settingClient, currentUser)
		if err != nil {
			_ = tx.Rollback()
			return nil, serializer.NewError(serializer.CodeDBError, "Failed to update registration setting", err)
		}
		if registrationDisabled {
			util.WithValue(c, registrationDisabledCtx{}, true)
		}
	}

	if currentUser.Status == user.StatusManualBanned || currentUser.Status == user.StatusSysBanned {
		_ = tx.Rollback()
		return nil, serializer.NewError(serializer.CodeUserBaned, "This account has been blocked", nil)
	}
	if currentUser.Status == user.StatusInactive {
		_ = tx.Rollback()
		return nil, serializer.NewError(serializer.CodeUserNotActivated, "This account is not activated", nil)
	}

	currentUser, err = updateOIDCShadowUser(c, tx.Client(), currentUser, profile)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}

	if identity == nil {
		identity, err = tx.ExternalIdentity.Create().
			SetProvider(oidcProviderName).
			SetIssuer(profile.Issuer).
			SetSubject(profile.Subject).
			SetExternalUserID(profile.ExternalUserID).
			SetTenantID(profile.TenantID).
			SetDepartmentID(profile.DepartmentID).
			SetEmail(profile.Email).
			SetUsername(profile.Username).
			SetNickname(profile.Nickname).
			SetAvatar(profile.Avatar).
			SetClaims(profile.Claims).
			SetUserID(currentUser.ID).
			Save(c)
		if err != nil {
			_ = tx.Rollback()
			return nil, serializer.NewError(serializer.CodeDBError, "Failed to create external identity", err)
		}
	} else {
		identity, err = tx.ExternalIdentity.UpdateOneID(identity.ID).
			SetExternalUserID(profile.ExternalUserID).
			SetTenantID(profile.TenantID).
			SetDepartmentID(profile.DepartmentID).
			SetEmail(profile.Email).
			SetUsername(profile.Username).
			SetNickname(profile.Nickname).
			SetAvatar(profile.Avatar).
			SetClaims(profile.Claims).
			SetUserID(currentUser.ID).
			Save(c)
		if err != nil {
			_ = tx.Rollback()
			return nil, serializer.NewError(serializer.CodeDBError, "Failed to update external identity", err)
		}
	}

	currentUser, err = promoteFirstOIDCIdentityUserToAdmin(c, currentUser, identity)
	if err != nil {
		_ = tx.Rollback()
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to promote first OIDC user to admin", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to commit OIDC login transaction", err)
	}
	if createdShadowUser {
		registrationDisabled, _ := c.Value(registrationDisabledCtx{}).(bool)
		if err := invalidateOpenRegistrationCache(dep.KV(), registrationDisabled); err != nil {
			dep.Logger().Warning("Failed to clear registration setting cache after OIDC signup: %s", err)
		}
	}

	ctx := context.WithValue(c, inventory.LoadUserGroup{}, true)
	loginUser, err := dep.UserClient().GetByID(ctx, currentUser.ID)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to reload OIDC user", err)
	}

	return loginUser, nil
}

func resolveOIDCLocalUserID(profile *oidcIdentityProfile) (int, error) {
	if profile == nil {
		return 0, serializer.NewError(serializer.CodeDBError, "OIDC profile is nil", nil)
	}

	raw := strings.TrimSpace(profile.ExternalUserID)
	if raw == "" {
		return 0, serializer.NewError(serializer.CodeDBError, "OIDC external_user_id is empty", nil)
	}

	userID, err := strconv.Atoi(raw)
	if err != nil || userID <= 0 {
		return 0, serializer.NewError(serializer.CodeDBError,
			fmt.Sprintf("OIDC external_user_id %q is not a valid positive integer", raw), err)
	}

	return userID, nil
}

// updateOIDCShadowUser 只同步允许由统一认证覆盖的可变字段，避免误伤本地业务字段。
// 其中邮箱以统一认证返回为准，只要不与其它本地用户冲突，就直接覆盖旧值。
func updateOIDCShadowUser(c *gin.Context, client *ent.Client, currentUser *ent.User, profile *oidcIdentityProfile) (*ent.User, error) {
	update := client.User.UpdateOneID(currentUser.ID)
	changed := false

	nickname := selectOIDCNickname(profile)
	if nickname != "" && currentUser.Nick != nickname {
		update.SetNick(nickname)
		changed = true
	}

	if profile.Username != "" && !strings.EqualFold(userUsernameValue(currentUser.Username), profile.Username) {
		existed, err := client.User.Query().
			Where(user.UsernameEqualFold(profile.Username), user.IDNEQ(currentUser.ID)).
			Exist(c)
		if err != nil {
			return nil, serializer.NewError(serializer.CodeDBError, "Failed to check username collision", err)
		}
		if !existed {
			update.SetUsername(profile.Username)
			changed = true
		}
	}

	if profile.Avatar != "" && currentUser.Avatar != profile.Avatar {
		update.SetAvatar(profile.Avatar)
		changed = true
	}

	if profile.Email != "" && !strings.EqualFold(currentUser.Email, profile.Email) {
		existed, err := client.User.Query().
			Where(user.EmailEqualFold(profile.Email), user.IDNEQ(currentUser.ID)).
			Exist(c)
		if err != nil {
			return nil, serializer.NewError(serializer.CodeDBError, "Failed to check email collision", err)
		}
		if !existed {
			update.SetEmail(profile.Email)
			changed = true
		}
	}

	if !changed {
		return currentUser, nil
	}

	updatedUser, err := update.Save(c)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to update shadow user", err)
	}

	return updatedUser, nil
}

func parseOIDCStatePayload(raw any) (*oidcStatePayload, error) {
	payloadStr, ok := raw.(string)
	if !ok || strings.TrimSpace(payloadStr) == "" {
		return nil, serializer.NewError(serializer.CodeLoginSessionNotExist, "OIDC login session payload is invalid", nil)
	}

	payload := &oidcStatePayload{}
	if err := json.Unmarshal([]byte(payloadStr), payload); err != nil {
		return nil, serializer.NewError(serializer.CodeLoginSessionNotExist, "Failed to parse OIDC login session", err)
	}

	return payload, nil
}

func oidcCodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func buildProviderToken(payload *oidcTokenPayload) auth.Token {
	now := time.Now()
	accessExpires := now
	if payload.ExpiresIn > 0 {
		accessExpires = now.Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	refreshExpires := accessExpires
	if payload.RefreshExpiresIn > 0 {
		refreshExpires = now.Add(time.Duration(payload.RefreshExpiresIn) * time.Second)
	} else if payload.RefreshToken != "" {
		refreshExpires = now.Add(30 * 24 * time.Hour)
	}

	return auth.Token{
		AccessToken:    payload.AccessToken,
		RefreshToken:   payload.RefreshToken,
		AccessExpires:  accessExpires,
		RefreshExpires: refreshExpires,
		IDToken:        payload.IDToken,
	}
}

func parseOIDCPayload[T any](body []byte) (*T, error) {
	envelope := &oidcEnvelope[json.RawMessage]{}
	if err := json.Unmarshal(body, envelope); err == nil && (envelope.Code != 0 || len(envelope.Data) > 0 || envelope.Msg != "") {
		if envelope.Code != 0 {
			msg := envelope.Msg
			if msg == "" {
				msg = "OIDC endpoint rejected the request"
			}
			return nil, serializer.NewError(serializer.CodeCredentialInvalid, msg, nil)
		}

		payload := new(T)
		if len(envelope.Data) == 0 {
			return payload, nil
		}
		if err := json.Unmarshal(envelope.Data, payload); err != nil {
			return nil, err
		}
		return payload, nil
	}

	payload := new(T)
	if err := json.Unmarshal(body, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// doOIDCRequest 封装统一认证相关的出站 HTTP 请求，统一超时、上下文和错误处理。
func doOIDCRequest(c *gin.Context, dep dependency.Dep, method string, target string, body io.Reader, header http.Header) ([]byte, error) {
	resp := dep.RequestClient().Request(
		method,
		target,
		body,
		crrequest.WithTimeout(15*time.Second),
		crrequest.WithContext(c),
		crrequest.WithHeader(header),
	)

	content, err := resp.CheckHTTPResponse(200).GetResponse()
	if err != nil {
		return nil, err
	}

	return []byte(content), nil
}

// oidcSPACallbackURL 始终返回前端 SPA 回调地址，避免第三方平台回调到后端页面。
func oidcSPACallbackURL(base *url.URL) string {
	callback := *base
	callback.Path = strings.TrimRight(callback.Path, "/") + "/session/oidc/callback"
	callback.RawQuery = ""
	callback.Fragment = ""
	return callback.String()
}

func oidcStateKey(state string) string {
	return oidcStatePrefix + state
}

// sanitizeOIDCRedirectTarget 只允许站内相对路径，防止统一认证完成后被利用做开放重定向。
func sanitizeOIDCRedirectTarget(next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return "/home"
	}

	parsed, err := url.Parse(next)
	if err != nil || parsed.IsAbs() || parsed.Host != "" {
		return "/home"
	}

	if parsed.Path == "" {
		parsed.Path = "/home"
	}
	if !strings.HasPrefix(parsed.Path, "/") {
		parsed.Path = "/" + parsed.Path
	}

	return parsed.RequestURI()
}

// oidcPlaceholderEmail 为无邮箱的外部账号生成稳定占位地址，便于后续重复登录命中同一用户。
func oidcPlaceholderEmail(subject string) string {
	sum := sha1.Sum([]byte(subject))
	return fmt.Sprintf("oidc-%s@local.invalid", hex.EncodeToString(sum[:8]))
}

func selectOIDCNickname(profile *oidcIdentityProfile) string {
	return firstNonEmptyString(profile.Nickname, profile.Username, strings.TrimSpace(strings.Split(profile.Email, "@")[0]), "OIDC User")
}

func selectOIDCUsername(profile *oidcIdentityProfile) string {
	return firstNonEmptyString(profile.Username, strings.TrimSpace(strings.Split(profile.Email, "@")[0]), profile.ExternalUserID, "oidc_user")
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func extractJWTIssuedAt(token string) int64 {
	if strings.TrimSpace(token) == "" {
		return 0
	}

	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil {
		return 0
	}

	switch value := claims["iat"].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return parsed
		}
	}

	return 0
}
