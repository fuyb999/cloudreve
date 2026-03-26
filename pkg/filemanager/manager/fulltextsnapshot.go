package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/driver"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	tikaextractor "github.com/cloudreve/Cloudreve/v4/pkg/searcher/extractor"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/samber/lo"
)

const ftsSnapshotVersion = 1

func BuildFTSFileDocument(ctx context.Context, dep dependency.Dep, user *ent.User, fileID int) (*searcher.SearchFileDocument, *fs.URI, error) {
	fm := NewFileManager(dep, user)
	defer fm.Recycle()

	internal, ok := fm.(*manager)
	if !ok {
		return nil, nil, fmt.Errorf("failed to construct file manager")
	}

	return internal.buildFTSFileDocument(ctx, fileID)
}

func (m *manager) buildFTSFileDocument(ctx context.Context, fileID int) (*searcher.SearchFileDocument, *fs.URI, error) {
	fileModel, err := m.loadFTSFileModel(ctx, fileID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load file model: %w", err)
	}

	ownerManager, err := m.fileManagerForOwner(ctx, fileModel.OwnerID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load file owner context: %w", err)
	}
	defer ownerManager.Recycle()

	traversed, err := ownerManager.TraverseFile(ctx, fileID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve file uri: %w", err)
	}

	ownerURI := traversed.Uri(true)
	if ownerURI == nil {
		return nil, nil, fmt.Errorf("failed to resolve file uri")
	}

	var primaryEntity *ent.Entity
	for _, entity := range fileModel.Edges.Entities {
		if entity.ID == fileModel.PrimaryEntity {
			primaryEntity = entity
			break
		}
	}
	var primaryFTSEntity fs.Entity
	if primaryEntity != nil {
		primaryFTSEntity = fs.NewEntity(primaryEntity)
	}

	content, embeddedAttachments, extractErr := extractFTSContent(
		ctx,
		m.dep.TextExtractor(ctx),
		ownerManager,
		fileModel,
		primaryFTSEntity,
		ownerURI,
	)
	if extractErr != nil {
		m.l.Warning("Failed to extract FTS content for file %d name=%q: %s", fileModel.ID, fileModel.Name, extractErr)
	}
	metadata := lo.Associate(fileModel.Edges.Metadata, func(item *ent.Metadata) (string, string) {
		return item.Name, item.Value
	})

	filePolicy, _ := m.storagePolicyFromID(ctx, fileModel.StoragePolicyFiles)
	latestVersion := buildSearchVersion(primaryEntity, fileModel.Name, filePolicy)
	versions, attachments := buildSearchEntities(fileModel, ownerURI, filePolicy)
	latestVersionID := 0
	latestVersionBucket := ""
	if latestVersion != nil {
		latestVersionID = latestVersion.EntityID
		latestVersionBucket = latestVersion.Bucket
	}

	publicURI := m.resolvePublicSearchURI(ctx, fileModel)
	paths, pathText := buildFTSSearchPathDocuments(
		ownerURI,
		publicURI,
		fileModel.Size,
		fileTypeString(types.FileType(fileModel.Type)),
		fileModel.PrimaryEntity,
		latestVersionID,
		latestVersionBucket,
	)
	attachments = append(attachments, embeddedAttachments...)

	doc := &searcher.SearchFileDocument{
		ID:              fmt.Sprintf("%d", fileModel.ID),
		FileID:          fileModel.ID,
		OwnerID:         fileModel.OwnerID,
		EntityID:        fileModel.PrimaryEntity,
		ParentID:        fileModel.FileChildren,
		FileName:        fileModel.Name,
		FileExt:         firstNonEmpty(fileModel.FileExt, util.Ext(fileModel.Name)),
		FileType:        fileTypeString(types.FileType(fileModel.Type)),
		FileTypeValue:   fileModel.Type,
		Size:            fileModel.Size,
		CreatedAt:       fileModel.CreatedAt,
		UpdatedAt:       fileModel.UpdatedAt,
		IsSymbolic:      fileModel.IsSymbolic,
		Shared:          len(fileModel.Edges.Shares) > 0,
		TreePath:        fileModel.TreePath,
		StoragePolicyID: fileModel.StoragePolicyFiles,
		Metadata:        metadata,
		MetadataText:    joinMetadata(metadata),
		Props:           mapFromFileProps(fileModel.Props),
		PathText:        pathText,
		Content:         content,
		ContentExcerpt:  excerpt(content, 320),
		LatestVersion:   latestVersion,
		Versions:        versions,
		Paths:           paths,
		Attachments:     attachments,
		SnapshotVersion: ftsSnapshotVersion,
		SynchronizedAt:  time.Now(),
	}

	if filePolicy != nil {
		doc.StorageType = filePolicy.Type
		doc.StorageName = filePolicy.Name
		doc.StorageBucket = filePolicy.BucketName
	}
	if latestVersion != nil {
		doc.StoragePolicyID = latestVersion.StoragePolicyID
		doc.StorageType = latestVersion.StorageType
		doc.StorageName = latestVersion.StorageName
		doc.StorageBucket = latestVersion.Bucket
	}

	return doc, ownerURI, nil
}

