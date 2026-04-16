package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/encrypt"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/request"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	tikaextractor "github.com/cloudreve/Cloudreve/v4/pkg/searcher/extractor"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

const slaveContentProcessingKindFullTextExtract = "full_text_extract"

type SlaveContentProcessingTaskState struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

type SlaveFullTextExtractPayload struct {
	FileID         int                                  `json:"file_id"`
	OwnerID        int                                  `json:"owner_id"`
	FileName       string                               `json:"file_name"`
	FileSize       int64                                `json:"file_size"`
	Entity         *ent.Entity                          `json:"entity"`
	Policy         *ent.StoragePolicy                   `json:"policy"`
	TikaConfig     *setting.FTSTikaExtractorSetting     `json:"tika_config,omitempty"`
	ExternalConfig *setting.FTSExternalExtractorSetting `json:"external_config,omitempty"`
}

type SlaveFullTextExtractResult struct {
	EntityID          int    `json:"entity_id"`
	ManifestPath      string `json:"manifest_path,omitempty"`
	ExternalRequestID string `json:"external_request_id,omitempty"`
}

func (m *manager) buildSlaveFullTextExtractPayload(ctx context.Context, fileID int) (*SlaveFullTextExtractPayload, error) {
	fileModel, err := m.loadFTSFileModel(ctx, fileID)
	if err != nil {
		return nil, fmt.Errorf("failed to load file model: %w", err)
	}

	var primaryEntity *ent.Entity
	for _, entity := range fileModel.Edges.Entities {
		if entity.ID == fileModel.PrimaryEntity {
			primaryEntity = entity
			break
		}
	}
	if primaryEntity == nil {
		return nil, fmt.Errorf("primary entity not found")
	}

	decodedEntity, err := decryptFTSEntityKeyIfNeeded(ctx, m.dep, primaryEntity)
	if err != nil {
		return nil, err
	}

	policy, err := m.storagePolicyFromID(ctx, fs.NewEntity(decodedEntity).PolicyID())
	if err != nil {
		return nil, fmt.Errorf("failed to load entity policy: %w", err)
	}

	return &SlaveFullTextExtractPayload{
		FileID:         fileModel.ID,
		OwnerID:        fileModel.OwnerID,
		FileName:       fileModel.Name,
		FileSize:       fileModel.Size,
		Entity:         decodedEntity,
		Policy:         policy,
		TikaConfig:     cloneFTSTikaExtractorSetting(m.settings.FTSTikaExtractor(ctx)),
		ExternalConfig: cloneFTSExternalExtractorSetting(m.settings.FTSExternalExtractor(ctx)),
	}, nil
}

