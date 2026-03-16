package manager

import (
	"context"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
)

func withPublicBypass(ctx context.Context, uris ...*fs.URI) context.Context {
	for _, uri := range uris {
		if uri != nil && uri.FileSystem() == constants.FileSystemPublic {
			return dbfs.WithBypassOwnerCheck(ctx)
		}
	}

	return ctx
}
