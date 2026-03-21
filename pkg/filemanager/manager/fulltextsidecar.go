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
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/driver"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/request"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	tikaextractor "github.com/cloudreve/Cloudreve/v4/pkg/searcher/extractor"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/gofrs/uuid"
)

const (
	ftsSidecarVersion  = 1
	ftsSidecarRootDir  = "cloudreve/fts-sidecar"
	ftsSidecarSelfName = "__self__"
	ftsSidecarMaxDepth = 8
)

var ftsSidecarFiles = []string{
	"content.txt",
	"rmeta.json",
	"assets.zip",
	"docx-media.zip",
	"manifest.json",
}

const (
	ftsSidecarEmbeddedDir = "attachments"
	ftsSidecarDocxDir     = "docx-media"
)

type FTSSidecarManifest struct {
	Version     int                  `json:"version"`
	FileID      int                  `json:"file_id"`
	EntityID    int                  `json:"entity_id"`
	SourcePath  string               `json:"source_path"`
	ExtractedAt time.Time            `json:"extracted_at"`
	TextReady   bool                 `json:"text_ready,omitempty"`
	AssetsReady bool                 `json:"assets_ready,omitempty"`
	Objects     []FTSSidecarArtifact `json:"objects,omitempty"`
}

type FTSSidecarArtifact struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Depth    int    `json:"depth,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	MimeType string `json:"mime_type"`
	Size     int64  `json:"size"`
}

type FTSSidecarContent struct {
	Artifact    FTSSidecarArtifact
	Content     io.ReadCloser
	RedirectURL string
	Expires     *time.Time
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

	_, handler, err := internal.getEntityPolicyDriver(ctx, primaryEntity, nil)
	if err != nil {
		internal.l.Warning("Failed to resolve storage driver for Tika sidecar: %s", err)
		return
	}

	manifest, savePath, err := internal.persistFTSSidecarsToHandler(ctx, extractor, fileModel, uri.String(), primaryEntity, handler, source, text)
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

	if err := internal.fs.PatchMetadata(ctx, []*fs.URI{uri}, patches...); err != nil {
		internal.l.Warning("Failed to update Tika sidecar metadata for file %d: %s", fileModel.ID, err)
	}
}

func (m *manager) persistFTSSidecarsForSlave(
	ctx context.Context,
	extractor searcher.TextExtractor,
	fileModel *ent.File,
	primaryEntity fs.Entity,
	policy *ent.StoragePolicy,
	source sidecarSource,
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

	return m.persistFTSSidecarsToHandler(ctx, extractor, fileModel, "", primaryEntity, handler, source, "")
}

func (m *manager) persistFTSSidecarsToHandler(
	ctx context.Context,
	extractor searcher.TextExtractor,
	fileModel *ent.File,
	sourcePath string,
	primaryEntity fs.Entity,
	handler driver.Handler,
	source sidecarSource,
	text string,
) (*FTSSidecarManifest, string, error) {
	if m == nil || primaryEntity == nil || fileModel == nil || handler == nil {
		return nil, "", nil
	}

	tika, ok := extractor.(*tikaextractor.TikaExtractor)
	if !ok {
		return nil, "", nil
	}

	cfg := m.settings.FTSTikaExtractor(ctx)
	if !cfg.SidecarEnabled || (!cfg.SidecarTextEnabled && !cfg.SidecarAssetsEnabled) {
		return nil, "", nil
	}

	artifactOpts := tikaextractor.ArtifactOptions{
		ExtractInlineImages: cfg.ExtractInlineImages,
	}
	prefix := ftsSidecarPrefix(fileModel.OwnerID, fileModel.ID, primaryEntity.ID())
	manifest := &FTSSidecarManifest{
		Version:     ftsSidecarVersion,
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

		if rewindSidecarSource(m, source) {
			raw, err := tika.RMetaFile(ctx, source, fileModel.Name, artifactOpts)
			if err != nil {
				return nil, "", fmt.Errorf("failed to extract tika rmeta: %w", err)
			}
			if len(bytes.TrimSpace(raw)) > 0 {
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

	if handler.Capabilities().StaticFeatures.Enabled(int(driver.HandlerCapabilityInboundGet)) {
		content, err := handler.Open(ctx, artifact.Path)
		if err != nil {
			return nil, serializer.NewError(serializer.CodeIOFailed, "Failed to open full text sidecar object", err)
		}

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

func readFTSSidecarBytes(ctx context.Context, client request.Client, handler driver.Handler, savePath string) ([]byte, error) {
	if handler.Capabilities().StaticFeatures.Enabled(int(driver.HandlerCapabilityInboundGet)) {
		reader, err := handler.Open(ctx, savePath)
		if err != nil {
			return nil, err
		}
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

func cleanupSidecarFiles(ctx context.Context, handler driver.Handler, manifestPath string) ([]string, error) {
	targets := ftsSidecarCleanupTargets(manifestPath, loadFTSSidecarManifestByPath(ctx, handler, manifestPath))

	return handler.Delete(ctx, targets...)
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

func ftsSidecarCleanupTargets(manifestPath string, manifest *FTSSidecarManifest) []string {
	baseDir := path.Dir(manifestPath)
	targets := make([]string, 0, len(ftsSidecarFiles)+1)
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

	for _, name := range ftsSidecarFiles {
		add(path.Join(baseDir, name))
	}
	if manifest != nil {
		for _, object := range manifest.Objects {
			add(object.Path)
		}
	}

	return targets
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
