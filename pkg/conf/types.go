package conf

import "github.com/cloudreve/Cloudreve/v4/pkg/util"

type DBType string

var (
	SQLiteDB   DBType = "sqlite"
	SQLite3DB  DBType = "sqlite3"
	MySqlDB    DBType = "mysql"
	MsSqlDB    DBType = "mssql"
	PostgresDB DBType = "postgres"
	MariaDB    DBType = "mariadb"
)

// Database 数据库
type Database struct {
	Type        DBType
	User        string
	Password    string
	Host        string
	Name        string
	TablePrefix string
	DBFile      string
	Port        int
	Charset     string
	UnixSocket  bool
	// 允许直接使用DATABASE_URL来配置数据库连接
	DatabaseURL string
	// SSLMode 允许使用SSL连接数据库, 用户可以在sslmode string中添加证书等配置
	SSLMode string
}

type SysMode string

var (
	MasterMode SysMode = "master"
	SlaveMode  SysMode = "slave"
)

// System 系统通用配置
type System struct {
	Mode           SysMode `validate:"eq=master|eq=slave"`
	Listen         string  `validate:"required"`
	Debug          bool
	ForceColor     bool
	CallerMode     string `validate:"omitempty,oneof=auto on off"`
	StacktraceMode string `validate:"omitempty,oneof=off panic error all"`
	SessionSecret  string
	HashIDSalt     string // deprecated
	GracePeriod    int    `validate:"gte=0"`
	ProxyHeader    string
	LogLevel       string `validate:"oneof=debug info warning error"`
	Pprof          string // Address to listen for pprof, e.g. "localhost:6060". Empty to disable.
}

type SSL struct {
	CertPath string `validate:"omitempty,required"`
	KeyPath  string `validate:"omitempty,required"`
	Listen   string `validate:"required"`
}

type Unix struct {
	Listen string
	Perm   uint32
}

// Slave 作为slave存储端配置
type Slave struct {
	Secret          string `validate:"omitempty,gte=64"`
	CallbackTimeout int    `validate:"omitempty,gte=1"`
	SignatureTTL    int    `validate:"omitempty,gte=1"`
}

// Redis 配置
type Redis struct {
	Network       string
	Server        string
	User          string
	Password      string
	DB            string
	UseTLS        bool
	TLSSkipVerify bool
}

// Kafka 配置。
// 当前版本覆盖 Cloudreve 接 Kafka 的常用能力：
// 1. 多 broker 集群连接；
// 2. 生产者幂等、ACK 策略、压缩；
// 3. consumer group 高可用消费；
// 4. PLAINTEXT / SSL / SASL_PLAINTEXT / SASL_SSL；
// 5. TLS CA / 客户端证书；
// 6. SASL-PLAIN 接入。
type Kafka struct {
	Enabled          bool
	Brokers          string
	Version          string
	ClientID         string
	DialTimeout      int    `validate:"gte=1"`
	ReadTimeout      int    `validate:"gte=1"`
	WriteTimeout     int    `validate:"gte=1"`
	KeepAlive        int    `validate:"gte=1"`
	SecurityProtocol string `validate:"omitempty,oneof=PLAINTEXT SSL SASL_PLAINTEXT SASL_SSL plaintext ssl sasl_plaintext sasl_ssl"`
	// TLSEnabled / SASLEnabled 保留给旧配置兼容。
	TLSEnabled    bool
	TLSSkipVerify bool
	TLSServerName string
	TLSCAPath     string
	TLSCertPath   string
	TLSKeyPath    string
	SASLEnabled   bool
	SASLMechanism string `validate:"omitempty,oneof=PLAIN plain"`
	SASLUsername  string
	SASLPassword  string
	SASLHandshake bool
	Producer      KafkaProducer
	Consumer      KafkaConsumer
}

