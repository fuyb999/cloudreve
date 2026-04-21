package setting

import (
	"time"
)

type PWASetting struct {
	SmallIcon       string
	MediumIcon      string
	LargeIcon       string
	Display         string
	ThemeColor      string
	BackgroundColor string
}

type SiteBasic struct {
	Name        string
	Title       string
	ID          string
	Description string
	Script      string
}

type CaptchaType string

const (
	CaptchaNormal    = CaptchaType("normal")
	CaptchaReCaptcha = CaptchaType("recaptcha")
	CaptchaTcaptcha  = CaptchaType("tcaptcha")
	CaptchaTurnstile = CaptchaType("turnstile")
	CaptchaCap       = CaptchaType("cap")
)

type ReCaptcha struct {
	Key    string
	Secret string
}

type TcCaptcha struct {
	AppID        string
	AppSecretKey string
	SecretID     string
	SecretKey    string
}

type Turnstile struct {
	Key    string
	Secret string
}

type Cap struct {
	InstanceURL string
	SiteKey     string
	SecretKey   string
	AssetServer string
}

type SMTP struct {
	FromName        string
	From            string
	Host            string
	ReplyTo         string
	User            string
	Password        string
	ForceEncryption bool
	Port            int
	Keepalive       int
}

type TokenAuth struct {
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
}

type DBFS struct {
	UseCursorPagination        bool
	MaxPageSize                int
	MaxRecursiveSearchedFolder int
	UseSSEForSearch            bool
}

type (
	QueueType    string
	QueueSetting struct {
		WorkerNum          int
		MaxExecution       time.Duration
		BackoffFactor      float64
		BackoffMaxDuration time.Duration
		MaxRetry           int
		RetryDelay         time.Duration
	}
)

type ThumbEncode struct {
	Quality int
	Format  string
}

var (
	QueueTypeContentProcessing = QueueType("content_processing")
	QueueTypeMediaMeta         = QueueType("media_meta")
	QueueTypeIOIntense         = QueueType("io_intense")
	QueueTypeThumb             = QueueType("thumb")
	QueueTypeEntityRecycle     = QueueType("recycle")
	QueueTypeSlave             = QueueType("slave")
	QueueTypeRemoteDownload    = QueueType("remote_download")
)

type CronType string

var (
	CronTypeEntityCollect    = CronType("entity_collect")
	CronTypeTrashBinCollect  = CronType("trash_bin_collect")
	CronTypeOauthCredRefresh = CronType("oauth_cred_refresh")
)

type Theme struct {
	Themes       string
	DefaultTheme string
}

type Logo struct {
	Normal string
	Light  string
}

type LegalDocuments struct {
	PrivacyPolicy  string
	TermsOfService string
}

type CaptchaMode int

const (
	CaptchaModeNumber = CaptchaMode(iota)
	CaptchaModeAlphabet
	CaptchaModeArithmetic
	CaptchaModeNumberAlphabet
)

type Captcha struct {
	Height             int
	Width              int
	Mode               CaptchaMode
	ComplexOfNoiseText int
	ComplexOfNoiseDot  int
	IsShowHollowLine   bool
	IsShowNoiseDot     bool
	IsShowNoiseText    bool
	IsShowSlimeLine    bool
	IsShowSineLine     bool
	Length             int
}

type ExplorerFrontendSettings struct {
	Icons string
}

type MapProvider string

const (
	MapProviderOpenStreetMap = MapProvider("openstreetmap")
	MapProviderGoogle        = MapProvider("google")
	MapProviderMapbox        = MapProvider("mapbox")
)

type MapGoogleTileType string

const (
	MapGoogleTileTypeRegular   = MapGoogleTileType("regular")
	MapGoogleTileTypeSatellite = MapGoogleTileType("satellite")
	MapGoogleTileTypeTerrain   = MapGoogleTileType("terrain")
)

type MapSetting struct {
	Provider       MapProvider
	GoogleTileType MapGoogleTileType
	MapboxAK       string
}

