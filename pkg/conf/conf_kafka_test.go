package conf

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/stretchr/testify/require"
)

func TestNewIniConfigProviderKafkaNestedSections(t *testing.T) {
	confPath := filepath.Join(t.TempDir(), "conf.ini")
	confContent := `[System]
Listen = :5212
SessionSecret = test-secret

[Kafka]
Enabled = true
Brokers = 127.0.0.1:9092,127.0.0.1:9093
Version = 3.6.0
ClientID = cloudreve-test
SecurityProtocol = SASL_SSL
TLSSkipVerify = true
TLSServerName = kafka.internal
TLSCAPath = /tmp/ca.pem
TLSCertPath = /tmp/client.pem
TLSKeyPath = /tmp/client-key.pem
SASLMechanism = PLAIN
SASLUsername = alice
SASLPassword = secret
SASLHandshake = false

[Kafka.Producer]
RequiredAcks = none
Compression = gzip
RetryMax = 8
RetryBackoff = 4
Idempotent = false
MaxMessageBytes = 2048
ReturnSuccesses = false

[Kafka.Consumer]
InitialOffset = oldest
RebalanceStrategy = roundrobin
SessionTimeout = 45
HeartbeatInterval = 5
RetryBackoff = 6
MaxProcessingTime = 7
FetchDefault = 4096
FetchMax = 8192
ReturnErrors = false
`
	require.NoError(t, os.WriteFile(confPath, []byte(confContent), 0o644))

	provider, err := NewIniConfigProvider(confPath, logging.NewConsoleLogger(logging.LevelError))
	require.NoError(t, err)

	kafkaCfg := provider.Kafka()
	require.True(t, kafkaCfg.Enabled)
	require.Equal(t, "127.0.0.1:9092,127.0.0.1:9093", kafkaCfg.Brokers)
	require.Equal(t, "3.6.0", kafkaCfg.Version)
	require.Equal(t, "cloudreve-test", kafkaCfg.ClientID)
	require.Equal(t, "SASL_SSL", kafkaCfg.SecurityProtocol)
	require.True(t, kafkaCfg.TLSSkipVerify)
	require.Equal(t, "kafka.internal", kafkaCfg.TLSServerName)
	require.Equal(t, "/tmp/ca.pem", kafkaCfg.TLSCAPath)
	require.Equal(t, "/tmp/client.pem", kafkaCfg.TLSCertPath)
	require.Equal(t, "/tmp/client-key.pem", kafkaCfg.TLSKeyPath)
	require.Equal(t, "PLAIN", kafkaCfg.SASLMechanism)
	require.Equal(t, "alice", kafkaCfg.SASLUsername)
	require.Equal(t, "secret", kafkaCfg.SASLPassword)
	require.False(t, kafkaCfg.SASLHandshake)
	require.Equal(t, "none", kafkaCfg.Producer.RequiredAcks)
	require.Equal(t, "gzip", kafkaCfg.Producer.Compression)
	require.Equal(t, 8, kafkaCfg.Producer.RetryMax)
	require.Equal(t, 4, kafkaCfg.Producer.RetryBackoff)
	require.False(t, kafkaCfg.Producer.Idempotent)
	require.Equal(t, 2048, kafkaCfg.Producer.MaxMessageBytes)
	require.False(t, kafkaCfg.Producer.ReturnSuccesses)
	require.Equal(t, "oldest", kafkaCfg.Consumer.InitialOffset)
	require.Equal(t, "roundrobin", kafkaCfg.Consumer.RebalanceStrategy)
	require.Equal(t, 45, kafkaCfg.Consumer.SessionTimeout)
	require.Equal(t, 5, kafkaCfg.Consumer.HeartbeatInterval)
	require.Equal(t, 6, kafkaCfg.Consumer.RetryBackoff)
	require.Equal(t, 7, kafkaCfg.Consumer.MaxProcessingTime)
	require.EqualValues(t, 4096, kafkaCfg.Consumer.FetchDefault)
	require.EqualValues(t, 8192, kafkaCfg.Consumer.FetchMax)
	require.False(t, kafkaCfg.Consumer.ReturnErrors)
}

func TestNewIniConfigProviderKafkaEnvOverride(t *testing.T) {
	confPath := filepath.Join(t.TempDir(), "conf.ini")
	require.NoError(t, os.WriteFile(confPath, []byte(`[System]
Listen = :5212
SessionSecret = test-secret
`), 0o644))

	t.Setenv("CR_CONF_Kafka.Enabled", "true")
	t.Setenv("CR_CONF_Kafka.Brokers", "kafka-1:9092")
	t.Setenv("CR_CONF_Kafka.SecurityProtocol", "SASL_SSL")
	t.Setenv("CR_CONF_Kafka.Producer.RequiredAcks", "local")
	t.Setenv("CR_CONF_Kafka.Consumer.InitialOffset", "oldest")

	provider, err := NewIniConfigProvider(confPath, logging.NewConsoleLogger(logging.LevelError))
	require.NoError(t, err)

	kafkaCfg := provider.Kafka()
	require.True(t, kafkaCfg.Enabled)
	require.Equal(t, "kafka-1:9092", kafkaCfg.Brokers)
	require.Equal(t, "SASL_SSL", kafkaCfg.SecurityProtocol)
	require.Equal(t, "local", kafkaCfg.Producer.RequiredAcks)
	require.Equal(t, "oldest", kafkaCfg.Consumer.InitialOffset)
}

func TestSplitOverrideKey(t *testing.T) {
	section, field, ok := splitOverrideKey("Kafka.Producer.RequiredAcks")
	require.True(t, ok)
	require.Equal(t, "Kafka.Producer", section)
	require.Equal(t, "RequiredAcks", field)

	_, _, ok = splitOverrideKey("Kafka")
	require.False(t, ok)
}
