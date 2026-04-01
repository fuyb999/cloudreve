package conf

import (
	"fmt"
	"os"
	"strings"

	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/go-ini/ini"
	"github.com/go-playground/validator/v10"
)

const (
	envConfOverrideKey = "CR_CONF_"
)

type ConfigProvider interface {
	Database() *Database
	System() *System
	SSL() *SSL
	Unix() *Unix
	Slave() *Slave
	Redis() *Redis
	Kafka() *Kafka
	Cors() *Cors
	OptionOverwrite() map[string]any
}

// NewIniConfigProvider initializes a new Ini config file provider. A default config file
// will be created if the given path does not exist.
func NewIniConfigProvider(configPath string, l logging.Logger) (ConfigProvider, error) {
	if configPath == "" || !util.Exists(configPath) {
		l.Info("Config file %q not found, creating a new one.", configPath)
		// 创建初始配置文件
		confContent := util.Replace(map[string]string{
			"{SessionSecret}": util.RandStringRunesCrypto(64),
		}, defaultConf)
		f, err := util.CreatNestedFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("failed to create config file: %w", err)
		}

		// 写入配置文件
		_, err = f.WriteString(confContent)
		if err != nil {
			return nil, fmt.Errorf("failed to write config file: %w", err)
		}

		f.Close()
	}

	cfg, err := ini.Load(configPath, []byte(getOverrideConfFromEnv(l)))
	if err != nil {
		return nil, fmt.Errorf("failed to parse config file %q: %w", configPath, err)
	}

	provider := &iniConfigProvider{
		database:        *DatabaseConfig,
		system:          *SystemConfig,
		ssl:             *SSLConfig,
		unix:            *UnixConfig,
		slave:           *SlaveConfig,
		redis:           *RedisConfig,
		kafka:           *KafkaConfig,
		cors:            *CORSConfig,
		optionOverwrite: make(map[string]interface{}),
	}

	sections := map[string]interface{}{
		"Database":   &provider.database,
		"System":     &provider.system,
		"SSL":        &provider.ssl,
		"UnixSocket": &provider.unix,
		"Redis":      &provider.redis,
		"Kafka":      &provider.kafka,
		"CORS":       &provider.cors,
		"Slave":      &provider.slave,
	}
	for sectionName, sectionStruct := range sections {
		err = mapSection(cfg, sectionName, sectionStruct)
		if err != nil {
			return nil, fmt.Errorf("failed to parse config section %q: %w", sectionName, err)
		}
	}

	// Kafka 生产者/消费者配置是嵌套结构，需要显式映射子 section。
	if err := mapSection(cfg, "Kafka.Producer", &provider.kafka.Producer); err != nil {
		return nil, fmt.Errorf("failed to parse config section %q: %w", "Kafka.Producer", err)
	}
	if err := mapSection(cfg, "Kafka.Consumer", &provider.kafka.Consumer); err != nil {
		return nil, fmt.Errorf("failed to parse config section %q: %w", "Kafka.Consumer", err)
	}

	// 映射数据库配置覆盖
	for _, key := range cfg.Section("OptionOverwrite").Keys() {
		provider.optionOverwrite[key.Name()] = key.Value()
	}

	return provider, nil
}

type iniConfigProvider struct {
	database        Database
	system          System
	ssl             SSL
	unix            Unix
	slave           Slave
	redis           Redis
	kafka           Kafka
	cors            Cors
	optionOverwrite map[string]any
}

func (i *iniConfigProvider) Database() *Database {
	return &i.database
}

func (i *iniConfigProvider) System() *System {
	return &i.system
}

func (i *iniConfigProvider) SSL() *SSL {
	return &i.ssl
}

func (i *iniConfigProvider) Unix() *Unix {
	return &i.unix
}

func (i *iniConfigProvider) Slave() *Slave {
	return &i.slave
}

func (i *iniConfigProvider) Redis() *Redis {
	return &i.redis
}

func (i *iniConfigProvider) Kafka() *Kafka {
	return &i.kafka
}

func (i *iniConfigProvider) Cors() *Cors {
	return &i.cors
}

func (i *iniConfigProvider) OptionOverwrite() map[string]any {
	return i.optionOverwrite
}

