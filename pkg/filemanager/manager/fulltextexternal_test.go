package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/ftsexternaljob"
	enttask "github.com/cloudreve/Cloudreve/v4/ent/task"
	inventorytypes "github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/kafka"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	_ "modernc.org/sqlite"
)

type testKafkaProducer struct {
	lastTopic string
	lastKey   []byte
	lastValue any
}

func (p *testKafkaProducer) Publish(ctx context.Context, msg *kafka.Message) (*kafka.PublishResult, error) {
	return &kafka.PublishResult{Partition: 0, Offset: 1}, nil
}

func (p *testKafkaProducer) PublishJSON(ctx context.Context, topic string, key []byte, payload any, headers map[string]string) (*kafka.PublishResult, error) {
	p.lastTopic = topic
	p.lastKey = append([]byte(nil), key...)
	p.lastValue = payload
	return &kafka.PublishResult{Partition: 0, Offset: 1}, nil
}

type testKafkaClient struct {
	producer      *testKafkaProducer
	registrations []kafka.ConsumerRegistration
	started       bool
}

func (c *testKafkaClient) Enabled() bool { return true }

func (c *testKafkaClient) Producer() kafka.Producer {
	if c.producer == nil {
		c.producer = &testKafkaProducer{}
	}
	return c.producer
}

func (c *testKafkaClient) RegisterConsumer(reg kafka.ConsumerRegistration) error {
	c.registrations = append(c.registrations, reg)
	return nil
}

func (c *testKafkaClient) Start(ctx context.Context) error {
	c.started = true
	return nil
}

func (c *testKafkaClient) Close() error { return nil }

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}

	return false
}

func TestUpsertFTSExternalJobPayloadIgnoresSnapshotMismatch(t *testing.T) {
	ctx := context.Background()
	client := newFTSExternalTestClient(t, ctx)
	defer client.Close()

	createFTSExternalJob(t, ctx, client, "req-1", "snapshot-a")

	dep := ftsExternalIntegrationDep{
		dbClient: client,
		logger:   logging.NewConsoleLogger(logging.LevelError),
	}

	payload := []byte(`{"version":1,"request_id":"req-1","snapshot_token":"snapshot-b","status":"success","root":{"content":"late result"}}`)
	if err := handleFTSExternalResultMessage(ctx, dep, payload); err != nil {
		t.Fatalf("expected snapshot mismatch to be ignored, got error: %v", err)
	}

	job, err := client.FTSExternalJob.Query().Where(ftsexternaljob.RequestIDEQ("req-1")).Only(ctx)
	if err != nil {
		t.Fatalf("failed to reload external job: %v", err)
	}
	if job.Status != ftsExternalJobStatusQueued {
		t.Fatalf("expected job status to stay queued, got %s", job.Status)
	}
	if job.ResultPayload != "" || job.CompletedAt != nil {
		t.Fatalf("expected mismatched payload to be ignored, got result=%q completed_at=%v", job.ResultPayload, job.CompletedAt)
	}
}

func TestPublishFTSExternalRequestUsesExplicitCfgWhenProviderDisabled(t *testing.T) {
	ctx := context.Background()
	client := newFTSExternalTestClient(t, ctx)
	defer client.Close()

	_ = CloseFTSExternalKafka()
	originalNewClient := newFTSExternalKafkaClient
	defer func() {
		newFTSExternalKafkaClient = originalNewClient
		_ = CloseFTSExternalKafka()
	}()

	fakeKafka := &testKafkaClient{producer: &testKafkaProducer{}}
	var capturedCfg *conf.Kafka
	newFTSExternalKafkaClient = func(cfg *conf.Kafka, logger logging.Logger) (kafka.Client, error) {
		capturedCfg = cfg
		return fakeKafka, nil
	}

	dep := ftsExternalIntegrationDep{
		dbClient: client,
		logger:   logging.NewConsoleLogger(logging.LevelError),
		settings: testSettingProvider{externalCfg: &setting.FTSExternalExtractorSetting{}},
	}

	fileModel := &ent.File{ID: 101, OwnerID: 7, Name: "report.pdf", Size: 4096, UpdatedAt: time.Now().UTC()}
	entity := &ent.Entity{ID: 201, Source: "tenant-a/u7/report.pdf", UpdatedAt: fileModel.UpdatedAt}
	policy := &ent.StoragePolicy{ID: 301, BucketName: "cloudreve-test-bucket"}
	explicitCfg := &setting.FTSExternalExtractorSetting{
		Enabled:        true,
		Mode:           setting.FTSExternalModePrimary,
		TimeoutSeconds: 20,
		Kafka: setting.FTSExternalKafkaSetting{
			UseGlobalKafka:   false,
			Brokers:          []string{"127.0.0.1:9092"},
			SecurityProtocol: "PLAINTEXT",
			ProcessTopic:     "process.override",
			ResultTopic:      "result.override",
			ErrorTopic:       "error.override",
			ConsumerGroup:    "group.override",
		},
	}

	job, err := publishFTSExternalRequest(ctx, dep, fileModel, entity, policy, explicitCfg, "primary", 1, "")
	if err != nil {
		t.Fatalf("expected explicit cfg to bootstrap kafka runtime, got error: %v", err)
	}
	if job == nil || strings.TrimSpace(job.RequestID) == "" {
		t.Fatalf("expected external job to be created, got %+v", job)
	}
	if capturedCfg == nil || strings.TrimSpace(capturedCfg.Brokers) != "127.0.0.1:9092" {
		t.Fatalf("expected kafka config to use explicit brokers, got %+v", capturedCfg)
	}
	if !fakeKafka.started {
		t.Fatal("expected explicit kafka runtime to be started")
	}
	if len(fakeKafka.registrations) != 1 {
		t.Fatalf("expected one consumer registration, got %d", len(fakeKafka.registrations))
	}
	if fakeKafka.producer.lastTopic != explicitCfg.Kafka.ProcessTopic {
		t.Fatalf("expected publish topic %q, got %q", explicitCfg.Kafka.ProcessTopic, fakeKafka.producer.lastTopic)
	}
}

