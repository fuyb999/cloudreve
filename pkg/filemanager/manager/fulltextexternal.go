package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/ftsexternaljob"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/kafka"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/gofrs/uuid"
)

const (
	ftsSidecarProviderTika     = "tika"
	ftsSidecarProviderExternal = "external"

	ftsExternalJobStatusQueued  = "queued"
	ftsExternalJobStatusSuccess = "success"
	ftsExternalJobStatusError   = "error"
)

type externalFTSProviderInfo struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

type externalFTSProcessMessage struct {
	Version       int                      `json:"version"`
	RequestID     string                   `json:"request_id"`
	SnapshotToken string                   `json:"snapshot_token"`
	File          externalFTSProcessFile   `json:"file"`
	Source        externalFTSProcessSource `json:"source"`
	Options       externalFTSProcessOption `json:"options,omitempty"`
}

type externalFTSProcessFile struct {
	FileID   int    `json:"file_id"`
	OwnerID  int    `json:"owner_id"`
	EntityID int    `json:"entity_id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mime_type,omitempty"`
	Ext      string `json:"ext,omitempty"`
}

type externalFTSProcessSource struct {
	Bucket string `json:"bucket"`
	Path   string `json:"path"`
}

type externalFTSProcessOption struct {
	RecursiveAttachments bool `json:"recursive_attachments,omitempty"`
}

type externalFTSResultMessage struct {
	Version       int                     `json:"version"`
	RequestID     string                  `json:"request_id"`
	SnapshotToken string                  `json:"snapshot_token"`
	Status        string                  `json:"status,omitempty"`
	Provider      externalFTSProviderInfo `json:"provider,omitempty"`
	Root          externalFTSResultRoot   `json:"root"`
	Attachments   []externalFTSAttachment `json:"attachments,omitempty"`
}

type externalFTSResultRoot struct {
	Content      string            `json:"content,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Warnings     []string          `json:"warnings,omitempty"`
	QualityScore float64           `json:"quality_score,omitempty"`
}

type externalFTSAttachment struct {
	ID       string            `json:"id,omitempty"`
	ParentID string            `json:"parent_id,omitempty"`
	Depth    int               `json:"depth,omitempty"`
	Type     string            `json:"type,omitempty"`
	Name     string            `json:"name,omitempty"`
	Path     string            `json:"path,omitempty"`
	MimeType string            `json:"mime_type,omitempty"`
	Size     int64             `json:"size,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Content  string            `json:"content,omitempty"`
}

type externalFTSErrorMessage struct {
	Version       int    `json:"version"`
	RequestID     string `json:"request_id"`
	SnapshotToken string `json:"snapshot_token"`
	Status        string `json:"status,omitempty"`
	Stage         string `json:"stage,omitempty"`
	Code          string `json:"code,omitempty"`
	Message       string `json:"message,omitempty"`
	Detail        string `json:"detail,omitempty"`
	Retryable     bool   `json:"retryable,omitempty"`
	OccurredAt    string `json:"occurred_at,omitempty"`
}

type externalFTSDiagnostics struct {
	Provider      externalFTSProviderInfo `json:"provider,omitempty"`
	SnapshotToken string                  `json:"snapshot_token,omitempty"`
	Warnings      []string                `json:"warnings,omitempty"`
	QualityScore  float64                 `json:"quality_score,omitempty"`
}

type externalFTSQualityReport struct {
	Accepted         bool     `json:"accepted"`
	Reasons          []string `json:"reasons,omitempty"`
	TextLength       int      `json:"text_length,omitempty"`
	VisibleCount     int      `json:"visible_count,omitempty"`
	ReplacementCount int      `json:"replacement_count,omitempty"`
	ControlCount     int      `json:"control_count,omitempty"`
	BoxGlyphCount    int      `json:"box_glyph_count,omitempty"`
	MaxBoxGlyphRun   int      `json:"max_box_glyph_run,omitempty"`
	PrintableCount   int      `json:"printable_count,omitempty"`
	ReplacementRatio float64  `json:"replacement_ratio,omitempty"`
	ControlRatio     float64  `json:"control_ratio,omitempty"`
	BoxGlyphRatio    float64  `json:"box_glyph_ratio,omitempty"`
	PrintableRatio   float64  `json:"printable_ratio,omitempty"`
}