func (m *manager) resolvePublicSearchURI(ctx context.Context, fileModel *ent.File) *fs.URI {
	if m == nil || fileModel == nil {
		return nil
	}

	publicService := publicshare.NewService(m.l, m.dep.FileClient(), m.dep.SettingClient(), m.hasher)
	rootID, err := publicService.RootID(ctx)
	if err != nil || rootID == 0 {
		return nil
	}

	publicURI := publicshare.BuildPublicURI()
	if fileModel.ID == rootID {
		return publicURI
	}

	ancestors, err := m.dep.FileClient().GetAncestorFiles(ctx, fileModel)
	if err != nil {
		return nil
	}

	rootIndex := -1
	for i, ancestor := range ancestors {
		if ancestor != nil && ancestor.ID == rootID {
			rootIndex = i
			break
		}
	}
	if rootIndex < 0 {
		return nil
	}

	for _, ancestor := range ancestors[rootIndex+1:] {
		if ancestor == nil || strings.TrimSpace(ancestor.Name) == "" {
			continue
		}
		publicURI = publicURI.Join(ancestor.Name)
	}

	return publicURI
}

func buildFTSSearchPathDocuments(
	ownerURI *fs.URI,
	publicURI *fs.URI,
	size int64,
	fileType string,
	entityID int,
	versionID int,
	bucket string,
) ([]searcher.SearchPathDocument, string) {
	paths := make([]searcher.SearchPathDocument, 0, 2)
	seen := map[string]struct{}{}
	appendPath := func(uri *fs.URI, primary bool) {
		if uri == nil {
			return
		}

		raw := strings.TrimSpace(uri.String())
		if raw == "" {
			return
		}
		if _, ok := seen[raw]; ok {
			return
		}

		seen[raw] = struct{}{}
		paths = append(paths, searcher.SearchPathDocument{
			Path:      raw,
			IsPrimary: primary,
			Bucket:    bucket,
			Size:      size,
			FileType:  fileType,
			EntityID:  entityID,
			VersionID: versionID,
		})
	}

	appendPath(publicURI, publicURI != nil)
	appendPath(ownerURI, publicURI == nil)

	if len(paths) > 0 {
		paths[0].IsPrimary = true
	}

	pathTextParts := make([]string, 0, len(paths))
	for _, item := range paths {
		if item.Path != "" {
			pathTextParts = append(pathTextParts, item.Path)
		}
	}

	return paths, strings.Join(pathTextParts, "\n")
}

func (m *manager) loadFTSFileModel(ctx context.Context, fileID int) (*ent.File, error) {
	ctx = context.WithValue(ctx, inventory.LoadFileEntity{}, true)
	ctx = context.WithValue(ctx, inventory.LoadFileMetadata{}, true)
	ctx = context.WithValue(ctx, inventory.LoadFileShare{}, true)
	ctx = context.WithValue(ctx, inventory.LoadFileUser{}, true)
	ctx = context.WithValue(ctx, inventory.LoadEntityUser{}, true)
	ctx = context.WithValue(ctx, inventory.LoadEntityStoragePolicy{}, true)
	return m.dep.FileClient().GetByID(ctx, fileID)
}