func TestBuildFTSExternalKafkaConfigUsesOldestInitialOffset(t *testing.T) {
	cfg := &setting.FTSExternalExtractorSetting{
		Enabled: true,
		Kafka: setting.FTSExternalKafkaSetting{
			UseGlobalKafka:   false,
			Brokers:          []string{"127.0.0.1:9092"},
			SecurityProtocol: "PLAINTEXT",
			ProcessTopic:     "process.override",
			ResultTopic:      "result.override",
			ErrorTopic:       "error.override",
			ConsumerGroup:    "group.override",
		},
	}

	kafkaCfg, err := buildFTSExternalKafkaConfig(ftsExternalIntegrationDep{
		logger: logging.NewConsoleLogger(logging.LevelError),
	}, cfg)
	if err != nil {
		t.Fatalf("expected external kafka config to build, got error: %v", err)
	}
	if kafkaCfg.Consumer.InitialOffset != "oldest" {
		t.Fatalf("expected external consumer initial offset to be oldest, got %q", kafkaCfg.Consumer.InitialOffset)
	}
}

func TestUpsertFTSExternalJobPayloadKeepsSuccessTerminal(t *testing.T) {
	ctx := context.Background()
	client := newFTSExternalTestClient(t, ctx)
	defer client.Close()

	createFTSExternalJob(t, ctx, client, "req-1", "snapshot-a")

	dep := ftsExternalIntegrationDep{
		dbClient: client,
		logger:   logging.NewConsoleLogger(logging.LevelError),
	}

	resultPayload := []byte(`{"version":1,"request_id":"req-1","snapshot_token":"snapshot-a","status":"success","provider":{"name":"vendor-a"},"root":{"content":"hello"}}`)
	if err := handleFTSExternalResultMessage(ctx, dep, resultPayload); err != nil {
		t.Fatalf("expected success payload to be accepted, got error: %v", err)
	}
	job, err := client.FTSExternalJob.Query().Where(ftsexternaljob.RequestIDEQ("req-1")).Only(ctx)
	if err != nil {
		t.Fatalf("failed to reload external job: %v", err)
	}
	if job.Status != ftsExternalJobStatusSuccess {
		t.Fatalf("expected job status to be success, got %s", job.Status)
	}
	if job.CompletedAt == nil {
		t.Fatal("expected completed_at to be set after success")
	}
	firstCompletedAt := *job.CompletedAt
	firstPayload := job.ResultPayload

	errorPayload := []byte(`{"version":1,"request_id":"req-1","snapshot_token":"snapshot-a","status":"error","code":"late_error","detail":"should be ignored"}`)
	if err := handleFTSExternalErrorMessage(ctx, dep, errorPayload); err != nil {
		t.Fatalf("expected late error payload to be ignored, got error: %v", err)
	}

	job, err = client.FTSExternalJob.Query().Where(ftsexternaljob.RequestIDEQ("req-1")).Only(ctx)
	if err != nil {
		t.Fatalf("failed to reload terminal success job: %v", err)
	}
	if job.Status != ftsExternalJobStatusSuccess {
		t.Fatalf("expected success to remain terminal, got %s", job.Status)
	}
	if job.ResultPayload != firstPayload {
		t.Fatalf("expected success payload to stay unchanged, got %q want %q", job.ResultPayload, firstPayload)
	}
	if job.ErrorPayload != "" {
		t.Fatalf("expected late error payload to be ignored, got %q", job.ErrorPayload)
	}
	if job.CompletedAt == nil || !job.CompletedAt.Equal(firstCompletedAt) {
		t.Fatalf("expected completed_at to stay unchanged, got %v want %v", job.CompletedAt, firstCompletedAt)
	}
}

func TestEvaluateFTSExtractionQualityRejectsGarbleAndControlChars(t *testing.T) {
	cfg := &setting.FTSExternalExtractorSetting{
		Quality: setting.FTSExternalQualitySetting{
			Enabled:             true,
			MinTextLength:       10,
			MaxReplacementRatio: 0.05,
			MaxControlCharRatio: 0.05,
			MinPrintableRatio:   0.9,
		},
	}
	doc := &searcher.SearchFileDocument{
		Content: "abc\uFFFD\uFFFD\uFFFDdef",
		Attachments: []searcher.SearchAttachmentDocument{{
			Content: "\x01\x02bad",
		}},
	}

	report := evaluateFTSExtractionQuality(doc, cfg)
	if report == nil {
		t.Fatal("expected quality report")
	}
	if report.Accepted {
		t.Fatalf("expected report to be rejected: %+v", report)
	}
	if report.TextLength == 0 || report.ReplacementCount == 0 || report.ControlCount == 0 {
		t.Fatalf("expected report counters to be populated: %+v", report)
	}
	if len(report.Reasons) == 0 {
		t.Fatalf("expected rejection reasons: %+v", report)
	}

	found := map[string]bool{}
	for _, reason := range report.Reasons {
		found[reason] = true
	}
	if !found["replacement_ratio_high"] {
		t.Fatalf("expected replacement_ratio_high reason: %+v", report.Reasons)
	}
	if !found["control_ratio_high"] {
		t.Fatalf("expected control_ratio_high reason: %+v", report.Reasons)
	}
}

func TestEvaluateFTSExtractionQualityRejectsBoxGlyphFontLoss(t *testing.T) {
	cfg := &setting.FTSExternalExtractorSetting{
		Quality: setting.FTSExternalQualitySetting{
			Enabled:             true,
			MinTextLength:       6,
			MaxReplacementRatio: 0.5,
			MaxControlCharRatio: 0.5,
			MinPrintableRatio:   0.5,
		},
	}
	doc := &searcher.SearchFileDocument{
		Content: "□□□□ □□□□ □□□□",
	}

	report := evaluateFTSExtractionQuality(doc, cfg)
	if report == nil {
		t.Fatal("expected quality report")
	}
	if report.Accepted {
		t.Fatalf("expected report to be rejected for tofu glyphs: %+v", report)
	}
	if report.BoxGlyphCount < 4 || report.MaxBoxGlyphRun < 3 || report.BoxGlyphRatio <= 0 {
		t.Fatalf("expected box glyph counters to be populated: %+v", report)
	}
	if !containsString(report.Reasons, "font_issue_box_glyphs") {
		t.Fatalf("expected font_issue_box_glyphs reason: %+v", report.Reasons)
	}
}