type externalFTSKafkaRuntime struct {
	mu        sync.Mutex
	client    kafka.Client
	signature string
}

var ftsExternalKafkaRuntime externalFTSKafkaRuntime

func StartFTSExternalKafka(ctx context.Context, dep dependency.Dep) error {
	_, _, err := ensureFTSExternalKafkaRuntime(ctx, dep, false)
	return err
}

func ReloadFTSExternalKafka(ctx context.Context, dep dependency.Dep) error {
	_, _, err := ensureFTSExternalKafkaRuntime(ctx, dep, true)
	return err
}

func CloseFTSExternalKafka() error {
	ftsExternalKafkaRuntime.mu.Lock()
	defer ftsExternalKafkaRuntime.mu.Unlock()

	return closeFTSExternalKafkaRuntimeLocked()
}

func closeFTSExternalKafkaRuntimeLocked() error {
	if ftsExternalKafkaRuntime.client == nil {
		ftsExternalKafkaRuntime.signature = ""
		return nil
	}

	err := ftsExternalKafkaRuntime.client.Close()
	ftsExternalKafkaRuntime.client = nil
	ftsExternalKafkaRuntime.signature = ""
	return err
}

func ensureFTSExternalKafkaRuntime(ctx context.Context, dep dependency.Dep, force bool) (kafka.Client, *setting.FTSExternalExtractorSetting, error) {
	cfg := dep.SettingProvider().FTSExternalExtractor(ctx)

	ftsExternalKafkaRuntime.mu.Lock()
	defer ftsExternalKafkaRuntime.mu.Unlock()

	if cfg == nil || !cfg.Enabled {
		if err := closeFTSExternalKafkaRuntimeLocked(); err != nil {
			return nil, cfg, err
		}
		return nil, cfg, nil
	}

	kafkaCfg, err := buildFTSExternalKafkaConfig(dep, cfg)
	if err != nil {
		return nil, cfg, err
	}

	signature, err := buildFTSExternalKafkaSignature(kafkaCfg, cfg)
	if err != nil {
		return nil, cfg, err
	}

	if !force && ftsExternalKafkaRuntime.client != nil && ftsExternalKafkaRuntime.signature == signature {
		return ftsExternalKafkaRuntime.client, cfg, nil
	}

	if err := closeFTSExternalKafkaRuntimeLocked(); err != nil {
		return nil, cfg, err
	}

	client, err := kafka.New(kafkaCfg, dep.Logger())
	if err != nil {
		return nil, cfg, err
	}

	if err := registerFTSExternalConsumers(client, dep, cfg); err != nil {
		_ = client.Close()
		return nil, cfg, err
	}

	if err := client.Start(context.Background()); err != nil {
		_ = client.Close()
		return nil, cfg, err
	}

	ftsExternalKafkaRuntime.client = client
	ftsExternalKafkaRuntime.signature = signature
	return client, cfg, nil
}