func (m *manager) fileManagerForOwner(ctx context.Context, ownerID int) (FileManager, error) {
	if m.user != nil && m.user.ID == ownerID {
		return NewFileManager(m.dep, m.user), nil
	}

	owner, err := m.dep.UserClient().GetLoginUserByID(ctx, ownerID)
	if err != nil {
		return nil, err
	}

	return NewFileManager(m.dep, owner), nil
}

func (m *manager) storagePolicyFromID(ctx context.Context, id int) (*ent.StoragePolicy, error) {
	if id <= 0 {
		return nil, nil
	}

	policy, err := m.dep.StoragePolicyClient().GetPolicyByID(ctx, id)
	if err != nil {
		return nil, err
	}

	return policy, nil
}

func extractFTSContent(
	ctx context.Context,
	extractor searcher.TextExtractor,
	ownerManager FileManager,
	fileModel *ent.File,
	primaryEntity fs.Entity,
	uri *fs.URI,
) (string, []searcher.SearchAttachmentDocument, error) {
	if primaryEntity == nil {
		return "", nil, nil
	}

	var (
		sidecarContent     string
		sidecarAttachments []searcher.SearchAttachmentDocument
		hasCurrentSidecar  bool
		sidecarManifest    *FTSSidecarManifest
		internal           *manager
		sidecarCfg         = struct {
			textEnabled   bool
			assetsEnabled bool
		}{}
	)
	if loaded, ok := ownerManager.(*manager); ok {
		internal = loaded
		cfg := internal.settings.FTSTikaExtractor(ctx)
		sidecarCfg.textEnabled = cfg.SidecarTextEnabled
		sidecarCfg.assetsEnabled = cfg.SidecarAssetsEnabled
		sidecarContent, sidecarAttachments, sidecarManifest, hasCurrentSidecar = internal.loadFTSContentFromSidecar(ctx, fileModel, primaryEntity, uri)
		if hasCurrentSidecar {
			if sidecarCfg.textEnabled && sidecarManifest != nil && sidecarManifest.TextReady {
				if !sidecarCfg.assetsEnabled {
					sidecarAttachments = nil
				}
				if !sidecarCfg.assetsEnabled || sidecarManifest.AssetsReady {
					return sidecarContent, sidecarAttachments, nil
				}
			}

			if !sidecarCfg.textEnabled && sidecarCfg.assetsEnabled && sidecarManifest != nil && sidecarManifest.AssetsReady {
				return "", sidecarAttachments, nil
			}
		}
	}

	source, err := ownerManager.GetEntitySource(ctx, primaryEntity.ID())
	if err != nil {
		return "", nil, err
	}
	defer source.Close()

	text := ""
	if sidecarCfg.textEnabled {
		text = sidecarContent
	}
	if text == "" && ShouldExtractText(extractor, fileModel.Name, fileModel.Size) {
		var err error
		if tika, ok := extractor.(*tikaextractor.TikaExtractor); ok {
			text, err = tika.ExtractFile(ctx, source, fileModel.Name)
		} else {
			text, err = extractor.Extract(ctx, source)
		}
		if err != nil {
			return "", nil, err
		}

		text = strings.TrimSpace(text)
	}

	var attachments []searcher.SearchAttachmentDocument
	if sidecarCfg.assetsEnabled {
		attachments = sidecarAttachments
	}
	if len(attachments) == 0 && (!hasCurrentSidecar || sidecarManifest == nil || !sidecarManifest.AssetsReady) {
		attachments = extractFTSEmbeddedAttachments(ctx, extractor, ownerManager, fileModel, primaryEntity, uri, source)
	}
	shouldPersistSidecar := !hasCurrentSidecar ||
		(sidecarCfg.textEnabled && (sidecarManifest == nil || !sidecarManifest.TextReady)) ||
		(sidecarCfg.assetsEnabled && (sidecarManifest == nil || !sidecarManifest.AssetsReady))
	if shouldPersistSidecar {
		persistFTSSidecars(ctx, extractor, ownerManager, fileModel, uri, primaryEntity, source, text)
		if internal != nil && sidecarCfg.assetsEnabled && len(attachments) > 0 {
			if refreshedText, refreshedAttachments, _, ok := internal.loadFTSContentFromSidecar(ctx, fileModel, primaryEntity, uri); ok {
				if sidecarCfg.textEnabled && text == "" {
					text = refreshedText
				}
				if len(refreshedAttachments) > 0 {
					attachments = refreshedAttachments
				}
			}
		}
	}
	return text, attachments, nil
}

