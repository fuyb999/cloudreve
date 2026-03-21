package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/encrypt"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	tikaextractor "github.com/cloudreve/Cloudreve/v4/pkg/searcher/extractor"
)

const slaveContentProcessingKindFullTextExtract = "full_text_extract"

type SlaveContentProcessingTaskState struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

type SlaveFullTextExtractPayload struct {
	FileID   int                `json:"file_id"`
	OwnerID  int                `json:"owner_id"`
	FileName string             `json:"file_name"`
	FileSize int64              `json:"file_size"`
	Entity   *ent.Entity        `json:"entity"`
	Policy   *ent.StoragePolicy `json:"policy"`
}

type SlaveFullTextExtractResult struct {
	EntityID     int    `json:"entity_id"`
	ManifestPath string `json:"manifest_path,omitempty"`
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
		FileID:   fileModel.ID,
		OwnerID:  fileModel.OwnerID,
		FileName: fileModel.Name,
		FileSize: fileModel.Size,
		Entity:   decodedEntity,
		Policy:   policy,
	}, nil
}

func ExecuteSlaveFullTextExtract(ctx context.Context, dep dependency.Dep, payload *SlaveFullTextExtractPayload) (*SlaveFullTextExtractResult, error) {
	if payload == nil || payload.Entity == nil {
		return nil, fmt.Errorf("invalid slave full text payload")
	}

	extractor := dep.TextExtractor(ctx)
	tika, ok := extractor.(*tikaextractor.TikaExtractor)
	if !ok {
		return nil, fmt.Errorf("slave full text extraction requires tika extractor")
	}

	if !ShouldExtractText(tika, payload.FileName, payload.FileSize) {
		return &SlaveFullTextExtractResult{
			EntityID: payload.Entity.ID,
		}, nil
	}

	cfg := dep.SettingProvider().FTSTikaExtractor(ctx)
	if !cfg.SidecarEnabled || (!cfg.SidecarTextEnabled && !cfg.SidecarAssetsEnabled) {
		return nil, fmt.Errorf("slave full text extraction requires text or assets sidecar to be enabled")
	}

	fm := NewFileManager(dep, nil)
	defer fm.Recycle()

	internal, ok := fm.(*manager)
	if !ok {
		return nil, fmt.Errorf("failed to construct stateless file manager")
	}

	primaryEntity := fs.NewEntity(payload.Entity)
	policy := internal.CastStoragePolicyOnSlave(ctx, payload.Policy)
	source, err := internal.GetEntitySource(ctx, 0, fs.WithEntity(primaryEntity), fs.WithPolicy(policy))
	if err != nil {
		return nil, fmt.Errorf("failed to get entity source: %w", err)
	}
	defer source.Close()

	fileModel := &ent.File{
		ID:      payload.FileID,
		OwnerID: payload.OwnerID,
		Name:    payload.FileName,
		Size:    payload.FileSize,
	}

	manifest, manifestPath, err := internal.persistFTSSidecarsForSlave(
		ctx,
		extractor,
		fileModel,
		primaryEntity,
		policy,
		source,
	)
	if err != nil {
		return nil, err
	}

	result := &SlaveFullTextExtractResult{
		EntityID: payload.Entity.ID,
	}
	if manifest != nil && manifestPath != "" {
		result.ManifestPath = manifestPath
	}

	return result, nil
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

	if err := m.fs.PatchMetadata(ctx, []*fs.URI{uri}, patches...); err != nil {
		return fmt.Errorf("failed to patch slave full text sidecar metadata: %w", err)
	}

	return nil
}
