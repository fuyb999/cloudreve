package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/ftsexternaljob"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/kafka"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type ftsExternalIntegrationDep struct {
	dependency.Dep
	dbClient *ent.Client
	settings setting.Provider
	logger   logging.Logger
	config   conf.ConfigProvider
}

func (d ftsExternalIntegrationDep) DBClient() *ent.Client {
	return d.dbClient
}

func (d ftsExternalIntegrationDep) SettingProvider() setting.Provider {
	return d.settings
}

func (d ftsExternalIntegrationDep) Logger() logging.Logger {
	return d.logger
}

func (d ftsExternalIntegrationDep) ConfigProvider() conf.ConfigProvider {
	return d.config
}

type ftsExternalIntegrationConfig struct {
	conf.ConfigProvider
	kafka *conf.Kafka
}

func (c ftsExternalIntegrationConfig) Kafka() *conf.Kafka {
	return c.kafka
}

func (c ftsExternalIntegrationConfig) System() *conf.System {
	return conf.SystemConfig
}

func (c ftsExternalIntegrationConfig) Slave() *conf.Slave {
	return conf.SlaveConfig
}

type ftsExternalIntegrationHarness struct {
	dep           ftsExternalIntegrationDep
	externalCfg   *setting.FTSExternalExtractorSetting
	responder     kafka.Client
	processStream chan externalFTSProcessMessage
}