func ExecuteSlaveFullTextExtract(ctx context.Context, dep dependency.Dep, payload *SlaveFullTextExtractPayload) (*SlaveFullTextExtractResult, error) {
	if payload == nil || payload.Entity == nil {
		return nil, fmt.Errorf("invalid slave full text payload")
	}

	logFailure := func(step string, err error) (*SlaveFullTextExtractResult, error) {
		if err != nil && dep != nil {
			dep.Logger().Warning(
				"Slave full text extract failed at %s for file=%d entity=%d policy=%d name=%q: %v",
				step,
				payload.FileID,
				payload.Entity.ID,
				func() int {
					if payload.Policy == nil {
						return 0
					}
					return payload.Policy.ID
				}(),
				payload.FileName,
				err,
			)
		}
		return nil, err
	}

	cfg := resolveSlaveFTSTikaConfig(payload.TikaConfig, dep.SettingProvider().FTSTikaExtractor(ctx))
	tika, err := buildSlaveTikaExtractor(dep, cfg)
	if err != nil {
		return logFailure("build_tika", err)
	}

	fileModel := &ent.File{
		ID:            payload.FileID,
		OwnerID:       payload.OwnerID,
		Name:          payload.FileName,
		Size:          payload.FileSize,
		PrimaryEntity: payload.Entity.ID,
		UpdatedAt:     payload.Entity.UpdatedAt,
	}
	externalCfg := resolveSlaveFTSExternalConfig(payload.ExternalConfig, dep.SettingProvider().FTSExternalExtractor(ctx))
	externalMode := normalizeExternalFTSMode(externalCfg.Mode)
	externalEligible := externalFTSEligible(fileModel, payload.Entity, payload.Policy, externalCfg)
	if externalEligible && externalMode == setting.FTSExternalModePrimary {
		return queueSlaveExternalRequest(ctx, dep, fileModel, payload.Entity, payload.Policy, externalCfg, "primary", 1, "")
	}

	if !ShouldExtractText(tika, payload.FileName, payload.FileSize) {
		return &SlaveFullTextExtractResult{
			EntityID: payload.Entity.ID,
		}, nil
	}

	if !cfg.SidecarEnabled || (!cfg.SidecarTextEnabled && !cfg.SidecarAssetsEnabled) {
		if externalEligible && externalMode != setting.FTSExternalModeDisabled {
			res, queueErr := queueSlaveExternalRequest(ctx, dep, fileModel, payload.Entity, payload.Policy, externalCfg, "local_build_error", 1, "")
			if queueErr == nil {
				return res, nil
			}
		}
		return logFailure("validate_sidecar_setting", fmt.Errorf("slave full text extraction requires text or assets sidecar to be enabled"))
	}

	fm := NewFileManager(dep, nil)
	defer fm.Recycle()

	internal, ok := fm.(*manager)
	if !ok {
		return logFailure("construct_file_manager", fmt.Errorf("failed to construct stateless file manager"))
	}

	primaryEntity := fs.NewEntity(payload.Entity)
	policy := internal.CastStoragePolicyOnSlave(ctx, payload.Policy)
	source, err := internal.GetEntitySource(ctx, 0, fs.WithEntity(primaryEntity), fs.WithPolicy(policy))
	if err != nil {
		if externalEligible && externalMode != setting.FTSExternalModeDisabled {
			res, queueErr := queueSlaveExternalRequest(ctx, dep, fileModel, payload.Entity, payload.Policy, externalCfg, "local_build_error", 1, "")
			if queueErr == nil {
				return res, nil
			}
		}
		return logFailure("get_entity_source", fmt.Errorf("failed to get entity source: %w", err))
	}
	defer source.Close()

	extractedText := ""
	if cfg.SidecarTextEnabled {
		if !rewindSidecarSource(internal, source) {
			if externalEligible && externalMode != setting.FTSExternalModeDisabled {
				res, queueErr := queueSlaveExternalRequest(ctx, dep, fileModel, payload.Entity, payload.Policy, externalCfg, "local_build_error", 1, "")
				if queueErr == nil {
					return res, nil
				}
			}
			return logFailure("rewind_source", fmt.Errorf("failed to rewind source for slave text extraction"))
		}
		extractedText, err = tika.ExtractFile(ctx, source, fileModel.Name)
		if err != nil {
			if externalEligible && externalMode != setting.FTSExternalModeDisabled {
				res, queueErr := queueSlaveExternalRequest(ctx, dep, fileModel, payload.Entity, payload.Policy, externalCfg, "local_build_error", 1, "")
				if queueErr == nil {
					return res, nil
				}
			}
			return logFailure("extract_text", err)
		}
		extractedText = strings.TrimSpace(extractedText)
	}

	manifest, manifestPath, err := internal.persistFTSSidecarsForSlave(
		ctx,
		tika,
		cfg,
		fileModel,
		primaryEntity,
		policy,
		source,
		extractedText,
	)
	if err != nil {
		if externalEligible && externalMode != setting.FTSExternalModeDisabled {
			res, queueErr := queueSlaveExternalRequest(ctx, dep, fileModel, payload.Entity, payload.Policy, externalCfg, "local_build_error", 1, "")
			if queueErr == nil {
				return res, nil
			}
		}
		return logFailure("persist_sidecars", err)
	}

	result := &SlaveFullTextExtractResult{
		EntityID: payload.Entity.ID,
	}
	if manifest != nil && manifestPath != "" {
		result.ManifestPath = manifestPath
	}

	if externalEligible {
		doc := &searcher.SearchFileDocument{
			FileID:   payload.FileID,
			EntityID: payload.Entity.ID,
			Content:  extractedText,
		}
		triggerReason := ""
		qualityReport := ""
		hasOCRCandidates := externalCfg.OCREnabled && hasFTSOCRCandidates(fileModel, primaryEntity, payload.Policy, extractedText, manifest)
		switch externalMode {
		case setting.FTSExternalModeFallbackOnError:
			if strings.TrimSpace(buildFTSQualityText(doc)) == "" {
				triggerReason = "local_text_empty"
			}
		case setting.FTSExternalModeFallbackOnErrorOrQuality:
			report := evaluateFTSExtractionQuality(doc, externalCfg)
			if report != nil && !report.Accepted {
				triggerReason = "quality_rejected"
				qualityReport = marshalExternalQualityReport(report)
			}
		}
		if triggerReason == "" && hasOCRCandidates {
			triggerReason = "ocr_candidates_ready"
		}
		if triggerReason != "" {
			externalResult, queueErr := queueSlaveExternalRequest(ctx, dep, fileModel, payload.Entity, payload.Policy, externalCfg, triggerReason, 1, qualityReport)
			if queueErr == nil {
				return externalResult, nil
			}
			dep.Logger().Warning(
				"Slave external FTS fallback queue failed for file=%d reason=%s, keeping local sidecar: %v",
				payload.FileID,
				triggerReason,
				queueErr,
			)
		}
	}

	return result, nil
}