func buildFTSExternalKafkaConfig(dep dependency.Dep, cfg *setting.FTSExternalExtractorSetting) (*conf.Kafka, error) {
	if cfg == nil {
		return nil, fmt.Errorf("fts external extractor config is nil")
	}

	base := *conf.KafkaConfig
	base.Producer = conf.KafkaProducer{}
	base.Consumer = conf.KafkaConsumer{}
	cloneKafkaDefaults(&base)

	if cfg.Kafka.UseGlobalKafka {
		if dep.ConfigProvider() == nil || dep.ConfigProvider().Kafka() == nil {
			return nil, fmt.Errorf("global kafka config is unavailable")
		}

		base = *dep.ConfigProvider().Kafka()
		base.Enabled = true
	} else {
		base.Enabled = true
		base.Brokers = strings.Join(cfg.Kafka.Brokers, ",")
		base.SecurityProtocol = cfg.Kafka.SecurityProtocol
		base.SASLMechanism = cfg.Kafka.SASLMechanism
		base.SASLUsername = cfg.Kafka.Username
		base.SASLPassword = cfg.Kafka.Password
		base.TLSSkipVerify = cfg.Kafka.TLSSkipVerify
		base.SASLEnabled = cfg.Kafka.Username != "" || cfg.Kafka.Password != ""
	}

	base.ClientID = strings.TrimSpace(base.ClientID) + "-fts-external"
	base.Consumer.InitialOffset = "newest"
	base.Consumer.ReturnErrors = true

	if strings.TrimSpace(base.Brokers) == "" {
		return nil, fmt.Errorf("fts external kafka brokers is empty")
	}
	if strings.TrimSpace(cfg.Kafka.ProcessTopic) == "" {
		return nil, fmt.Errorf("fts external process topic is empty")
	}
	if strings.TrimSpace(cfg.Kafka.ResultTopic) == "" {
		return nil, fmt.Errorf("fts external result topic is empty")
	}
	if strings.TrimSpace(cfg.Kafka.ErrorTopic) == "" {
		return nil, fmt.Errorf("fts external error topic is empty")
	}
	if strings.TrimSpace(cfg.Kafka.ConsumerGroup) == "" {
		return nil, fmt.Errorf("fts external consumer group is empty")
	}

	return &base, nil
}

func cloneKafkaDefaults(cfg *conf.Kafka) {
	if cfg == nil {
		return
	}

	cfg.Producer = conf.KafkaProducer{
		RequiredAcks:    conf.KafkaConfig.Producer.RequiredAcks,
		Compression:     conf.KafkaConfig.Producer.Compression,
		RetryMax:        conf.KafkaConfig.Producer.RetryMax,
		RetryBackoff:    conf.KafkaConfig.Producer.RetryBackoff,
		Idempotent:      conf.KafkaConfig.Producer.Idempotent,
		MaxMessageBytes: conf.KafkaConfig.Producer.MaxMessageBytes,
		ReturnSuccesses: conf.KafkaConfig.Producer.ReturnSuccesses,
	}
	cfg.Consumer = conf.KafkaConsumer{
		InitialOffset:     conf.KafkaConfig.Consumer.InitialOffset,
		RebalanceStrategy: conf.KafkaConfig.Consumer.RebalanceStrategy,
		SessionTimeout:    conf.KafkaConfig.Consumer.SessionTimeout,
		HeartbeatInterval: conf.KafkaConfig.Consumer.HeartbeatInterval,
		RetryBackoff:      conf.KafkaConfig.Consumer.RetryBackoff,
		MaxProcessingTime: conf.KafkaConfig.Consumer.MaxProcessingTime,
		FetchDefault:      conf.KafkaConfig.Consumer.FetchDefault,
		FetchMax:          conf.KafkaConfig.Consumer.FetchMax,
		ReturnErrors:      conf.KafkaConfig.Consumer.ReturnErrors,
	}
}

func buildFTSExternalKafkaSignature(kafkaCfg *conf.Kafka, cfg *setting.FTSExternalExtractorSetting) (string, error) {
	payload := map[string]any{
		"kafka": map[string]any{
			"brokers":           kafkaCfg.Brokers,
			"security_protocol": kafkaCfg.SecurityProtocol,
			"sasl_mechanism":    kafkaCfg.SASLMechanism,
			"sasl_username":     kafkaCfg.SASLUsername,
			"tls_skip_verify":   kafkaCfg.TLSSkipVerify,
			"client_id":         kafkaCfg.ClientID,
		},
		"topics": map[string]any{
			"process": cfg.Kafka.ProcessTopic,
			"result":  cfg.Kafka.ResultTopic,
			"error":   cfg.Kafka.ErrorTopic,
			"group":   cfg.Kafka.ConsumerGroup,
		},
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	return string(raw), nil
}

func registerFTSExternalConsumers(client kafka.Client, dep dependency.Dep, cfg *setting.FTSExternalExtractorSetting) error {
	topics := uniqueFTSTopics(cfg.Kafka.ResultTopic, cfg.Kafka.ErrorTopic)
	return client.RegisterConsumer(kafka.ConsumerRegistration{
		Name:        "fts-external",
		Group:       cfg.Kafka.ConsumerGroup,
		Topics:      topics,
		Concurrency: 1,
		Handler: func(ctx context.Context, msg *kafka.Message) error {
			switch msg.Topic {
			case cfg.Kafka.ResultTopic:
				return handleFTSExternalResultMessage(ctx, dep, msg.Value)
			case cfg.Kafka.ErrorTopic:
				return handleFTSExternalErrorMessage(ctx, dep, msg.Value)
			default:
				return nil
			}
		},
	})
}

func uniqueFTSTopics(items ...string) []string {
	seen := map[string]struct{}{}
	res := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		res = append(res, item)
	}

	return res
}

