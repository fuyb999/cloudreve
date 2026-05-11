package manager

import (
	"context"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
)

func withPublicBypass(ctx context.Context, uris ...*fs.URI) context.Context {
	resolved := ctx
	needsHiddenPublicAccess := false
	for _, uri := range uris {
		if uri == nil {
			continue
		}
		switch uri.FileSystem() {
		case constants.FileSystemPublic:
			resolved = dbfs.WithBypassOwnerCheck(resolved)
			needsHiddenPublicAccess = true
		case constants.FileSystemMy:
			elements := uri.Elements()
			if len(elements) > 0 && elements[0] == publicshare.DefaultRootName {
				needsHiddenPublicAccess = true
			}
		}
	}

	if needsHiddenPublicAccess {
		resolved = dbfs.WithHiddenPublicRootAccess(resolved)
	}

	return resolved
}

func publicVisibilityPayload(ctx context.Context) string {
	return publicshare.EncodeVisibilityOverride(publicshare.VisibilityOverrideFromContext(ctx))
}

func withUploadSessionPublicVisibility(ctx context.Context, session *fs.UploadSession) context.Context {
	if session == nil || session.PublicVisibility == "" {
		return ctx
	}

	visibility, err := publicshare.DecodeVisibilityOverride(session.PublicVisibility)
	if err != nil || visibility == nil {
		return ctx
	}

	return context.WithValue(ctx, publicshare.VisibilityOverrideCtx{}, visibility)
}
