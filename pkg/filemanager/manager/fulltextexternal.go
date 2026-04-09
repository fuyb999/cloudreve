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
	"github.com/cloudreve/Cloudreve/v4/pkg/kafka"
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

type externalFTSObjectReference struct {
	PolicyID int    `json:"policy_id,omitempty"`
	Bucket   string `json:"bucket,omitempty"`
	Path     string `json:"path,omitempty"`
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
	PolicyID int    `json:"policy_id,omitempty"`
	Bucket   string `json:"bucket"`
	Path     string `json:"path"`
}

type externalFTSProcessOption struct {
	RecursiveAttachments bool `json:"recursive_attachments,omitempty"`
	OCREnabled           bool `json:"ocr_enabled,omitempty"`
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
	Content      string                      `json:"content,omitempty"`
	ContentRef   *externalFTSObjectReference `json:"content_ref,omitempty"`
	Metadata     map[string]string           `json:"metadata,omitempty"`
	Warnings     []string                    `json:"warnings,omitempty"`
	QualityScore float64                     `json:"quality_score,omitempty"`
}

type externalFTSAttachment struct {
	ID         string                      `json:"id,omitempty"`
	ParentID   string                      `json:"parent_id,omitempty"`
	Depth      int                         `json:"depth,omitempty"`
	Type       string                      `json:"type,omitempty"`
	Name       string                      `json:"name,omitempty"`
	Path       string                      `json:"path,omitempty"`
	MimeType   string                      `json:"mime_type,omitempty"`
	Size       int64                       `json:"size,omitempty"`
	Metadata   map[string]string           `json:"metadata,omitempty"`
	Content    string                      `json:"content,omitempty"`
	ContentRef *externalFTSObjectReference `json:"content_ref,omitempty"`
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

func (r *externalFTSResultRoot) UnmarshalJSON(data []byte) error {
	type alias externalFTSResultRoot
	aux := struct {
		alias
		ContentPolicyID int    `json:"content_policy_id,omitempty"`
		ContentBucket   string `json:"content_bucket,omitempty"`
		ContentPath     string `json:"content_path,omitempty"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	*r = externalFTSResultRoot(aux.alias)
	r.ContentRef = normalizeExternalFTSObjectReference(r.ContentRef, aux.ContentPolicyID, aux.ContentBucket, aux.ContentPath)
	return nil
}

func (a *externalFTSAttachment) UnmarshalJSON(data []byte) error {
	type alias externalFTSAttachment
	aux := struct {
		alias
		ContentPolicyID int    `json:"content_policy_id,omitempty"`
		ContentBucket   string `json:"content_bucket,omitempty"`
		ContentPath     string `json:"content_path,omitempty"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	*a = externalFTSAttachment(aux.alias)
	a.ContentRef = normalizeExternalFTSObjectReference(a.ContentRef, aux.ContentPolicyID, aux.ContentBucket, aux.ContentPath)
	return nil
}

type externalFTSKafkaRuntime struct {
	mu        sync.Mutex
	client    kafka.Client
	signature string
}

var ftsExternalKafkaRuntime externalFTSKafkaRuntime
var newFTSExternalKafkaClient = kafka.New

func StartFTSExternalKafka(ctx context.Context, dep dependency.Dep) error {
	_, _, err := ensureFTSExternalKafkaRuntimeWithConfig(ctx, dep, nil, false)
	return err
}

func ReloadFTSExternalKafka(ctx context.Context, dep dependency.Dep) error {
	_, _, err := ensureFTSExternalKafkaRuntimeWithConfig(ctx, dep, nil, true)
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
	return ensureFTSExternalKafkaRuntimeWithConfig(ctx, dep, nil, force)
}

func ensureFTSExternalKafkaRuntimeWithConfig(
	ctx context.Context,
	dep dependency.Dep,
	override *setting.FTSExternalExtractorSetting,
	force bool,
) (kafka.Client, *setting.FTSExternalExtractorSetting, error) {
	cfg := cloneFTSExternalExtractorSetting(override)
	if cfg == nil && dep != nil && dep.SettingProvider() != nil {
		cfg = cloneFTSExternalExtractorSetting(dep.SettingProvider().FTSExternalExtractor(ctx))
	}

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

	client, err := newFTSExternalKafkaClient(kafkaCfg, dep.Logger())
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
	// External result consumers may start slightly after the process request is published.
	// Use oldest so a fast third-party response is still visible to a brand-new consumer group.
	base.Consumer.InitialOffset = "oldest"
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
	client, _, err := ensureFTSExternalKafkaRuntimeWithConfig(ctx, dep, cfg, false)
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
	snapshotToken := buildFTSExternalSnapshotToken(fileModel, primaryEntity, cfg)

	options := externalFTSProcessOption{}
	if cfg != nil {
		options.RecursiveAttachments = cfg.RecursiveAttachments
		options.OCREnabled = cfg.OCREnabled
	}

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
			PolicyID: policy.ID,
			Bucket:   policy.BucketName,
			Path:     primaryEntity.Source,
		},
		Options: options,
	}, snapshotToken, nil
}