func TestFTSExternalKafkaRoundTripIntegration(t *testing.T) {
	broker := strings.TrimSpace(os.Getenv("CLOUDREVE_FTS_EXTERNAL_KAFKA_ADDR"))
	if broker == "" {
		t.Skip("set CLOUDREVE_FTS_EXTERNAL_KAFKA_ADDR to run kafka integration test")
	}

	h := newFTSExternalIntegrationHarness(t, broker)
	defer h.close(t)

	ctx := context.Background()
	reqTime := time.Now().UTC().Round(time.Second)
	fileModel := &ent.File{ID: 101, OwnerID: 7, Name: "report.pdf", Size: 4096, UpdatedAt: reqTime}
	entity := &ent.Entity{ID: 201, Source: "tenant-a/u7/report.pdf", UpdatedAt: reqTime}
	policy := &ent.StoragePolicy{ID: 301, BucketName: "cloudreve-test-bucket"}

	job, err := publishFTSExternalRequest(ctx, h.dep, fileModel, entity, policy, h.externalCfg, "integration_success", 1, "")
	require.NoError(t, err)
	require.NotEmpty(t, job.RequestID)

	processMsg := h.waitProcessMessage(t)
	require.Equal(t, job.RequestID, processMsg.RequestID)
	require.Equal(t, job.SnapshotToken, processMsg.SnapshotToken)
	require.Equal(t, fileModel.ID, processMsg.File.FileID)
	require.Equal(t, policy.BucketName, processMsg.Source.Bucket)
	require.Equal(t, entity.Source, processMsg.Source.Path)
	require.True(t, processMsg.Options.RecursiveAttachments)

	result := externalFTSResultMessage{
		Version:       1,
		RequestID:     job.RequestID,
		SnapshotToken: job.SnapshotToken,
		Status:        "success",
		Provider: externalFTSProviderInfo{
			Name:    "integration-mock",
			Version: "1.0.0",
		},
		Root: externalFTSResultRoot{
			Content:      "hello from external extractor",
			Warnings:     []string{"font fallback used"},
			QualityScore: 0.97,
		},
		Attachments: []externalFTSAttachment{{
			ID:       "att-1",
			ParentID: fmt.Sprintf("file:%d", fileModel.ID),
			Name:     "embedded.txt",
			MimeType: "text/plain",
			Content:  "embedded content",
		}},
	}
	_, err = h.responder.Producer().PublishJSON(ctx, h.externalCfg.Kafka.ResultTopic, []byte(job.RequestID), result, map[string]string{
		"content-type": "application/json",
	})
	require.NoError(t, err)

	stored := h.waitJobStatus(t, job.RequestID, ftsExternalJobStatusSuccess)
	require.Contains(t, stored.ResultPayload, "hello from external extractor")
	require.Empty(t, stored.ErrorPayload)
	require.False(t, stored.CompletedAt.IsZero())
	firstCompletedAt := *stored.CompletedAt
	firstPayload := stored.ResultPayload

	parsed, err := parseFTSExternalResultPayload(stored.ResultPayload)
	require.NoError(t, err)
	require.Equal(t, "integration-mock", parsed.Provider.Name)
	require.Len(t, parsed.Attachments, 1)

	duplicate := result
	duplicate.Provider.Name = "integration-mock-duplicate"
	duplicate.Root.Content = "duplicate payload should be ignored"
	_, err = h.responder.Producer().PublishJSON(ctx, h.externalCfg.Kafka.ResultTopic, []byte(job.RequestID), duplicate, map[string]string{
		"content-type": "application/json",
	})
	require.NoError(t, err)

	time.Sleep(2 * time.Second)
	afterDuplicate, err := h.dep.dbClient.FTSExternalJob.Query().Where(ftsexternaljob.RequestIDEQ(job.RequestID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, ftsExternalJobStatusSuccess, afterDuplicate.Status)
	require.Equal(t, firstPayload, afterDuplicate.ResultPayload)
	require.Equal(t, firstCompletedAt, *afterDuplicate.CompletedAt)
}

func TestFTSExternalKafkaErrorRoundTripIntegration(t *testing.T) {
	broker := strings.TrimSpace(os.Getenv("CLOUDREVE_FTS_EXTERNAL_KAFKA_ADDR"))
	if broker == "" {
		t.Skip("set CLOUDREVE_FTS_EXTERNAL_KAFKA_ADDR to run kafka integration test")
	}

	h := newFTSExternalIntegrationHarness(t, broker)
	defer h.close(t)

	ctx := context.Background()
	reqTime := time.Now().UTC().Round(time.Second)
	fileModel := &ent.File{ID: 102, OwnerID: 8, Name: "broken.docx", Size: 2048, UpdatedAt: reqTime}
	entity := &ent.Entity{ID: 202, Source: "tenant-a/u8/broken.docx", UpdatedAt: reqTime}
	policy := &ent.StoragePolicy{ID: 302, BucketName: "cloudreve-test-bucket"}

	job, err := publishFTSExternalRequest(ctx, h.dep, fileModel, entity, policy, h.externalCfg, "integration_error", 1, `{"accepted":false}`)
	require.NoError(t, err)

	processMsg := h.waitProcessMessage(t)
	require.Equal(t, job.RequestID, processMsg.RequestID)

	errorPayload := externalFTSErrorMessage{
		Version:       1,
		RequestID:     job.RequestID,
		SnapshotToken: job.SnapshotToken,
		Status:        "error",
		Stage:         "extract",
		Code:          "font_missing",
		Message:       "font package missing",
		Detail:        "Simulated third-party failure",
		Retryable:     true,
		OccurredAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	_, err = h.responder.Producer().PublishJSON(ctx, h.externalCfg.Kafka.ErrorTopic, []byte(job.RequestID), errorPayload, map[string]string{
		"content-type": "application/json",
	})
	require.NoError(t, err)

	stored := h.waitJobStatus(t, job.RequestID, ftsExternalJobStatusError)
	require.Contains(t, stored.ErrorPayload, "font_missing")
	require.Contains(t, stored.ErrorPayload, "Simulated third-party failure")
	require.False(t, stored.CompletedAt.IsZero())
	firstCompletedAt := *stored.CompletedAt
	firstPayload := stored.ErrorPayload

	duplicate := errorPayload
	duplicate.Code = "duplicate_error"
	duplicate.Detail = "duplicate should be ignored"
	_, err = h.responder.Producer().PublishJSON(ctx, h.externalCfg.Kafka.ErrorTopic, []byte(job.RequestID), duplicate, map[string]string{
		"content-type": "application/json",
	})
	require.NoError(t, err)

	time.Sleep(2 * time.Second)
	afterDuplicate, err := h.dep.dbClient.FTSExternalJob.Query().Where(ftsexternaljob.RequestIDEQ(job.RequestID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, ftsExternalJobStatusError, afterDuplicate.Status)
	require.Equal(t, firstPayload, afterDuplicate.ErrorPayload)
	require.Equal(t, firstCompletedAt, *afterDuplicate.CompletedAt)

	lateSuccess := externalFTSResultMessage{
		Version:       1,
		RequestID:     job.RequestID,
		SnapshotToken: job.SnapshotToken,
		Status:        "success",
		Provider: externalFTSProviderInfo{
			Name:    "integration-mock-late-success",
			Version: "1.0.0",
		},
		Root: externalFTSResultRoot{
			Content: "late success should be ignored",
		},
	}
	_, err = h.responder.Producer().PublishJSON(ctx, h.externalCfg.Kafka.ResultTopic, []byte(job.RequestID), lateSuccess, map[string]string{
		"content-type": "application/json",
	})
	require.NoError(t, err)

	time.Sleep(2 * time.Second)
	afterLateSuccess, err := h.dep.dbClient.FTSExternalJob.Query().Where(ftsexternaljob.RequestIDEQ(job.RequestID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, ftsExternalJobStatusError, afterLateSuccess.Status)
	require.Equal(t, firstPayload, afterLateSuccess.ErrorPayload)
	require.Empty(t, afterLateSuccess.ResultPayload)
	require.Equal(t, firstCompletedAt, *afterLateSuccess.CompletedAt)
}

func TestFTSExternalKafkaSnapshotMismatchIgnoredIntegration(t *testing.T) {
	broker := strings.TrimSpace(os.Getenv("CLOUDREVE_FTS_EXTERNAL_KAFKA_ADDR"))
	if broker == "" {
		t.Skip("set CLOUDREVE_FTS_EXTERNAL_KAFKA_ADDR to run kafka integration test")
	}

	h := newFTSExternalIntegrationHarness(t, broker)
	defer h.close(t)

	ctx := context.Background()
	reqTime := time.Now().UTC().Round(time.Second)
	fileModel := &ent.File{ID: 103, OwnerID: 9, Name: "late.pdf", Size: 1024, UpdatedAt: reqTime}
	entity := &ent.Entity{ID: 203, Source: "tenant-a/u9/late.pdf", UpdatedAt: reqTime}
	policy := &ent.StoragePolicy{ID: 303, BucketName: "cloudreve-test-bucket"}

	job, err := publishFTSExternalRequest(ctx, h.dep, fileModel, entity, policy, h.externalCfg, "integration_snapshot_mismatch", 1, "")
	require.NoError(t, err)

	processMsg := h.waitProcessMessage(t)
	require.Equal(t, job.RequestID, processMsg.RequestID)

	mismatched := externalFTSResultMessage{
		Version:       1,
		RequestID:     job.RequestID,
		SnapshotToken: job.SnapshotToken + "-new",
		Status:        "success",
		Root: externalFTSResultRoot{
			Content: "stale payload should be ignored",
		},
	}
	_, err = h.responder.Producer().PublishJSON(ctx, h.externalCfg.Kafka.ResultTopic, []byte(job.RequestID), mismatched, map[string]string{
		"content-type": "application/json",
	})
	require.NoError(t, err)

	time.Sleep(2 * time.Second)
	reloaded, err := h.dep.dbClient.FTSExternalJob.Query().Where(ftsexternaljob.RequestIDEQ(job.RequestID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, ftsExternalJobStatusQueued, reloaded.Status)
	require.Empty(t, reloaded.ResultPayload)
	require.Empty(t, reloaded.ErrorPayload)
	require.Nil(t, reloaded.CompletedAt)
}

func newFTSExternalIntegrationHarness(t *testing.T, broker string) *ftsExternalIntegrationHarness {
	t.Helper()

	require.NoError(t, CloseFTSExternalKafka())

	originalInitialOffset := conf.KafkaConfig.Consumer.InitialOffset
	conf.KafkaConfig.Consumer.InitialOffset = "oldest"
	t.Cleanup(func() { conf.KafkaConfig.Consumer.InitialOffset = originalInitialOffset })

	ctx := context.Background()
	client, err := ent.Open("sqlite3", fmt.Sprintf("file:fts-external-integration-%d?mode=memory&cache=shared&_fk=1", time.Now().UnixNano()))
	require.NoError(t, err)
	require.NoError(t, client.Schema.Create(ctx))

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	cfg := &setting.FTSExternalExtractorSetting{
		Enabled:              true,
		Mode:                 setting.FTSExternalModePrimary,
		TimeoutSeconds:       30,
		RetryMax:             1,
		RecursiveAttachments: true,
		SkipEncryptedFiles:   true,
		Kafka: setting.FTSExternalKafkaSetting{
			UseGlobalKafka:   false,
			Brokers:          []string{broker},
			SecurityProtocol: "PLAINTEXT",
			ProcessTopic:     "cloudreve.fts.process." + suffix,
			ResultTopic:      "cloudreve.fts.result." + suffix,
			ErrorTopic:       "cloudreve.fts.error." + suffix,
			ConsumerGroup:    "cloudreve-fts-external-test-" + suffix,
		},
	}

	ensureKafkaTopics(t, broker, cfg.Kafka.ProcessTopic, cfg.Kafka.ResultTopic, cfg.Kafka.ErrorTopic)

	kafkaCfg := *conf.KafkaConfig
	kafkaCfg.Enabled = true
	kafkaCfg.Brokers = broker
	kafkaCfg.SecurityProtocol = "PLAINTEXT"
	kafkaCfg.ClientID = "cloudreve-fts-responder-" + suffix
	kafkaCfg.Consumer.InitialOffset = "oldest"

	responder, err := kafka.New(&kafkaCfg, logging.NewConsoleLogger(logging.LevelError))
	require.NoError(t, err)

	processStream := make(chan externalFTSProcessMessage, 4)
	require.NoError(t, responder.RegisterConsumer(kafka.ConsumerRegistration{
		Name:        "fts-process-listener-" + suffix,
		Group:       "fts-process-listener-" + suffix,
		Topics:      []string{cfg.Kafka.ProcessTopic},
		Concurrency: 1,
		Handler: func(ctx context.Context, msg *kafka.Message) error {
			var payload externalFTSProcessMessage
			if err := json.Unmarshal(msg.Value, &payload); err != nil {
				return err
			}
			processStream <- payload
			return nil
		},
	}))
	require.NoError(t, responder.Start(ctx))
	time.Sleep(3 * time.Second)

	dep := ftsExternalIntegrationDep{
		dbClient: client,
		settings: testSettingProvider{externalCfg: cfg},
		logger:   logging.NewConsoleLogger(logging.LevelError),
		config: ftsExternalIntegrationConfig{
			kafka: &kafkaCfg,
		},
	}

	require.NoError(t, StartFTSExternalKafka(ctx, dep))
	time.Sleep(6 * time.Second)

	return &ftsExternalIntegrationHarness{
		dep:           dep,
		externalCfg:   cfg,
		responder:     responder,
		processStream: processStream,
	}
}

func (h *ftsExternalIntegrationHarness) close(t *testing.T) {
	t.Helper()
	if h == nil {
		return
	}
	if h.responder != nil {
		require.NoError(t, h.responder.Close())
	}
	require.NoError(t, CloseFTSExternalKafka())
	if h.dep.dbClient != nil {
		require.NoError(t, h.dep.dbClient.Close())
	}
}

func (h *ftsExternalIntegrationHarness) waitProcessMessage(t *testing.T) externalFTSProcessMessage {
	t.Helper()
	select {
	case msg := <-h.processStream:
		return msg
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for external process message")
		return externalFTSProcessMessage{}
	}
}

func (h *ftsExternalIntegrationHarness) waitJobStatus(t *testing.T, requestID, status string) *ent.FTSExternalJob {
	t.Helper()

	ctx := context.Background()
	var current *ent.FTSExternalJob
	require.Eventually(t, func() bool {
		job, err := h.dep.dbClient.FTSExternalJob.Query().Where(ftsexternaljob.RequestIDEQ(requestID)).Only(ctx)
		if err != nil {
			return false
		}
		current = job
		return job.Status == status
	}, 20*time.Second, 300*time.Millisecond)

	return current
}

func ensureKafkaTopics(t *testing.T, broker string, topics ...string) {
	t.Helper()

	cfg := sarama.NewConfig()
	cfg.Version = sarama.V3_7_0_0
	cfg.Net.DialTimeout = 10 * time.Second
	cfg.Net.ReadTimeout = 30 * time.Second
	cfg.Net.WriteTimeout = 30 * time.Second

	admin, err := sarama.NewClusterAdmin([]string{broker}, cfg)
	require.NoError(t, err)
	defer func() { _ = admin.Close() }()

	for _, topic := range topics {
		err := admin.CreateTopic(topic, &sarama.TopicDetail{
			NumPartitions:     1,
			ReplicationFactor: 1,
		}, false)
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			require.NoError(t, err)
		}
	}
}