func (m *manager) loadFTSContentFromSidecar(
	ctx context.Context,
	fileModel *ent.File,
	primaryEntity fs.Entity,
	uri *fs.URI,
) (string, []searcher.SearchAttachmentDocument, *FTSSidecarManifest, bool) {
	if m == nil || fileModel == nil || primaryEntity == nil || uri == nil {
		return "", nil, nil, false
	}

	_, manifest, handler, _, err := m.loadFTSSidecarManifest(ctx, uri)
	if err != nil || manifest == nil || manifest.EntityID != primaryEntity.ID() {
		return "", nil, nil, false
	}

	var (
		content  string
		rmetaRaw []byte
	)

	if raw, ok := m.readFTSSidecarObject(ctx, handler, manifest, "content.txt"); ok {
		content = strings.TrimSpace(string(raw))
	}
	if raw, ok := m.readFTSSidecarObject(ctx, handler, manifest, "rmeta.json"); ok {
		rmetaRaw = raw
	}

	return content, buildEmbeddedSearchAttachmentsFromManifest(fileModel, primaryEntity, manifest, rmetaRaw), manifest, true
}

func (m *manager) readFTSSidecarObject(
	ctx context.Context,
	handler driver.Handler,
	manifest *FTSSidecarManifest,
	name string,
) ([]byte, bool) {
	if manifest == nil || handler == nil {
		return nil, false
	}

	object, ok := manifest.ObjectByName(name)
	if !ok {
		return nil, false
	}

	raw, err := readFTSSidecarBytes(ctx, m.dep.RequestClient(), handler, object.Path)
	if err != nil {
		return nil, false
	}

	return raw, true
}

func extractFTSEmbeddedAttachments(
	ctx context.Context,
	extractor searcher.TextExtractor,
	ownerManager FileManager,
	fileModel *ent.File,
	primaryEntity fs.Entity,
	uri *fs.URI,
	source sidecarSource,
) []searcher.SearchAttachmentDocument {
	internal, ok := ownerManager.(*manager)
	if !ok || fileModel == nil || uri == nil || primaryEntity == nil || source == nil {
		return nil
	}

	tika, ok := extractor.(*tikaextractor.TikaExtractor)
	if !ok {
		return nil
	}

	cfg := internal.settings.FTSTikaExtractor(ctx)
	if !cfg.SidecarEnabled || !cfg.SidecarAssetsEnabled {
		return nil
	}

	artifactOpts := tikaextractor.ArtifactOptions{
		ExtractInlineImages: cfg.ExtractInlineImages,
	}

	var (
		rmetaRaw  []byte
		unpackRaw []byte
		docxRaw   []byte
	)

	if rewindSidecarSource(internal, source) {
		if raw, err := tika.RMetaFile(ctx, source, fileModel.Name, artifactOpts); err != nil {
			internal.l.Warning("Failed to extract Tika rmeta for file %d when building FTS attachments: %s", fileModel.ID, err)
		} else {
			rmetaRaw = raw
		}
	}

	if rewindSidecarSource(internal, source) {
		if raw, err := unpackTikaAssets(ctx, tika, fileModel.Name, source, artifactOpts); err != nil {
			internal.l.Warning("Failed to unpack Tika embedded resources for file %d when building FTS attachments: %s", fileModel.ID, err)
		} else {
			unpackRaw = raw
		}
	}

	if strings.EqualFold(filepath.Ext(fileModel.Name), ".docx") {
		if raw, err := buildDocxMediaArchive(source, primaryEntity.Size()); err != nil {
			internal.l.Warning("Failed to collect DOCX media for file %d when building FTS attachments: %s", fileModel.ID, err)
		} else {
			docxRaw = raw
		}
	}

	return buildEmbeddedSearchAttachments(fileModel, uri, primaryEntity, rmetaRaw, unpackRaw, docxRaw)
}