func cloneFTSTikaExtractorSetting(cfg *setting.FTSTikaExtractorSetting) *setting.FTSTikaExtractorSetting {
	if cfg == nil {
		return nil
	}

	clone := *cfg
	clone.Exts = append([]string(nil), cfg.Exts...)
	clone.DocumentExts = append([]string(nil), cfg.DocumentExts...)
	clone.ArchiveExts = append([]string(nil), cfg.ArchiveExts...)
	return &clone
}

func cloneFTSExternalExtractorSetting(cfg *setting.FTSExternalExtractorSetting) *setting.FTSExternalExtractorSetting {
	if cfg == nil {
		return nil
	}

	clone := *cfg
	clone.Kafka.Brokers = append([]string(nil), cfg.Kafka.Brokers...)
	return &clone
}

func resolveSlaveFTSTikaConfig(taskCfg *setting.FTSTikaExtractorSetting, fallback *setting.FTSTikaExtractorSetting) *setting.FTSTikaExtractorSetting {
	if taskCfg != nil {
		return cloneFTSTikaExtractorSetting(taskCfg)
	}
	return cloneFTSTikaExtractorSetting(fallback)
}

func resolveSlaveFTSExternalConfig(taskCfg *setting.FTSExternalExtractorSetting, fallback *setting.FTSExternalExtractorSetting) *setting.FTSExternalExtractorSetting {
	if taskCfg != nil {
		return cloneFTSExternalExtractorSetting(taskCfg)
	}
	return cloneFTSExternalExtractorSetting(fallback)
}

func buildSlaveTikaExtractor(dep dependency.Dep, cfg *setting.FTSTikaExtractorSetting) (*tikaextractor.TikaExtractor, error) {
	if cfg == nil || cfg.Endpoint == "" {
		return nil, fmt.Errorf("slave full text extraction requires tika extractor")
	}

	return tikaextractor.NewTikaExtractor(slaveRequestClient(dep), dep.SettingProvider(), dep.Logger(), cfg), nil
}

func slaveRequestClient(dep dependency.Dep) (client request.Client) {
	client = request.GeneralClient
	defer func() {
		if recover() != nil || client == nil {
			client = request.GeneralClient
		}
	}()

	if dep == nil {
		return client
	}

	if c := dep.RequestClient(); c != nil {
		client = c
	}
	return client
}

func decryptFTSEntityKeyIfNeeded(ctx context.Context, dep dependency.Dep, entity *ent.Entity) (*ent.Entity, error) {
	if entity == nil || entity.Props == nil || entity.Props.EncryptMetadata == nil || entity.Props.EncryptMetadata.KeyPlainText != nil {
		return entity, nil
	}

	masterKey, err := dep.MasterEncryptKeyVault(ctx).GetMasterKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load master key for entity decryption: %w", err)
	}

	decryptedKey, err := encrypt.DecryptWithMasterKey(masterKey, entity.Props.EncryptMetadata.Key)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt entity key: %w", err)
	}

	entityCopy := *entity
	propsCopy := *entity.Props
	metaCopy := *entity.Props.EncryptMetadata
	metaCopy.KeyPlainText = decryptedKey
	metaCopy.Key = nil
	propsCopy.EncryptMetadata = &metaCopy
	entityCopy.Props = &propsCopy
	return &entityCopy, nil
}