func TestEvaluateFTSExtractionQualityAllowsSingleCheckboxLikeText(t *testing.T) {
	cfg := &setting.FTSExternalExtractorSetting{
		Quality: setting.FTSExternalQualitySetting{
			Enabled:             true,
			MinTextLength:       10,
			MaxReplacementRatio: 0.5,
			MaxControlCharRatio: 0.5,
			MinPrintableRatio:   0.5,
		},
	}
	doc := &searcher.SearchFileDocument{
		Content: "□ 已阅读并同意服务条款，以下正文内容均为正常文字，不应被误判为字体缺失。",
	}

	report := evaluateFTSExtractionQuality(doc, cfg)
	if report == nil {
		t.Fatal("expected quality report")
	}
	if !report.Accepted {
		t.Fatalf("expected single checkbox style text to pass quality check: %+v", report)
	}
	if containsString(report.Reasons, "font_issue_box_glyphs") {
		t.Fatalf("unexpected font_issue_box_glyphs reason: %+v", report.Reasons)
	}
}

func BenchmarkEvaluateFTSExtractionQuality(b *testing.B) {
	cfg := &setting.FTSExternalExtractorSetting{
		Quality: setting.FTSExternalQualitySetting{
			Enabled:             true,
			MinTextLength:       16,
			MaxReplacementRatio: 0.05,
			MaxControlCharRatio: 0.05,
			MinPrintableRatio:   0.9,
		},
	}
	doc := &searcher.SearchFileDocument{
		Content: strings.Repeat("这是正常正文内容。", 64) + strings.Repeat("□", 8),
		Attachments: []searcher.SearchAttachmentDocument{{
			Content: strings.Repeat("附件内容。", 32),
		}},
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		report := evaluateFTSExtractionQuality(doc, cfg)
		if report == nil {
			b.Fatal("expected quality report")
		}
	}
}

func TestUpsertFTSExternalJobPayloadIgnoresSuccessAfterErrorTerminal(t *testing.T) {
	ctx := context.Background()
	client := newFTSExternalTestClient(t, ctx)
	defer client.Close()

	createFTSExternalJob(t, ctx, client, "req-2", "snapshot-a")

	dep := ftsExternalIntegrationDep{
		dbClient: client,
		logger:   logging.NewConsoleLogger(logging.LevelError),
	}

	errorPayload := []byte(`{"version":1,"request_id":"req-2","snapshot_token":"snapshot-a","status":"error","code":"extract_failed","detail":"first failure"}`)
	if err := handleFTSExternalErrorMessage(ctx, dep, errorPayload); err != nil {
		t.Fatalf("expected first error payload to be accepted, got error: %v", err)
	}

	successPayload := []byte(`{"version":1,"request_id":"req-2","snapshot_token":"snapshot-a","status":"success","root":{"content":"recovered later"}}`)
	if err := handleFTSExternalResultMessage(ctx, dep, successPayload); err != nil {
		t.Fatalf("expected late success payload to be ignored, got error: %v", err)
	}

	job, err := client.FTSExternalJob.Query().Where(ftsexternaljob.RequestIDEQ("req-2")).Only(ctx)
	if err != nil {
		t.Fatalf("failed to reload terminal error job: %v", err)
	}
	if job.Status != ftsExternalJobStatusError {
		t.Fatalf("expected error status to remain terminal, got %s", job.Status)
	}
	if job.ResultPayload != "" || job.ErrorPayload == "" {
		t.Fatalf("expected late success not to replace error payload, got result=%q error=%q", job.ResultPayload, job.ErrorPayload)
	}
}

func TestUpsertFTSExternalJobPayloadIgnoresDuplicateError(t *testing.T) {
	ctx := context.Background()
	client := newFTSExternalTestClient(t, ctx)
	defer client.Close()

	createFTSExternalJob(t, ctx, client, "req-3", "snapshot-a")

	dep := ftsExternalIntegrationDep{
		dbClient: client,
		logger:   logging.NewConsoleLogger(logging.LevelError),
	}

	firstPayload := []byte(`{"version":1,"request_id":"req-3","snapshot_token":"snapshot-a","status":"error","code":"first","detail":"first failure"}`)
	if err := handleFTSExternalErrorMessage(ctx, dep, firstPayload); err != nil {
		t.Fatalf("expected first error payload to be accepted, got error: %v", err)
	}

	job, err := client.FTSExternalJob.Query().Where(ftsexternaljob.RequestIDEQ("req-3")).Only(ctx)
	if err != nil {
		t.Fatalf("failed to reload error job: %v", err)
	}
	if job.CompletedAt == nil {
		t.Fatal("expected completed_at after error payload")
	}
	firstCompletedAt := *job.CompletedAt
	firstError := job.ErrorPayload

	duplicatePayload := []byte(`{"version":1,"request_id":"req-3","snapshot_token":"snapshot-a","status":"error","code":"second","detail":"duplicate should be ignored"}`)
	if err := handleFTSExternalErrorMessage(ctx, dep, duplicatePayload); err != nil {
		t.Fatalf("expected duplicate error payload to be ignored, got error: %v", err)
	}

	job, err = client.FTSExternalJob.Query().Where(ftsexternaljob.RequestIDEQ("req-3")).Only(ctx)
	if err != nil {
		t.Fatalf("failed to reload duplicate error job: %v", err)
	}
	if job.Status != ftsExternalJobStatusError {
		t.Fatalf("expected error status to remain terminal, got %s", job.Status)
	}
	if job.ErrorPayload != firstError {
		t.Fatalf("expected duplicate error not to overwrite payload, got %q want %q", job.ErrorPayload, firstError)
	}
	if job.CompletedAt == nil || !job.CompletedAt.Equal(firstCompletedAt) {
		t.Fatalf("expected duplicate error not to overwrite completed_at, got %v want %v", job.CompletedAt, firstCompletedAt)
	}
}

