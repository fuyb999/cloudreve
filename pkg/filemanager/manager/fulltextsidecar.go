package manager

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/storagepolicy"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/driver"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/request"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	tikaextractor "github.com/cloudreve/Cloudreve/v4/pkg/searcher/extractor"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/gofrs/uuid"
)

const (
	ftsSidecarVersion  = 1
	ftsSidecarRootDir  = "cloudreve/fts-sidecar"
	ftsSidecarSelfName = "__self__"
	ftsSidecarMaxDepth = 8
)

var ftsSidecarBaseFiles = []string{
	"content.txt",
	"rmeta.json",
	"manifest.json",
}

const (
	ftsSidecarEmbeddedDir = "attachments"
	ftsSidecarDocxDir     = "docx-media"
	ftsSidecarTextDir     = "attachment-text"
)

type FTSSidecarManifest struct {
	Version       int                  `json:"version"`
	Provider      string               `json:"provider,omitempty"`
	SnapshotToken string               `json:"snapshot_token,omitempty"`
	FileID        int                  `json:"file_id"`
	EntityID      int                  `json:"entity_id"`
	SourcePath    string               `json:"source_path"`
	ExtractedAt   time.Time            `json:"extracted_at"`
	TextReady     bool                 `json:"text_ready,omitempty"`
	AssetsReady   bool                 `json:"assets_ready,omitempty"`
	Objects       []FTSSidecarArtifact `json:"objects,omitempty"`
}

