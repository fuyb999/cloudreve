package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/inventory/types"
)

func TestNewDownloaderValidateOptions(t *testing.T) {
	t.Run("nil_options", func(t *testing.T) {
		_, err := NewDownloader(context.Background(), nil, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "downloader options not configured") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("aria2_missing_settings", func(t *testing.T) {
		_, err := NewDownloader(context.Background(), nil, nil, &types.NodeSetting{
			Provider: types.DownloaderProviderAria2,
		})
		if err == nil || !strings.Contains(err.Error(), "aria2 settings not configured") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("qbittorrent_missing_settings", func(t *testing.T) {
		_, err := NewDownloader(context.Background(), nil, nil, &types.NodeSetting{
			Provider: types.DownloaderProviderQBittorrent,
		})
		if err == nil || !strings.Contains(err.Error(), "qbittorrent settings not configured") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
