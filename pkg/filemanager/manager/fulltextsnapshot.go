package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"mime"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
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

const ftsSnapshotVersion = 3

const (
	ftsMetadataTagPrefix        = "tag:"
	ftsMetadataCustomPropPrefix = "props:"
)

const maxFTSExtractionSnapshotAttempts = 3

var tikaMarkupTagPattern = regexp.MustCompile(`(?s)<[^>]+>`)

type FTSBuildOptions struct {
	SkipTextExtraction        bool `json:"skip_text_extraction,omitempty"`
	SkipAttachmentExtraction  bool `json:"skip_attachment_extraction,omitempty"`
	ForceTextExtraction       bool `json:"-"`
	ForceAttachmentExtraction bool `json:"-"`
}

type ftsDocumentSnapshot struct {
	fileModel        *ent.File
	ownerManager     FileManager
	ownerURI         *fs.URI
	publicURI        *fs.URI
	sidecarURI       *fs.URI
	primaryEntity    *ent.Entity
	primaryFTSEntity fs.Entity
}

func (s *ftsDocumentSnapshot) recycle() {
	if s == nil || s.ownerManager == nil {
		return
	}

	s.ownerManager.Recycle()
	s.ownerManager = nil
}

func BuildFTSFileDocument(ctx context.Context, dep dependency.Dep, user *ent.User, fileID int) (*searcher.SearchFileDocument, *fs.URI, error) {
	return BuildFTSFileDocumentWithOptions(ctx, dep, user, fileID, FTSBuildOptions{})
}

func BuildFTSFileDocumentWithOptions(
	ctx context.Context,
	dep dependency.Dep,
	user *ent.User,
	fileID int,
	opts FTSBuildOptions,
) (*searcher.SearchFileDocument, *fs.URI, error) {
	fm := NewFileManager(dep, user)
	defer fm.Recycle()

	internal, ok := fm.(*manager)
	if !ok {
		return nil, nil, fmt.Errorf("failed to construct file manager")
	}

	return internal.buildFTSFileDocumentWithOptions(ctx, fileID, opts)
}

func (m *manager) buildFTSFileDocument(ctx context.Context, fileID int) (*searcher.SearchFileDocument, *fs.URI, error) {
	return m.buildFTSFileDocumentWithOptions(ctx, fileID, FTSBuildOptions{})
}

func (m *manager) buildFTSFileDocumentWithOptions(
	ctx context.Context,
	fileID int,
	opts FTSBuildOptions,
) (*searcher.SearchFileDocument, *fs.URI, error) {
	snapshot, content, embeddedAttachments, err := m.extractFTSContentWithFreshSnapshot(ctx, fileID, opts)
	if err != nil {
		return nil, nil, err
	}
	defer snapshot.recycle()

	fileModel := snapshot.fileModel
	ownerURI := snapshot.ownerURI
	publicURI := snapshot.publicURI
	primaryEntity := snapshot.primaryEntity
	metadata := lo.Associate(fileModel.Edges.Metadata, func(item *ent.Metadata) (string, string) {
		return item.Name, item.Value
	})

	filePolicy, _ := m.storagePolicyFromID(ctx, fileModel.StoragePolicyFiles)
	latestVersion := buildSearchVersion(primaryEntity, fileModel.Name, filePolicy)
	if latestVersion == nil && fileModel.Type == int(types.FileTypeFile) {
		latestVersion = buildFallbackSearchVersion(fileModel, filePolicy)
	}
	attachments := buildSearchAttachments(fileModel, ownerURI, filePolicy)
	pathText := buildFTSSearchPathText(ownerURI, publicURI)
	searchURIs := buildFTSSearchURIs(ownerURI, publicURI)
	searchPaths := buildFTSSearchPaths(ownerURI, publicURI)
	attachments = append(attachments, embeddedAttachments...)

	doc := &searcher.SearchFileDocument{
		ID:              fmt.Sprintf("%d", fileModel.ID),
		FileID:          fileModel.ID,
		OwnerID:         fileModel.OwnerID,
		EntityID:        fileModel.PrimaryEntity,
		ParentID:        fileModel.FileChildren,
		FileName:        fileModel.Name,
		FileExt:         firstNonEmpty(fileModel.FileExt, util.Ext(fileModel.Name)),
		FileType:        fileModel.Type,
		Size:            fileModel.Size,
		CreatedAt:       util.NewDateTimeSecond(fileModel.CreatedAt),
		UpdatedAt:       util.NewDateTimeSecond(fileModel.UpdatedAt),
		IsSymbolic:      fileModel.IsSymbolic,
		Shared:          len(fileModel.Edges.Shares) > 0,
		TreePath:        fileModel.TreePath,
		OwnerURI:        uriString(ownerURI),
		PublicURI:       uriString(publicURI),
		SearchURIs:      searchURIs,
		SearchPaths:     searchPaths,
		StoragePolicyID: fileModel.StoragePolicyFiles,
		Metadata:        metadata,
		MetadataKeys:    metadataKeys(metadata),
		Tags:            metadataTags(metadata),
		CustomProps:     metadataCustomProps(metadata),
		MetadataText:    joinMetadata(metadata),
		Props:           mapFromFileProps(fileModel.Props),
		PathText:        pathText,
		Content:         content,
		LatestVersion:   latestVersion,
		Attachments:     attachments,
		SnapshotVersion: ftsSnapshotVersion,
		SynchronizedAt:  util.NewDateTimeSecond(time.Now()),
	}

	if filePolicy != nil {
		doc.StorageType = filePolicy.Type
		doc.StorageBucket = filePolicy.BucketName
	}
	if latestVersion != nil {
		doc.StoragePolicyID = latestVersion.StoragePolicyID
		doc.StorageType = latestVersion.StorageType
		doc.StorageBucket = latestVersion.Bucket
	}

	return doc, ownerURI, nil
}