func handleFTSExternalResultMessage(ctx context.Context, dep dependency.Dep, payload []byte) error {
	var message externalFTSResultMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		dep.Logger().Warning("Failed to parse external FTS result payload: %s", err)
		return nil
	}
	if strings.TrimSpace(message.RequestID) == "" {
		dep.Logger().Warning("Ignoring external FTS result without request_id")
		return nil
	}

	return upsertFTSExternalJobPayload(ctx, dep, message.RequestID, message.SnapshotToken, string(payload), true)
}

func handleFTSExternalErrorMessage(ctx context.Context, dep dependency.Dep, payload []byte) error {
	var message externalFTSErrorMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		dep.Logger().Warning("Failed to parse external FTS error payload: %s", err)
		return nil
	}
	if strings.TrimSpace(message.RequestID) == "" {
		dep.Logger().Warning("Ignoring external FTS error without request_id")
		return nil
	}

	return upsertFTSExternalJobPayload(ctx, dep, message.RequestID, message.SnapshotToken, string(payload), false)
}

func upsertFTSExternalJobPayload(ctx context.Context, dep dependency.Dep, requestID, snapshotToken, payload string, success bool) error {
	job, err := dep.DBClient().FTSExternalJob.Query().Where(ftsexternaljob.RequestIDEQ(requestID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			dep.Logger().Warning("Ignoring external FTS payload for unknown request_id=%s", requestID)
			return nil
		}
		return err
	}

	if snapshotToken != "" && job.SnapshotToken != "" && !strings.EqualFold(job.SnapshotToken, snapshotToken) {
		dep.Logger().Warning(
			"Ignoring external FTS payload for request_id=%s due to snapshot mismatch got=%s want=%s",
			requestID,
			snapshotToken,
			job.SnapshotToken,
		)
		return nil
	}
	if job.Status == ftsExternalJobStatusSuccess || job.Status == ftsExternalJobStatusError {
		return nil
	}

	now := time.Now()
	update := dep.DBClient().FTSExternalJob.UpdateOneID(job.ID).SetCompletedAt(now)
	if success {
		update.SetStatus(ftsExternalJobStatusSuccess).
			SetResultPayload(payload).
			ClearErrorPayload()
	} else {
		update.SetStatus(ftsExternalJobStatusError).
			SetErrorPayload(payload)
	}

	return update.Exec(ctx)
}

