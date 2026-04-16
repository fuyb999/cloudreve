package local

import (
	"path/filepath"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/pkg/util"
)

func TestResolveLocalStoragePathKeepsRegularRelativePath(t *testing.T) {
	util.UseWorkingDir = true
	t.Cleanup(func() {
		util.UseWorkingDir = false
	})

	got := resolveLocalStoragePath("uploads/demo.txt")
	want := filepath.Clean("uploads/demo.txt")
	if got != want {
		t.Fatalf("unexpected regular path: got %q want %q", got, want)
	}
}

func TestResolveLocalStoragePathMovesReservedCloudrevePrefixIntoDataDir(t *testing.T) {
	util.UseWorkingDir = true
	t.Cleanup(func() {
		util.UseWorkingDir = false
	})

	got := resolveLocalStoragePath("cloudreve/fts-sidecar/1/2/3/content.txt")
	want := filepath.Join("data", "cloudreve", "fts-sidecar", "1", "2", "3", "content.txt")
	if got != want {
		t.Fatalf("unexpected internal storage path: got %q want %q", got, want)
	}
}
