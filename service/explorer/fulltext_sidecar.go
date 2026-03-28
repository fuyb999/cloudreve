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
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
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
		Objects     []FullTextSidecarObjectResponse `json:"objects,omitempty"`
	}

	FullTextSidecarObjectResponse struct {
		ID       string `json:"id"`
		ParentID string `json:"parent_id,omitempty"`
		Depth    int    `json:"depth,omitempty"`
		Kind     string `json:"kind,omitempty"`
		Name     string `json:"name"`
		URI      string `json:"uri"`
		Path     string `json:"path"`
		MimeType string `json:"mime_type"`
		Size     int64  `json:"size"`
		URL      string `json:"url"`
	}

	fullTextSidecarObjectAccess struct {
		FileID   int    `json:"file_id"`
		ObjectID string `json:"object_id"`
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

	owner, parentURI, objectID, err := resolveFullTextSidecarFileURI(c, access.FileID, access.ObjectID)
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
				FileID:   manifest.FileID,
				ObjectID: objectID,
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
	return &FullTextSidecarResponse{
		Version:     manifest.Version,
		FileID:      manifest.FileID,
		EntityID:    manifest.EntityID,
		SourcePath:  manifest.SourcePath,
		ExtractedAt: manifest.ExtractedAt,
		Objects: lo.Map(manifest.Objects, func(item manager.FTSSidecarArtifact, _ int) FullTextSidecarObjectResponse {
			return FullTextSidecarObjectResponse{
				ID:       item.ID,
				ParentID: item.ParentID,
				Depth:    item.Depth,
				Kind:     item.Kind,
				Name:     item.Name,
				URI:      buildFullTextSidecarObjectURI(uri, item.ID),
				Path:     item.Path,
				MimeType: item.MimeType,
				Size:     item.Size,
				URL:      urlBuilder(buildFullTextSidecarObjectURI(uri, item.ID)),
			}
		}),
	}
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

func resolveFullTextSidecarFileURI(c *gin.Context, fileID int, objectID string) (*ent.User, *fs.URI, string, error) {
	dep := dependency.FromContext(c)

	fileModel, err := dep.FileClient().GetByID(c, fileID)
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

	file, err := fm.TraverseFile(c, fileID)
	if err != nil {
		return nil, nil, "", serializer.NewError(serializer.CodeNotFound, "full text sidecar file not found", err)
	}

	return owner, file.Uri(true), normalizeFullTextSidecarObjectID(objectID), nil
}

func buildFullTextSidecarObjectAccessToken(access fullTextSidecarObjectAccess) string {
	normalized := fullTextSidecarObjectAccess{
		FileID:   access.FileID,
		ObjectID: normalizeFullTextSidecarObjectID(access.ObjectID),
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
	_, manifest, artifact, isSidecarURI, err := getFullTextSidecarObject(c, raw)
	if err != nil || !isSidecarURI {
		return nil, nil, isSidecarURI, err
	}

	dep := dependency.FromContext(c)
	expire := time.Now().Add(dep.SettingProvider().EntityUrlValidDuration(c))
	return &manager.EntityUrl{
		Url: buildSignedFullTextSidecarObjectURL(dep, c, fullTextSidecarObjectAccess{
			FileID:   manifest.FileID,
			ObjectID: artifact.ID,
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