const defaultConf = `[System]
Debug = false
; 强制输出 ANSI 颜色。IDEA Debug Console 等非 TTY 场景可开启。
ForceColor = false
; 调用点显示策略：auto=仅 debug 模式显示；on=始终显示；off=始终关闭。
CallerMode = off
; 堆栈输出策略：off=关闭；panic=仅 panic/recover 输出；error=error/panic 输出；all=所有级别输出。
StacktraceMode = panic
; 日志级别：debug/info/warning/error
LogLevel = info
Mode = master
Listen = :5212
SessionSecret = {SessionSecret}
HashIDSalt = {HashIDSalt}

[Kafka]
; 是否启用 Kafka。关闭时 Cloudreve 会退化为 no-op 客户端，不影响原有功能。
Enabled = false
; 多 broker 使用英文逗号分隔，例如：127.0.0.1:9092,127.0.0.1:9093
Brokers =
; Kafka broker 版本，用于 Sarama 协议协商。
Version = 3.7.0
; 客户端 ID，建议区分实例或环境。
ClientID = cloudreve
; 连接协议：PLAINTEXT / SSL / SASL_PLAINTEXT / SASL_SSL
SecurityProtocol = PLAINTEXT
; 连接与读写超时，单位秒。
DialTimeout = 10
ReadTimeout = 30
WriteTimeout = 30
KeepAlive = 30
; TLS 相关配置。使用 SASL_SSL / SSL 时可按需设置。
TLSSkipVerify = false
TLSServerName =
TLSCAPath =
TLSCertPath =
TLSKeyPath =
; SASL 相关配置。使用 SASL_PLAINTEXT / SASL_SSL 时生效。
SASLMechanism = PLAIN
SASLUsername =
SASLPassword =
SASLHandshake = true

[Kafka.Producer]
; all/local/none，对高可用场景建议保持 all。
RequiredAcks = all
; none/gzip/snappy/lz4/zstd
Compression = snappy
RetryMax = 5
RetryBackoff = 2
; 高可用生产建议开启幂等。
Idempotent = true
MaxMessageBytes = 0
ReturnSuccesses = true

[Kafka.Consumer]
; oldest/newest
InitialOffset = newest
; range/roundrobin/sticky
RebalanceStrategy = sticky
SessionTimeout = 30
HeartbeatInterval = 3
RetryBackoff = 2
MaxProcessingTime = 5
FetchDefault = 1048576
FetchMax = 0
ReturnErrors = true
`

// mapSection 将配置文件的 Section 映射到结构体上
func mapSection(cfg *ini.File, section string, confStruct interface{}) error {
	err := cfg.Section(section).MapTo(confStruct)
	if err != nil {
		return err
	}

	// 验证合法性
	validate := validator.New()
	err = validate.Struct(confStruct)
	if err != nil {
		return err
	}

	return nil
}

func getOverrideConfFromEnv(l logging.Logger) string {
	confMaps := make(map[string]map[string]string)
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, envConfOverrideKey) {
			continue
		}

		// split by key=value and get key
		kv := strings.SplitN(env, "=", 2)
		configKey := strings.TrimPrefix(kv[0], envConfOverrideKey)
		configValue := kv[1]
		sectionName, fieldName, ok := splitOverrideKey(configKey)
		if !ok {
			l.Warning("Skip invalid override config key %q", configKey)
			continue
		}
		if confMaps[sectionName] == nil {
			confMaps[sectionName] = make(map[string]string)
		}

		confMaps[sectionName][fieldName] = configValue
		l.Info("Override config %q = %q", configKey, configValue)
	}

	// generate ini content
	var sb strings.Builder
	for section, kvs := range confMaps {
		sb.WriteString(fmt.Sprintf("[%s]\n", section))
		for k, v := range kvs {
			sb.WriteString(fmt.Sprintf("%s = %s\n", k, v))
		}
	}

	return sb.String()
}

func splitOverrideKey(key string) (string, string, bool) {
	idx := strings.LastIndex(key, ".")
	if idx <= 0 || idx >= len(key)-1 {
		return "", "", false
	}

	return key[:idx], key[idx+1:], true
}
