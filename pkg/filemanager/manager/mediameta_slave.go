package manager

import (
	"context"
	"fmt"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/driver"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/mediameta"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/samber/lo"
)

const slaveContentProcessingKindMediaMetaExtract = "media_meta_extract"

type SlaveMediaMetaExtractPayload struct {
	FileName string             `json:"file_name"`
	FileExt  string             `json:"file_ext"`
	Language string             `json:"language,omitempty"`
	Entity   *ent.Entity        `json:"entity"`
	Policy   *ent.StoragePolicy `json:"policy"`
}

type SlaveMediaMetaExtractResult struct {
	EntityID int                `json:"entity_id"`
	Metas    []driver.MediaMeta `json:"metas,omitempty"`
}

func (m *manager) buildSlaveMediaMetaExtractPayload(ctx context.Context, uri *fs.URI, fileID, entityID int) (*SlaveMediaMetaExtractPayload, error) {
	file, targetVersion, language, shouldProcess, err := m.resolveMediaMetaTarget(ctx, uri, fileID, entityID)
	if err != nil {
		return nil, err
	}
	if !shouldProcess {
		return nil, nil
	}

	entityModel := targetVersion.Model()
	if entityModel == nil {
		return nil, fmt.Errorf("failed to resolve version entity model")
	}

	decodedEntity, err := decryptFTSEntityKeyIfNeeded(ctx, m.dep, entityModel)
	if err != nil {
		return nil, err
	}

	policy, err := m.storagePolicyFromID(ctx, targetVersion.PolicyID())
	if err != nil {
		return nil, fmt.Errorf("failed to resolve media meta storage policy: %w", err)
	}

	return &SlaveMediaMetaExtractPayload{
		FileName: file.Name(),
		FileExt:  file.Ext(),
		Language: language,
		Entity:   decodedEntity,
		Policy:   policy,
	}, nil
}

func ExecuteSlaveMediaMetaExtract(ctx context.Context, dep dependency.Dep, payload *SlaveMediaMetaExtractPayload) (*SlaveMediaMetaExtractResult, error) {
	if payload == nil || payload.Entity == nil {
		return nil, fmt.Errorf("invalid slave media meta payload")
	}

	fm := NewFileManager(dep, nil)
	defer fm.Recycle()

	internal, ok := fm.(*manager)
	if !ok {
		return nil, fmt.Errorf("failed to construct stateless file manager")
	}

	entity := fs.NewEntity(payload.Entity)
	policy := internal.CastStoragePolicyOnSlave(ctx, payload.Policy)
	metas, err := internal.extractMediaMetaForEntity(ctx, entity, payload.FileName, payload.FileExt, payload.Language, policy)
	if err != nil {
		return nil, err
	}

	return &SlaveMediaMetaExtractResult{
		EntityID: payload.Entity.ID,
		Metas:    metas,
	}, nil
}

func (m *manager) applySlaveMediaMetaResult(ctx context.Context, uri *fs.URI, fileID, ownerID, entityID int, result *SlaveMediaMetaExtractResult) error {
	if result == nil {
		return nil
	}

	if (fileID <= 0 || ownerID <= 0) && uri != nil {
		file, _, _, _, err := m.resolveMediaMetaTarget(ctx, uri, fileID, entityID)
		if err != nil {
			return err
		}
		if fileID <= 0 {
			fileID = file.ID()
		}
		if ownerID <= 0 {
			ownerID = file.OwnerID()
		}
		if entityID <= 0 {
			entityID = file.PrimaryEntityID()
		}
	}

	return m.saveMediaMeta(ctx, uri, fileID, ownerID, entityID, result.Metas)
}