func marshalSlaveContentProcessingState(kind string, payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	wrapper, err := json.Marshal(&SlaveContentProcessingTaskState{
		Kind:    kind,
		Payload: raw,
	})
	if err != nil {
		return "", err
	}

	return string(wrapper), nil
}

func parseSlaveContentProcessingState(raw string) (*SlaveContentProcessingTaskState, error) {
	state := &SlaveContentProcessingTaskState{}
	if err := json.Unmarshal([]byte(raw), state); err != nil {
		return nil, err
	}
	return state, nil
}

func queueSlaveExternalRequest(
	ctx context.Context,
	dep dependency.Dep,
	fileModel *ent.File,
	primaryEntity *ent.Entity,
	policy *ent.StoragePolicy,
	cfg *setting.FTSExternalExtractorSetting,
	triggerReason string,
	attempt int,
	qualityReport string,
) (*SlaveFullTextExtractResult, error) {
	if cfg == nil || normalizeExternalFTSMode(cfg.Mode) == setting.FTSExternalModeDisabled {
		return nil, fmt.Errorf("slave external fts is disabled")
	}

	reusable, err := findReusableFTSExternalJob(ctx, dep, fileModel, primaryEntity, cfg)
	if err != nil {
		return nil, err
	}
	if reusable != nil && strings.TrimSpace(reusable.RequestID) != "" {
		switch reusable.Status {
		case ftsExternalJobStatusQueued, ftsExternalJobStatusSuccess:
			return &SlaveFullTextExtractResult{
				EntityID:          primaryEntity.ID,
				ExternalRequestID: reusable.RequestID,
			}, nil
		}
	}

	job, err := publishFTSExternalRequest(ctx, dep, fileModel, primaryEntity, policy, cfg, triggerReason, attempt, qualityReport)
	if err != nil {
		return nil, err
	}

	return &SlaveFullTextExtractResult{
		EntityID:          primaryEntity.ID,
		ExternalRequestID: job.RequestID,
	}, nil
}

func (m *manager) applySlaveFTSSidecarResult(ctx context.Context, fileID int, uri *fs.URI, result *SlaveFullTextExtractResult) error {
	if m == nil || uri == nil {
		return nil
	}

	fileModel, err := m.loadFTSFileModel(ctx, fileID)
	if err != nil {
		return fmt.Errorf("failed to load file model for slave full text finalize: %w", err)
	}

	metadata := metadataMap(fileModel.Edges.Metadata)
	oldManifestPath := metadata[dbfs.FTSSidecarManifestKey]
	oldEntityID := metadata[dbfs.FTSSidecarEntityIDKey]
	newManifestPath := ""
	newEntityID := ""
	if result != nil {
		newManifestPath = result.ManifestPath
		if result.EntityID > 0 {
			newEntityID = strconv.Itoa(result.EntityID)
		}
	}

	patches := []fs.MetadataPatch{
		{
			Key:     dbfs.FTSSidecarManifestKey,
			Remove:  true,
			Private: true,
		},
		{
			Key:     dbfs.FTSSidecarEntityIDKey,
			Remove:  true,
			Private: true,
		},
	}
	if newManifestPath != "" && newEntityID != "" {
		patches = []fs.MetadataPatch{
			{
				Key:     dbfs.FTSSidecarManifestKey,
				Value:   newManifestPath,
				Private: true,
			},
			{
				Key:     dbfs.FTSSidecarEntityIDKey,
				Value:   newEntityID,
				Private: true,
			},
		}
	}

	if oldManifestPath != "" && (oldManifestPath != newManifestPath || oldEntityID != newEntityID || newManifestPath == "") {
		var fallback fs.Entity
		for _, entity := range fileModel.Edges.Entities {
			if oldEntityID == strconv.Itoa(entity.ID) || (oldEntityID == "" && entity.ID == fileModel.PrimaryEntity) {
				fallback = fs.NewEntity(entity)
				break
			}
		}
		if err := cleanupFTSSidecarsByMetadata(ctx, m, oldManifestPath, oldEntityID, fallback); err != nil {
			m.l.Warning("Failed to cleanup stale slave Tika sidecars for file %d: %s", fileID, err)
		}
	}

	if err := m.fs.PatchMetadata(withPublicBypass(ctx, uri), []*fs.URI{uri}, patches...); err != nil {
		return fmt.Errorf("failed to patch slave full text sidecar metadata: %w", err)
	}

	return nil
}