func buildSearchEntities(fileModel *ent.File, uri *fs.URI, fallbackPolicy *ent.StoragePolicy) ([]searcher.SearchFileVersionDocument, []searcher.SearchAttachmentDocument) {
	versions := make([]searcher.SearchFileVersionDocument, 0)
	attachments := make([]searcher.SearchAttachmentDocument, 0)
	fileName := fileModel.Name

	for _, entity := range fileModel.Edges.Entities {
		entityPolicy := fallbackPolicy
		if entity.Edges.StoragePolicy != nil {
			entityPolicy = entity.Edges.StoragePolicy
		}

		versionDoc := buildSearchVersion(entity, fileName, entityPolicy)
		if versionDoc == nil {
			continue
		}

		if types.EntityType(entity.Type) == types.EntityTypeVersion {
			versions = append(versions, *versionDoc)
			continue
		}

		attachments = append(attachments, searcher.SearchAttachmentDocument{
			ID:        versionDoc.ID,
			ParentID:  fileModel.ID,
			EntityID:  entity.ID,
			Type:      versionDoc.EntityType,
			Name:      attachmentName(fileName, types.EntityType(entity.Type)),
			Path:      uri.String(),
			Bucket:    versionDoc.Bucket,
			Size:      versionDoc.Size,
			MimeType:  versionDoc.MimeType,
			Source:    versionDoc.Source,
			CreatedAt: versionDoc.CreatedAt,
			UpdatedAt: versionDoc.UpdatedAt,
		})
	}

	sort.SliceStable(versions, func(i, j int) bool {
		if versions[i].EntityID == fileModel.PrimaryEntity {
			return true
		}
		if versions[j].EntityID == fileModel.PrimaryEntity {
			return false
		}
		return versions[i].EntityID > versions[j].EntityID
	})

	return versions, attachments
}

type embeddedAttachmentAccumulator struct {
	doc *searcher.SearchAttachmentDocument
}

func buildEmbeddedSearchAttachments(
	fileModel *ent.File,
	uri *fs.URI,
	primaryEntity fs.Entity,
	rmetaRaw, unpackRaw, docxRaw []byte,
) []searcher.SearchAttachmentDocument {
	if fileModel == nil || uri == nil || primaryEntity == nil {
		return nil
	}

	prefix := ftsSidecarPrefix(fileModel.OwnerID, fileModel.ID, primaryEntity.ID())
	items := map[string]*embeddedAttachmentAccumulator{}
	order := make([]string, 0)

	upsert := func(key string, update func(*searcher.SearchAttachmentDocument)) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}

		acc, ok := items[key]
		if !ok {
			doc := &searcher.SearchAttachmentDocument{
				ID:       fmt.Sprintf("%d:embedded:%s", fileModel.ID, key),
				ParentID: fileModel.ID,
				EntityID: primaryEntity.ID(),
				Type:     "embedded",
				Path:     key,
			}
			acc = &embeddedAttachmentAccumulator{doc: doc}
			items[key] = acc
			order = append(order, key)
		}

		update(acc.doc)
		if acc.doc.Name == "" {
			acc.doc.Name = filepath.Base(acc.doc.Path)
		}
		if acc.doc.MimeType == "" {
			acc.doc.MimeType = mime.TypeByExtension(filepath.Ext(acc.doc.Name))
		}
		if acc.doc.Source == "" {
			acc.doc.Source = acc.doc.Path
		}
	}

	if len(rmetaRaw) > 0 {
		for index, item := range parseTikaRMetaAttachments(rmetaRaw) {
			relativeName := firstNonEmpty(item.Path, item.Name)
			if normalized, ok := normalizeFTSSidecarRelativePath(relativeName); ok {
				relativeName = normalized
			} else {
				relativeName = fmt.Sprintf("embedded_%d", index)
			}
			key := path.Join(prefix, ftsSidecarEmbeddedDir, relativeName)

			upsert(key, func(doc *searcher.SearchAttachmentDocument) {
				if item.Type != "" {
					doc.Type = item.Type
				}
				doc.Name = firstNonEmpty(doc.Name, item.Name)
				doc.Path = firstNonEmpty(doc.Path, key)
				doc.MimeType = firstNonEmpty(doc.MimeType, item.MimeType)
				if item.Size > 0 {
					doc.Size = item.Size
				}
				doc.Content = firstNonEmpty(doc.Content, item.Content)
				if len(item.Metadata) > 0 {
					if doc.Metadata == nil {
						doc.Metadata = map[string]string{}
					}
					for mk, mv := range item.Metadata {
						if _, ok := doc.Metadata[mk]; !ok {
							doc.Metadata[mk] = mv
						}
					}
				}
				if item.Path != "" {
					if doc.Metadata == nil {
						doc.Metadata = map[string]string{}
					}
					doc.Metadata["embedded_path"] = item.Path
				}
			})
		}
	}

	addArchiveEntriesToAttachments(unpackRaw, prefix, ftsSidecarEmbeddedDir, "embedded", upsert)
	addArchiveEntriesToAttachments(docxRaw, prefix, ftsSidecarDocxDir, "docx_media", upsert)

	attachments := make([]searcher.SearchAttachmentDocument, 0, len(order))
	for _, key := range order {
		item := items[key].doc
		item.Name = firstNonEmpty(item.Name, filepath.Base(item.Path))
		item.Path = firstNonEmpty(item.Path, item.Name)
		item.Source = firstNonEmpty(item.Source, item.Path)
		attachments = append(attachments, *item)
	}

	return attachments
}