func TestExternalFTSEligible(t *testing.T) {
	baseFile := &ent.File{ID: 12, Type: int(inventorytypes.FileTypeFile), Size: 128}
	baseEntity := &ent.Entity{ID: 34, Source: "tenant-a/u7/report.pdf"}
	basePolicy := &ent.StoragePolicy{BucketName: "cloudreve", Type: inventorytypes.PolicyTypeS3}
	baseCfg := &setting.FTSExternalExtractorSetting{Enabled: true, SkipEncryptedFiles: true}

	tests := []struct {
		name          string
		fileModel     *ent.File
		primaryEntity *ent.Entity
		policy        *ent.StoragePolicy
		cfg           *setting.FTSExternalExtractorSetting
		want          bool
	}{
		{name: "allow remote object storage", fileModel: baseFile, primaryEntity: baseEntity, policy: basePolicy, cfg: baseCfg, want: true},
		{name: "reject disabled config", fileModel: baseFile, primaryEntity: baseEntity, policy: basePolicy, cfg: &setting.FTSExternalExtractorSetting{}, want: false},
		{name: "reject empty file", fileModel: &ent.File{ID: 12, Size: 0}, primaryEntity: baseEntity, policy: basePolicy, cfg: baseCfg, want: false},
		{name: "reject folders", fileModel: &ent.File{ID: 12, Type: int(inventorytypes.FileTypeFolder)}, primaryEntity: baseEntity, policy: basePolicy, cfg: baseCfg, want: false},
		{name: "reject encrypted when skip enabled", fileModel: baseFile, primaryEntity: &ent.Entity{ID: 34, Source: "tenant-a/u7/report.pdf", Props: &inventorytypes.EntityProps{EncryptMetadata: &inventorytypes.EncryptMetadata{}}}, policy: basePolicy, cfg: baseCfg, want: false},
		{name: "allow encrypted when skip disabled", fileModel: baseFile, primaryEntity: &ent.Entity{ID: 34, Source: "tenant-a/u7/report.pdf", Props: &inventorytypes.EntityProps{EncryptMetadata: &inventorytypes.EncryptMetadata{}}}, policy: basePolicy, cfg: &setting.FTSExternalExtractorSetting{Enabled: true, SkipEncryptedFiles: false}, want: true},
		{name: "reject missing bucket", fileModel: baseFile, primaryEntity: baseEntity, policy: &ent.StoragePolicy{Type: inventorytypes.PolicyTypeS3}, cfg: baseCfg, want: false},
		{name: "reject missing source", fileModel: baseFile, primaryEntity: &ent.Entity{ID: 34}, policy: basePolicy, cfg: baseCfg, want: false},
		{name: "reject local policy", fileModel: baseFile, primaryEntity: baseEntity, policy: &ent.StoragePolicy{BucketName: "cloudreve", Type: inventorytypes.PolicyTypeLocal}, cfg: baseCfg, want: false},
		{name: "reject onedrive policy", fileModel: baseFile, primaryEntity: baseEntity, policy: &ent.StoragePolicy{BucketName: "cloudreve", Type: inventorytypes.PolicyTypeOd}, cfg: baseCfg, want: false},
		{name: "reject remote relay policy", fileModel: baseFile, primaryEntity: baseEntity, policy: &ent.StoragePolicy{BucketName: "cloudreve", Type: inventorytypes.PolicyTypeRemote}, cfg: baseCfg, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := externalFTSEligible(tt.fileModel, tt.primaryEntity, tt.policy, tt.cfg); got != tt.want {
				t.Fatalf("unexpected eligibility: got %v want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeExternalAttachmentsMapsFileRootParent(t *testing.T) {
	fileModel := &ent.File{ID: 42}
	entity := &testEntity{id: 7}

	attachments := normalizeExternalAttachments(fileModel, entity, []externalFTSAttachment{
		{
			ID:       "att-1",
			ParentID: "file:42",
			Depth:    1,
			Type:     "attachment",
			Name:     "outer.txt",
			Path:     "embedded/outer.txt",
			MimeType: "text/plain",
			Content:  "outer",
		},
		{
			ID:       "att-2",
			ParentID: "att-1",
			Depth:    2,
			Type:     "attachment",
			Name:     "inner.txt",
			Path:     "embedded/inner.txt",
			MimeType: "text/plain",
			Content:  "inner",
		},
	})
	if len(attachments) != 2 {
		t.Fatalf("unexpected attachment count: got %d want 2", len(attachments))
	}
	if got, want := attachments[0].ParentID, attachmentRootParentID(42); got != want {
		t.Fatalf("expected file root parent id, got %q want %q", got, want)
	}
	if got, want := attachments[1].ParentID, attachments[0].ID; got != want {
		t.Fatalf("expected nested attachment to point to parent doc id, got %q want %q", got, want)
	}
}

func TestRetryOrFallbackExternalFallsBackToLocalWhenRetryBudgetExhausted(t *testing.T) {
	ctx := context.Background()
	client := newFTSExternalTestClient(t, ctx)
	defer client.Close()

	job := createFTSExternalJob(t, ctx, client, "req-timeout", "snapshot-timeout")

	original := fullTextPerformIndexing
	defer func() { fullTextPerformIndexing = original }()

	called := 0
	fullTextPerformIndexing = func(ctx context.Context, fm *manager, fileID int) (enttask.Status, error) {
		called++
		if fileID != 101 {
			t.Fatalf("unexpected fallback file id: %d", fileID)
		}
		return enttask.StatusCompleted, nil
	}

	taskState := &FullTextIndexTaskState{}
	taskState.Upsert(FullTextIndexTaskItem{FileID: 101})
	if !taskState.ActivateNext() {
		t.Fatal("expected active task item")
	}

	taskModel := &FullTextIndexTask{
		DBTask: &queue.DBTask{Task: &ent.Task{}},
	}
	fm := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		settings: testSettingProvider{externalCfg: &setting.FTSExternalExtractorSetting{Enabled: true, RetryMax: 0}},
		dep: ftsExternalIntegrationDep{
			dbClient: client,
			logger:   logging.NewConsoleLogger(logging.LevelError),
		},
	}

	status, err := taskModel.retryOrFallbackExternal(ctx, fm, taskState, job, "external job timed out")
	if err != nil {
		t.Fatalf("expected fallback to local indexing, got error: %v", err)
	}
	if status != enttask.StatusProcessing {
		t.Fatalf("unexpected status after fallback: got %s want %s", status, enttask.StatusProcessing)
	}
	if called != 1 {
		t.Fatalf("expected local fallback indexing to be called once, got %d", called)
	}
	if taskState.Active != nil {
		t.Fatalf("expected active item to be cleared after fallback, got %+v", taskState.Active)
	}
	if taskState.Len() != 0 {
		t.Fatalf("expected task queue to be drained after fallback, got len=%d", taskState.Len())
	}
	if taskModel.State() == "" {
		t.Fatal("expected task private state to be persisted after fallback")
	}

	reloaded, err := client.FTSExternalJob.Query().Where(ftsexternaljob.RequestIDEQ("req-timeout")).Only(ctx)
	if err != nil {
		t.Fatalf("failed to reload timed-out job: %v", err)
	}
	if reloaded.Status != ftsExternalJobStatusError {
		t.Fatalf("expected timed-out job to be marked as error after local fallback, got %s", reloaded.Status)
	}
	if reloaded.CompletedAt == nil {
		t.Fatal("expected timed-out job to have completed_at after local fallback")
	}
	if reloaded.ErrorPayload == "" {
		t.Fatal("expected timed-out job to keep fallback error payload")
	}
	parsed, err := parseFTSExternalErrorPayload(reloaded.ErrorPayload)
	if err != nil {
		t.Fatalf("expected fallback error payload to be json, got error: %v", err)
	}
	if parsed.Code != "timeout" || parsed.Stage != "orchestrate" {
		t.Fatalf("unexpected fallback error payload: %+v", parsed)
	}
}

func TestFinalizeExternalIndexedFileDeletesStaleIndexWhenFileMissing(t *testing.T) {
	searchIndexer := &testSearchIndexer{}
	dep := testDep{
		fileClient:    &testFileClient{},
		searchIndexer: searchIndexer,
	}
	fm := &manager{
		l:   logging.NewConsoleLogger(logging.LevelError),
		dep: dep,
	}
	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)

	status, err := finalizeExternalIndexedFile(ctx, fm, 404, &ent.FTSExternalJob{
		RequestID:     "req-missing",
		ResultPayload: `{"version":1,"request_id":"req-missing","snapshot_token":"snapshot-a","status":"success","root":{"content":"late result"}}`,
	})
	if err != nil {
		t.Fatalf("expected missing file to be ignored by deleting stale index, got error: %v", err)
	}
	if status != enttask.StatusCompleted {
		t.Fatalf("unexpected status for missing file finalize: got %s want %s", status, enttask.StatusCompleted)
	}
	if len(searchIndexer.deleted) != 1 || searchIndexer.deleted[0] != 404 {
		t.Fatalf("expected stale file id to be deleted from index, got %#v", searchIndexer.deleted)
	}
	if searchIndexer.upserted != 0 {
		t.Fatalf("expected no upsert on missing file finalize, got %d", searchIndexer.upserted)
	}
}

func TestFindReusableFTSExternalJobPrefersSuccessForSameSnapshot(t *testing.T) {
	ctx := context.Background()
	client := newFTSExternalTestClient(t, ctx)
	defer client.Close()

	reqTime := time.Now().UTC().Round(time.Second)
	fileModel := &ent.File{ID: 10, Size: 4096, UpdatedAt: reqTime}
	entity := &ent.Entity{ID: 30, UpdatedAt: reqTime}
	snapshotToken := buildFTSExternalSnapshotToken(fileModel, entity)

	createFTSExternalJob(t, ctx, client, "req-queued", snapshotToken)
	successJob := createFTSExternalJob(t, ctx, client, "req-success", snapshotToken)
	if err := client.FTSExternalJob.UpdateOneID(successJob.ID).
		SetStatus(ftsExternalJobStatusSuccess).
		SetResultPayload(`{"version":1,"request_id":"req-success","snapshot_token":"` + snapshotToken + `","status":"success","root":{"content":"ok"}}`).
		SetCompletedAt(time.Now()).
		Exec(ctx); err != nil {
		t.Fatalf("failed to mark success job: %v", err)
	}

	dep := ftsExternalIntegrationDep{
		dbClient: client,
		logger:   logging.NewConsoleLogger(logging.LevelError),
	}

	job, err := findReusableFTSExternalJob(ctx, dep, fileModel, entity)
	if err != nil {
		t.Fatalf("expected reusable job query to succeed, got error: %v", err)
	}
	if job == nil {
		t.Fatal("expected reusable external job")
	}
	if got, want := job.RequestID, "req-success"; got != want {
		t.Fatalf("unexpected reusable job request id: got %q want %q", got, want)
	}
}

func TestQueueExternalExtractionReusesQueuedJob(t *testing.T) {
	originalHasSidecar := hasCurrentExternalFTSSidecarForTask
	originalFindReusable := findReusableFTSExternalJobForTask
	originalPublish := publishFTSExternalRequestForTask
	defer func() {
		hasCurrentExternalFTSSidecarForTask = originalHasSidecar
		findReusableFTSExternalJobForTask = originalFindReusable
		publishFTSExternalRequestForTask = originalPublish
	}()

	hasCurrentExternalFTSSidecarForTask = func(m *manager, ctx context.Context, candidate *ftsExternalCandidate) bool {
		return false
	}
	findReusableFTSExternalJobForTask = func(ctx context.Context, dep dependency.Dep, fileModel *ent.File, primaryEntity *ent.Entity) (*ent.FTSExternalJob, error) {
		return &ent.FTSExternalJob{
			RequestID: "req-reused-queued",
			Status:    ftsExternalJobStatusQueued,
		}, nil
	}
	publishFTSExternalRequestForTask = func(ctx context.Context, dep dependency.Dep, fileModel *ent.File, primaryEntity *ent.Entity, policy *ent.StoragePolicy, cfg *setting.FTSExternalExtractorSetting, triggerReason string, attempt int, qualityReport string) (*ent.FTSExternalJob, error) {
		t.Fatalf("expected queued reusable job to prevent new publish")
		return nil, nil
	}

	uri := mustURI(t, "cloudreve:///my/report.pdf")
	state := &FullTextIndexTaskState{}
	state.Upsert(FullTextIndexTaskItem{FileID: 101, Uri: uri})
	if !state.ActivateNext() {
		t.Fatal("expected active file")
	}

	taskModel := &FullTextIndexTask{DBTask: &queue.DBTask{Task: &ent.Task{PublicState: &inventorytypes.TaskPublicState{}}}}
	status, err := taskModel.queueExternalExtraction(context.Background(), &manager{
		l: logging.NewConsoleLogger(logging.LevelError),
	}, state, &ftsExternalCandidate{
		fileModel:     &ent.File{ID: 101},
		primaryEntity: &ent.Entity{ID: 201},
	}, "primary", 1, "")
	if err != nil {
		t.Fatalf("expected queued reusable job to suspend cleanly, got error: %v", err)
	}
	if status != enttask.StatusSuspending {
		t.Fatalf("unexpected status for queued reusable job: got %s want %s", status, enttask.StatusSuspending)
	}
	if state.Phase != fullTextIndexPhaseAwaitExternal || state.ExternalRequestID != "req-reused-queued" {
		t.Fatalf("expected task to await existing external job, got phase=%s request=%q", state.Phase, state.ExternalRequestID)
	}
}

func TestQueueExternalExtractionReusesSuccessfulJob(t *testing.T) {
	originalHasSidecar := hasCurrentExternalFTSSidecarForTask
	originalFindReusable := findReusableFTSExternalJobForTask
	originalFinalize := finalizeExternalIndexedFileForTask
	originalPublish := publishFTSExternalRequestForTask
	defer func() {
		hasCurrentExternalFTSSidecarForTask = originalHasSidecar
		findReusableFTSExternalJobForTask = originalFindReusable
		finalizeExternalIndexedFileForTask = originalFinalize
		publishFTSExternalRequestForTask = originalPublish
	}()

	hasCurrentExternalFTSSidecarForTask = func(m *manager, ctx context.Context, candidate *ftsExternalCandidate) bool {
		return false
	}
	findReusableFTSExternalJobForTask = func(ctx context.Context, dep dependency.Dep, fileModel *ent.File, primaryEntity *ent.Entity) (*ent.FTSExternalJob, error) {
		return &ent.FTSExternalJob{
			RequestID:     "req-reused-success",
			Status:        ftsExternalJobStatusSuccess,
			ResultPayload: `{"version":1,"request_id":"req-reused-success","status":"success","root":{"content":"ok"}}`,
		}, nil
	}
	publishFTSExternalRequestForTask = func(ctx context.Context, dep dependency.Dep, fileModel *ent.File, primaryEntity *ent.Entity, policy *ent.StoragePolicy, cfg *setting.FTSExternalExtractorSetting, triggerReason string, attempt int, qualityReport string) (*ent.FTSExternalJob, error) {
		t.Fatalf("expected successful reusable job to prevent new publish")
		return nil, nil
	}

	finalizedFileID := 0
	finalizeExternalIndexedFileForTask = func(ctx context.Context, fm *manager, fileID int, job *ent.FTSExternalJob) (enttask.Status, error) {
		finalizedFileID = fileID
		if job == nil || job.RequestID != "req-reused-success" {
			t.Fatalf("unexpected reusable success job: %+v", job)
		}
		return enttask.StatusCompleted, nil
	}

	uri := mustURI(t, "cloudreve:///my/report.pdf")
	state := &FullTextIndexTaskState{}
	state.Upsert(FullTextIndexTaskItem{FileID: 101, Uri: uri})
	if !state.ActivateNext() {
		t.Fatal("expected active file")
	}

	taskModel := &FullTextIndexTask{DBTask: &queue.DBTask{Task: &ent.Task{PublicState: &inventorytypes.TaskPublicState{}}}}
	status, err := taskModel.queueExternalExtraction(context.Background(), &manager{
		l: logging.NewConsoleLogger(logging.LevelError),
	}, state, &ftsExternalCandidate{
		fileModel:     &ent.File{ID: 101},
		primaryEntity: &ent.Entity{ID: 201},
	}, "primary", 1, "")
	if err != nil {
		t.Fatalf("expected reusable success job to finalize cleanly, got error: %v", err)
	}
	if status != enttask.StatusProcessing {
		t.Fatalf("unexpected status for reusable success job: got %s want %s", status, enttask.StatusProcessing)
	}
	if finalizedFileID != 101 {
		t.Fatalf("expected reusable success to finalize file 101, got %d", finalizedFileID)
	}
	if state.Active != nil || state.Len() != 0 {
		t.Fatalf("expected reusable success path to complete active item, got state=%+v", state)
	}
}

func TestDispatchExternalIfConfiguredFallbackOnErrorQueuesWhenLocalTextEmpty(t *testing.T) {
	originalLoadCandidate := loadFTSExternalCandidateForTask
	originalHasSidecar := hasCurrentExternalFTSSidecarForTask
	originalBuildDoc := buildFTSFileDocumentForTask
	originalPublish := publishFTSExternalRequestForTask
	defer func() {
		loadFTSExternalCandidateForTask = originalLoadCandidate
		hasCurrentExternalFTSSidecarForTask = originalHasSidecar
		buildFTSFileDocumentForTask = originalBuildDoc
		publishFTSExternalRequestForTask = originalPublish
	}()

	uri := mustURI(t, "cloudreve:///my/report.pdf")
	loadFTSExternalCandidateForTask = func(m *manager, ctx context.Context, fileID int) (*ftsExternalCandidate, error) {
		return &ftsExternalCandidate{
			fileModel:     &ent.File{ID: fileID, OwnerID: 7, Name: "report.pdf"},
			primaryEntity: &ent.Entity{ID: 201, Source: "tenant-a/u7/report.pdf", UpdatedAt: time.Now()},
			policy:        &ent.StoragePolicy{ID: 301, BucketName: "cloudreve", Type: inventorytypes.PolicyTypeS3},
			uri:           uri,
		}, nil
	}
	hasCurrentExternalFTSSidecarForTask = func(m *manager, ctx context.Context, candidate *ftsExternalCandidate) bool {
		return false
	}
	buildFTSFileDocumentForTask = func(m *manager, ctx context.Context, fileID int, opts FTSBuildOptions) (*searcher.SearchFileDocument, *fs.URI, error) {
		return &searcher.SearchFileDocument{FileID: fileID, EntityID: 201}, uri, nil
	}

	var capturedReason string
	publishFTSExternalRequestForTask = func(ctx context.Context, dep dependency.Dep, fileModel *ent.File, primaryEntity *ent.Entity, policy *ent.StoragePolicy, cfg *setting.FTSExternalExtractorSetting, triggerReason string, attempt int, qualityReport string) (*ent.FTSExternalJob, error) {
		capturedReason = triggerReason
		return &ent.FTSExternalJob{RequestID: "req-empty"}, nil
	}

	cfg := &setting.FTSExternalExtractorSetting{
		Enabled:        true,
		Mode:           setting.FTSExternalModeFallbackOnError,
		TimeoutSeconds: 60,
	}
	taskModel := &FullTextIndexTask{DBTask: &queue.DBTask{Task: &ent.Task{PublicState: &inventorytypes.TaskPublicState{}}}}
	state := &FullTextIndexTaskState{}
	state.Upsert(FullTextIndexTaskItem{FileID: 101, Uri: uri})
	fm := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		settings: testSettingProvider{externalCfg: cfg},
	}

	status, handled, err := taskModel.dispatchExternalIfConfigured(context.Background(), fm, state, FullTextIndexTaskItem{FileID: 101, Uri: uri})
	if err != nil {
		t.Fatalf("expected empty local text to queue external extraction, got error: %v", err)
	}
	if !handled {
		t.Fatal("expected external dispatcher to handle empty local text case")
	}
	if status != enttask.StatusSuspending {
		t.Fatalf("unexpected status for empty local text fallback: got %s want %s", status, enttask.StatusSuspending)
	}
	if state.Phase != fullTextIndexPhaseAwaitExternal || state.ExternalRequestID != "req-empty" {
		t.Fatalf("expected await external state, got phase=%s request=%q", state.Phase, state.ExternalRequestID)
	}
	if capturedReason != "local_text_empty" {
		t.Fatalf("unexpected trigger reason: got %q want %q", capturedReason, "local_text_empty")
	}
}

func TestDispatchExternalIfConfiguredFallbackOnQualityQueuesWhenRejected(t *testing.T) {
	originalLoadCandidate := loadFTSExternalCandidateForTask
	originalHasSidecar := hasCurrentExternalFTSSidecarForTask
	originalBuildDoc := buildFTSFileDocumentForTask
	originalPublish := publishFTSExternalRequestForTask
	defer func() {
		loadFTSExternalCandidateForTask = originalLoadCandidate
		hasCurrentExternalFTSSidecarForTask = originalHasSidecar
		buildFTSFileDocumentForTask = originalBuildDoc
		publishFTSExternalRequestForTask = originalPublish
	}()

	uri := mustURI(t, "cloudreve:///my/bad.pdf")
	loadFTSExternalCandidateForTask = func(m *manager, ctx context.Context, fileID int) (*ftsExternalCandidate, error) {
		return &ftsExternalCandidate{
			fileModel:     &ent.File{ID: fileID, OwnerID: 7, Name: "bad.pdf"},
			primaryEntity: &ent.Entity{ID: 202, Source: "tenant-a/u7/bad.pdf", UpdatedAt: time.Now()},
			policy:        &ent.StoragePolicy{ID: 302, BucketName: "cloudreve", Type: inventorytypes.PolicyTypeS3},
			uri:           uri,
		}, nil
	}
	hasCurrentExternalFTSSidecarForTask = func(m *manager, ctx context.Context, candidate *ftsExternalCandidate) bool {
		return false
	}
	buildFTSFileDocumentForTask = func(m *manager, ctx context.Context, fileID int, opts FTSBuildOptions) (*searcher.SearchFileDocument, *fs.URI, error) {
		return &searcher.SearchFileDocument{
			FileID:   fileID,
			EntityID: 202,
			Content:  "□□□□ □□□□ □□□□",
		}, uri, nil
	}

	var (
		capturedReason        string
		capturedQualityReport string
	)
	publishFTSExternalRequestForTask = func(ctx context.Context, dep dependency.Dep, fileModel *ent.File, primaryEntity *ent.Entity, policy *ent.StoragePolicy, cfg *setting.FTSExternalExtractorSetting, triggerReason string, attempt int, qualityReport string) (*ent.FTSExternalJob, error) {
		capturedReason = triggerReason
		capturedQualityReport = qualityReport
		return &ent.FTSExternalJob{RequestID: "req-quality"}, nil
	}

	cfg := &setting.FTSExternalExtractorSetting{
		Enabled:        true,
		Mode:           setting.FTSExternalModeFallbackOnErrorOrQuality,
		TimeoutSeconds: 60,
		Quality: setting.FTSExternalQualitySetting{
			Enabled:             true,
			MinTextLength:       6,
			MaxReplacementRatio: 0.5,
			MaxControlCharRatio: 0.5,
			MinPrintableRatio:   0.5,
		},
	}
	taskModel := &FullTextIndexTask{DBTask: &queue.DBTask{Task: &ent.Task{PublicState: &inventorytypes.TaskPublicState{}}}}
	state := &FullTextIndexTaskState{}
	state.Upsert(FullTextIndexTaskItem{FileID: 102, Uri: uri})
	fm := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		settings: testSettingProvider{externalCfg: cfg},
	}

	status, handled, err := taskModel.dispatchExternalIfConfigured(context.Background(), fm, state, FullTextIndexTaskItem{FileID: 102, Uri: uri})
	if err != nil {
		t.Fatalf("expected rejected quality to queue external extraction, got error: %v", err)
	}
	if !handled {
		t.Fatal("expected external dispatcher to handle rejected quality case")
	}
	if status != enttask.StatusSuspending {
		t.Fatalf("unexpected status for quality fallback: got %s want %s", status, enttask.StatusSuspending)
	}
	if state.Phase != fullTextIndexPhaseAwaitExternal || state.ExternalRequestID != "req-quality" {
		t.Fatalf("expected await external state, got phase=%s request=%q", state.Phase, state.ExternalRequestID)
	}
	if capturedReason != "quality_rejected" {
		t.Fatalf("unexpected trigger reason: got %q want %q", capturedReason, "quality_rejected")
	}
	var report externalFTSQualityReport
	if err := json.Unmarshal([]byte(capturedQualityReport), &report); err != nil {
		t.Fatalf("expected quality report json, got error: %v", err)
	}
	if report.Accepted {
		t.Fatalf("expected rejected quality report, got %+v", report)
	}
	if !containsString(report.Reasons, "font_issue_box_glyphs") {
		t.Fatalf("expected box glyph rejection reason, got %+v", report.Reasons)
	}
}

