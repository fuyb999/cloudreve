package manager

import (
	"context"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
)

func withPublicBypass(ctx context.Context, uris ...*fs.URI) context.Context {
	for _, uri := range uris {
		if uri != nil && uri.FileSystem() == constants.FileSystemPublic {
			return dbfs.WithBypassOwnerCheck(ctx)
		}
	}

	return ctx
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
