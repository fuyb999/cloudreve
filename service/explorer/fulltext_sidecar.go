package explorer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/auth"
	"github.com/cloudreve/Cloudreve/v4/pkg/cluster/routes"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/gin-gonic/gin"
)

type (
	FullTextSidecarParameterCtx              struct{}
	FullTextSidecarContentParameterCtx       struct{}
	FullTextSidecarObjectContentParameterCtx struct{}

	FullTextSidecarService struct {
		Uri string `form:"uri" binding:"required"`
	}

	FullTextSidecarContentService struct {
		Uri      string `form:"uri" binding:"required"`
		Name     string `form:"name" binding:"required"`
		Download bool   `form:"download"`
	}

	FullTextSidecarObjectContentService struct {
		Token string `uri:"token" binding:"required"`
	}

	FullTextSidecarResponse struct {
		Version     int                             `json:"version"`
		FileID      int                             `json:"file_id"`
		EntityID    int                             `json:"entity_id"`
		SourcePath  string                          `json:"source_path"`
		ExtractedAt time.Time                       `json:"extracted_at"`
		Objects     []FullTextSidecarObjectResponse `json:"objects"`
	}

	FullTextSidecarObjectResponse struct {
		ID         string `json:"id"`
		ParentID   string `json:"parent_id,omitempty"`
		Depth      int    `json:"depth,omitempty"`
		Kind       string `json:"kind,omitempty"`
		Name       string `json:"name"`
		URI        string `json:"uri"`
		PreviewURI string `json:"preview_uri,omitempty"`
		Path       string `json:"path"`
		MimeType   string `json:"mime_type"`
		Size       int64  `json:"size"`
		URL        string `json:"url"`
		PreviewURL string `json:"preview_url,omitempty"`
	}

	fullTextSidecarObjectAccess struct {
		FileID    int    `json:"file_id"`
		ObjectID  string `json:"object_id"`
		ParentURI string `json:"parent_uri,omitempty"`
	}
)

const fullTextSidecarVirtualFS = "sidecar"

func (s *FullTextSidecarService) Get(c *gin.Context) (*FullTextSidecarResponse, error) {
	dep := dependency.FromContext(c)
	fm := manager.NewFileManager(dep, inventory.UserFromContext(c))
	defer fm.Recycle()

	uri, err := fs.NewUriFromString(s.Uri)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeParamErr, "unknown uri", err)
	}

	manifest, err := fm.GetFTSSidecar(c, uri)
	if err != nil {
		return nil, err
	}

	return BuildFullTextSidecarResponse(dep, c, s.Uri, manifest), nil
}

func (s *FullTextSidecarContentService) Serve(c *gin.Context) error {
	uri, err := fs.NewUriFromString(s.Uri)
	if err != nil {
		return serializer.NewError(serializer.CodeParamErr, "unknown uri", err)
	}

	return serveFullTextSidecarContent(c, nil, uri, s.Name, s.Download, false)
}

func (s *FullTextSidecarObjectContentService) Serve(c *gin.Context) error {
	access, err := parseFullTextSidecarObjectAccessToken(s.Token)
	if err != nil {
		return err
	}

	owner, parentURI, objectID, err := resolveFullTextSidecarFileURI(c, access)
	if err != nil {
		return err
	}

	return serveFullTextSidecarContent(c, owner, parentURI, objectID, c.Query(routes.IsDownloadQuery) != "", true)
}