func TestDispatchExternalIfConfiguredFallbackOnQualityKeepsLocalResultWhenAccepted(t *testing.T) {
	originalLoadCandidate := loadFTSExternalCandidateForTask
	originalHasSidecar := hasCurrentExternalFTSSidecarForTask
	originalBuildDoc := buildFTSFileDocumentForTask
	originalPublish := publishFTSExternalRequestForTask
	defer func() {
		loadFTSExternalCandidateForTask = originalLoadCandidate
		hasCurrentExternalFTSSidecarForTask = originalHasSidecar
		buildFTSFileDocumentForTask = originalBuildDoc
		publishFTSExternalRequestForTask = originalPublish
	}()

	uri := mustURI(t, "cloudreve:///my/good.pdf")
	loadFTSExternalCandidateForTask = func(m *manager, ctx context.Context, fileID int) (*ftsExternalCandidate, error) {
		return &ftsExternalCandidate{
			fileModel:     &ent.File{ID: fileID, OwnerID: 7, Name: "good.pdf"},
			primaryEntity: &ent.Entity{ID: 203, Source: "tenant-a/u7/good.pdf", UpdatedAt: time.Now()},
			policy:        &ent.StoragePolicy{ID: 303, BucketName: "cloudreve", Type: inventorytypes.PolicyTypeS3},
			uri:           uri,
		}, nil
	}
	hasCurrentExternalFTSSidecarForTask = func(m *manager, ctx context.Context, candidate *ftsExternalCandidate) bool {
		return false
	}
	buildFTSFileDocumentForTask = func(m *manager, ctx context.Context, fileID int, opts FTSBuildOptions) (*searcher.SearchFileDocument, *fs.URI, error) {
		return &searcher.SearchFileDocument{
			ID:       "103",
			FileID:   fileID,
			EntityID: 203,
			Content:  "this is a healthy extraction result with enough printable text",
		}, uri, nil
	}
	publishFTSExternalRequestForTask = func(ctx context.Context, dep dependency.Dep, fileModel *ent.File, primaryEntity *ent.Entity, policy *ent.StoragePolicy, cfg *setting.FTSExternalExtractorSetting, triggerReason string, attempt int, qualityReport string) (*ent.FTSExternalJob, error) {
		t.Fatalf("expected accepted quality result to stay local, but external publish was triggered reason=%q", triggerReason)
		return nil, nil
	}

	searchIndexer := &testSearchIndexer{}
	dep := testDep{searchIndexer: searchIndexer}
	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)
	taskModel := &FullTextIndexTask{DBTask: &queue.DBTask{Task: &ent.Task{}}}
	state := &FullTextIndexTaskState{}
	state.Upsert(FullTextIndexTaskItem{FileID: 103, Uri: uri})
	if !state.ActivateNext() {
		t.Fatal("expected active task item")
	}
	backend := &testMetadataFS{}
	fm := &manager{
		l: logging.NewConsoleLogger(logging.LevelError),
		settings: testSettingProvider{externalCfg: &setting.FTSExternalExtractorSetting{
			Enabled:        true,
			Mode:           setting.FTSExternalModeFallbackOnErrorOrQuality,
			TimeoutSeconds: 60,
			Quality: setting.FTSExternalQualitySetting{
				Enabled:             true,
				MinTextLength:       10,
				MaxReplacementRatio: 0.5,
				MaxControlCharRatio: 0.5,
				MinPrintableRatio:   0.5,
			},
		}},
		dep:    dep,
		fs:     backend,
		hasher: nil,
	}

	status, handled, err := taskModel.dispatchExternalIfConfigured(ctx, fm, state, FullTextIndexTaskItem{FileID: 103, Uri: uri})
	if err != nil {
		t.Fatalf("expected accepted quality to use local result, got error: %v", err)
	}
	if !handled {
		t.Fatal("expected external dispatcher to handle accepted quality case with local indexing")
	}
	if status != enttask.StatusProcessing {
		t.Fatalf("unexpected status for accepted quality path: got %s want %s", status, enttask.StatusProcessing)
	}
	if searchIndexer.upserted != 1 {
		t.Fatalf("expected one local upsert, got %d", searchIndexer.upserted)
	}
	if state.Len() != 0 || state.Active != nil {
		t.Fatalf("expected active file to be completed locally, got state=%+v", state)
	}
	if len(backend.patches) != 1 || backend.patches[0].Key != dbfs.FullTextIndexKey {
		t.Fatalf("expected fulltext metadata patch, got %+v", backend.patches)
	}
}

func newFTSExternalTestClient(t *testing.T, ctx context.Context) *ent.Client {
	t.Helper()

	client, err := ent.Open("sqlite3", fmt.Sprintf("file:fts-external-unit-%d?mode=memory&cache=shared&_fk=1", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("failed to open sqlite client: %v", err)
	}
	if err := client.Schema.Create(ctx); err != nil {
		_ = client.Close()
		t.Fatalf("failed to create schema: %v", err)
	}

	return client
}

func createFTSExternalJob(t *testing.T, ctx context.Context, client *ent.Client, requestID, snapshotToken string) *ent.FTSExternalJob {
	t.Helper()

	job, err := client.FTSExternalJob.Create().
		SetRequestID(requestID).
		SetStatus(ftsExternalJobStatusQueued).
		SetFileID(10).
		SetOwnerID(20).
		SetEntityID(30).
		SetSnapshotToken(snapshotToken).
		SetMode(string(setting.FTSExternalModePrimary)).
		SetTriggerReason("unit_test").
		SetAttempt(1).
		SetRequestedAt(time.Now()).
		SetDeadlineAt(time.Now().Add(time.Minute)).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create external job: %v", err)
	}

	return job
}