func buildEmbeddedSearchAttachmentsFromManifest(
	fileModel *ent.File,
	primaryEntity fs.Entity,
	manifest *FTSSidecarManifest,
	rmetaRaw []byte,
) []searcher.SearchAttachmentDocument {
	if fileModel == nil || primaryEntity == nil || manifest == nil {
		return nil
	}

	rmetaByName := map[string]tikaRMetaAttachment{}
	for _, item := range parseTikaRMetaAttachments(rmetaRaw) {
		relativeName := firstNonEmpty(item.Path, item.Name)
		relativeName, ok := normalizeFTSSidecarRelativePath(relativeName)
		if !ok {
			continue
		}

		key := path.Join(ftsSidecarEmbeddedDir, relativeName)
		if _, exists := rmetaByName[key]; !exists {
			rmetaByName[key] = item
		}
	}

	attachments := make([]searcher.SearchAttachmentDocument, 0, len(manifest.Objects))
	for _, object := range manifest.Objects {
		objectName := firstNonEmpty(object.ID, object.Name)
		if objectName == "" {
			continue
		}
		if objectName == "content.txt" || objectName == "rmeta.json" || objectName == "manifest.json" {
			continue
		}

		docType := ""
		switch {
		case object.Kind == "archive":
			docType = "archive"
		case object.Kind != "":
			docType = object.Kind
		case strings.HasPrefix(objectName, ftsSidecarEmbeddedDir+"/"):
			docType = "embedded"
		case strings.HasPrefix(objectName, ftsSidecarDocxDir+"/"):
			docType = "docx_media"
		default:
			continue
		}

		doc := searcher.SearchAttachmentDocument{
			ID:                 fmt.Sprintf("%d:embedded:%s", fileModel.ID, objectName),
			ParentID:           fileModel.ID,
			ParentAttachmentID: object.ParentID,
			Depth:              object.Depth,
			EntityID:           primaryEntity.ID(),
			Type:               docType,
			Name:               path.Base(objectName),
			Path:               object.Path,
			Size:               object.Size,
			MimeType:           object.MimeType,
			Source:             object.Path,
			CreatedAt:          primaryEntity.CreatedAt(),
			UpdatedAt:          primaryEntity.UpdatedAt(),
		}

		if doc.MimeType == "" {
			doc.MimeType = mime.TypeByExtension(filepath.Ext(doc.Name))
		}

		if item, ok := rmetaByName[objectName]; ok {
			doc.Name = firstNonEmpty(doc.Name, item.Name)
			doc.MimeType = firstNonEmpty(doc.MimeType, item.MimeType)
			if item.Size > 0 {
				doc.Size = item.Size
			}
			doc.Content = firstNonEmpty(doc.Content, item.Content)
			if len(item.Metadata) > 0 {
				doc.Metadata = cloneStringMap(item.Metadata)
			}
			if item.Path != "" {
				if doc.Metadata == nil {
					doc.Metadata = map[string]string{}
				}
				doc.Metadata["embedded_path"] = item.Path
			}
		}

		attachments = append(attachments, doc)
	}

	return attachments
}

