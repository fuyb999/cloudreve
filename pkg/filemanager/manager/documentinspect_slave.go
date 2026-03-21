package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"path/filepath"
	"strings"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	tikaextractor "github.com/cloudreve/Cloudreve/v4/pkg/searcher/extractor"
)

const slaveContentProcessingKindDocumentInspect = "document_inspect"

type SlaveDocumentInspectPayload struct {
	FileName string             `json:"file_name"`
	FileSize int64              `json:"file_size"`
	Entity   *ent.Entity        `json:"entity"`
	Policy   *ent.StoragePolicy `json:"policy"`
}

func (m *manager) buildSlaveDocumentInspectPayload(ctx context.Context, uri *fs.URI, fileID, entityID int) (*SlaveDocumentInspectPayload, error) {
	file, targetVersion, shouldProcess, err := m.resolveDocumentInspectTarget(ctx, uri, fileID, entityID)
	if err != nil {
		return nil, err
	}
	if !shouldProcess {
		return nil, nil
	}

	entityModel := targetVersion.Model()
	if entityModel == nil {
		return nil, fmt.Errorf("failed to resolve document inspection entity model")
	}

	decodedEntity, err := decryptFTSEntityKeyIfNeeded(ctx, m.dep, entityModel)
	if err != nil {
		return nil, err
	}

	policy, err := m.storagePolicyFromID(ctx, targetVersion.PolicyID())
	if err != nil {
		return nil, fmt.Errorf("failed to resolve document inspection storage policy: %w", err)
	}

	return &SlaveDocumentInspectPayload{
		FileName: file.Name(),
		FileSize: file.Size(),
		Entity:   decodedEntity,
		Policy:   policy,
	}, nil
}

func ExecuteSlaveDocumentInspect(ctx context.Context, dep dependency.Dep, payload *SlaveDocumentInspectPayload) (*DocumentInspection, error) {
	if payload == nil || payload.Entity == nil {
		return nil, fmt.Errorf("invalid slave document inspect payload")
	}

	extractor := dep.TextExtractor(ctx)
	tika, ok := extractor.(*tikaextractor.TikaExtractor)
	if !ok {
		return nil, fmt.Errorf("slave document inspection requires tika extractor")
	}
	if !ShouldExtractText(tika, payload.FileName, payload.FileSize) {
		return &DocumentInspection{EntityID: payload.Entity.ID}, nil
	}

	fm := NewFileManager(dep, nil)
	defer fm.Recycle()

	internal, ok := fm.(*manager)
	if !ok {
		return nil, fmt.Errorf("failed to construct stateless file manager")
	}

	entity := fs.NewEntity(payload.Entity)
	policy := internal.CastStoragePolicyOnSlave(ctx, payload.Policy)
	result, err := internal.inspectDocumentEntity(ctx, payload.FileName, entity, policy)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return &DocumentInspection{EntityID: payload.Entity.ID}, nil
	}

	return result, nil
}

func (m *manager) resolveDocumentInspectTarget(ctx context.Context, uri *fs.URI, fileID, entityID int) (*dbfs.File, fs.Entity, bool, error) {
	if uri == nil {
		return nil, nil, false, fmt.Errorf("failed to resolve document inspect uri: %w", queue.CriticalErr)
	}

	if fileID <= 0 {
		if m.fs == nil {
			return nil, nil, false, fmt.Errorf("missing file id for document inspect task (%w)", queue.CriticalErr)
		}

		file, err := m.fs.Get(ctx, uri)
		if err != nil {
			return nil, nil, false, fmt.Errorf("failed to get file: %w", err)
		}
		fileID = file.ID()
	}

	fileModel, err := m.loadFTSFileModel(ctx, fileID)
	if err != nil {
		return nil, nil, false, fmt.Errorf("failed to load file model: %w", err)
	}

	var targetVersion *ent.Entity
	for _, entity := range fileModel.Edges.Entities {
		if entity.Type == int(types.EntityTypeVersion) && entity.ID == entityID {
			targetVersion = entity
			break
		}
	}
	if targetVersion == nil {
		return nil, nil, false, fmt.Errorf("failed to find version entity %d (%w)", entityID, queue.CriticalErr)
	}

	file := &dbfs.File{
		Model:      fileModel,
		OwnerModel: fileModel.Edges.Owner,
	}
	if fileModel.PrimaryEntity != entityID {
		m.l.Debug("Skip document inspect task for non-latest version.")
		return file, nil, false, nil
	}
	if !m.shouldInspectDocument(ctx, file.Name(), file.Size()) {
		return file, nil, false, nil
	}

	return file, fs.NewEntity(targetVersion), true, nil
}

func parseTikaDocumentInspection(raw []byte) *DocumentInspection {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	var payload []map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || len(payload) == 0 {
		return nil
	}

	item := payload[0]
	res := &DocumentInspection{
		MimeType: firstNonEmpty(
			stringValue(item["Content-Type"]),
			stringValue(item["dc:format"]),
		),
		Parser: firstNonEmpty(
			stringValue(item["X-TIKA:Parsed-By"]),
			stringValue(item["X-TIKA:Parsed-By-Full-Set"]),
		),
		Language: firstNonEmpty(
			stringValue(item["dc:language"]),
			stringValue(item["language"]),
		),
		Title: firstNonEmpty(
			stringValue(item["dc:title"]),
			stringValue(item["title"]),
		),
		Author: firstNonEmpty(
			stringValue(item["meta:author"]),
			stringValue(item["Author"]),
			stringValue(item["dc:creator"]),
			stringValue(item["creator"]),
		),
		Metadata: map[string]string{},
	}

	if res.MimeType == "" {
		res.MimeType = mimeByInspectionFallback(item)
	}

	for key, value := range item {
		if value == nil || strings.EqualFold(key, "X-TIKA:content") || strings.EqualFold(key, "content") {
			continue
		}
		if text := strings.TrimSpace(stringValue(value)); text != "" {
			res.Metadata[key] = text
		}
	}
	if len(res.Metadata) == 0 {
		res.Metadata = nil
	}

	if res.MimeType == "" && res.Parser == "" && res.Language == "" && res.Title == "" && res.Author == "" && len(res.Metadata) == 0 {
		return nil
	}

	return res
}

func mimeByInspectionFallback(item map[string]any) string {
	name := firstNonEmpty(
		stringValue(item["resourceName"]),
		stringValue(item["resource_name"]),
	)
	if name == "" {
		name = firstNonEmpty(
			stringValue(item["X-TIKA:embedded_resource_path"]),
			stringValue(item["embedded_resource_path"]),
		)
	}
	if name == "" {
		return ""
	}

	return strings.TrimSpace(mime.TypeByExtension(filepath.Ext(name)))
}