// KafkaProducer 生产者配置。
type KafkaProducer struct {
	RequiredAcks    string `validate:"oneof=all local none"`
	Compression     string `validate:"oneof=none gzip snappy lz4 zstd"`
	RetryMax        int    `validate:"gte=0"`
	RetryBackoff    int    `validate:"gte=0"`
	Idempotent      bool
	MaxMessageBytes int `validate:"gte=0"`
	ReturnSuccesses bool
}

// KafkaConsumer 消费者配置。
type KafkaConsumer struct {
	InitialOffset     string `validate:"oneof=oldest newest"`
	RebalanceStrategy string `validate:"oneof=range roundrobin sticky"`
	SessionTimeout    int    `validate:"gte=1"`
	HeartbeatInterval int    `validate:"gte=1"`
	RetryBackoff      int    `validate:"gte=0"`
	MaxProcessingTime int    `validate:"gte=1"`
	FetchDefault      int32  `validate:"gte=0"`
	FetchMax          int32  `validate:"gte=0"`
	ReturnErrors      bool
}

// 跨域配置
type Cors struct {
	AllowOrigins     []string
	AllowMethods     []string
	AllowHeaders     []string
	AllowCredentials bool
	ExposeHeaders    []string
	SameSite         string
	Secure           bool
}

// RedisConfig Redis服务器配置
var RedisConfig = &Redis{
	Network:       "tcp",
	Server:        "",
	Password:      "",
	DB:            "0",
	UseTLS:        false,
	TLSSkipVerify: true,
}

// KafkaConfig Kafka 默认配置。
var KafkaConfig = &Kafka{
	Enabled:          false,
	Brokers:          "",
	Version:          "3.7.0",
	ClientID:         "cloudreve",
	DialTimeout:      10,
	ReadTimeout:      30,
	WriteTimeout:     30,
	KeepAlive:        30,
	SecurityProtocol: "",
	TLSEnabled:       false,
	TLSSkipVerify:    false,
	SASLEnabled:      false,
	SASLMechanism:    "PLAIN",
	SASLHandshake:    true,
	Producer: KafkaProducer{
		RequiredAcks:    "all",
		Compression:     "snappy",
		RetryMax:        5,
		RetryBackoff:    2,
		Idempotent:      true,
		MaxMessageBytes: 0,
		ReturnSuccesses: true,
	},
	Consumer: KafkaConsumer{
		InitialOffset:     "newest",
		RebalanceStrategy: "sticky",
		SessionTimeout:    30,
		HeartbeatInterval: 3,
		RetryBackoff:      2,
		MaxProcessingTime: 5,
		FetchDefault:      1024 * 1024,
		FetchMax:          0,
		ReturnErrors:      true,
	},
}

// DatabaseConfig 数据库配置
var DatabaseConfig = &Database{
	Charset:     "utf8mb4",
	DBFile:      util.DataPath("cloudreve.db"),
	Port:        3306,
	UnixSocket:  false,
	DatabaseURL: "",
}

// SystemConfig 系统公用配置
var SystemConfig = &System{
	Debug:          false,
	Mode:           MasterMode,
	Listen:         ":5212",
	CallerMode:     "off",
	StacktraceMode: "panic",
	ProxyHeader:    "",
	LogLevel:       "info",
}

// CORSConfig 跨域配置
var CORSConfig = &Cors{
	AllowOrigins:     []string{"UNSET"},
	AllowMethods:     []string{"PUT", "POST", "GET", "OPTIONS"},
	AllowHeaders:     []string{"Cookie", "X-Cr-Policy", "Authorization", "Content-Length", "Content-Type", "X-Cr-Path", "X-Cr-FileName"},
	AllowCredentials: false,
	ExposeHeaders:    nil,
	SameSite:         "Default",
	Secure:           false,
}

// SlaveConfig 从机配置
var SlaveConfig = &Slave{
	CallbackTimeout: 20,
	SignatureTTL:    600,
}

var SSLConfig = &SSL{
	Listen:   ":443",
	CertPath: "",
	KeyPath:  "",
}

var UnixConfig = &Unix{
	Listen: "",
}

var OptionOverwrite = map[string]interface{}{}
