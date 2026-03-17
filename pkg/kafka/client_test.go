package kafka

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/IBM/sarama/mocks"

	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/stretchr/testify/require"
)

func TestBuildSaramaConfig(t *testing.T) {
	cfg := *conf.KafkaConfig
	cfg.Version = "3.6.0"
	cfg.ClientID = "cloudreve-kafka-test"
	cfg.SecurityProtocol = "SASL_SSL"
	cfg.TLSSkipVerify = true
	cfg.TLSServerName = "kafka.internal"
	cfg.SASLMechanism = "PLAIN"
	cfg.SASLUsername = "user-1"
	cfg.SASLPassword = "pass-1"
	cfg.SASLHandshake = true
	cfg.Producer.RequiredAcks = "local"
	cfg.Producer.Compression = "zstd"
	cfg.Producer.RetryMax = 6
	cfg.Producer.RetryBackoff = 4
	cfg.Producer.Idempotent = true
	cfg.Consumer.InitialOffset = "oldest"
	cfg.Consumer.RebalanceStrategy = "roundrobin"
	cfg.Consumer.RetryBackoff = 5
	cfg.Consumer.ReturnErrors = true

	saramaCfg, err := buildSaramaConfig(&cfg)
	require.NoError(t, err)

	require.Equal(t, "cloudreve-kafka-test", saramaCfg.ClientID)
	require.True(t, saramaCfg.Metadata.Full)
	require.Equal(t, 6, saramaCfg.Metadata.Retry.Max)
	require.Equal(t, 5*time.Second, saramaCfg.Metadata.Retry.Backoff)
	require.Equal(t, sarama.WaitForAll, saramaCfg.Producer.RequiredAcks)
	require.Equal(t, sarama.CompressionZSTD, saramaCfg.Producer.Compression)
	require.True(t, saramaCfg.Producer.Idempotent)
	require.True(t, saramaCfg.Producer.Return.Successes)
	require.Equal(t, 1, saramaCfg.Net.MaxOpenRequests)
	require.Equal(t, sarama.OffsetOldest, saramaCfg.Consumer.Offsets.Initial)
	require.NotNil(t, saramaCfg.Consumer.Group.Rebalance.Strategy)
	require.True(t, saramaCfg.Consumer.Return.Errors)
	require.True(t, saramaCfg.Net.TLS.Enable)
	require.NotNil(t, saramaCfg.Net.TLS.Config)
	require.True(t, saramaCfg.Net.TLS.Config.InsecureSkipVerify)
	require.Equal(t, "kafka.internal", saramaCfg.Net.TLS.Config.ServerName)
	require.True(t, saramaCfg.Net.SASL.Enable)
	require.True(t, saramaCfg.Net.SASL.Handshake)
	require.Equal(t, "user-1", saramaCfg.Net.SASL.User)
	require.Equal(t, "pass-1", saramaCfg.Net.SASL.Password)
}

func TestBuildSaramaConfigBackwardCompatibleFlags(t *testing.T) {
	cfg := *conf.KafkaConfig
	cfg.SecurityProtocol = ""
	cfg.TLSEnabled = true
	cfg.SASLEnabled = true
	cfg.SASLUsername = "legacy-user"
	cfg.SASLPassword = "legacy-pass"

	saramaCfg, err := buildSaramaConfig(&cfg)
	require.NoError(t, err)
	require.True(t, saramaCfg.Net.TLS.Enable)
	require.True(t, saramaCfg.Net.SASL.Enable)
	require.Equal(t, "legacy-user", saramaCfg.Net.SASL.User)
}

func TestBuildSaramaConfigMissingTLSKeyPair(t *testing.T) {
	cfg := *conf.KafkaConfig
	cfg.SecurityProtocol = "SSL"
	cfg.TLSCertPath = "/tmp/client.pem"

	_, err := buildSaramaConfig(&cfg)
	require.ErrorContains(t, err, "kafka tls cert and key must be configured together")
}

func TestResolveSecurityProtocol(t *testing.T) {
	cfg := *conf.KafkaConfig
	cfg.SecurityProtocol = "SASL_SSL"

	protocol, err := resolveSecurityProtocol(&cfg)
	require.NoError(t, err)
	require.Equal(t, securityProtocolSASLSSL, protocol)

	cfg.SecurityProtocol = ""
	cfg.TLSEnabled = true
	cfg.SASLEnabled = true

	protocol, err = resolveSecurityProtocol(&cfg)
	require.NoError(t, err)
	require.Equal(t, securityProtocolSASLSSL, protocol)
}

func TestProducerPublishJSON(t *testing.T) {
	saramaCfg := sarama.NewConfig()
	saramaCfg.Version = sarama.V3_6_0_0
	saramaCfg.Producer.Return.Successes = true

	mockProducer := mocks.NewSyncProducer(t, saramaCfg)
	mockProducer.ExpectSendMessageWithMessageCheckerFunctionAndSucceed(func(msg *sarama.ProducerMessage) error {
		require.Equal(t, "cloudreve.events", msg.Topic)

		key, err := msg.Key.Encode()
		require.NoError(t, err)
		require.Equal(t, []byte("user-1"), key)

		val, err := msg.Value.Encode()
		require.NoError(t, err)

		var body map[string]any
		require.NoError(t, json.Unmarshal(val, &body))
		require.Equal(t, "created", body["event"])

		headers := make(map[string][]byte, len(msg.Headers))
		for _, header := range msg.Headers {
			headers[string(header.Key)] = header.Value
		}
		require.Equal(t, []byte("application/json"), headers["content-type"])
		require.Equal(t, []byte("cloudreve"), headers["x-source"])
		return nil
	})

	p := &producer{
		p:   mockProducer,
		log: logging.NewConsoleLogger(logging.LevelError),
	}

	result, err := p.PublishJSON(context.Background(), "cloudreve.events", []byte("user-1"), map[string]string{
		"event": "created",
	}, map[string]string{
		"x-source": "cloudreve",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NoError(t, mockProducer.Close())
}

func TestProducerPublishValidateMessage(t *testing.T) {
	p := &producer{
		log: logging.NewConsoleLogger(logging.LevelError),
	}

	_, err := p.Publish(context.Background(), nil)
	require.ErrorContains(t, err, "kafka message is nil")

	_, err = p.Publish(context.Background(), &Message{})
	require.ErrorContains(t, err, "kafka topic is empty")
}

func TestSafeHandleMessageRecoverPanic(t *testing.T) {
	err := safeHandleMessage(context.Background(), func(ctx context.Context, msg *Message) error {
		panic("boom")
	}, &Message{Topic: "test"})
	require.ErrorContains(t, err, "panic in kafka handler")
}

func TestValidateRegistration(t *testing.T) {
	err := validateRegistration(ConsumerRegistration{
		Name:   "public-file-sync",
		Group:  "cloudreve-public-sync",
		Topics: []string{"cloudreve.public.file"},
		Handler: func(ctx context.Context, msg *Message) error {
			return nil
		},
	})
	require.NoError(t, err)

	err = validateRegistration(ConsumerRegistration{})
	require.ErrorContains(t, err, "consumer name is empty")
}
