package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
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

	uri := traversed.Uri(true)
	if uri == nil {
		return nil, nil, fmt.Errorf("failed to resolve file uri")
	}

	var primaryEntity *ent.Entity
	for _, entity := range fileModel.Edges.Entities {
		if entity.ID == fileModel.PrimaryEntity {
			primaryEntity = entity
			break
		}
	}

	content, _ := extractFTSContent(ctx, m.dep.TextExtractor(ctx), ownerManager, fileModel, primaryEntity)
	metadata := lo.Associate(fileModel.Edges.Metadata, func(item *ent.Metadata) (string, string) {
		return item.Name, item.Value
	})

	filePolicy, _ := m.storagePolicyFromID(ctx, fileModel.StoragePolicyFiles)
	latestVersion := buildSearchVersion(primaryEntity, fileModel.Name, filePolicy)
	versions, attachments := buildSearchEntities(fileModel, uri, filePolicy)

	pathDoc := searcher.SearchPathDocument{
		Path:      uri.String(),
		IsPrimary: true,
		Size:      fileModel.Size,
		FileType:  fileTypeString(types.FileType(fileModel.Type)),
		EntityID:  fileModel.PrimaryEntity,
	}
	if latestVersion != nil {
		pathDoc.Bucket = latestVersion.Bucket
		pathDoc.VersionID = latestVersion.EntityID
	}

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
		PathText:        uri.String(),
		Content:         content,
		ContentExcerpt:  excerpt(content, 320),
		LatestVersion:   latestVersion,
		Versions:        versions,
		Paths:           []searcher.SearchPathDocument{pathDoc},
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

	return doc, uri, nil
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

func extractFTSContent(ctx context.Context, extractor searcher.TextExtractor, ownerManager FileManager, fileModel *ent.File, primaryEntity *ent.Entity) (string, error) {
	if primaryEntity == nil {
		return "", nil
	}

	if !ShouldExtractText(extractor, fileModel.Name, fileModel.Size) {
		return "", nil
	}

	source, err := ownerManager.GetEntitySource(ctx, primaryEntity.ID)
	if err != nil {
		return "", err
	}
	defer source.Close()

	text, err := extractor.Extract(ctx, source)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(text), nil
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