func (m *manager) resolveMediaMetaTarget(ctx context.Context, uri *fs.URI, fileID, entityID int) (*dbfs.File, fs.Entity, string, bool, error) {
	if uri == nil {
		return nil, nil, "", false, fmt.Errorf("failed to resolve media meta uri: %w", queue.CriticalErr)
	}

	if fileID <= 0 {
		if m.fs == nil {
			return nil, nil, "", false, fmt.Errorf("missing file id for media meta task (%w)", queue.CriticalErr)
		}

		file, err := m.fs.Get(ctx, uri)
		if err != nil {
			return nil, nil, "", false, fmt.Errorf("failed to get file: %w", err)
		}
		fileID = file.ID()
	}

	fileModel, err := m.loadFTSFileModel(ctx, fileID)
	if err != nil {
		return nil, nil, "", false, fmt.Errorf("failed to load file model: %w", err)
	}

	var targetVersion *ent.Entity
	for _, entity := range fileModel.Edges.Entities {
		if entity.Type == int(types.EntityTypeVersion) && entity.ID == entityID {
			targetVersion = entity
			break
		}
	}
	if targetVersion == nil {
		return nil, nil, "", false, fmt.Errorf("failed to find version entity %d (%w)", entityID, queue.CriticalErr)
	}

	file := &dbfs.File{
		Model:      fileModel,
		OwnerModel: fileModel.Edges.Owner,
	}

	if fileModel.PrimaryEntity != entityID {
		m.l.Debug("Skip media meta task for non-latest version.")
		return file, nil, "", false, nil
	}

	language := ""
	if owner := file.Owner(); owner != nil && owner.Settings != nil {
		language = owner.Settings.Language
	}

	return file, fs.NewEntity(targetVersion), language, true, nil
}

func (m *manager) extractMediaMetaForEntity(ctx context.Context, targetVersion fs.Entity, fileName, fileExt, language string, policyOverride *ent.StoragePolicy) ([]driver.MediaMeta, error) {
	_, d, err := m.getEntityPolicyDriver(ctx, targetVersion, policyOverride)
	if err != nil {
		return nil, fmt.Errorf("failed to get storage driver: %s (%w)", err, queue.CriticalErr)
	}

	driverCaps := d.Capabilities()
	if util.IsInExtensionList(driverCaps.MediaMetaSupportedExts, fileName) {
		m.l.Debug("Using native driver to generate media meta.")
		metas, err := d.MediaMeta(ctx, targetVersion.Source(), fileExt, language)
		if err != nil {
			return nil, fmt.Errorf("failed to get media meta using native driver: %w", err)
		}
		return metas, nil
	}

	if driverCaps.MediaMetaProxy && util.IsInExtensionList(m.dep.MediaMetaExtractor(ctx).Exts(), fileName) {
		m.l.Debug("Using local extractor to generate media meta.")
		source, err := m.GetEntitySource(ctx, 0, fs.WithEntity(targetVersion), fs.WithPolicy(policyOverride))
		if err != nil {
			return nil, fmt.Errorf("failed to get entity source: %w", err)
		}
		defer source.Close()

		metas, err := m.dep.MediaMetaExtractor(ctx).Extract(ctx, fileExt, source, mediameta.WithLanguage(language))
		if err != nil {
			return nil, fmt.Errorf("failed to extract media meta using local extractor: %w", err)
		}
		return metas, nil
	}

	m.l.Debug("No available generator for media meta.")
	return nil, nil
}

func (m *manager) saveMediaMeta(ctx context.Context, uri *fs.URI, fileID, ownerID, entityID int, metas []driver.MediaMeta) error {
	if len(metas) == 0 || uri == nil {
		return nil
	}

	if err := m.fs.PatchMetadata(ctx, []*fs.URI{uri}, lo.Map(metas, func(i driver.MediaMeta, index int) fs.MetadataPatch {
		return fs.MetadataPatch{
			Key:   fmt.Sprintf("%s:%s", i.Type, i.Key),
			Value: i.Value,
		}
	})...); err != nil {
		return fmt.Errorf("failed to save media meta: %s (%w)", err, queue.CriticalErr)
	}

	m.queueFullTextSync(ctx, uri, fileID, ownerID, entityID)
	return nil
}