func (m *manager) extractFTSContentWithFreshSnapshot(
	ctx context.Context,
	fileID int,
	opts FTSBuildOptions,
) (*ftsDocumentSnapshot, string, []searcher.SearchAttachmentDocument, error) {
	var lastSnapshot *ftsDocumentSnapshot
	for attempt := 0; attempt < maxFTSExtractionSnapshotAttempts; attempt++ {
		if lastSnapshot != nil {
			lastSnapshot.recycle()
		}

		snapshot, err := m.loadFTSDocumentSnapshot(ctx, fileID)
		if err != nil {
			return nil, "", nil, err
		}
		lastSnapshot = snapshot

		content, embeddedAttachments, extractErr := extractFTSContent(
			ctx,
			m.dep.TextExtractor(ctx),
			snapshot.ownerManager,
			snapshot.fileModel,
			snapshot.primaryFTSEntity,
			snapshot.sidecarURI,
			opts,
		)
		if extractErr != nil {
			m.l.Warning("Failed to extract FTS content for file %d name=%q: %s", snapshot.fileModel.ID, snapshot.fileModel.Name, extractErr)
		}

		refreshed, err := m.loadFTSDocumentSnapshot(ctx, fileID)
		if err != nil {
			snapshot.recycle()
			return nil, "", nil, err
		}

		if sameFTSExtractionInput(snapshot.fileModel, refreshed.fileModel) {
			snapshot.recycle()
			return refreshed, content, embeddedAttachments, nil
		}

		refreshed.recycle()
	}

	if lastSnapshot != nil {
		lastSnapshot.recycle()
	}
	return nil, "", nil, fmt.Errorf("file %d changed during full text extraction; retry later", fileID)
}

func (m *manager) loadFTSDocumentSnapshot(ctx context.Context, fileID int) (*ftsDocumentSnapshot, error) {
	fileModel, err := m.loadFTSFileModel(ctx, fileID)
	if err != nil {
		return nil, fmt.Errorf("failed to load file model: %w", err)
	}

	ownerManager, err := m.fileManagerForOwner(ctx, fileModel.OwnerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load file owner context: %w", err)
	}

	actualOwnerURI, err := m.resolveOwnerFTSActualURI(ctx, fileModel, ownerManager)
	if err != nil {
		ownerManager.Recycle()
		return nil, fmt.Errorf("failed to resolve owner file uri: %w", err)
	}

	ownerURI := actualOwnerURI
	if displayOwnerURI, displayErr := m.resolveOwnerFTSURI(ctx, fileModel, ownerManager); displayErr == nil && displayOwnerURI != nil {
		ownerURI = displayOwnerURI
	}

	publicURI := m.resolvePublicSearchURI(ctx, fileModel)
	sidecarURI := ownerURI
	if publicURI != nil {
		sidecarURI = publicURI
	}

	primaryEntity := findPrimaryFTSEntity(fileModel)
	var primaryFTSEntity fs.Entity
	if primaryEntity != nil {
		primaryFTSEntity = fs.NewEntity(primaryEntity)
	}

	return &ftsDocumentSnapshot{
		fileModel:        fileModel,
		ownerManager:     ownerManager,
		ownerURI:         ownerURI,
		publicURI:        publicURI,
		sidecarURI:       sidecarURI,
		primaryEntity:    primaryEntity,
		primaryFTSEntity: primaryFTSEntity,
	}, nil
}