type tikaRMetaAttachment struct {
	Type     string
	Name     string
	Path     string
	MimeType string
	Size     int64
	Content  string
	Metadata map[string]string
}

func parseTikaRMetaAttachments(raw []byte) []tikaRMetaAttachment {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	var payload []map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}

	res := make([]tikaRMetaAttachment, 0, len(payload))
	for index, item := range payload {
		path := firstNonEmpty(
			stringValue(item["X-TIKA:embedded_resource_path"]),
			stringValue(item["embedded_resource_path"]),
			stringValue(item["embeddedResourcePath"]),
		)
		name := firstNonEmpty(
			stringValue(item["resourceName"]),
			stringValue(item["resource_name"]),
			filepath.Base(path),
		)

		// The first rmeta item is typically the parent document. Keep only embedded items.
		if index == 0 && path == "" {
			continue
		}
		if path == "" && name == "" {
			continue
		}

		content := strings.TrimSpace(firstNonEmpty(
			stringValue(item["X-TIKA:content"]),
			stringValue(item["content"]),
		))
		mimeType := firstNonEmpty(
			stringValue(item["Content-Type"]),
			stringValue(item["dc:format"]),
			mime.TypeByExtension(filepath.Ext(name)),
		)
		size := firstNonZeroInt64(
			int64Value(item["Content-Length"]),
			int64Value(item["content_length"]),
		)

		metadata := map[string]string{}
		for key, value := range item {
			if value == nil || strings.EqualFold(key, "X-TIKA:content") || strings.EqualFold(key, "content") {
				continue
			}
			if text := strings.TrimSpace(stringValue(value)); text != "" {
				metadata[key] = text
			}
		}

		res = append(res, tikaRMetaAttachment{
			Type:     "embedded",
			Name:     name,
			Path:     firstNonEmpty(path, name),
			MimeType: mimeType,
			Size:     size,
			Content:  content,
			Metadata: metadata,
		})
	}

	return res
}

func addArchiveEntriesToAttachments(
	raw []byte,
	prefix string,
	storageDir string,
	defaultType string,
	upsert func(key string, update func(*searcher.SearchAttachmentDocument)),
) {
	if len(raw) == 0 {
		return
	}

	entries, ok := readArchiveEntries(raw)
	if !ok {
		return
	}

	for _, entry := range entries {
		relativeName, ok := normalizeFTSSidecarRelativePath(entry.Name)
		if !ok {
			continue
		}
		entryName := entry.Name
		key := path.Join(prefix, storageDir, relativeName)
		upsert(key, func(doc *searcher.SearchAttachmentDocument) {
			if doc.Type == "" || doc.Type == "embedded" {
				doc.Type = defaultType
			}
			doc.Name = firstNonEmpty(doc.Name, filepath.Base(relativeName))
			doc.Path = firstNonEmpty(doc.Path, key)
			doc.MimeType = firstNonEmpty(doc.MimeType, mime.TypeByExtension(filepath.Ext(relativeName)))
			if entry.Data != nil {
				doc.Size = int64(len(entry.Data))
			}
			if doc.Metadata == nil {
				doc.Metadata = map[string]string{}
			}
			if _, ok := doc.Metadata["archive_entry"]; !ok {
				doc.Metadata["archive_entry"] = entryName
			}
		})
	}
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}

	cloned := make(map[string]string, len(src))
	for key, value := range src {
		cloned[key] = value
	}

	return cloned
}