func publishFTSExternalRequest(
	ctx context.Context,
	dep dependency.Dep,
	fileModel *ent.File,
	primaryEntity *ent.Entity,
	policy *ent.StoragePolicy,
	cfg *setting.FTSExternalExtractorSetting,
	triggerReason string,
	attempt int,
	qualityReport string,
) (*ent.FTSExternalJob, error) {
	client, _, err := ensureFTSExternalKafkaRuntime(ctx, dep, false)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("fts external kafka client is unavailable")
	}

	message, snapshotToken, err := buildFTSExternalProcessMessage(fileModel, primaryEntity, policy, cfg)
	if err != nil {
		return nil, err
	}

	job, err := dep.DBClient().FTSExternalJob.Create().
		SetRequestID(message.RequestID).
		SetStatus(ftsExternalJobStatusQueued).
		SetFileID(fileModel.ID).
		SetOwnerID(fileModel.OwnerID).
		SetEntityID(primaryEntity.ID).
		SetSnapshotToken(snapshotToken).
		SetMode(string(cfg.Mode)).
		SetTriggerReason(triggerReason).
		SetAttempt(attempt).
		SetQualityReport(qualityReport).
		SetRequestedAt(time.Now()).
		SetDeadlineAt(time.Now().Add(time.Duration(cfg.TimeoutSeconds) * time.Second)).
		Save(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := client.Producer().PublishJSON(ctx, cfg.Kafka.ProcessTopic, []byte(fmt.Sprintf("%d", fileModel.ID)), message, map[string]string{
		"content-type": "application/json",
		"x-source":     "cloudreve",
	}); err != nil {
		now := time.Now()
		updateErr := dep.DBClient().FTSExternalJob.UpdateOneID(job.ID).
			SetStatus(ftsExternalJobStatusError).
			SetErrorPayload(err.Error()).
			SetCompletedAt(now).
			Exec(ctx)
		if updateErr != nil {
			dep.Logger().Warning("Failed to persist external FTS publish failure for request_id=%s: %s", job.RequestID, updateErr)
		}
		return nil, err
	}

	return job, nil
}

func buildFTSExternalProcessMessage(
	fileModel *ent.File,
	primaryEntity *ent.Entity,
	policy *ent.StoragePolicy,
	cfg *setting.FTSExternalExtractorSetting,
) (*externalFTSProcessMessage, string, error) {
	if fileModel == nil || primaryEntity == nil {
		return nil, "", fmt.Errorf("failed to build external fts process message: file model or entity is nil")
	}
	if policy == nil {
		return nil, "", fmt.Errorf("failed to build external fts process message: storage policy is nil")
	}
	if strings.TrimSpace(policy.BucketName) == "" {
		return nil, "", fmt.Errorf("failed to build external fts process message: storage bucket is empty")
	}
	if strings.TrimSpace(primaryEntity.Source) == "" {
		return nil, "", fmt.Errorf("failed to build external fts process message: entity source path is empty")
	}

	requestID := "fts-ext-" + uuid.Must(uuid.NewV4()).String()
	snapshotToken := buildFTSExternalSnapshotToken(fileModel, primaryEntity)

	return &externalFTSProcessMessage{
		Version:       1,
		RequestID:     requestID,
		SnapshotToken: snapshotToken,
		File: externalFTSProcessFile{
			FileID:   fileModel.ID,
			OwnerID:  fileModel.OwnerID,
			EntityID: primaryEntity.ID,
			Name:     fileModel.Name,
			Size:     fileModel.Size,
			MimeType: mime.TypeByExtension(filepath.Ext(fileModel.Name)),
			Ext:      strings.TrimPrefix(strings.ToLower(filepath.Ext(fileModel.Name)), "."),
		},
		Source: externalFTSProcessSource{
			Bucket: policy.BucketName,
			Path:   primaryEntity.Source,
		},
		Options: externalFTSProcessOption{
			RecursiveAttachments: cfg.RecursiveAttachments,
		},
	}, snapshotToken, nil
}

func buildFTSExternalSnapshotToken(fileModel *ent.File, primaryEntity *ent.Entity) string {
	updatedAt := time.Time{}
	if primaryEntity != nil {
		updatedAt = primaryEntity.UpdatedAt
	}
	if updatedAt.IsZero() && fileModel != nil {
		updatedAt = fileModel.UpdatedAt
	}

	return fmt.Sprintf(
		"file:%d:entity:%d:size:%d:updated:%s",
		fileModel.ID,
		func() int {
			if primaryEntity == nil {
				return 0
			}
			return primaryEntity.ID
		}(),
		fileModel.Size,
		updatedAt.UTC().Format(time.RFC3339Nano),
	)
}

