package basic

import (
	"regexp"
	"strings"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	settingpkg "github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/gin-gonic/gin"
)

type (
	GetSyncthingReleasesService struct{}

	SyncthingRelease struct {
		Tag        string                  `json:"tag_name"`
		Prerelease bool                    `json:"prerelease"`
		Assets     []SyncthingReleaseAsset `json:"assets"`
	}

	SyncthingReleaseAsset struct {
		URL        string `json:"url"`
		Name       string `json:"name"`
		BrowserURL string `json:"browser_download_url,omitempty"`
	}
)

var syncthingReleaseVersionPattern = regexp.MustCompile(`(?i)^v?\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)

func (service *GetSyncthingReleasesService) Get(c *gin.Context) []SyncthingRelease {
	appSetting := dependency.FromContext(c).SettingProvider().AppSetting(c)
	return BuildSyncthingReleases(appSetting)
}

func BuildSyncthingReleases(appSetting *settingpkg.AppSetting) []SyncthingRelease {
	if appSetting == nil {
		return []SyncthingRelease{}
	}

	tag, ok := normalizeSyncthingReleaseTag(appSetting.SyncthingUpgradeVersion)
	if !ok {
		return []SyncthingRelease{}
	}

	assets := make([]SyncthingReleaseAsset, 0, 2)
	if asset, ok := buildSyncthingReleaseAsset(appSetting.SyncthingLinuxURL, tag, "linux"); ok {
		assets = append(assets, asset)
	}
	if asset, ok := buildSyncthingReleaseAsset(appSetting.SyncthingWindowsURL, tag, "windows"); ok {
		assets = append(assets, asset)
	}

	if len(assets) == 0 {
		return []SyncthingRelease{}
	}

	return []SyncthingRelease{{
		Tag:        tag,
		Prerelease: strings.Contains(tag, "-"),
		Assets:     assets,
	}}
}

func normalizeSyncthingReleaseTag(raw string) (string, bool) {
	version := strings.TrimSpace(raw)
	if version == "" || !syncthingReleaseVersionPattern.MatchString(version) {
		return "", false
	}

	if strings.HasPrefix(strings.ToLower(version), "v") {
		return "v" + version[1:], true
	}

	return "v" + version, true
}

func buildSyncthingReleaseAsset(rawURL, tag, platform string) (SyncthingReleaseAsset, bool) {
	downloadURL := strings.TrimSpace(rawURL)
	if downloadURL == "" {
		return SyncthingReleaseAsset{}, false
	}

	var assetName string
	switch platform {
	case "linux":
		assetName = "syncthing-linux-amd64-" + tag + ".tar.gz"
	case "windows":
		assetName = "syncthing-windows-amd64-" + tag + ".zip"
	default:
		return SyncthingReleaseAsset{}, false
	}

	return SyncthingReleaseAsset{
		URL:        downloadURL,
		Name:       assetName,
		BrowserURL: downloadURL,
	}, true
}
