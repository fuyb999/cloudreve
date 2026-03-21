package explorer

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/cluster/routes"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

type (
	FullTextSidecarParameterCtx        struct{}
	FullTextSidecarContentParameterCtx struct{}

	FullTextSidecarService struct {
		Uri string `form:"uri" binding:"required"`
	}

	FullTextSidecarContentService struct {
		Uri      string `form:"uri" binding:"required"`
		Name     string `form:"name" binding:"required"`
		Download bool   `form:"download"`
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
		Path     string `json:"path"`
		MimeType string `json:"mime_type"`
		Size     int64  `json:"size"`
		URL      string `json:"url"`
	}
)

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
	dep := dependency.FromContext(c)
	fm := manager.NewFileManager(dep, inventory.UserFromContext(c))
	defer fm.Recycle()

	uri, err := fs.NewUriFromString(s.Uri)
	if err != nil {
		return serializer.NewError(serializer.CodeParamErr, "unknown uri", err)
	}

	expire := time.Now().Add(dep.SettingProvider().EntityUrlValidDuration(c))
	content, err := fm.GetFTSSidecarContent(c, uri, s.Name, s.Download, &expire)
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
	if s.Download {
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
	base := dep.SettingProvider().SiteURL(c)
	return buildFullTextSidecarResponse(base, uri, manifest)
}

func buildFullTextSidecarResponse(base *url.URL, uri string, manifest *manager.FTSSidecarManifest) *FullTextSidecarResponse {
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
				Path:     item.Path,
				MimeType: item.MimeType,
				Size:     item.Size,
				URL:      routes.MasterFTSSidecarContentUrl(base, uri, item.ID, false).String(),
			}
		}),
	}
}