func sameFTSExtractionInput(a, b *ent.File) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.ID != b.ID ||
		a.OwnerID != b.OwnerID ||
		a.PrimaryEntity != b.PrimaryEntity ||
		a.Type != b.Type ||
		a.Size != b.Size ||
		a.StoragePolicyFiles != b.StoragePolicyFiles {
		return false
	}

	return sameFTSEntityForExtraction(findPrimaryFTSEntity(a), findPrimaryFTSEntity(b))
}

func sameFTSEntityForExtraction(a, b *ent.Entity) bool {
	if a == nil || b == nil {
		return a == b
	}

	return a.ID == b.ID &&
		a.Type == b.Type &&
		a.Source == b.Source &&
		a.Size == b.Size &&
		a.StoragePolicyEntities == b.StoragePolicyEntities &&
		a.UpdatedAt.Equal(b.UpdatedAt)
}

func (m *manager) resolveOwnerFTSURI(ctx context.Context, fileModel *ent.File, ownerManager FileManager) (*fs.URI, error) {
	ownerURI, err := m.resolveOwnerFTSActualURI(ctx, fileModel, ownerManager)
	if err != nil {
		return nil, err
	}

	if publicURI := m.resolvePublicSearchURI(ctx, fileModel); publicURI != nil && len(publicURI.Elements()) > 0 {
		ownerElems := ownerURI.Elements()
		publicElems := publicURI.Elements()
		if len(publicElems) <= len(ownerElems) {
			tail := ownerElems[len(ownerElems)-len(publicElems):]
			if strings.EqualFold(strings.Join(tail, "/"), strings.Join(publicElems, "/")) {
				relative := ownerElems[:len(ownerElems)-len(publicElems)]
				root := ownerURI.Root()
				for _, elem := range relative {
					root = root.Join(elem)
				}
				ownerURI = root.Join(publicElems...)
			}
		}
	}

	return ownerURI, nil
}

func (m *manager) resolveOwnerFTSActualURI(ctx context.Context, fileModel *ent.File, ownerManager FileManager) (*fs.URI, error) {
	if fileModel == nil {
		return nil, fmt.Errorf("file model is nil")
	}

	if ownerManager == nil {
		var err error
		ownerManager, err = m.fileManagerForOwner(ctx, fileModel.OwnerID)
		if err != nil {
			return nil, err
		}
		defer ownerManager.Recycle()
	}

	traversed, err := ownerManager.TraverseFile(ctx, fileModel.ID)
	if err != nil {
		return nil, err
	}

	if traversed == nil || traversed.IsNil() {
		return nil, fmt.Errorf("failed to resolve file uri")
	}

	ownerURI := traversed.Uri(true)
	if ownerURI == nil {
		return nil, fmt.Errorf("failed to resolve file uri")
	}

	ownerURI = m.rebaseHiddenPublicOwnerURI(ctx, fileModel, ownerURI)
	return ownerURI, nil
}

func (m *manager) rebaseHiddenPublicOwnerURI(ctx context.Context, fileModel *ent.File, ownerURI *fs.URI) *fs.URI {
	if m == nil || fileModel == nil || ownerURI == nil || m.dep == nil || m.dep.FileClient() == nil || m.dep.SettingClient() == nil {
		return ownerURI
	}

	publicService := publicshare.NewService(m.l, m.dep.FileClient(), m.dep.SettingClient(), m.hasher)
	rootID, err := publicService.RootID(ctx)
	if err != nil || rootID == 0 {
		return ownerURI
	}

	var hiddenRoot *ent.File
	if fileModel.ID == rootID && fileModel.Name == inventory.RootFolderName {
		hiddenRoot = fileModel
	} else {
		ancestors, err := m.dep.FileClient().GetAncestorFiles(ctx, fileModel)
		if err != nil {
			return ownerURI
		}

		for _, ancestor := range ancestors {
			if ancestor != nil && ancestor.ID == rootID && ancestor.Name == inventory.RootFolderName {
				hiddenRoot = ancestor
				break
			}
		}
	}

	if hiddenRoot == nil {
		return ownerURI
	}

	elements := ownerURI.Elements()
	if len(elements) > 0 && elements[0] == publicshare.DefaultRootName {
		return ownerURI
	}

	rebased := ownerURI.Root().Join(publicshare.DefaultRootName)
	if len(elements) > 0 {
		rebased = rebased.Join(elements...)
	}

	return rebased
}

