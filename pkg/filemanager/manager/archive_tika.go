package manager

import (
	"context"
	"fmt"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/pkg/request"
	tikaextractor "github.com/cloudreve/Cloudreve/v4/pkg/searcher/extractor"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

// BuildArchiveTikaExtractor resolves a Tika client that can be reused by
// archive preview/extraction flows only when Tika archive support is explicitly enabled.
func BuildArchiveTikaExtractor(ctx context.Context, dep dependency.Dep) (*tikaextractor.TikaExtractor, error) {
	if dep == nil || dep.SettingProvider() == nil {
		return nil, fmt.Errorf("tika extractor not configured")
	}

	cfg := dep.SettingProvider().FTSTikaExtractor(ctx)
	if !archiveTikaFallbackEnabled(cfg) {
		return nil, fmt.Errorf("tika extractor not configured")
	}

	return tikaextractor.NewTikaExtractor(archiveRequestClient(dep), dep.SettingProvider(), dep.Logger(), cfg), nil
}

func archiveTikaFallbackEnabled(cfg *setting.FTSTikaExtractorSetting) bool {
	if cfg == nil {
		return false
	}

	return cfg.ArchiveEnabled && cfg.Endpoint != ""
}

func archiveRequestClient(dep dependency.Dep) (client request.Client) {
	client = request.GeneralClient
	defer func() {
		if recover() != nil || client == nil {
			client = request.GeneralClient
		}
	}()

	if dep == nil {
		return client
	}

	if c := dep.RequestClient(); c != nil {
		client = c
	}

	return client
}