func findReusableFTSExternalJob(ctx context.Context, dep dependency.Dep, fileModel *ent.File, primaryEntity *ent.Entity) (*ent.FTSExternalJob, error) {
	if dep == nil || dep.DBClient() == nil || fileModel == nil || primaryEntity == nil {
		return nil, nil
	}

	snapshotToken := buildFTSExternalSnapshotToken(fileModel, primaryEntity)
	jobs, err := dep.DBClient().FTSExternalJob.Query().
		Where(
			ftsexternaljob.FileIDEQ(fileModel.ID),
			ftsexternaljob.EntityIDEQ(primaryEntity.ID),
			ftsexternaljob.SnapshotTokenEQ(snapshotToken),
		).
		Order(ent.Desc(ftsexternaljob.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	var queued *ent.FTSExternalJob
	for _, job := range jobs {
		if job == nil {
			continue
		}

		switch job.Status {
		case ftsExternalJobStatusSuccess:
			if strings.TrimSpace(job.ResultPayload) != "" {
				return job, nil
			}
		case ftsExternalJobStatusQueued:
			if queued == nil {
				queued = job
			}
		}
	}

	return queued, nil
}

func findPrimaryFTSEntity(fileModel *ent.File) *ent.Entity {
	if fileModel == nil {
		return nil
	}

	for _, entity := range fileModel.Edges.Entities {
		if entity != nil && entity.ID == fileModel.PrimaryEntity {
			return entity
		}
	}

	return nil
}

func externalFTSEligible(fileModel *ent.File, primaryEntity *ent.Entity, policy *ent.StoragePolicy, cfg *setting.FTSExternalExtractorSetting) bool {
	if cfg == nil || !cfg.Enabled || fileModel == nil || primaryEntity == nil || policy == nil {
		return false
	}
	if fileModel.Type == int(types.FileTypeFolder) {
		return false
	}
	if cfg.SkipEncryptedFiles && primaryEntity.Props != nil && primaryEntity.Props.EncryptMetadata != nil {
		return false
	}
	if strings.TrimSpace(policy.BucketName) == "" || strings.TrimSpace(primaryEntity.Source) == "" {
		return false
	}

	switch policy.Type {
	case types.PolicyTypeLocal, types.PolicyTypeOd, types.PolicyTypeRemote:
		return false
	default:
		return true
	}
}

func normalizeExternalFTSMode(mode setting.FTSExternalMode) setting.FTSExternalMode {
	switch mode {
	case setting.FTSExternalModePrimary, setting.FTSExternalModeFallbackOnError, setting.FTSExternalModeFallbackOnErrorOrQuality:
		return mode
	default:
		return setting.FTSExternalModeDisabled
	}
}

func marshalExternalQualityReport(report *externalFTSQualityReport) string {
	if report == nil {
		return ""
	}

	raw, err := json.Marshal(report)
	if err != nil {
		return ""
	}

	return string(raw)
}

func readFTSExternalJobByRequestID(ctx context.Context, dep dependency.Dep, requestID string) (*ent.FTSExternalJob, error) {
	return dep.DBClient().FTSExternalJob.Query().Where(ftsexternaljob.RequestIDEQ(requestID)).Only(ctx)
}

func parseFTSExternalResultPayload(raw string) (*externalFTSResultMessage, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("external fts result payload is empty")
	}

	var payload externalFTSResultMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, err
	}

	return &payload, nil
}

func parseFTSExternalErrorPayload(raw string) (*externalFTSErrorMessage, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("external fts error payload is empty")
	}

	var payload externalFTSErrorMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, err
	}

	return &payload, nil
}