// Viewer related

type (
	SearchCategory string
)

const (
	CategoryUnknown  = SearchCategory("unknown")
	CategoryImage    = SearchCategory("image")
	CategoryVideo    = SearchCategory("video")
	CategoryAudio    = SearchCategory("audio")
	CategoryDocument = SearchCategory("document")
)

type AppSetting struct {
	Promotion               bool
	DesktopPromotion        bool
	SyncthingUpgradeVersion string
	SyncthingLinuxURL       string
	SyncthingWindowsURL     string
}

type OIDCConfigMode string

const (
	OIDCConfigModeStandard OIDCConfigMode = "standard"
	OIDCConfigModeRemote   OIDCConfigMode = "remote"
)

type OIDCRuntimeConfigSource string

const (
	OIDCRuntimeConfigSourceLocal         OIDCRuntimeConfigSource = "local"
	OIDCRuntimeConfigSourceRemote        OIDCRuntimeConfigSource = "remote"
	OIDCRuntimeConfigSourceRemoteCache   OIDCRuntimeConfigSource = "remote_cache"
	OIDCRuntimeConfigSourceLocalFallback OIDCRuntimeConfigSource = "local_fallback"
)

type OIDCRuntimeStatus string

const (
	OIDCRuntimeStatusDisabled      OIDCRuntimeStatus = "disabled"
	OIDCRuntimeStatusStandard      OIDCRuntimeStatus = "standard"
	OIDCRuntimeStatusRemoteReady   OIDCRuntimeStatus = "remote_ready"
	OIDCRuntimeStatusRemoteCached  OIDCRuntimeStatus = "remote_cached"
	OIDCRuntimeStatusLocalFallback OIDCRuntimeStatus = "local_fallback"
	OIDCRuntimeStatusRemoteError   OIDCRuntimeStatus = "remote_error"
)

type OIDCRuntimeState struct {
	Status         OIDCRuntimeStatus       `json:"status"`
	Source         OIDCRuntimeConfigSource `json:"source"`
	BindingCode    string                  `json:"binding_code,omitempty"`
	ClientID       string                  `json:"client_id,omitempty"`
	Scope          string                  `json:"scope,omitempty"`
	SSOURL         string                  `json:"sso_url,omitempty"`
	WellKnownURL   string                  `json:"wellknown_url,omitempty"`
	CacheExpiresAt *time.Time              `json:"cache_expires_at,omitempty"`
	LastSuccessAt  *time.Time              `json:"last_success_at,omitempty"`
	LastAttemptAt  *time.Time              `json:"last_attempt_at,omitempty"`
	LastError      string                  `json:"last_error,omitempty"`
	LastErrorAt    *time.Time              `json:"last_error_at,omitempty"`
}

// OIDCSetting 对应后台“参数设置 -> 用户会话 -> OIDC”的统一认证配置。
// 开关打开后，前后端都会切换到统一认证链路；关闭时则完全回退到原有本地登录逻辑。
type OIDCSetting struct {
	Enabled bool
	// DisplayName 用于登录页按钮文案，例如 “统一认证”。
	DisplayName string
	// AutoRedirect 控制是否在用户打开登录页时自动跳转到统一认证入口。
	AutoRedirect bool
	// ConfigMode 控制是直接使用本地标准 OIDC 配置，还是通过授权中心运行时拉取配置。
	ConfigMode OIDCConfigMode
	// BindingCode 是授权中心统一接入绑定编码；配置后，网盘会优先按该编码拉取运行时配置。
	BindingCode string
	// SSOURL 是第三方前端统一登录入口；为空时根据 issuer 自动推导 /sso。
	SSOURL string
	// WellKnownURL 用于拉取 OIDC 发现文档，从中解析 token/userinfo 端点。
	WellKnownURL string
	ClientID     string
	ClientSecret string
	// Scope 至少需要包含 openid、user_info、user.read，以及网盘运行态鉴权实际使用的
	// UserInfo.Read、Admin.Read、Files.Read、Files.Write、Workflow.Read、Workflow.Write、Shares.Read、Shares.Write。
	Scope string
	// RuntimeState 描述当前实际生效的运行态来源、缓存与回退信息。
	RuntimeState *OIDCRuntimeState `json:"runtime_state,omitempty"`
}