func serveFullTextSidecarContent(
	c *gin.Context,
	user *ent.User,
	uri *fs.URI,
	name string,
	download bool,
	bypassOwnerCheck bool,
) error {
	dep := dependency.FromContext(c)
	if user == nil {
		user = inventory.UserFromContext(c)
	}

	fm := manager.NewFileManager(dep, user)
	defer fm.Recycle()

	ctx := context.Context(c)
	if bypassOwnerCheck {
		ctx = dbfs.WithBypassOwnerCheck(ctx)
	}

	expire := time.Now().Add(dep.SettingProvider().EntityUrlValidDuration(c))
	content, err := fm.GetFTSSidecarContent(ctx, uri, name, download, &expire)
	if err != nil {
		return err
	}

	if content.RedirectURL != "" {
		if content.Expires != nil {
			cacheTTL := int(time.Until(*content.Expires).Seconds())
			if cacheTTL < 0 {
				cacheTTL = 0
			}
			c.Header("Cache-Control", fmt.Sprintf("private, max-age=%d", cacheTTL))
		}
		c.Redirect(http.StatusFound, content.RedirectURL)
		return nil
	}

	if content.Content == nil {
		return serializer.NewError(serializer.CodeNotFound, "Full text sidecar object not found", nil)
	}
	defer content.Content.Close()

	dispositionType := "inline"
	if download {
		dispositionType = "attachment"
	}
	c.Header("Content-Disposition", mime.FormatMediaType(dispositionType, map[string]string{
		"filename": content.Artifact.Name,
	}))
	if content.Artifact.MimeType != "" {
		c.Header("Content-Type", content.Artifact.MimeType)
	}
	if content.Artifact.Size > 0 {
		c.Header("Content-Length", strconv.FormatInt(content.Artifact.Size, 10))
	}

	if seeker, ok := content.Content.(io.ReadSeeker); ok {
		http.ServeContent(c.Writer, c.Request, content.Artifact.Name, time.Time{}, seeker)
		return nil
	}

	if _, err := io.Copy(c.Writer, content.Content); err != nil {
		return serializer.NewError(serializer.CodeIOFailed, "Failed to stream full text sidecar object", err)
	}

	return nil
}

func BuildFullTextSidecarResponse(dep dependency.Dep, c *gin.Context, uri string, manifest *manager.FTSSidecarManifest) *FullTextSidecarResponse {
	return buildFullTextSidecarResponse(uri, manifest, func(objectURI string) string {
		parentURI, objectID, _, err := parseFullTextSidecarObjectURI(objectURI)
		if err != nil || parentURI == nil {
			return ""
		}

		return buildSignedFullTextSidecarObjectURL(
			dep,
			c,
			fullTextSidecarObjectAccess{
				FileID:    manifest.FileID,
				ObjectID:  objectID,
				ParentURI: parentURI.String(),
			},
			false,
			false,
		)
	})
}

func buildFullTextSidecarResponse(
	uri string,
	manifest *manager.FTSSidecarManifest,
	urlBuilder func(objectURI string) string,
) *FullTextSidecarResponse {
	if manifest == nil {
		return nil
	}

	textArtifacts := map[string]manager.FTSSidecarArtifact{}
	for _, item := range manifest.Objects {
		if item.Kind != "attachment_text" {
			continue
		}
		logicalID := managerSidecarAttachmentLogicalID(item.ID)
		if logicalID == "" {
			continue
		}
		textArtifacts[logicalID] = item
	}

	objects := make([]FullTextSidecarObjectResponse, 0, len(manifest.Objects))
	for _, item := range manifest.Objects {
		if !shouldExposeFullTextSidecarObject(manifest, item) {
			continue
		}

		objectURI := buildFullTextSidecarObjectURI(uri, item.ID)
		object := FullTextSidecarObjectResponse{
			ID:       item.ID,
			ParentID: item.ParentID,
			Depth:    item.Depth,
			Kind:     item.Kind,
			Name:     item.Name,
			URI:      objectURI,
			Path:     item.Path,
			MimeType: item.MimeType,
			Size:     item.Size,
			URL:      urlBuilder(objectURI),
		}

		previewArtifact := item
		if textArtifact, ok := textArtifacts[item.ID]; ok {
			previewArtifact = textArtifact
		}
		if previewArtifact.ID != item.ID {
			object.PreviewURI = buildFullTextSidecarObjectURI(uri, previewArtifact.ID)
			object.PreviewURL = urlBuilder(buildFullTextSidecarObjectURI(uri, previewArtifact.ID))
		}

		objects = append(objects, object)
	}

	return &FullTextSidecarResponse{
		Version:     manifest.Version,
		FileID:      manifest.FileID,
		EntityID:    manifest.EntityID,
		SourcePath:  manifest.SourcePath,
		ExtractedAt: manifest.ExtractedAt,
		Objects:     objects,
	}
}

