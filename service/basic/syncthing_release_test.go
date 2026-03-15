package basic

import (
	"testing"

	settingpkg "github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

func TestBuildSyncthingReleases(t *testing.T) {
	t.Parallel()

	t.Run("builds both platform assets from configured version", func(t *testing.T) {
		t.Parallel()

		releases := BuildSyncthingReleases(&settingpkg.AppSetting{
			SyncthingUpgradeVersion: "1.30.0",
			SyncthingLinuxURL:       "https://downloads.example/linux",
			SyncthingWindowsURL:     "https://downloads.example/windows",
		})

		if len(releases) != 1 {
			t.Fatalf("expected 1 release, got %d: %#v", len(releases), releases)
		}
		if releases[0].Tag != "v1.30.0" {
			t.Fatalf("unexpected tag: %#v", releases[0])
		}
		if len(releases[0].Assets) != 2 {
			t.Fatalf("expected 2 assets, got %#v", releases[0].Assets)
		}
		if got := releases[0].Assets[0].Name; got != "syncthing-linux-amd64-v1.30.0.tar.gz" {
			t.Fatalf("unexpected linux asset name: %q", got)
		}
		if got := releases[0].Assets[1].Name; got != "syncthing-windows-amd64-v1.30.0.zip" {
			t.Fatalf("unexpected windows asset name: %q", got)
		}
	})

	t.Run("marks prerelease tag", func(t *testing.T) {
		t.Parallel()

		releases := BuildSyncthingReleases(&settingpkg.AppSetting{
			SyncthingUpgradeVersion: "v1.30.0-beta.1",
			SyncthingLinuxURL:       "https://downloads.example/linux",
			SyncthingWindowsURL:     "https://downloads.example/windows",
		})

		if len(releases) != 1 {
			t.Fatalf("expected 1 release, got %d: %#v", len(releases), releases)
		}
		if !releases[0].Prerelease {
			t.Fatalf("expected prerelease flag set: %#v", releases[0])
		}
	})

	t.Run("returns configured platforms only", func(t *testing.T) {
		t.Parallel()

		releases := BuildSyncthingReleases(&settingpkg.AppSetting{
			SyncthingUpgradeVersion: "v1.30.0",
			SyncthingLinuxURL:       "https://downloads.example/linux",
		})

		if len(releases) != 1 {
			t.Fatalf("expected 1 release, got %d: %#v", len(releases), releases)
		}
		if len(releases[0].Assets) != 1 {
			t.Fatalf("expected 1 asset, got %#v", releases[0].Assets)
		}
		if got := releases[0].Assets[0].Name; got != "syncthing-linux-amd64-v1.30.0.tar.gz" {
			t.Fatalf("unexpected linux asset name: %q", got)
		}
	})

	t.Run("returns empty when version is invalid", func(t *testing.T) {
		t.Parallel()

		releases := BuildSyncthingReleases(&settingpkg.AppSetting{
			SyncthingUpgradeVersion: "latest",
			SyncthingLinuxURL:       "https://downloads.example/linux",
			SyncthingWindowsURL:     "https://downloads.example/windows",
		})

		if len(releases) != 0 {
			t.Fatalf("expected 0 releases, got %#v", releases)
		}
	})
}