type FTSSidecarArtifact struct {
	ID       string            `json:"id"`
	ParentID string            `json:"parent_id,omitempty"`
	Depth    int               `json:"depth,omitempty"`
	Kind     string            `json:"kind,omitempty"`
	Name     string            `json:"name"`
	Path     string            `json:"path"`
	MimeType string            `json:"mime_type"`
	Size     int64             `json:"size"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type FTSSidecarContent struct {
	Artifact    FTSSidecarArtifact
	Content     io.ReadCloser
	RedirectURL string
	Expires     *time.Time
}

type FTSSidecarOCRCandidate struct {
	Scope            string `json:"scope"`
	FileID           int    `json:"file_id"`
	EntityID         int    `json:"entity_id"`
	DocumentID       string `json:"document_id,omitempty"`
	AttachmentID     string `json:"attachment_id,omitempty"`
	ArtifactID       string `json:"artifact_id,omitempty"`
	PolicyID         int    `json:"policy_id,omitempty"`
	Bucket           string `json:"bucket,omitempty"`
	Path             string `json:"path,omitempty"`
	Name             string `json:"name,omitempty"`
	MimeType         string `json:"mime_type,omitempty"`
	Size             int64  `json:"size,omitempty"`
	Reason           string `json:"reason,omitempty"`
	TargetContentKey string `json:"target_content_key,omitempty"`
}

type ftsSidecarEntity struct {
	source   string
	size     int64
	policyID int
}

func (e *ftsSidecarEntity) ID() int {
	return 0
}

func (e *ftsSidecarEntity) Type() types.EntityType {
	return types.EntityTypeVersion
}

func (e *ftsSidecarEntity) Size() int64 {
	return e.size
}

func (e *ftsSidecarEntity) UpdatedAt() time.Time {
	return time.Now()
}

func (e *ftsSidecarEntity) CreatedAt() time.Time {
	return time.Now()
}

func (e *ftsSidecarEntity) Source() string {
	return e.source
}

func (e *ftsSidecarEntity) ReferenceCount() int {
	return 1
}

func (e *ftsSidecarEntity) PolicyID() int {
	return e.policyID
}

func (e *ftsSidecarEntity) UploadSessionID() *uuid.UUID {
	return nil
}

func (e *ftsSidecarEntity) CreatedBy() *ent.User {
	return nil
}

func (e *ftsSidecarEntity) Model() *ent.Entity {
	return nil
}

func (e *ftsSidecarEntity) Props() *types.EntityProps {
	return nil
}

func (e *ftsSidecarEntity) Encrypted() bool {
	return false
}

func sidecarAttachmentTextObjectID(logicalID string) string {
	logicalID, ok := normalizeFTSSidecarRelativePath(logicalID)
	if !ok {
		return ""
	}

	return path.Join(ftsSidecarTextDir, logicalID) + ".txt"
}

func sidecarAttachmentLogicalIDFromDocID(docID string) string {
	const prefix = ":embedded:"
	index := strings.Index(docID, prefix)
	if index >= 0 {
		return docID[index+len(prefix):]
	}

	return docID
}

func sidecarAttachmentTextObjectIDFromDocID(docID string) string {
	return sidecarAttachmentTextObjectID(sidecarAttachmentLogicalIDFromDocID(docID))
}

func isFTSSidecarAttachmentTextPath(source string) bool {
	source = strings.TrimSpace(source)
	if source == "" {
		return false
	}

	return strings.Contains(source, "/"+ftsSidecarTextDir+"/")
}

func buildFTSSidecarOCRCandidates(
	fileModel *ent.File,
	primaryEntity fs.Entity,
	policy *ent.StoragePolicy,
	rootText string,
	manifest *FTSSidecarManifest,
) []FTSSidecarOCRCandidate {
	if fileModel == nil || primaryEntity == nil || policy == nil {
		return nil
	}

	candidates := make([]FTSSidecarOCRCandidate, 0)
	if shouldCreateFTSOCRCandidate(fileModel.Name, mime.TypeByExtension(filepath.Ext(fileModel.Name)), fileModel.Size, rootText) {
		candidates = append(candidates, FTSSidecarOCRCandidate{
			Scope:            "file",
			FileID:           fileModel.ID,
			EntityID:         primaryEntity.ID(),
			DocumentID:       strconv.Itoa(fileModel.ID),
			PolicyID:         policy.ID,
			Bucket:           policy.BucketName,
			Path:             primaryEntity.Source(),
			Name:             fileModel.Name,
			MimeType:         mime.TypeByExtension(filepath.Ext(fileModel.Name)),
			Size:             fileModel.Size,
			Reason:           "image_file_without_text",
			TargetContentKey: "content",
		})
	}

	if manifest == nil {
		return candidates
	}

	textArtifacts := map[string]struct{}{}
	for _, item := range manifest.Objects {
		if item.Kind != "attachment_text" {
			continue
		}
		textArtifacts[item.ID] = struct{}{}
	}

	for _, object := range manifest.Objects {
		objectName := strings.TrimSpace(firstNonEmpty(object.ID, object.Name))
		if objectName == "" {
			continue
		}
		if objectName == "content.txt" || objectName == "rmeta.json" || objectName == "manifest.json" {
			continue
		}
		if object.Kind == "attachment_text" || object.Kind == "archive" || isLegacyFTSSidecarAuxiliaryKind(object.Kind) {
			continue
		}
		if strings.TrimSpace(object.Path) == "" {
			continue
		}
		if _, ok := textArtifacts[sidecarAttachmentTextObjectID(objectName)]; ok {
			continue
		}
		if !shouldCreateFTSOCRCandidate(object.Name, object.MimeType, object.Size, "") {
			continue
		}

		candidates = append(candidates, FTSSidecarOCRCandidate{
			Scope:            "attachment",
			FileID:           fileModel.ID,
			EntityID:         primaryEntity.ID(),
			DocumentID:       embeddedAttachmentDocID(fileModel.ID, objectName),
			AttachmentID:     embeddedAttachmentDocID(fileModel.ID, objectName),
			ArtifactID:       object.ID,
			PolicyID:         policy.ID,
			Bucket:           policy.BucketName,
			Path:             object.Path,
			Name:             object.Name,
			MimeType:         firstNonEmpty(object.MimeType, mime.TypeByExtension(filepath.Ext(object.Name))),
			Size:             object.Size,
			Reason:           attachmentOCRReason(object),
			TargetContentKey: "content",
		})
	}

	return candidates
}

func isLegacyFTSSidecarAuxiliaryKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "diagnostics", "external_attachments", "ocr_candidates":
		return true
	default:
		return false
	}
}

func hasFTSOCRCandidates(
	fileModel *ent.File,
	primaryEntity fs.Entity,
	policy *ent.StoragePolicy,
	rootText string,
	manifest *FTSSidecarManifest,
) bool {
	return len(buildFTSSidecarOCRCandidates(fileModel, primaryEntity, policy, rootText, manifest)) > 0
}

func shouldCreateFTSOCRCandidate(name, mimeType string, size int64, content string) bool {
	if strings.TrimSpace(content) != "" {
		return false
	}
	if size <= 0 {
		return false
	}

	mimeType = strings.TrimSpace(firstNonEmpty(mimeType, mime.TypeByExtension(filepath.Ext(name))))
	if !isFTSOCRImageMimeType(mimeType) {
		return false
	}

	return size >= 512
}

func isFTSOCRImageMimeType(mimeType string) bool {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if mimeType == "" || !strings.HasPrefix(mimeType, "image/") {
		return false
	}

	switch mimeType {
	case "image/svg+xml", "image/x-icon", "image/vnd.microsoft.icon":
		return false
	default:
		return true
	}
}

func attachmentOCRReason(object FTSSidecarArtifact) string {
	switch object.Kind {
	case "docx_media":
		return "docx_embedded_image_without_text"
	default:
		return "embedded_image_without_text"
	}
}

func persistFTSSidecars(
	ctx context.Context,
	extractor searcher.TextExtractor,
	ownerManager FileManager,
	fileModel *ent.File,
	uri *fs.URI,
	primaryEntity fs.Entity,
	source sidecarSource,
	text string,
) {
	internal, ok := ownerManager.(*manager)
	if !ok || primaryEntity == nil || uri == nil {
		return
	}

	tika, ok := extractor.(*tikaextractor.TikaExtractor)
	if !ok {
		return
	}

	cfg := internal.settings.FTSTikaExtractor(ctx)
	if !cfg.SidecarEnabled || (!cfg.SidecarTextEnabled && !cfg.SidecarAssetsEnabled) {
		return
	}

	existingMetadata := metadataMap(fileModel.Edges.Metadata)
	existingManifestPath := existingMetadata[dbfs.FTSSidecarManifestKey]
	existingEntityID := existingMetadata[dbfs.FTSSidecarEntityIDKey]

	if !ShouldExtractText(tika, fileModel.Name, fileModel.Size) {
		return
	}

	policy, handler, err := internal.getEntityPolicyDriver(ctx, primaryEntity, nil)
	if err != nil {
		internal.l.Warning("Failed to resolve storage driver for Tika sidecar: %s", err)
		return
	}

	manifest, savePath, err := internal.persistFTSSidecarsToHandler(ctx, extractor, nil, fileModel, uri.String(), primaryEntity, policy, handler, source, text)
	if err != nil {
		internal.l.Warning("Failed to persist Tika sidecars for file %d: %s", fileModel.ID, err)
		return
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

	if manifest != nil && savePath != "" {
		patches = []fs.MetadataPatch{
			{
				Key:     dbfs.FTSSidecarManifestKey,
				Value:   savePath,
				Private: true,
			},
			{
				Key:     dbfs.FTSSidecarEntityIDKey,
				Value:   strconv.Itoa(primaryEntity.ID()),
				Private: true,
			},
		}
	}

	if existingManifestPath != "" && (existingEntityID != strconv.Itoa(primaryEntity.ID()) || manifest == nil || savePath == "") {
		if err := cleanupFTSSidecarsByMetadata(ctx, internal, existingManifestPath, existingEntityID, primaryEntity); err != nil {
			internal.l.Warning("Failed to cleanup stale Tika sidecars for file %d: %s", fileModel.ID, err)
		}
	}

	if err := internal.fs.PatchMetadata(withPublicBypass(ctx, uri), []*fs.URI{uri}, patches...); err != nil {
		internal.l.Warning("Failed to update Tika sidecar metadata for file %d: %s", fileModel.ID, err)
	}
}

func (m *manager) persistFTSSidecarsForSlave(
	ctx context.Context,
	extractor searcher.TextExtractor,
	cfg *setting.FTSTikaExtractorSetting,
	fileModel *ent.File,
	primaryEntity fs.Entity,
	policy *ent.StoragePolicy,
	source sidecarSource,
	text string,
) (*FTSSidecarManifest, string, error) {
	if m == nil || primaryEntity == nil || fileModel == nil {
		return nil, "", nil
	}
	if policy == nil {
		return nil, "", fmt.Errorf("storage policy is required for slave sidecar persistence")
	}

	_, handler, err := m.getEntityPolicyDriver(ctx, primaryEntity, policy)
	if err != nil {
		return nil, "", fmt.Errorf("failed to resolve storage driver for slave Tika sidecar: %w", err)
	}

	return m.persistFTSSidecarsToHandler(ctx, extractor, cfg, fileModel, "", primaryEntity, policy, handler, source, text)
}

func (m *manager) persistFTSSidecarsToHandler(
	ctx context.Context,
	extractor searcher.TextExtractor,
	cfg *setting.FTSTikaExtractorSetting,
	fileModel *ent.File,
	sourcePath string,
	primaryEntity fs.Entity,
	policy *ent.StoragePolicy,
	handler driver.Handler,
	source sidecarSource,
	text string,
) (*FTSSidecarManifest, string, error) {
	if m == nil || primaryEntity == nil || fileModel == nil || policy == nil || handler == nil {
		return nil, "", nil
	}

	tika, ok := extractor.(*tikaextractor.TikaExtractor)
	if !ok {
		return nil, "", nil
	}

	cfg = resolveSlaveFTSTikaConfig(cfg, m.settings.FTSTikaExtractor(ctx))
	if !cfg.SidecarEnabled || (!cfg.SidecarTextEnabled && !cfg.SidecarAssetsEnabled) {
		return nil, "", nil
	}

	artifactOpts := tikaextractor.ArtifactOptions{
		ExtractInlineImages: cfg.ExtractInlineImages,
	}
	prefix := ftsSidecarPrefix(fileModel.OwnerID, fileModel.ID, primaryEntity.ID())
	manifest := &FTSSidecarManifest{
		Version:     ftsSidecarVersion,
		Provider:    ftsSidecarProviderTika,
		FileID:      fileModel.ID,
		EntityID:    primaryEntity.ID(),
		SourcePath:  sourcePath,
		ExtractedAt: time.Now(),
	}

	if cfg.SidecarTextEnabled {
		manifest.TextReady = true
		if text == "" && rewindSidecarSource(m, source) {
			extracted, err := tika.ExtractFile(ctx, source, fileModel.Name)
			if err != nil {
				return nil, "", fmt.Errorf("failed to extract sidecar text: %w", err)
			}
			text = strings.TrimSpace(extracted)
		}

		if text != "" {
			savePath := path.Join(prefix, "content.txt")
			if err := putSidecarBytes(ctx, handler, savePath, "content.txt", "text/plain; charset=utf-8", []byte(text)); err != nil {
				return nil, "", fmt.Errorf("failed to save sidecar text: %w", err)
			}

			manifest.Objects = append(manifest.Objects, FTSSidecarArtifact{
				ID:       "content.txt",
				Depth:    0,
				Kind:     "text",
				Name:     "content.txt",
				Path:     savePath,
				MimeType: "text/plain; charset=utf-8",
				Size:     int64(len(text)),
			})
		}
	}

	if cfg.SidecarAssetsEnabled {
		manifest.AssetsReady = true
		var rmetaRaw []byte

		if rewindSidecarSource(m, source) {
			raw, err := tika.RMetaFile(ctx, source, fileModel.Name, artifactOpts)
			if err != nil {
				return nil, "", fmt.Errorf("failed to extract tika rmeta: %w", err)
			}
			if len(bytes.TrimSpace(raw)) > 0 {
				rmetaRaw = append([]byte(nil), raw...)
				savePath := path.Join(prefix, "rmeta.json")
				if err := putSidecarBytes(ctx, handler, savePath, "rmeta.json", "application/json", raw); err != nil {
					return nil, "", fmt.Errorf("failed to save tika rmeta sidecar: %w", err)
				}
				manifest.Objects = append(manifest.Objects, FTSSidecarArtifact{
					ID:       "rmeta.json",
					Depth:    0,
					Kind:     "metadata",
					Name:     "rmeta.json",
					Path:     savePath,
					MimeType: "application/json",
					Size:     int64(len(raw)),
				})
			}
		}

		if len(rmetaRaw) > 0 {
			for _, item := range parseTikaRMetaAttachments(rmetaRaw) {
				relativeName, ok := normalizeFTSSidecarRelativePath(firstNonEmpty(item.Path, item.Name))
				if !ok || item.Content == "" {
					continue
				}

				logicalID := path.Join(ftsSidecarEmbeddedDir, relativeName)
				artifact, _, err := putSidecarTextArtifact(ctx, handler, prefix, logicalID, item.Name, item.Content)
				if err != nil {
					return nil, "", fmt.Errorf("failed to save tika attachment text sidecar %q: %w", logicalID, err)
				}
				if artifact.ID != "" {
					manifest.Objects = append(manifest.Objects, artifact)
				}
			}
		}

		if rewindSidecarSource(m, source) {
			raw, err := unpackTikaAssets(ctx, tika, fileModel.Name, source, artifactOpts)
			if err != nil {
				return nil, "", fmt.Errorf("failed to unpack tika embedded resources: %w", err)
			}
			if len(raw) > 0 {
				artifacts, err := saveSidecarArchive(ctx, handler, prefix, ftsSidecarEmbeddedDir, raw)
				if err != nil {
					return nil, "", fmt.Errorf("failed to save tika embedded resources: %w", err)
				}
				manifest.Objects = append(manifest.Objects, artifacts...)
			}
		}

		if strings.EqualFold(filepath.Ext(fileModel.Name), ".docx") {
			raw, err := buildDocxMediaArchive(source, primaryEntity.Size())
			if err != nil {
				return nil, "", fmt.Errorf("failed to collect docx media sidecar: %w", err)
			}
			if len(raw) > 0 {
				artifacts, err := saveSidecarArchive(ctx, handler, prefix, ftsSidecarDocxDir, raw)
				if err != nil {
					return nil, "", fmt.Errorf("failed to save docx media sidecar: %w", err)
				}
				manifest.Objects = append(manifest.Objects, artifacts...)
			}
		}
	}

	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, "", fmt.Errorf("failed to marshal tika sidecar manifest: %w", err)
	}

	savePath := path.Join(prefix, "manifest.json")
	if err := putSidecarBytes(ctx, handler, savePath, "manifest.json", "application/json", raw); err != nil {
		return nil, "", fmt.Errorf("failed to save tika sidecar manifest: %w", err)
	}

	return manifest, savePath, nil
}

func (m *manager) persistExternalFTSSidecars(
	ctx context.Context,
	fileModel *ent.File,
	uri *fs.URI,
	primaryEntity fs.Entity,
	result *externalFTSResultMessage,
) (*FTSSidecarManifest, string, error) {
	if m == nil || fileModel == nil || uri == nil || primaryEntity == nil || result == nil {
		return nil, "", fmt.Errorf("failed to persist external fts sidecars: invalid arguments")
	}

	policy, handler, err := m.getEntityPolicyDriver(ctx, primaryEntity, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to resolve storage driver for external fts sidecar: %w", err)
	}

	manifest, savePath, err := m.persistExternalFTSSidecarsToHandler(ctx, fileModel, primaryEntity, policy, handler, result)
	if err != nil {
		return nil, "", err
	}

	existingMetadata := metadataMap(fileModel.Edges.Metadata)
	existingManifestPath := existingMetadata[dbfs.FTSSidecarManifestKey]
	existingEntityID := existingMetadata[dbfs.FTSSidecarEntityIDKey]

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

	if manifest != nil && savePath != "" {
		patches = []fs.MetadataPatch{
			{
				Key:     dbfs.FTSSidecarManifestKey,
				Value:   savePath,
				Private: true,
			},
			{
				Key:     dbfs.FTSSidecarEntityIDKey,
				Value:   strconv.Itoa(primaryEntity.ID()),
				Private: true,
			},
		}
	}

	if existingManifestPath != "" && (existingEntityID != strconv.Itoa(primaryEntity.ID()) || manifest == nil || savePath == "") {
		if err := cleanupFTSSidecarsByMetadata(ctx, m, existingManifestPath, existingEntityID, primaryEntity); err != nil {
			m.l.Warning("Failed to cleanup stale external fts sidecars for file %d: %s", fileModel.ID, err)
		}
	}

	if err := m.fs.PatchMetadata(withPublicBypass(ctx, uri), []*fs.URI{uri}, patches...); err != nil {
		return nil, "", fmt.Errorf("failed to update external fts sidecar metadata: %w", err)
	}

	return manifest, savePath, nil
}

func (m *manager) persistExternalFTSSidecarsToHandler(
	ctx context.Context,
	fileModel *ent.File,
	primaryEntity fs.Entity,
	defaultPolicy *ent.StoragePolicy,
	handler driver.Handler,
	result *externalFTSResultMessage,
) (*FTSSidecarManifest, string, error) {
	if m == nil || fileModel == nil || primaryEntity == nil || handler == nil || result == nil {
		return nil, "", fmt.Errorf("failed to persist external fts sidecars: invalid arguments")
	}

	prefix := ftsSidecarPrefix(fileModel.OwnerID, fileModel.ID, primaryEntity.ID())
	manifest := &FTSSidecarManifest{
		Version:       ftsSidecarVersion,
		Provider:      ftsSidecarProviderExternal,
		SnapshotToken: strings.TrimSpace(result.SnapshotToken),
		FileID:        fileModel.ID,
		EntityID:      primaryEntity.ID(),
		SourcePath:    primaryEntity.Source(),
		ExtractedAt:   time.Now(),
	}

	content := strings.TrimSpace(result.Root.Content)
	if ref := result.Root.contentReference(); ref != nil {
		raw, err := m.resolveExternalFTSReferenceBytes(ctx, handler, defaultPolicy, ref)
		if err != nil {
			return nil, "", fmt.Errorf("failed to load external fts content object %q from bucket %q: %w", ref.Path, ref.Bucket, err)
		}
		content = strings.TrimSpace(string(raw))
	}

	if content != "" {
		savePath := path.Join(prefix, "content.txt")
		if err := putSidecarBytes(ctx, handler, savePath, "content.txt", "text/plain; charset=utf-8", []byte(content)); err != nil {
			return nil, "", fmt.Errorf("failed to save external fts content sidecar: %w", err)
		}
		manifest.TextReady = true
		manifest.Objects = append(manifest.Objects, FTSSidecarArtifact{
			ID:       "content.txt",
			Depth:    0,
			Kind:     "text",
			Name:     "content.txt",
			Path:     savePath,
			MimeType: "text/plain; charset=utf-8",
			Size:     int64(len(content)),
		})
	}

	attachments := normalizeExternalAttachmentArtifacts(fileModel.ID, result.Attachments)
	if len(attachments) > 0 {
		manifest.AssetsReady = true
	}
	for index := range attachments {
		artifact := attachments[index].Artifact
		artifact.Path = sidecarStoragePath(prefix, attachments[index].LogicalID, attachments[index].HasChildren)
		if err := putSidecarBytes(ctx, handler, artifact.Path, artifact.Name, artifact.MimeType, nil); err != nil {
			return nil, "", fmt.Errorf("failed to save external fts attachment artifact %q: %w", artifact.ID, err)
		}
		manifest.Objects = append(manifest.Objects, artifact)

		attachmentText := strings.TrimSpace(result.Attachments[index].Content)
		if ref := result.Attachments[index].contentReference(); ref != nil {
			raw, err := m.resolveExternalFTSReferenceBytes(ctx, handler, defaultPolicy, ref)
			if err != nil {
				return nil, "", fmt.Errorf("failed to load external fts attachment text object %q from bucket %q: %w", ref.Path, ref.Bucket, err)
			}
			attachmentText = strings.TrimSpace(string(raw))
		}

		if attachmentText == "" {
			continue
		}

		textArtifact, _, err := putSidecarTextArtifact(ctx, handler, prefix, attachments[index].LogicalID, artifact.Name, attachmentText)
		if err != nil {
			return nil, "", fmt.Errorf("failed to save external fts attachment text sidecar %q: %w", attachments[index].LogicalID, err)
		}
		if textArtifact.ID != "" {
			manifest.Objects = append(manifest.Objects, textArtifact)
		}
	}

	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, "", fmt.Errorf("failed to marshal external fts sidecar manifest: %w", err)
	}

	savePath := path.Join(prefix, "manifest.json")
	if err := putSidecarBytes(ctx, handler, savePath, "manifest.json", "application/json", raw); err != nil {
		return nil, "", fmt.Errorf("failed to save external fts sidecar manifest: %w", err)
	}

	return manifest, savePath, nil
}

func cleanupFTSSidecarsForFile(ctx context.Context, m *manager, file fs.File) error {
	if m == nil || file == nil {
		return nil
	}

	manifestPath := file.Metadata()[dbfs.FTSSidecarManifestKey]
	if manifestPath == "" {
		return nil
	}

	return cleanupFTSSidecarsByMetadata(
		ctx,
		m,
		manifestPath,
		file.Metadata()[dbfs.FTSSidecarEntityIDKey],
		file.PrimaryEntity(),
	)
}

func (m *manager) GetFTSSidecar(ctx context.Context, uri *fs.URI) (*FTSSidecarManifest, error) {
	_, manifest, _, _, err := m.loadFTSSidecarManifest(ctx, uri)
	return manifest, err
}

func (m *manager) GetFTSSidecarContent(ctx context.Context, uri *fs.URI, name string, download bool, expire *time.Time) (*FTSSidecarContent, error) {
	_, manifest, handler, entity, err := m.loadFTSSidecarManifest(ctx, uri)
	if err != nil {
		return nil, err
	}

	artifact, found := manifest.ObjectByID(name)
	if !found {
		artifact, found = manifest.ObjectByName(name)
	}
	if !found {
		return nil, serializer.NewError(serializer.CodeNotFound, "Full text sidecar object not found", nil)
	}

	if content, ok := tryOpenSidecarFile(ctx, handler, artifact.Path); ok {
		return &FTSSidecarContent{
			Artifact: artifact,
			Content:  content,
		}, nil
	}

	redirectURL, resolvedExpire, err := m.resolveFTSSidecarSource(ctx, artifact, entity, handler, download, expire)
	if err != nil {
		return nil, err
	}

	return &FTSSidecarContent{
		Artifact:    artifact,
		RedirectURL: redirectURL,
		Expires:     resolvedExpire,
	}, nil
}

func tryOpenSidecarFile(ctx context.Context, handler driver.Handler, savePath string) (*os.File, bool) {
	if handler == nil {
		return nil, false
	}

	reader, err := handler.Open(ctx, savePath)
	if err != nil || reader == nil {
		return nil, false
	}

	return reader, true
}

func cleanupFTSSidecarsForEntity(ctx context.Context, handler driver.Handler, entity fs.Entity) error {
	if entity == nil || entity.Model() == nil || entity.Model().Edges.File == nil || len(entity.Model().Edges.File) == 0 {
		return nil
	}

	fileModel := entity.Model().Edges.File[0]
	baseDir := ftsSidecarPrefix(fileModel.OwnerID, fileModel.ID, entity.ID())
	targets := ftsSidecarCleanupTargets(path.Join(baseDir, "manifest.json"), loadFTSSidecarManifestByPath(ctx, handler, path.Join(baseDir, "manifest.json")))

	failed, err := handler.Delete(ctx, targets...)
	if err != nil {
		return fmt.Errorf("failed to delete sidecars %v: %w", failed, err)
	}

	return nil
}

func (m *manager) loadFTSSidecarManifest(ctx context.Context, uri *fs.URI) (fs.File, *FTSSidecarManifest, driver.Handler, fs.Entity, error) {
	if uri == nil {
		return nil, nil, nil, nil, serializer.NewError(serializer.CodeParamErr, "unknown uri", nil)
	}

	loadCtx := context.WithValue(ctx, inventory.LoadFileMetadata{}, true)
	file, err := m.Get(loadCtx, uri, dbfs.WithFileEntities(), dbfs.WithNotRoot())
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to get file: %w", err)
	}

	manifestPath := file.Metadata()[dbfs.FTSSidecarManifestKey]
	if manifestPath == "" {
		return file, nil, nil, nil, serializer.NewError(serializer.CodeNotFound, "Full text sidecar not found", nil)
	}

	entity, err := m.resolveFTSSidecarEntity(loadCtx, file)
	if err != nil {
		return file, nil, nil, nil, err
	}

	_, handler, err := m.getEntityPolicyDriver(loadCtx, entity, nil)
	if err != nil {
		return file, nil, nil, nil, serializer.NewError(serializer.CodeInternalSetting, "Failed to resolve full text sidecar storage driver", err)
	}

	raw, err := readFTSSidecarBytes(loadCtx, m.dep.RequestClient(), handler, manifestPath)
	if err != nil {
		return file, nil, nil, nil, serializer.NewError(serializer.CodeIOFailed, "Failed to read full text sidecar manifest", err)
	}

	var manifest FTSSidecarManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return file, nil, nil, nil, serializer.NewError(serializer.CodeIOFailed, "Failed to parse full text sidecar manifest", err)
	}

	return file, &manifest, handler, entity, nil
}

func (m *manager) cloneFTSSidecarsForCopiedFile(ctx context.Context, originalFileID, targetFileID int) (bool, error) {
	if m == nil || originalFileID <= 0 || targetFileID <= 0 {
		return false, nil
	}

	sourceFileModel, err := m.loadFTSFileModel(ctx, originalFileID)
	if err != nil {
		return false, fmt.Errorf("failed to load source file model: %w", err)
	}

	sourceMetadata := metadataMap(sourceFileModel.Edges.Metadata)
	sourceManifestPath := strings.TrimSpace(sourceMetadata[dbfs.FTSSidecarManifestKey])
	if sourceManifestPath == "" {
		return false, nil
	}

	targetFileModel, err := m.loadFTSFileModel(ctx, targetFileID)
	if err != nil {
		return false, fmt.Errorf("failed to load target file model: %w", err)
	}

	targetEntityModel := findPrimaryFTSEntity(targetFileModel)
	if targetEntityModel == nil {
		return false, fmt.Errorf("failed to resolve primary entity for copied file %d", targetFileID)
	}

	sourceEntity, err := m.resolveFTSSidecarEntityForFileModel(ctx, sourceFileModel)
	if err != nil {
		return false, fmt.Errorf("failed to resolve source sidecar entity: %w", err)
	}

	_, sourceHandler, err := m.getEntityPolicyDriver(ctx, sourceEntity, nil)
	if err != nil {
		return false, fmt.Errorf("failed to resolve source sidecar handler: %w", err)
	}

	targetEntity := fs.NewEntity(targetEntityModel)
	_, targetHandler, err := m.getEntityPolicyDriver(ctx, targetEntity, nil)
	if err != nil {
		return false, fmt.Errorf("failed to resolve target sidecar handler: %w", err)
	}

	rawManifest, err := readFTSSidecarBytes(ctx, requestClientForSidecar(m), sourceHandler, sourceManifestPath)
	if err != nil {
		return false, fmt.Errorf("failed to read source sidecar manifest: %w", err)
	}

	var sourceManifest FTSSidecarManifest
	if err := json.Unmarshal(rawManifest, &sourceManifest); err != nil {
		return false, fmt.Errorf("failed to parse source sidecar manifest: %w", err)
	}

	targetPrefix := ftsSidecarPrefix(targetFileModel.OwnerID, targetFileModel.ID, targetEntityModel.ID)
	sourceBaseDir := path.Dir(sourceManifestPath)
	writtenPaths := make([]string, 0, len(sourceManifest.Objects)+1)
	cleanupTarget := func() {
		if targetHandler == nil {
			return
		}
		if len(writtenPaths) > 0 {
			_, _ = targetHandler.Delete(ctx, writtenPaths...)
		}
		_, _ = targetHandler.Delete(ctx, ftsSidecarCleanupDirectories(path.Join(targetPrefix, "manifest.json"), nil)...)
	}

	clonedObjects := make([]FTSSidecarArtifact, 0, len(sourceManifest.Objects))
	for _, object := range sourceManifest.Objects {
		relativePath, ok := ftsSidecarRelativePath(sourceBaseDir, object.Path)
		if !ok {
			cleanupTarget()
			return false, fmt.Errorf("sidecar object path %q is not under manifest base %q", object.Path, sourceBaseDir)
		}

		rawObject, err := readFTSSidecarBytes(ctx, requestClientForSidecar(m), sourceHandler, object.Path)
		if err != nil {
			cleanupTarget()
			return false, fmt.Errorf("failed to read sidecar object %q: %w", object.Path, err)
		}

		targetObjectPath := path.Join(targetPrefix, relativePath)
		mimeType := strings.TrimSpace(firstNonEmpty(object.MimeType, "application/octet-stream"))
		if err := putSidecarBytes(ctx, targetHandler, targetObjectPath, path.Base(targetObjectPath), mimeType, rawObject); err != nil {
			cleanupTarget()
			return false, fmt.Errorf("failed to persist cloned sidecar object %q: %w", targetObjectPath, err)
		}

		clonedObject := object
		clonedObject.Path = targetObjectPath
		clonedObject.Size = int64(len(rawObject))
		clonedObjects = append(clonedObjects, clonedObject)
		writtenPaths = append(writtenPaths, targetObjectPath)
	}

	targetManifest := sourceManifest
	targetManifest.FileID = targetFileModel.ID
	targetManifest.EntityID = targetEntityModel.ID
	targetManifest.SourcePath = targetEntityModel.Source
	targetManifest.Objects = clonedObjects
	if sourceManifest.Provider == ftsSidecarProviderExternal {
		targetManifest.SnapshotToken = buildFTSExternalSnapshotToken(targetFileModel, targetEntityModel, m.settings.FTSExternalExtractor(ctx))
	}

	targetManifestRaw, err := json.Marshal(&targetManifest)
	if err != nil {
		cleanupTarget()
		return false, fmt.Errorf("failed to marshal cloned sidecar manifest: %w", err)
	}

	targetManifestPath := path.Join(targetPrefix, "manifest.json")
	if err := putSidecarBytes(ctx, targetHandler, targetManifestPath, "manifest.json", "application/json", targetManifestRaw); err != nil {
		cleanupTarget()
		return false, fmt.Errorf("failed to persist cloned sidecar manifest: %w", err)
	}
	writtenPaths = append(writtenPaths, targetManifestPath)

	targetURI, err := m.resolveFTSFileURIByModel(ctx, targetFileModel)
	if err != nil {
		cleanupTarget()
		return false, fmt.Errorf("failed to resolve copied file uri: %w", err)
	}

	if err := m.fs.PatchMetadata(withPublicBypass(ctx, targetURI), []*fs.URI{targetURI},
		fs.MetadataPatch{
			Key:     dbfs.FTSSidecarManifestKey,
			Value:   targetManifestPath,
			Private: true,
		},
		fs.MetadataPatch{
			Key:     dbfs.FTSSidecarEntityIDKey,
			Value:   strconv.Itoa(targetEntityModel.ID),
			Private: true,
		},
	); err != nil {
		cleanupTarget()
		return false, fmt.Errorf("failed to patch cloned sidecar metadata: %w", err)
	}

	return true, nil
}

func (m *manager) resolveFTSSidecarEntity(ctx context.Context, file fs.File) (fs.Entity, error) {
	entityID := file.Metadata()[dbfs.FTSSidecarEntityIDKey]
	if entityID != "" {
		for _, entity := range file.Entities() {
			if strconv.Itoa(entity.ID()) == entityID {
				return entity, nil
			}
		}

		id, err := strconv.Atoi(entityID)
		if err != nil {
			return nil, serializer.NewError(serializer.CodeIOFailed, "Invalid full text sidecar entity id", err)
		}

		entity, err := m.fs.GetEntity(ctx, id)
		if err == nil && entity != nil {
			return entity, nil
		}
	}

	entity := file.PrimaryEntity()
	if entity == nil {
		return nil, serializer.NewError(serializer.CodeNotFound, "Full text sidecar source entity not found", nil)
	}

	return entity, nil
}

func (m *manager) resolveFTSSidecarEntityForFileModel(ctx context.Context, fileModel *ent.File) (fs.Entity, error) {
	if fileModel == nil {
		return nil, serializer.NewError(serializer.CodeNotFound, "Full text sidecar source entity not found", nil)
	}

	metadata := metadataMap(fileModel.Edges.Metadata)
	entityID := strings.TrimSpace(metadata[dbfs.FTSSidecarEntityIDKey])
	if entityID != "" {
		for _, item := range fileModel.Edges.Entities {
			if item != nil && strconv.Itoa(item.ID) == entityID {
				return fs.NewEntity(item), nil
			}
		}

		id, err := strconv.Atoi(entityID)
		if err != nil {
			return nil, serializer.NewError(serializer.CodeIOFailed, "Invalid full text sidecar entity id", err)
		}

		entity, err := m.fs.GetEntity(ctx, id)
		if err == nil && entity != nil {
			return entity, nil
		}
	}

	if primary := findPrimaryFTSEntity(fileModel); primary != nil {
		return fs.NewEntity(primary), nil
	}

	return nil, serializer.NewError(serializer.CodeNotFound, "Full text sidecar source entity not found", nil)
}

func (m *manager) resolveFTSSidecarSource(
	ctx context.Context,
	artifact FTSSidecarArtifact,
	entity fs.Entity,
	handler driver.Handler,
	download bool,
	expire *time.Time,
) (string, *time.Time, error) {
	if handler == nil || entity == nil {
		return "", nil, serializer.NewError(serializer.CodeNotFound, "Full text sidecar source not found", nil)
	}

	args := &driver.GetSourceArgs{
		Expire:      expire,
		IsDownload:  download,
		DisplayName: artifact.Name,
	}

	url, err := handler.Source(ctx, &ftsSidecarEntity{
		source:   artifact.Path,
		size:     artifact.Size,
		policyID: entity.PolicyID(),
	}, args)
	if err != nil {
		return "", nil, serializer.NewError(serializer.CodeIOFailed, "Failed to resolve full text sidecar object url", err)
	}

	return url, expire, nil
}

func (m *FTSSidecarManifest) ObjectByName(name string) (FTSSidecarArtifact, bool) {
	name = strings.TrimSpace(name)
	for _, item := range m.Objects {
		if item.Name == name || item.ID == name {
			return item, true
		}
	}

	return FTSSidecarArtifact{}, false
}

func rewindSidecarSource(m *manager, source io.Seeker) bool {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		m.l.Warning("Failed to rewind source for Tika sidecar extraction: %s", err)
		return false
	}

	return true
}

type sidecarSource interface {
	io.ReadSeekCloser
	io.ReaderAt
}

func putSidecarBytes(ctx context.Context, handler driver.Handler, savePath, fileName, mimeType string, data []byte) error {
	reader := bytes.NewReader(data)
	uri, err := fs.NewUriFromString("cloudreve:///my/" + path.Clean("/"+fileName))
	if err != nil {
		return fmt.Errorf("failed to build sidecar uri: %w", err)
	}

	return handler.Put(ctx, &fs.UploadRequest{
		Mode: fs.ModeOverwrite,
		Props: &fs.UploadProps{
			Uri:      uri,
			SavePath: savePath,
			Size:     int64(len(data)),
			MimeType: mimeType,
		},
		File:   io.NopCloser(reader),
		Seeker: reader,
	})
}

func putSidecarTextArtifact(ctx context.Context, handler driver.Handler, prefix, logicalID, fileName, content string) (FTSSidecarArtifact, string, error) {
	content = strings.TrimSpace(content)
	if handler == nil || content == "" {
		return FTSSidecarArtifact{}, "", nil
	}

	objectID := sidecarAttachmentTextObjectID(logicalID)
	if objectID == "" {
		return FTSSidecarArtifact{}, "", fmt.Errorf("invalid sidecar attachment text logical id %q", logicalID)
	}

	savePath := path.Join(prefix, objectID)
	displayName := path.Base(fileName)
	if displayName == "" || displayName == "." || displayName == "/" {
		displayName = "content.txt"
	}
	displayName += ".txt"

	raw := []byte(content)
	if err := putSidecarBytes(ctx, handler, savePath, displayName, "text/plain; charset=utf-8", raw); err != nil {
		return FTSSidecarArtifact{}, "", err
	}

	return FTSSidecarArtifact{
		ID:       objectID,
		ParentID: logicalID,
		Kind:     "attachment_text",
		Name:     path.Base(savePath),
		Path:     savePath,
		MimeType: "text/plain; charset=utf-8",
		Size:     int64(len(raw)),
	}, savePath, nil
}

func requestClientForSidecar(m *manager) request.Client {
	if m == nil || m.dep == nil || m.dep.RequestClient() == nil {
		return request.GeneralClient
	}

	return m.dep.RequestClient()
}

func readFTSSidecarBytes(ctx context.Context, client request.Client, handler driver.Handler, savePath string) ([]byte, error) {
	if handler.Capabilities().StaticFeatures.Enabled(int(driver.HandlerCapabilityInboundGet)) {
		reader, err := handler.Open(ctx, savePath)
		if err != nil {
			return nil, err
		}
		defer reader.Close()

		return io.ReadAll(reader)
	}

	if reader, ok := tryOpenSidecarFile(ctx, handler, savePath); ok {
		defer reader.Close()
		return io.ReadAll(reader)
	}

	expire := time.Now().Add(5 * time.Minute)
	sourceURL, err := handler.Source(ctx, &ftsSidecarEntity{
		source: savePath,
	}, &driver.GetSourceArgs{
		Expire:      &expire,
		DisplayName: path.Base(savePath),
	})
	if err != nil {
		return nil, err
	}

	resp := client.Request(http.MethodGet, sourceURL, nil, request.WithContext(ctx)).CheckHTTPResponse(http.StatusOK)
	raw, err := resp.GetResponse()
	if err != nil {
		return nil, err
	}

	return []byte(raw), nil
}

func (m *manager) resolveExternalFTSReferenceBytes(
	ctx context.Context,
	handler driver.Handler,
	defaultPolicy *ent.StoragePolicy,
	ref *externalFTSObjectReference,
) ([]byte, error) {
	ref = normalizeExternalFTSObjectReference(ref, 0, "", "")
	if ref == nil || strings.TrimSpace(ref.Path) == "" {
		return nil, nil
	}

	targetHandler := handler
	policyModel, err := m.resolveExternalFTSReferencePolicy(ctx, defaultPolicy, ref)
	if err != nil {
		return nil, err
	}
	if policyModel != nil && (targetHandler == nil || defaultPolicy == nil || policyModel.ID != defaultPolicy.ID) {
		if m == nil {
			return nil, fmt.Errorf("failed to resolve storage driver for policy %d", policyModel.ID)
		}

		targetHandler, err = m.GetStorageDriver(ctx, m.CastStoragePolicyOnSlave(ctx, policyModel))
		if err != nil {
			return nil, fmt.Errorf("failed to resolve storage driver for policy %d: %w", policyModel.ID, err)
		}
	}
	if targetHandler == nil {
		return nil, fmt.Errorf("external fts content source handler is nil")
	}

	return readFTSSidecarBytes(ctx, requestClientForSidecar(m), targetHandler, ref.Path)
}

func (m *manager) resolveExternalFTSReferencePolicy(
	ctx context.Context,
	defaultPolicy *ent.StoragePolicy,
	ref *externalFTSObjectReference,
) (*ent.StoragePolicy, error) {
	if ref == nil {
		return defaultPolicy, nil
	}

	if ref.PolicyID > 0 {
		if defaultPolicy != nil && defaultPolicy.ID == ref.PolicyID {
			return defaultPolicy, nil
		}

		if m != nil {
			switch {
			case m.policyClient != nil:
				policyModel, err := m.policyClient.GetPolicyByID(ctx, ref.PolicyID)
				if err != nil {
					return nil, fmt.Errorf("failed to load storage policy by id %d: %w", ref.PolicyID, err)
				}
				return policyModel, nil
			case m.dep != nil && m.dep.StoragePolicyClient() != nil:
				policyModel, err := m.dep.StoragePolicyClient().GetPolicyByID(ctx, ref.PolicyID)
				if err != nil {
					return nil, fmt.Errorf("failed to load storage policy by id %d: %w", ref.PolicyID, err)
				}
				return policyModel, nil
			}
		}

		return nil, fmt.Errorf("failed to resolve storage policy by id %d", ref.PolicyID)
	}

	bucket := strings.TrimSpace(ref.Bucket)
	if bucket == "" {
		return defaultPolicy, nil
	}
	if defaultPolicy != nil && strings.EqualFold(bucket, defaultPolicy.BucketName) {
		return defaultPolicy, nil
	}
	if m == nil || m.dep == nil || m.dep.DBClient() == nil {
		return nil, fmt.Errorf("failed to resolve storage policy for bucket %q", bucket)
	}

	policies, err := m.dep.DBClient().StoragePolicy.Query().Where(storagepolicy.BucketNameEQ(bucket)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load storage policy by bucket %q: %w", bucket, err)
	}
	switch len(policies) {
	case 0:
		return nil, fmt.Errorf("storage policy for bucket %q not found", bucket)
	case 1:
		return policies[0], nil
	default:
		return nil, fmt.Errorf("found %d storage policies for bucket %q; external fts result should include policy_id", len(policies), bucket)
	}
}

func (m *manager) hydrateFTSSidecarAttachmentContents(
	ctx context.Context,
	handler driver.Handler,
	attachments []searcher.SearchAttachmentDocument,
) []searcher.SearchAttachmentDocument {
	if handler == nil || len(attachments) == 0 {
		return attachments
	}

	client := requestClientForSidecar(m)
	for i := range attachments {
		if strings.TrimSpace(attachments[i].Content) != "" || !isFTSSidecarAttachmentTextPath(attachments[i].Source) {
			continue
		}

		raw, err := readFTSSidecarBytes(ctx, client, handler, attachments[i].Source)
		if err != nil {
			if m != nil {
				m.l.Warning("Failed to load FTS attachment text sidecar %q: %s", attachments[i].Source, err)
			}
			continue
		}

		attachments[i].Content = strings.TrimSpace(string(raw))
	}

	return attachments
}

func cleanupSidecarFiles(ctx context.Context, handler driver.Handler, manifestPath string) ([]string, error) {
	manifest := loadFTSSidecarManifestByPath(ctx, handler, manifestPath)
	targets := ftsSidecarCleanupTargets(manifestPath, manifest)
	failed, err := handler.Delete(ctx, targets...)
	if err != nil {
		return failed, err
	}

	directories := ftsSidecarCleanupDirectories(manifestPath, manifest)
	if len(directories) == 0 {
		return cleanupFTSSidecarFileDir(ctx, handler, manifestPath)
	}

	failed, err = handler.Delete(ctx, directories...)
	if err != nil {
		return failed, err
	}

	return cleanupFTSSidecarFileDir(ctx, handler, manifestPath)
}

func cleanupFTSSidecarsByMetadata(ctx context.Context, m *manager, manifestPath, entityID string, fallback fs.Entity) error {
	entity := fallback
	if entityID != "" {
		if id, err := strconv.Atoi(entityID); err == nil {
			if loaded, err := m.fs.GetEntity(ctx, id); err == nil && loaded != nil {
				entity = loaded
			}
		}
	}

	if entity == nil {
		return nil
	}

	_, handler, err := m.getEntityPolicyDriver(ctx, entity, nil)
	if err != nil {
		return fmt.Errorf("failed to resolve storage driver: %w", err)
	}

	failed, err := cleanupSidecarFiles(ctx, handler, manifestPath)
	if err != nil {
		return fmt.Errorf("failed to delete sidecars %v: %w", failed, err)
	}

	return nil
}

func buildDocxMediaArchive(source io.ReaderAt, size int64) ([]byte, error) {
	if size <= 0 {
		return nil, nil
	}

	reader, err := zip.NewReader(source, size)
	if err != nil {
		return nil, err
	}

	var buffer bytes.Buffer
	zw := zip.NewWriter(&buffer)
	found := false

	for _, file := range reader.File {
		if file.FileInfo().IsDir() || !strings.HasPrefix(file.Name, "word/media/") {
			continue
		}

		rc, err := file.Open()
		if err != nil {
			_ = zw.Close()
			return nil, err
		}

		header := file.FileHeader
		header.Name = path.Base(file.Name)
		header.Method = zip.Deflate

		writer, err := zw.CreateHeader(&header)
		if err != nil {
			_ = rc.Close()
			_ = zw.Close()
			return nil, err
		}

		if _, err := io.Copy(writer, rc); err != nil {
			_ = rc.Close()
			_ = zw.Close()
			return nil, err
		}
		_ = rc.Close()
		found = true
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}

	if !found {
		return nil, nil
	}

	return buffer.Bytes(), nil
}

func ftsSidecarPrefix(ownerID, fileID, entityID int) string {
	return path.Join(ftsSidecarRootDir, strconv.Itoa(ownerID), strconv.Itoa(fileID), strconv.Itoa(entityID))
}

func loadFTSSidecarManifestByPath(ctx context.Context, handler driver.Handler, manifestPath string) *FTSSidecarManifest {
	if manifestPath == "" || handler == nil {
		return nil
	}

	raw, err := readFTSSidecarBytes(ctx, request.GeneralClient, handler, manifestPath)
	if err != nil {
		return nil
	}

	var manifest FTSSidecarManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil
	}

	return &manifest
}

func ftsSidecarRelativePath(baseDir, itemPath string) (string, bool) {
	baseDir = strings.TrimSpace(path.Clean(baseDir))
	itemPath = strings.TrimSpace(path.Clean(itemPath))
	if baseDir == "" || itemPath == "" {
		return "", false
	}

	prefix := strings.TrimSuffix(baseDir, "/") + "/"
	if !strings.HasPrefix(itemPath, prefix) {
		return "", false
	}

	relative := strings.TrimPrefix(itemPath, prefix)
	if relative == "" || relative == "." {
		return "", false
	}

	return relative, true
}

func ftsSidecarCleanupTargets(manifestPath string, manifest *FTSSidecarManifest) []string {
	baseDir := path.Dir(manifestPath)
	targets := make([]string, 0, len(ftsSidecarBaseFiles)+1)
	seen := map[string]struct{}{}
	add := func(item string) {
		item = strings.TrimSpace(item)
		if item == "" {
			return
		}
		if _, ok := seen[item]; ok {
			return
		}
		seen[item] = struct{}{}
		targets = append(targets, item)
	}

	for _, name := range ftsSidecarBaseFiles {
		add(path.Join(baseDir, name))
	}
	if manifest != nil {
		for _, object := range manifest.Objects {
			add(object.Path)
		}
	}

	return targets
}

func ftsSidecarCleanupDirectories(manifestPath string, manifest *FTSSidecarManifest) []string {
	baseDir := path.Dir(manifestPath)
	seen := map[string]struct{}{}
	directories := make([]string, 0, 4)
	add := func(item string) {
		item = strings.TrimSpace(item)
		if item == "" || item == "." || item == "/" {
			return
		}
		if item != baseDir && !strings.HasPrefix(item, baseDir+"/") {
			return
		}
		if _, ok := seen[item]; ok {
			return
		}
		seen[item] = struct{}{}
		directories = append(directories, item)
	}

	add(path.Join(baseDir, ftsSidecarEmbeddedDir))
	add(path.Join(baseDir, ftsSidecarDocxDir))
	add(baseDir)

	if manifest != nil {
		for _, object := range manifest.Objects {
			dir := path.Dir(strings.TrimSpace(object.Path))
			for dir != "." && dir != "/" {
				add(dir)
				if dir == baseDir {
					break
				}
				dir = path.Dir(dir)
			}
		}
	}

	sort.SliceStable(directories, func(i, j int) bool {
		if len(directories[i]) == len(directories[j]) {
			return directories[i] > directories[j]
		}
		return len(directories[i]) > len(directories[j])
	})

	return directories
}

func cleanupFTSSidecarFileDir(ctx context.Context, handler driver.Handler, manifestPath string) ([]string, error) {
	fileDir := path.Dir(path.Dir(manifestPath))
	if strings.TrimSpace(fileDir) == "" || fileDir == "." || fileDir == "/" {
		return nil, nil
	}

	// Best-effort cleanup for the per-file container directory. It may still
	// contain sidecars for other entities, so ignore a non-empty directory.
	if _, err := handler.Delete(ctx, fileDir); err != nil {
		return nil, nil
	}

	return nil, nil
}

func saveSidecarArchive(
	ctx context.Context,
	handler driver.Handler,
	prefix, dir string,
	raw []byte,
) ([]FTSSidecarArtifact, error) {
	return saveSidecarArchiveRecursive(ctx, handler, prefix, dir, "", raw, 0, logicalArtifactKind(dir))
}

func saveSidecarArchiveRecursive(
	ctx context.Context,
	handler driver.Handler,
	prefix, logicalRoot, parentID string,
	raw []byte,
	depth int,
	entryKind string,
) ([]FTSSidecarArtifact, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if depth > ftsSidecarMaxDepth {
		return nil, nil
	}

	entries, ok := readArchiveEntries(raw)
	if !ok {
		return nil, fmt.Errorf("unsupported archive format")
	}

	artifacts := make([]FTSSidecarArtifact, 0, len(entries))
	for _, entry := range entries {
		relativeName, ok := normalizeFTSSidecarRelativePath(entry.Name)
		if !ok {
			continue
		}
		if shouldSkipFTSSidecarArchiveArtifact(logicalRoot, relativeName) {
			continue
		}

		logicalID := path.Join(logicalRoot, relativeName)
		hasChildren, childRaw := archiveBytes(entry.Data)
		savePath := sidecarStoragePath(prefix, logicalID, hasChildren)
		mimeType := firstNonEmpty(mime.TypeByExtension(filepath.Ext(relativeName)), "application/octet-stream")
		if err := putSidecarBytes(ctx, handler, savePath, path.Base(relativeName), mimeType, entry.Data); err != nil {
			return nil, err
		}

		artifact := FTSSidecarArtifact{
			ID:       logicalID,
			ParentID: parentID,
			Depth:    depth,
			Kind:     entryKind,
			Name:     path.Base(relativeName),
			Path:     savePath,
			MimeType: mimeType,
			Size:     int64(len(entry.Data)),
		}
		if hasChildren {
			artifact.Kind = "archive"
		}
		artifacts = append(artifacts, artifact)

		if hasChildren {
			children, err := saveSidecarArchiveRecursive(ctx, handler, prefix, logicalID, logicalID, childRaw, depth+1, entryKind)
			if err != nil {
				return nil, err
			}
			artifacts = append(artifacts, children...)
		}
	}

	return artifacts, nil
}

func shouldSkipFTSSidecarArchiveArtifact(logicalRoot string, relativeName string) bool {
	logicalRoot = strings.TrimSpace(logicalRoot)
	if logicalRoot == "" || !strings.HasPrefix(logicalRoot, ftsSidecarEmbeddedDir) {
		return false
	}

	return isTikaSyntheticAttachmentArtifact(relativeName)
}

type archiveEntry struct {
	Name string
	Data []byte
}

func readArchiveEntries(raw []byte) ([]archiveEntry, bool) {
	if len(raw) == 0 {
		return nil, false
	}

	if entries, ok := readTarEntries(raw); ok {
		return entries, true
	}
	if entries, ok := readZipEntries(raw); ok {
		return entries, true
	}

	return nil, false
}

func readTarEntries(raw []byte) ([]archiveEntry, bool) {
	tarReader := tar.NewReader(bytes.NewReader(raw))
	entries := make([]archiveEntry, 0)
	for {
		hdr, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false
		}
		if hdr == nil || hdr.FileInfo().IsDir() {
			continue
		}

		data, err := io.ReadAll(io.LimitReader(tarReader, hdr.Size))
		if err != nil {
			return nil, false
		}
		if int64(len(data)) != hdr.Size {
			return nil, false
		}

		entries = append(entries, archiveEntry{Name: hdr.Name, Data: data})
	}
	if len(entries) == 0 {
		return nil, false
	}

	return entries, true
}

func readZipEntries(raw []byte) ([]archiveEntry, bool) {
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, false
	}

	entries := make([]archiveEntry, 0, len(reader.File))
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}

		rc, err := file.Open()
		if err != nil {
			return nil, false
		}

		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, false
		}

		entries = append(entries, archiveEntry{Name: file.Name, Data: data})
	}
	if len(entries) == 0 {
		return nil, false
	}

	return entries, true
}

func normalizeFTSSidecarRelativePath(name string) (string, bool) {
	cleaned := path.Clean("/" + strings.TrimSpace(name))
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}

	return cleaned, true
}

func sidecarStoragePath(prefix, logicalID string, hasChildren bool) string {
	if hasChildren {
		return path.Join(prefix, logicalID, ftsSidecarSelfName)
	}

	return path.Join(prefix, logicalID)
}

func logicalArtifactKind(logicalRoot string) string {
	switch path.Base(logicalRoot) {
	case ftsSidecarDocxDir:
		return "docx_media"
	case ftsSidecarEmbeddedDir:
		return "embedded"
	default:
		return "artifact"
	}
}

func archiveBytes(raw []byte) (bool, []byte) {
	if _, ok := readArchiveEntries(raw); !ok {
		return false, nil
	}

	return true, raw
}

func unpackTikaAssets(ctx context.Context, tika *tikaextractor.TikaExtractor, fileName string, source sidecarSource, opts tikaextractor.ArtifactOptions) ([]byte, error) {
	if tika == nil {
		return nil, fmt.Errorf("tika extractor not available")
	}

	if raw, err := tika.UnpackAllFile(ctx, source, fileName, opts); err == nil && len(raw) > 0 {
		return raw, nil
	}

	return tika.UnpackFile(ctx, source, fileName, opts)
}

func (m *FTSSidecarManifest) ObjectByID(id string) (FTSSidecarArtifact, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return FTSSidecarArtifact{}, false
	}

	for _, item := range m.Objects {
		if item.ID == id {
			return item, true
		}
	}

	return FTSSidecarArtifact{}, false
}

func metadataMap(items []*ent.Metadata) map[string]string {
	res := make(map[string]string, len(items))
	for _, item := range items {
		res[item.Name] = item.Value
	}

	return res
}