func (m *manager) resolvePublicSearchURI(ctx context.Context, fileModel *ent.File) *fs.URI {
	if m == nil || fileModel == nil || m.dep == nil {
		return nil
	}
	if m.dep.FileClient() == nil || m.dep.SettingClient() == nil || m.hasher == nil {
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

func buildFTSSearchPathText(ownerURI *fs.URI, publicURI *fs.URI) string {
	paths := make([]string, 0, 2)
	seen := map[string]struct{}{}
	appendPath := func(uri *fs.URI) {
		raw := strings.TrimSpace(searchableURIText(uri))
		if raw == "" {
			return
		}
		if _, ok := seen[raw]; ok {
			return
		}

		seen[raw] = struct{}{}
		paths = append(paths, raw)
	}

	appendPath(publicURI)
	appendPath(ownerURI)

	return strings.Join(paths, "\n")
}

func buildFTSSearchURIs(ownerURI *fs.URI, publicURI *fs.URI) []string {
	uris := make([]string, 0, 2)
	seen := map[string]struct{}{}
	appendURI := func(uri *fs.URI) {
		raw := uriString(uri)
		if raw == "" {
			return
		}
		if _, ok := seen[raw]; ok {
			return
		}

		seen[raw] = struct{}{}
		uris = append(uris, raw)
	}

	appendURI(publicURI)
	appendURI(ownerURI)
	return uris
}

func buildFTSSearchPaths(ownerURI *fs.URI, publicURI *fs.URI) []string {
	paths := make([]string, 0, 8)
	seen := map[string]struct{}{}
	appendPath := func(uri *fs.URI) {
		for _, raw := range uriScopePaths(uri) {
			if _, ok := seen[raw]; ok {
				continue
			}

			seen[raw] = struct{}{}
			paths = append(paths, raw)
		}
	}

	appendPath(publicURI)
	appendPath(ownerURI)
	return paths
}

func uriScopePaths(uri *fs.URI) []string {
	if uri == nil || uri.U == nil {
		return nil
	}

	base := uri.Root()
	elements := uri.Elements()
	paths := make([]string, 0, len(elements)+1)
	paths = append(paths, uriString(base))
	current := base
	for _, elem := range elements {
		current = current.Join(elem)
		if raw := uriString(current); raw != "" {
			paths = append(paths, raw)
		}
	}

	return paths
}

func uriString(uri *fs.URI) string {
	if uri == nil || uri.U == nil {
		return ""
	}

	raw := uri.SetQuery("").String()
	return strings.TrimSuffix(raw, "/")
}

func searchableURIText(uri *fs.URI) string {
	if uri == nil || uri.U == nil {
		return ""
	}

	pathText := uri.Path()
	if pathText == "." {
		pathText = ""
	}

	scheme := strings.TrimSpace(uri.U.Scheme)
	host := strings.TrimSpace(uri.U.Host)
	userInfo := ""
	if uri.U.User != nil {
		userInfo = uri.U.User.String()
	}

	authority := host
	if userInfo != "" {
		authority = userInfo + "@" + authority
	}

	if authority != "" {
		return strings.TrimSpace(fmt.Sprintf("%s://%s%s", scheme, authority, pathText))
	}
	if scheme != "" {
		return strings.TrimSpace(fmt.Sprintf("%s://%s", scheme, strings.TrimPrefix(pathText, "/")))
	}

	return strings.TrimSpace(pathText)
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

	loadCtx := context.WithValue(ctx, inventory.LoadUserGroup{}, true)
	owner, err := m.dep.UserClient().GetByID(loadCtx, ownerID)
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
	sidecarURI *fs.URI,
	opts FTSBuildOptions,
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
			reuseTextEnabled     bool
			reuseAssetsEnabled   bool
			persistTextEnabled   bool
			persistAssetsEnabled bool
		}{}
	)
	if loaded, ok := ownerManager.(*manager); ok {
		internal = loaded
		cfg := internal.settings.FTSTikaExtractor(ctx)
		sidecarContent, sidecarAttachments, sidecarManifest, hasCurrentSidecar = internal.loadFTSContentFromSidecar(ctx, fileModel, primaryEntity, sidecarURI)
		sidecarCfg.reuseTextEnabled = cfg.SidecarTextEnabled
		sidecarCfg.reuseAssetsEnabled = cfg.SidecarAssetsEnabled
		sidecarCfg.persistTextEnabled = cfg.SidecarTextEnabled
		sidecarCfg.persistAssetsEnabled = cfg.SidecarAssetsEnabled
		if sidecarManifest != nil && sidecarManifest.Provider == ftsSidecarProviderExternal {
			sidecarCfg.reuseTextEnabled = true
			sidecarCfg.reuseAssetsEnabled = true
		}
	}

	plan := buildFTSExtractionPlan(
		opts,
		extractor,
		fileModel,
		sidecarContent,
		sidecarAttachments,
		hasCurrentSidecar,
		sidecarManifest,
		sidecarCfg.reuseTextEnabled,
		sidecarCfg.reuseAssetsEnabled,
		sidecarCfg.persistTextEnabled,
		sidecarCfg.persistAssetsEnabled,
	)

	text := ""
	if opts.SkipTextExtraction || plan.ReuseSidecarText {
		text = strings.TrimSpace(sidecarContent)
	}

	var attachments []searcher.SearchAttachmentDocument
	if opts.SkipAttachmentExtraction || plan.ReuseSidecarAttachments {
		attachments = sidecarAttachments
	}

	if !plan.NeedTextExtraction && !plan.NeedAttachmentExtraction && !plan.ShouldPersistSidecar {
		return text, attachments, nil
	}

	source, err := ownerManager.GetEntitySource(ctx, primaryEntity.ID())
	if err != nil {
		return "", nil, err
	}
	defer source.Close()

	if plan.NeedTextExtraction {
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

	if plan.NeedAttachmentExtraction {
		attachments = extractFTSEmbeddedAttachments(ctx, extractor, ownerManager, fileModel, primaryEntity, sidecarURI, source)
	}
	if plan.ShouldPersistSidecar {
		persistFTSSidecars(ctx, extractor, ownerManager, fileModel, sidecarURI, primaryEntity, source, text)
		if internal != nil && (sidecarCfg.persistTextEnabled || sidecarCfg.persistAssetsEnabled) {
			if refreshedText, refreshedAttachments, _, ok := internal.loadFTSContentFromSidecar(ctx, fileModel, primaryEntity, sidecarURI); ok {
				if shouldRetryFTSSidecarReloadForPublicURI(sidecarURI, refreshedAttachments) {
					if ownerURI, err := internal.resolveOwnerFTSURI(ctx, fileModel, ownerManager); err == nil && ownerURI != nil {
						if fallbackText, fallbackAttachments, _, fallbackOK := internal.loadFTSContentFromSidecar(ctx, fileModel, primaryEntity, ownerURI); fallbackOK {
							if sidecarCfg.persistTextEnabled && strings.TrimSpace(fallbackText) != "" {
								refreshedText = fallbackText
							}
							if len(fallbackAttachments) > 0 {
								refreshedAttachments = fallbackAttachments
							}
						}
					}
				}
				if sidecarCfg.persistTextEnabled && text == "" {
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

type ftsExtractionPlan struct {
	ReuseSidecarText         bool
	ReuseSidecarAttachments  bool
	NeedTextExtraction       bool
	NeedAttachmentExtraction bool
	ShouldPersistSidecar     bool
}

func buildFTSExtractionPlan(
	opts FTSBuildOptions,
	extractor searcher.TextExtractor,
	fileModel *ent.File,
	currentText string,
	currentAttachments []searcher.SearchAttachmentDocument,
	hasCurrentSidecar bool,
	sidecarManifest *FTSSidecarManifest,
	reuseTextSidecarEnabled bool,
	reuseAssetSidecarEnabled bool,
	persistTextSidecarEnabled bool,
	persistAssetSidecarEnabled bool,
) ftsExtractionPlan {
	if hasCurrentSidecar && sidecarManifest != nil && sidecarManifest.Provider == ftsSidecarProviderExternal &&
		!opts.ForceTextExtraction && !opts.ForceAttachmentExtraction {
		return ftsExtractionPlan{
			ReuseSidecarText:        true,
			ReuseSidecarAttachments: true,
		}
	}

	plan := ftsExtractionPlan{
		ReuseSidecarText:        reuseTextSidecarEnabled && !opts.ForceTextExtraction,
		ReuseSidecarAttachments: reuseAssetSidecarEnabled && !opts.ForceAttachmentExtraction,
	}

	textReady := hasCurrentSidecar && sidecarManifest != nil && sidecarManifest.TextReady
	assetsReady := hasCurrentSidecar && sidecarManifest != nil && sidecarManifest.AssetsReady
	textExtractable := ShouldExtractText(extractor, fileModel.Name, fileModel.Size)
	attachmentsSupported := supportsFTSAttachmentExtraction(extractor, fileModel)

	plan.NeedTextExtraction = !opts.SkipTextExtraction &&
		(!plan.ReuseSidecarText || strings.TrimSpace(currentText) == "") &&
		textExtractable

	plan.NeedAttachmentExtraction = attachmentsSupported &&
		!opts.SkipAttachmentExtraction &&
		(opts.ForceAttachmentExtraction ||
			!plan.ReuseSidecarAttachments ||
			(len(currentAttachments) == 0 && !assetsReady))

	plan.ShouldPersistSidecar = (persistTextSidecarEnabled || persistAssetSidecarEnabled) &&
		(textExtractable || attachmentsSupported) &&
		!opts.SkipTextExtraction && !opts.SkipAttachmentExtraction &&
		(!hasCurrentSidecar ||
			(persistTextSidecarEnabled && (opts.ForceTextExtraction || !textReady)) ||
			(persistAssetSidecarEnabled && (opts.ForceAttachmentExtraction || !assetsReady)))

	return plan
}

func shouldRetryFTSSidecarReloadForPublicURI(
	sidecarURI *fs.URI,
	attachments []searcher.SearchAttachmentDocument,
) bool {
	if sidecarURI == nil || sidecarURI.FileSystem() != constants.FileSystemPublic {
		return false
	}
	if len(attachments) == 0 {
		return true
	}

	for _, attachment := range attachments {
		if !isFTSSidecarAttachmentTextPath(attachment.Source) && strings.TrimSpace(attachment.Content) == "" {
			return true
		}
	}

	return false
}

func supportsFTSAttachmentExtraction(extractor searcher.TextExtractor, fileModel *ent.File) bool {
	if extractor == nil || fileModel == nil {
		return false
	}
	if _, ok := extractor.(*tikaextractor.TikaExtractor); !ok {
		return false
	}

	return ShouldExtractText(extractor, fileModel.Name, fileModel.Size)
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
		content string
	)

	if raw, ok := m.readFTSSidecarObject(ctx, handler, manifest, "content.txt"); ok {
		content = strings.TrimSpace(string(raw))
	}

	return content, m.hydrateFTSSidecarAttachmentContents(ctx, handler, buildEmbeddedSearchAttachmentsFromManifest(fileModel, primaryEntity, manifest, nil)), manifest, true
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

	raw, err := readFTSSidecarBytes(ctx, requestClientForSidecar(m), handler, object.Path)
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

func buildSearchAttachments(fileModel *ent.File, uri *fs.URI, fallbackPolicy *ent.StoragePolicy) []searcher.SearchAttachmentDocument {
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
			continue
		}

		attachments = append(attachments, searcher.SearchAttachmentDocument{
			ID:        versionDoc.ID,
			ParentID:  attachmentRootParentID(fileModel.ID),
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

	return attachments
}

func attachmentRootParentID(fileID int) string {
	if fileID <= 0 {
		return ""
	}

	return strconv.Itoa(fileID)
}

func embeddedAttachmentDocID(fileID int, objectName string) string {
	return fmt.Sprintf("%d:embedded:%s", fileID, objectName)
}

func embeddedAttachmentParentID(fileID int, objectParentID string) string {
	objectParentID = strings.TrimSpace(objectParentID)
	if objectParentID == "" {
		return attachmentRootParentID(fileID)
	}

	return embeddedAttachmentDocID(fileID, objectParentID)
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
				ID:       embeddedAttachmentDocID(fileModel.ID, key),
				ParentID: attachmentRootParentID(fileModel.ID),
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
			relativeName := preferredTikaAttachmentRelativeName(item.Name, item.Path)
			if normalized, ok := normalizeFTSSidecarRelativePath(relativeName); ok {
				relativeName = normalized
			} else {
				relativeName = fmt.Sprintf("embedded_%d", index)
			}
			if isTikaSyntheticAttachmentArtifact(relativeName) {
				continue
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
		relativeName := preferredTikaAttachmentRelativeName(item.Name, item.Path)
		relativeName, ok := normalizeFTSSidecarRelativePath(relativeName)
		if !ok {
			continue
		}
		if isTikaSyntheticAttachmentArtifact(relativeName) {
			continue
		}

		key := path.Join(ftsSidecarEmbeddedDir, relativeName)
		if _, exists := rmetaByName[key]; !exists {
			rmetaByName[key] = item
		}
	}

	textArtifacts := map[string]FTSSidecarArtifact{}
	legacyTextArtifacts := map[string][]FTSSidecarArtifact{}
	for _, item := range manifest.Objects {
		if item.Kind != "attachment_text" {
			continue
		}
		textArtifacts[item.ID] = item
		if logicalID := sidecarAttachmentLogicalIDFromTextObjectID(item.ID); logicalID != "" {
			legacyTextArtifacts[path.Base(logicalID)] = append(legacyTextArtifacts[path.Base(logicalID)], item)
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
		if strings.HasPrefix(objectName, ftsSidecarEmbeddedDir+"/") && isTikaSyntheticAttachmentArtifact(objectName) {
			continue
		}
		if object.Kind == "attachment_text" || isLegacyFTSSidecarAuxiliaryKind(object.Kind) {
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
			ID:        embeddedAttachmentDocID(fileModel.ID, objectName),
			ParentID:  embeddedAttachmentParentID(fileModel.ID, object.ParentID),
			Depth:     object.Depth,
			EntityID:  primaryEntity.ID(),
			Type:      docType,
			Name:      path.Base(objectName),
			Path:      object.Path,
			Size:      object.Size,
			MimeType:  object.MimeType,
			Source:    object.Path,
			CreatedAt: util.NewDateTimeSecond(primaryEntity.CreatedAt()),
			UpdatedAt: util.NewDateTimeSecond(primaryEntity.UpdatedAt()),
			Metadata:  cloneStringMap(object.Metadata),
		}
		if textArtifact, ok := textArtifacts[sidecarAttachmentTextObjectID(objectName)]; ok {
			doc.Source = textArtifact.Path
		} else if candidates := legacyTextArtifacts[path.Base(objectName)]; len(candidates) == 1 {
			doc.Source = candidates[0].Path
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
			if !isFTSSidecarAttachmentTextPath(doc.Source) {
				doc.Content = firstNonEmpty(doc.Content, item.Content)
			}
			if len(item.Metadata) > 0 {
				if doc.Metadata == nil {
					doc.Metadata = map[string]string{}
				}
				for mk, mv := range item.Metadata {
					doc.Metadata[mk] = mv
				}
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

func parseTikaRMetaRootContent(raw []byte) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}

	var payload []map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || len(payload) == 0 {
		return ""
	}

	for index, item := range payload {
		if item == nil {
			continue
		}

		embeddedPath := firstNonEmpty(
			stringValue(item["X-TIKA:embedded_resource_path"]),
			stringValue(item["embedded_resource_path"]),
			stringValue(item["embeddedResourcePath"]),
		)
		if index > 0 && strings.TrimSpace(embeddedPath) != "" {
			continue
		}

		content := strings.TrimSpace(firstNonEmpty(
			stringValue(item["X-TIKA:content"]),
			stringValue(item["content"]),
		))
		if content == "" {
			continue
		}

		mimeType := firstNonEmpty(
			stringValue(item["Content-Type"]),
			stringValue(item["dc:format"]),
		)
		return sanitizeTikaAttachmentContent(content, mimeType)
	}

	return ""
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
		content = sanitizeTikaAttachmentContent(content, mimeType)

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

func preferredTikaAttachmentRelativeName(name string, pathValue string) string {
	type candidate struct {
		raw        string
		normalized string
		depth      int
	}

	normalize := func(input string) candidate {
		raw := strings.TrimSpace(strings.ReplaceAll(input, "\\", "/"))
		normalized := strings.TrimPrefix(raw, "./")
		normalized = strings.TrimPrefix(normalized, "/")
		normalized = path.Clean(normalized)
		if normalized == "." {
			normalized = ""
		}

		depth := 0
		if normalized != "" {
			depth = len(strings.Split(normalized, "/"))
		}

		return candidate{
			raw:        raw,
			normalized: normalized,
			depth:      depth,
		}
	}

	nameCandidate := normalize(name)
	pathCandidate := normalize(pathValue)
	if nameCandidate.normalized == "" {
		return pathCandidate.raw
	}
	if pathCandidate.normalized == "" {
		return nameCandidate.raw
	}
	if nameCandidate.depth > pathCandidate.depth {
		return nameCandidate.raw
	}
	if pathCandidate.depth > nameCandidate.depth {
		return pathCandidate.raw
	}
	if len(nameCandidate.normalized) >= len(pathCandidate.normalized) {
		return nameCandidate.raw
	}
	return pathCandidate.raw
}

func sanitizeTikaAttachmentContent(content string, mimeType string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}

	if !looksLikeTikaMarkupContent(content) {
		return content
	}

	plain := html.UnescapeString(tikaMarkupTagPattern.ReplaceAllString(content, " "))
	plain = strings.Join(strings.Fields(plain), " ")

	if plain == "" {
		return ""
	}

	return plain
}

func looksLikeTikaMarkupContent(content string) bool {
	content = strings.TrimSpace(strings.ToLower(content))
	return strings.HasPrefix(content, "<html") ||
		strings.HasPrefix(content, "<?xml") ||
		strings.HasPrefix(content, "<body") ||
		strings.HasPrefix(content, "<div") ||
		strings.HasPrefix(content, "<p") ||
		strings.HasPrefix(content, "<span") ||
		strings.HasPrefix(content, "<meta") ||
		strings.HasPrefix(content, "<head")
}

func isTikaSyntheticAttachmentArtifact(name string) bool {
	base := strings.TrimSpace(path.Base(name))
	return strings.EqualFold(base, "__TEXT__") || strings.EqualFold(base, "__METADATA__")
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
		if storageDir == ftsSidecarEmbeddedDir && isTikaSyntheticAttachmentArtifact(relativeName) {
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
		CreatedAt:       util.NewDateTimeSecond(entity.CreatedAt),
		UpdatedAt:       util.NewDateTimeSecond(entity.UpdatedAt),
		StoragePolicyID: entity.StoragePolicyEntities,
		ReferenceCount:  entity.ReferenceCount,
		Encrypted:       entity.Props != nil && entity.Props.EncryptMetadata != nil,
		Props:           mapFromEntityProps(entity.Props),
		MimeType:        mime.TypeByExtension(filepath.Ext(fileName)),
	}

	if policy != nil {
		doc.StorageType = policy.Type
		doc.Bucket = policy.BucketName
		if doc.StoragePolicyID == 0 {
			doc.StoragePolicyID = policy.ID
		}
	}

	return doc
}

func buildFallbackSearchVersion(fileModel *ent.File, policy *ent.StoragePolicy) *searcher.SearchFileVersionDocument {
	if fileModel == nil {
		return nil
	}

	doc := &searcher.SearchFileVersionDocument{
		ID:              fmt.Sprintf("file:%d", fileModel.ID),
		EntityType:      entityTypeString(types.EntityTypeVersion),
		EntityTypeValue: int(types.EntityTypeVersion),
		Size:            fileModel.Size,
		CreatedAt:       util.NewDateTimeSecond(fileModel.CreatedAt),
		UpdatedAt:       util.NewDateTimeSecond(fileModel.UpdatedAt),
		StoragePolicyID: fileModel.StoragePolicyFiles,
		MimeType:        mime.TypeByExtension(filepath.Ext(fileModel.Name)),
	}

	if policy != nil {
		doc.StorageType = policy.Type
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

func metadataKeys(metadata map[string]string) []string {
	if len(metadata) == 0 {
		return nil
	}

	keys := lo.Keys(metadata)
	sort.Strings(keys)
	return keys
}

func metadataTags(metadata map[string]string) []string {
	if len(metadata) == 0 {
		return nil
	}

	tags := make([]string, 0)
	for key := range metadata {
		if tag, ok := strings.CutPrefix(key, ftsMetadataTagPrefix); ok && strings.TrimSpace(tag) != "" {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	return tags
}

func metadataCustomProps(metadata map[string]string) map[string]any {
	if len(metadata) == 0 {
		return nil
	}

	props := make(map[string]any)
	for key, value := range metadata {
		if propID, ok := strings.CutPrefix(key, ftsMetadataCustomPropPrefix); ok && strings.TrimSpace(propID) != "" {
			props[propID] = value
		}
	}
	if len(props) == 0 {
		return nil
	}

	return props
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