type EmailTemplate struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Language string `json:"language"`
}

type Avatar struct {
	Gravatar string `json:"gravatar"`
	Path     string `json:"path"`
}

type AvatarProcess struct {
	Path        string `json:"path"`
	MaxFileSize int64  `json:"max_file_size"`
	MaxWidth    int    `json:"max_width"`
}

type CustomNavItem struct {
	Icon string `json:"icon"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type CustomHTML struct {
	HeadlessFooter string `json:"headless_footer,omitempty"`
	HeadlessBody   string `json:"headless_bottom,omitempty"`
	SidebarBottom  string `json:"sidebar_bottom,omitempty"`
}

type FTSIndexType string

const (
	FTSIndexTypeNone          = FTSIndexType("")
	FTSIndexTypeMeilisearch   = FTSIndexType("meilisearch")
	FTSIndexTypeElasticsearch = FTSIndexType("elasticsearch")
)

type FTSExtractorType string

const (
	FTSExtractorTypeNone = FTSExtractorType("")
	FTSExtractorTypeTika = FTSExtractorType("tika")
)

type FTSIndexMeilisearchSetting struct {
	Endpoint         string
	APIKey           string
	PageSize         int
	EmbeddingEnbaled bool
	EmbeddingSetting string
}

type FTSIndexElasticsearchSetting struct {
	Endpoint      string
	CloudID       string
	APIKey        string
	Username      string
	Password      string
	Index         string
	PageSize      int
	SkipTLSVerify bool
}

type FTSTikaExtractorSetting struct {
	Endpoint             string
	Exts                 []string
	DocumentEnabled      bool
	DocumentExts         []string
	ArchiveEnabled       bool
	ArchiveExts          []string
	MaxFileSize          int64
	SidecarEnabled       bool
	SidecarTextEnabled   bool
	SidecarAssetsEnabled bool
	ExtractInlineImages  bool
}

type FTSExternalMode string

const (
	FTSExternalModeDisabled                 = FTSExternalMode("disabled")
	FTSExternalModePrimary                  = FTSExternalMode("primary")
	FTSExternalModeFallbackOnError          = FTSExternalMode("fallback_on_error")
	FTSExternalModeFallbackOnErrorOrQuality = FTSExternalMode("fallback_on_error_or_quality")
)

type FTSExternalKafkaSetting struct {
	UseGlobalKafka   bool
	Brokers          []string
	SecurityProtocol string
	SASLMechanism    string
	Username         string
	Password         string
	TLSSkipVerify    bool
	ProcessTopic     string
	ResultTopic      string
	ErrorTopic       string
	ConsumerGroup    string
}

type FTSExternalQualitySetting struct {
	Enabled             bool
	MinTextLength       int
	MaxReplacementRatio float64
	MaxControlCharRatio float64
	MinPrintableRatio   float64
	FontBoxMinCount     int
	FontBoxMinRun       int
	FontBoxMinRatio     float64
}

type FTSExternalExtractorSetting struct {
	Enabled              bool
	Mode                 FTSExternalMode
	MaxFileSize          int64
	TimeoutSeconds       int
	RetryMax             int
	RecursiveAttachments bool
	OCREnabled           bool
	SkipEncryptedFiles   bool
	Kafka                FTSExternalKafkaSetting
	Quality              FTSExternalQualitySetting
}

type MasterEncryptKeyVaultType string

const (
	MasterEncryptKeyVaultTypeSetting = MasterEncryptKeyVaultType("setting")
	MasterEncryptKeyVaultTypeEnv     = MasterEncryptKeyVaultType("env")
	MasterEncryptKeyVaultTypeFile    = MasterEncryptKeyVaultType("file")
)