func buildFTSExternalSnapshotToken(fileModel *ent.File, primaryEntity *ent.Entity, cfg *setting.FTSExternalExtractorSetting) string {
	updatedAt := time.Time{}
	if primaryEntity != nil {
		updatedAt = primaryEntity.UpdatedAt
	}
	if updatedAt.IsZero() && fileModel != nil {
		updatedAt = fileModel.UpdatedAt
	}

	recursiveAttachments := false
	ocrEnabled := false
	if cfg != nil {
		recursiveAttachments = cfg.RecursiveAttachments
		ocrEnabled = cfg.OCREnabled
	}

	return fmt.Sprintf(
		"file:%d:entity:%d:size:%d:updated:%s:recursive:%t:ocr:%t",
		fileModel.ID,
		func() int {
			if primaryEntity == nil {
				return 0
			}
			return primaryEntity.ID
		}(),
		fileModel.Size,
		updatedAt.UTC().Format(time.RFC3339Nano),
		recursiveAttachments,
		ocrEnabled,
	)
}

func findReusableFTSExternalJob(ctx context.Context, dep dependency.Dep, fileModel *ent.File, primaryEntity *ent.Entity, cfg *setting.FTSExternalExtractorSetting) (*ent.FTSExternalJob, error) {
	if dep == nil || dep.DBClient() == nil || fileModel == nil || primaryEntity == nil {
		return nil, nil
	}

	snapshotToken := buildFTSExternalSnapshotToken(fileModel, primaryEntity, cfg)
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
	if fileModel.Size <= 0 {
		return false
	}
	if cfg.MaxFileSize > 0 && fileModel.Size > cfg.MaxFileSize {
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

func normalizeExternalFTSObjectReference(ref *externalFTSObjectReference, policyID int, bucket, path string) *externalFTSObjectReference {
	if ref != nil {
		if ref.PolicyID > 0 {
			policyID = ref.PolicyID
		}
		bucket = firstNonEmpty(ref.Bucket, bucket)
		path = firstNonEmpty(ref.Path, path)
	}

	if policyID < 0 {
		policyID = 0
	}
	bucket = strings.TrimSpace(bucket)
	path = strings.TrimSpace(path)
	if policyID == 0 && bucket == "" && path == "" {
		return nil
	}

	return &externalFTSObjectReference{
		PolicyID: policyID,
		Bucket:   bucket,
		Path:     path,
	}
}

func (r externalFTSResultRoot) contentReference() *externalFTSObjectReference {
	return normalizeExternalFTSObjectReference(r.ContentRef, 0, "", "")
}

func (a externalFTSAttachment) contentReference() *externalFTSObjectReference {
	return normalizeExternalFTSObjectReference(a.ContentRef, 0, "", "")
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

type externalFTSSidecarAttachment struct {
	LogicalID   string
	HasChildren bool
	Artifact    FTSSidecarArtifact
}

func normalizeExternalAttachmentArtifacts(fileID int, items []externalFTSAttachment) []externalFTSSidecarAttachment {
	if fileID <= 0 || len(items) == 0 {
		return nil
	}

	res := make([]externalFTSSidecarAttachment, 0, len(items))
	seen := map[string]struct{}{}
	idMap := map[string]string{}
	childCounts := map[string]int{}

	for _, item := range items {
		parentID := strings.TrimSpace(item.ParentID)
		if parentID == "" || isExternalAttachmentRootParentID(fileID, parentID) {
			continue
		}
		childCounts[parentID]++
	}

	buildLogicalID := func(item externalFTSAttachment, index int) string {
		candidate := strings.TrimSpace(item.Path)
		if normalized, ok := normalizeFTSSidecarRelativePath(candidate); ok {
			candidate = normalized
		} else {
			candidate = ""
		}
		if candidate == "" {
			candidate = strings.TrimSpace(item.Name)
			if normalized, ok := normalizeFTSSidecarRelativePath(candidate); ok {
				candidate = normalized
			} else {
				candidate = ""
			}
		}
		if candidate == "" {
			candidate = strings.TrimSpace(item.ID)
			if normalized, ok := normalizeFTSSidecarRelativePath(candidate); ok {
				candidate = normalized
			} else {
				candidate = ""
			}
		}
		if candidate == "" {
			candidate = fmt.Sprintf("attachment_%d", index+1)
		}

		candidate = path.Join(ftsSidecarEmbeddedDir, candidate)
		ext := path.Ext(candidate)
		base := strings.TrimSuffix(candidate, ext)
		unique := candidate
		for suffix := 2; ; suffix++ {
			if _, ok := seen[unique]; !ok {
				seen[unique] = struct{}{}
				return unique
			}
			unique = fmt.Sprintf("%s_%d%s", base, suffix, ext)
		}
	}

	for index, item := range items {
		logicalID := buildLogicalID(item, index)
		if rawID := strings.TrimSpace(item.ID); rawID != "" {
			idMap[rawID] = logicalID
		}
		res = append(res, externalFTSSidecarAttachment{
			LogicalID:   logicalID,
			HasChildren: childCounts[strings.TrimSpace(item.ID)] > 0,
		})
	}

	for index, item := range items {
		parentLogical := ""
		if parentID := strings.TrimSpace(item.ParentID); parentID != "" && !isExternalAttachmentRootParentID(fileID, parentID) {
			parentLogical = idMap[parentID]
		}

		name := firstNonEmpty(item.Name, filepath.Base(item.Path), filepath.Base(res[index].LogicalID))
		if name == "" || name == "." || name == "/" {
			name = fmt.Sprintf("attachment_%d", index+1)
		}

		kind := strings.TrimSpace(item.Type)
		metadata := cloneStringMap(item.Metadata)
		if res[index].HasChildren {
			if kind != "" && kind != "archive" {
				if metadata == nil {
					metadata = map[string]string{}
				}
				metadata["external_type"] = kind
			}
			kind = "archive"
		} else if kind == "" {
			kind = "embedded"
		}

		res[index].Artifact = FTSSidecarArtifact{
			ID:       res[index].LogicalID,
			ParentID: parentLogical,
			Depth:    max(item.Depth, 0),
			Kind:     kind,
			Name:     name,
			MimeType: firstNonEmpty(item.MimeType, mime.TypeByExtension(filepath.Ext(name)), "application/octet-stream"),
			Size:     item.Size,
			Metadata: metadata,
		}
	}

	return res
}

func isExternalAttachmentRootParent(fileModel *ent.File, parentID string) bool {
	if fileModel == nil {
		return false
	}

	return isExternalAttachmentRootParentID(fileModel.ID, parentID)
}

func isExternalAttachmentRootParentID(fileID int, parentID string) bool {
	parentID = strings.TrimSpace(parentID)
	if fileID <= 0 || parentID == "" {
		return false
	}

	return parentID == fmt.Sprintf("file:%d", fileID) || parentID == strconv.Itoa(fileID)
}