func shouldExposeFullTextSidecarObject(manifest *manager.FTSSidecarManifest, item manager.FTSSidecarArtifact) bool {
	if shouldHideLegacyArchiveRootContent(manifest, item) {
		return false
	}

	switch strings.TrimSpace(item.Kind) {
	case "attachment_text", "metadata", "diagnostics", "external_attachments", "ocr_candidates":
		return false
	}

	switch strings.TrimSpace(item.ID) {
	case "", "manifest.json", "rmeta.json":
		return false
	}

	return true
}

func shouldHideLegacyArchiveRootContent(manifest *manager.FTSSidecarManifest, item manager.FTSSidecarArtifact) bool {
	if strings.TrimSpace(item.ID) != "content.txt" {
		return false
	}
	if manifest == nil || !strings.EqualFold(strings.TrimSpace(manifest.Provider), "tika") {
		return false
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(strings.TrimSpace(manifest.SourcePath)), "."))
	switch ext {
	case "zip", "tar", "tgz", "tbz", "tbz2", "txz", "tlz", "7z", "rar", "ar",
		"gz", "z", "bz", "bz2", "xz", "lzma", "lz4", "br", "snappy", "sz",
		"pack200", "cpio", "arj", "dump", "jar", "war", "ear":
		return true
	default:
		return false
	}
}

func managerSidecarAttachmentLogicalID(objectID string) string {
	objectID = normalizeFullTextSidecarObjectID(objectID)
	if !strings.HasPrefix(objectID, "attachment-text/") || !strings.HasSuffix(objectID, ".txt") {
		return ""
	}

	logicalID := strings.TrimPrefix(objectID, "attachment-text/")
	logicalID = strings.TrimSuffix(logicalID, ".txt")
	logicalID = normalizeFullTextSidecarObjectID(logicalID)
	return logicalID
}

func buildFullTextSidecarObjectURI(parentURI, objectID string) string {
	normalizedID := normalizeFullTextSidecarObjectID(objectID)
	virtualURI := &url.URL{
		Scheme: constants.CloudreveScheme,
		User:   url.User(base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(parentURI)))),
		Host:   fullTextSidecarVirtualFS,
		Path:   "/" + normalizedID,
	}

	return virtualURI.String()
}

func parseFullTextSidecarObjectURI(raw string) (*fs.URI, string, bool, error) {
	uri, err := fs.NewUriFromString(raw)
	if err != nil {
		return nil, "", false, err
	}
	if strings.ToLower(uri.U.Host) != fullTextSidecarVirtualFS {
		return nil, "", false, nil
	}
	if uri.U.User == nil || strings.TrimSpace(uri.U.User.Username()) == "" {
		return nil, "", true, serializer.NewError(serializer.CodeParamErr, "missing full text sidecar parent uri", nil)
	}

	parentRaw, err := base64.RawURLEncoding.DecodeString(uri.U.User.Username())
	if err != nil {
		return nil, "", true, serializer.NewError(serializer.CodeParamErr, "invalid full text sidecar parent uri", err)
	}

	parentURI, err := fs.NewUriFromString(string(parentRaw))
	if err != nil {
		return nil, "", true, serializer.NewError(serializer.CodeParamErr, "invalid full text sidecar parent uri", err)
	}

	objectID := normalizeFullTextSidecarObjectID(uri.PathTrimmed())
	if objectID == "" {
		return nil, "", true, serializer.NewError(serializer.CodeParamErr, "missing full text sidecar object id", nil)
	}

	return parentURI, objectID, true, nil
}