func markFTSExternalJobLocalFallback(ctx context.Context, dep dependency.Dep, job *ent.FTSExternalJob, reason string) {
	if dep.DBClient() == nil || job == nil || job.ID == 0 {
		return
	}
	if job.Status == ftsExternalJobStatusSuccess || job.Status == ftsExternalJobStatusError {
		return
	}

	payload := externalFTSErrorMessage{
		Version:       1,
		RequestID:     job.RequestID,
		SnapshotToken: job.SnapshotToken,
		Status:        "error",
		Stage:         "orchestrate",
		Code:          fallbackErrorCode(reason),
		Message:       "local fallback triggered",
		Detail:        reason,
		Retryable:     false,
		OccurredAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(reason)
	}

	if err := dep.DBClient().FTSExternalJob.UpdateOneID(job.ID).
		SetStatus(ftsExternalJobStatusError).
		SetErrorPayload(string(raw)).
		SetCompletedAt(time.Now()).
		Exec(ctx); err != nil {
		dep.Logger().Warning("Failed to persist external FTS local fallback for request_id=%s: %s", job.RequestID, err)
	}
}

func fallbackErrorCode(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	switch {
	case strings.Contains(reason, "timed out"):
		return "timeout"
	case strings.Contains(reason, "reported error"):
		return "external_error"
	default:
		return "local_fallback"
	}
}

func normalizeExternalAttachments(
	fileModel *ent.File,
	primaryEntity fs.Entity,
	items []externalFTSAttachment,
) []searcher.SearchAttachmentDocument {
	if fileModel == nil || primaryEntity == nil || len(items) == 0 {
		return nil
	}

	res := make([]searcher.SearchAttachmentDocument, 0, len(items))
	seen := map[string]int{}
	idMap := map[string]string{}

	buildLogicalID := func(item externalFTSAttachment, index int) string {
		candidate := strings.TrimSpace(item.ID)
		if candidate == "" {
			candidate = strings.TrimSpace(item.Path)
		}
		if candidate == "" {
			candidate = strings.TrimSpace(item.Name)
		}
		if candidate == "" {
			candidate = fmt.Sprintf("attachment_%d", index+1)
		}
		candidate = path.Join("external", candidate)
		if count := seen[candidate]; count > 0 {
			candidate = fmt.Sprintf("%s_%d", candidate, count+1)
		}
		seen[candidate]++
		return candidate
	}

	for index, item := range items {
		logicalID := buildLogicalID(item, index)
		idMap[strings.TrimSpace(item.ID)] = logicalID
	}

	for index, item := range items {
		rawID := strings.TrimSpace(item.ID)
		logicalID := idMap[rawID]
		if logicalID == "" {
			logicalID = buildLogicalID(item, index)
		}

		parentLogical := ""
		if parentID := strings.TrimSpace(item.ParentID); parentID != "" {
			if !isExternalAttachmentRootParent(fileModel, parentID) {
				parentLogical = idMap[parentID]
				if parentLogical == "" {
					parentLogical = path.Join("external", parentID)
				}
			}
		}

		doc := searcher.SearchAttachmentDocument{
			ID:        embeddedAttachmentDocID(fileModel.ID, logicalID),
			ParentID:  embeddedAttachmentParentID(fileModel.ID, parentLogical),
			Depth:     item.Depth,
			EntityID:  primaryEntity.ID(),
			Type:      firstNonEmpty(item.Type, "external"),
			Name:      firstNonEmpty(item.Name, filepath.Base(item.Path)),
			Path:      firstNonEmpty(item.Path, item.Name, logicalID),
			Size:      item.Size,
			MimeType:  firstNonEmpty(item.MimeType, mime.TypeByExtension(filepath.Ext(item.Name))),
			Source:    firstNonEmpty(item.Path, item.Name, logicalID),
			Metadata:  cloneStringMap(item.Metadata),
			Content:   strings.TrimSpace(item.Content),
			CreatedAt: primaryEntity.CreatedAt(),
			UpdatedAt: primaryEntity.UpdatedAt(),
		}
		res = append(res, doc)
	}

	return res
}

func isExternalAttachmentRootParent(fileModel *ent.File, parentID string) bool {
	parentID = strings.TrimSpace(parentID)
	if fileModel == nil || fileModel.ID <= 0 || parentID == "" {
		return false
	}

	return parentID == fmt.Sprintf("file:%d", fileModel.ID) || parentID == strconv.Itoa(fileModel.ID)
}