func buildSearchVersion(entity *ent.Entity, fileName string, policy *ent.StoragePolicy) *searcher.SearchFileVersionDocument {
	if entity == nil {
		return nil
	}

	doc := &searcher.SearchFileVersionDocument{
		ID:              fmt.Sprintf("%d", entity.ID),
		EntityID:        entity.ID,
		EntityType:      entityTypeString(types.EntityType(entity.Type)),
		EntityTypeValue: entity.Type,
		Source:          entity.Source,
		Size:            entity.Size,
		CreatedAt:       entity.CreatedAt,
		UpdatedAt:       entity.UpdatedAt,
		StoragePolicyID: entity.StoragePolicyEntities,
		ReferenceCount:  entity.ReferenceCount,
		Encrypted:       entity.Props != nil && entity.Props.EncryptMetadata != nil,
		Props:           mapFromEntityProps(entity.Props),
		MimeType:        mime.TypeByExtension(filepath.Ext(fileName)),
	}

	if policy != nil {
		doc.StorageType = policy.Type
		doc.StorageName = policy.Name
		doc.Bucket = policy.BucketName
		if doc.StoragePolicyID == 0 {
			doc.StoragePolicyID = policy.ID
		}
	}

	return doc
}

func joinMetadata(metadata map[string]string) string {
	if len(metadata) == 0 {
		return ""
	}

	keys := lo.Keys(metadata)
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+": "+metadata[key])
	}

	return strings.Join(parts, "\n")
}

func mapFromFileProps(props *types.FileProps) map[string]any {
	if props == nil {
		return nil
	}

	return mapFromAny(props)
}

func mapFromEntityProps(props *types.EntityProps) map[string]any {
	if props == nil {
		return nil
	}

	result := map[string]any{
		"unlink_only": props.UnlinkOnly,
	}
	if props.EncryptMetadata != nil {
		result["encrypted"] = true
		result["encrypt_algorithm"] = string(props.EncryptMetadata.Algorithm)
	}

	return result
}

func mapFromAny(value any) map[string]any {
	if value == nil {
		return nil
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}

	result := make(map[string]any)
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}

	return result
}

func fileTypeString(fileType types.FileType) string {
	switch fileType {
	case types.FileTypeFolder:
		return "folder"
	default:
		return "file"
	}
}

func entityTypeString(entityType types.EntityType) string {
	switch entityType {
	case types.EntityTypeThumbnail:
		return "thumbnail"
	case types.EntityTypeLivePhoto:
		return "live_photo"
	default:
		return "version"
	}
}

func attachmentName(fileName string, entityType types.EntityType) string {
	switch entityType {
	case types.EntityTypeThumbnail:
		return fileName + "_thumbnail"
	case types.EntityTypeLivePhoto:
		return fileName + "_live_photo.mov"
	default:
		return fileName
	}
}

func excerpt(content string, limit int) string {
	if limit <= 0 || content == "" {
		return ""
	}

	runes := []rune(content)
	if len(runes) <= limit {
		return content
	}

	return string(runes[:limit]) + "..."
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}

	return ""
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}

	return 0
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []string:
		return strings.Join(typed, ", ")
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(stringValue(item)); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ", ")
	case json.Number:
		return typed.String()
	case fmt.Stringer:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int16:
		return strconv.FormatInt(int64(typed), 10)
	case int8:
		return strconv.FormatInt(int64(typed), 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint16:
		return strconv.FormatUint(uint64(typed), 10)
	case uint8:
		return strconv.FormatUint(uint64(typed), 10)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

func int64Value(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int16:
		return int64(typed)
	case int8:
		return int64(typed)
	case uint64:
		return int64(typed)
	case uint:
		return int64(typed)
	case uint32:
		return int64(typed)
	case uint16:
		return int64(typed)
	case uint8:
		return int64(typed)
	case float64:
		return int64(typed)
	case float32:
		return int64(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed
		}
	case string:
		if parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); err == nil {
			return parsed
		}
	}

	return 0
}