func normalizeFullTextSidecarObjectID(objectID string) string {
	cleaned := path.Clean("/" + strings.TrimSpace(objectID))
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." {
		return ""
	}

	return cleaned
}

func getFullTextSidecarObject(
	c *gin.Context,
	raw string,
) (*fs.URI, *manager.FTSSidecarManifest, manager.FTSSidecarArtifact, bool, error) {
	parentURI, objectID, isSidecarURI, err := parseFullTextSidecarObjectURI(raw)
	if err != nil || !isSidecarURI {
		return nil, nil, manager.FTSSidecarArtifact{}, isSidecarURI, err
	}

	dep := dependency.FromContext(c)
	fm := manager.NewFileManager(dep, inventory.UserFromContext(c))
	defer fm.Recycle()

	manifest, err := fm.GetFTSSidecar(c, parentURI)
	if err != nil {
		return nil, nil, manager.FTSSidecarArtifact{}, true, err
	}

	artifact, ok := manifest.ObjectByID(objectID)
	if !ok {
		return nil, nil, manager.FTSSidecarArtifact{}, true, serializer.NewError(serializer.CodeNotFound, "Full text sidecar object not found", nil)
	}

	return parentURI, manifest, artifact, true, nil
}

func resolveFullTextSidecarFileURI(c *gin.Context, access *fullTextSidecarObjectAccess) (*ent.User, *fs.URI, string, error) {
	dep := dependency.FromContext(c)
	if access == nil || access.FileID <= 0 {
		return nil, nil, "", serializer.NewError(serializer.CodeParamErr, "invalid full text sidecar token", nil)
	}

	fileModel, err := dep.FileClient().GetByID(c, access.FileID)
	if err != nil {
		return nil, nil, "", serializer.NewError(serializer.CodeNotFound, "full text sidecar file not found", err)
	}

	loadCtx := context.WithValue(context.Context(c), inventory.LoadUserGroup{}, true)
	owner, err := dep.UserClient().GetByID(loadCtx, fileModel.OwnerID)
	if err != nil {
		return nil, nil, "", serializer.NewError(serializer.CodeNotFound, "full text sidecar owner not found", err)
	}

	fm := manager.NewFileManager(dep, owner)
	defer fm.Recycle()

	ownerFile, err := fm.TraverseFile(c, access.FileID)
	if err != nil {
		return nil, nil, "", serializer.NewError(serializer.CodeNotFound, "full text sidecar file not found", err)
	}
	if ownerFile == nil || ownerFile.IsNil() {
		return nil, nil, "", serializer.NewError(serializer.CodeNotFound, "full text sidecar file not found", nil)
	}

	ownerURI := ownerFile.Uri(true)
	if ownerURI == nil {
		return nil, nil, "", serializer.NewError(serializer.CodeNotFound, "full text sidecar file not found", nil)
	}

	parentURI, err := resolveFullTextSidecarParentURI(access.ParentURI)
	if err != nil {
		return nil, nil, "", err
	}
	if parentURI != nil && parentURI.FileSystem() == constants.FileSystemPublic {
		if _, err := fm.Get(dbfs.WithBypassOwnerCheck(c), parentURI, dbfs.WithNotRoot()); err != nil {
			parentURI = nil
		}
	}
	if parentURI == nil {
		parentURI = ownerURI
	}
	if parentURI == nil {
		return nil, nil, "", serializer.NewError(serializer.CodeNotFound, "full text sidecar file not found", nil)
	}

	return owner, parentURI, normalizeFullTextSidecarObjectID(access.ObjectID), nil
}

func resolveFullTextSidecarParentURI(raw string) (*fs.URI, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	uri, err := fs.NewUriFromString(raw)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeParamErr, "invalid full text sidecar parent uri", err)
	}

	return uri, nil
}

func resolvePublicFTSSidecarURI(c *gin.Context, dep dependency.Dep, fileModel *ent.File) *fs.URI {
	if dep == nil || fileModel == nil || dep.FileClient() == nil || dep.SettingClient() == nil {
		return nil
	}

	publicService := publicshare.NewService(dep.Logger(), dep.FileClient(), dep.SettingClient(), dep.HashIDEncoder())
	visibility, err := publicService.ResolveVisibility(c, inventory.UserFromContext(c))
	if err != nil {
		return nil
	}

	resolved, err := publicService.ResolveVisibleURI(c, fileModel, visibility)
	if err != nil {
		return nil
	}

	return resolved
}

func buildFullTextSidecarObjectAccessToken(access fullTextSidecarObjectAccess) string {
	normalized := fullTextSidecarObjectAccess{
		FileID:    access.FileID,
		ObjectID:  normalizeFullTextSidecarObjectID(access.ObjectID),
		ParentURI: strings.TrimSpace(access.ParentURI),
	}
	raw, _ := json.Marshal(normalized)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func parseFullTextSidecarObjectAccessToken(token string) (*fullTextSidecarObjectAccess, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return nil, serializer.NewError(serializer.CodeParamErr, "invalid full text sidecar token", err)
	}

	var access fullTextSidecarObjectAccess
	if err := json.Unmarshal(raw, &access); err != nil {
		return nil, serializer.NewError(serializer.CodeParamErr, "invalid full text sidecar token", err)
	}
	access.ObjectID = normalizeFullTextSidecarObjectID(access.ObjectID)
	access.ParentURI = strings.TrimSpace(access.ParentURI)
	if access.FileID <= 0 || access.ObjectID == "" {
		return nil, serializer.NewError(serializer.CodeParamErr, "invalid full text sidecar token", nil)
	}

	return &access, nil
}

func buildSignedFullTextSidecarObjectURL(
	dep dependency.Dep,
	c *gin.Context,
	access fullTextSidecarObjectAccess,
	download bool,
	usePrimarySiteURL bool,
) string {
	baseCtx := context.Context(c)
	if usePrimarySiteURL {
		baseCtx = setting.UseFirstSiteUrl(baseCtx)
	}

	base := dep.SettingProvider().SiteURL(baseCtx)
	expire := time.Now().Add(dep.SettingProvider().EntityUrlValidDuration(c))
	raw := routes.MasterFTSSidecarObjectContentUrl(base, buildFullTextSidecarObjectAccessToken(access), download).String()
	signed, err := auth.SignURI(c, dep.GeneralAuth(), raw, &expire)
	if err != nil {
		return raw
	}

	return signed.String()
}

func resolveFullTextSidecarObjectURL(
	c *gin.Context,
	raw string,
	download bool,
	usePrimarySiteURL bool,
) (*manager.EntityUrl, *time.Time, bool, error) {
	parentURI, manifest, artifact, isSidecarURI, err := getFullTextSidecarObject(c, raw)
	if err != nil || !isSidecarURI {
		return nil, nil, isSidecarURI, err
	}

	dep := dependency.FromContext(c)
	expire := time.Now().Add(dep.SettingProvider().EntityUrlValidDuration(c))
	return &manager.EntityUrl{
		Url: buildSignedFullTextSidecarObjectURL(dep, c, fullTextSidecarObjectAccess{
			FileID:    manifest.FileID,
			ObjectID:  artifact.ID,
			ParentURI: parentURI.String(),
		}, download, usePrimarySiteURL),
	}, &expire, true, nil
}

func buildFullTextSidecarFileResponse(raw string, manifest *manager.FTSSidecarManifest, artifact manager.FTSSidecarArtifact) *FileResponse {
	if manifest == nil {
		return nil
	}

	return &FileResponse{
		Type:      int(types.FileTypeFile),
		ID:        fmt.Sprintf("fts-sidecar:%d:%d:%s", manifest.FileID, manifest.EntityID, artifact.ID),
		Name:      artifact.Name,
		CreatedAt: manifest.ExtractedAt,
		UpdatedAt: manifest.ExtractedAt,
		Size:      artifact.Size,
		Path:      raw,
	}
}
