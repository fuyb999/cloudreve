package local

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/inventory/types"
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

func TestResolveExistingLocalStoragePathFallsBackToLegacyProjectDir(t *testing.T) {
	util.UseWorkingDir = true
	t.Cleanup(func() {
		util.UseWorkingDir = false
	})

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}

	legacyPath := filepath.Join("cloudreve", "data", "uploads", "legacy.txt")
	if err := os.MkdirAll(filepath.Join(cwd, filepath.Dir(legacyPath)), 0o755); err != nil {
		t.Fatalf("failed to create legacy dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, legacyPath), []byte("legacy"), 0o644); err != nil {
		t.Fatalf("failed to write legacy file: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(cwd, legacyPath))
	})

	got := resolveExistingLocalStoragePath("data/uploads/legacy.txt")
	if got != legacyPath {
		t.Fatalf("unexpected legacy path resolution: got %q want %q", got, legacyPath)
	}
}

func TestResolveExistingLocalStoragePathPrefersPrimaryPath(t *testing.T) {
	util.UseWorkingDir = true
	t.Cleanup(func() {
		util.UseWorkingDir = false
	})

	primaryPath := filepath.Clean("data/uploads/current.txt")
	if err := os.MkdirAll(filepath.Dir(primaryPath), 0o755); err != nil {
		t.Fatalf("failed to create primary dir: %v", err)
	}
	if err := os.WriteFile(primaryPath, []byte("current"), 0o644); err != nil {
		t.Fatalf("failed to write primary file: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(primaryPath)
	})

	got := resolveExistingLocalStoragePath("data/uploads/current.txt")
	if got != primaryPath {
		t.Fatalf("unexpected primary path resolution: got %q want %q", got, primaryPath)
	}
}

func TestNewLocalFileEntitySupportsLegacyProjectDir(t *testing.T) {
	util.UseWorkingDir = true
	t.Cleanup(func() {
		util.UseWorkingDir = false
	})

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}

	legacyPath := filepath.Join(cwd, "cloudreve", "data", "uploads", "legacy-entity.txt")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("failed to create legacy dir: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte("entity"), 0o644); err != nil {
		t.Fatalf("failed to write legacy file: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(legacyPath)
	})

	entity, err := NewLocalFileEntity(types.EntityTypeVersion, "data/uploads/legacy-entity.txt")
	if err != nil {
		t.Fatalf("expected legacy entity to resolve, got error: %v", err)
	}
	if entity.Size() != int64(len("entity")) {
		t.Fatalf("unexpected entity size: got %d", entity.Size())
	}
}

func TestResolveExistingLocalStoragePathFallsBackToCurrentWorkingTreeDataDir(t *testing.T) {
	util.UseWorkingDir = false
	t.Cleanup(func() {
		util.UseWorkingDir = false
	})

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}

	repoRoot := filepath.Join(t.TempDir(), "repo")
	cloudreveRoot := filepath.Join(repoRoot, "cloudreve")
	if err := os.MkdirAll(cloudreveRoot, 0o755); err != nil {
		t.Fatalf("failed to create working tree: %v", err)
	}
	if err := os.Chdir(cloudreveRoot); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	sidecarPath := filepath.Join(cloudreveRoot, "data", "cloudreve", "fts-sidecar", "1", "2", "3", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(sidecarPath), 0o755); err != nil {
		t.Fatalf("failed to create sidecar dir: %v", err)
	}
	if err := os.WriteFile(sidecarPath, []byte("manifest"), 0o644); err != nil {
		t.Fatalf("failed to write sidecar file: %v", err)
	}

	got := resolveExistingLocalStoragePath("cloudreve/fts-sidecar/1/2/3/manifest.json")
	gotEval, _ := filepath.EvalSymlinks(got)
	wantEval, _ := filepath.EvalSymlinks(sidecarPath)
	if gotEval != wantEval {
		t.Fatalf("unexpected sidecar cwd fallback: got %q want %q", gotEval, wantEval)
	}
}

func TestResolveExistingLocalStoragePathFallsBackToParentDataDir(t *testing.T) {
	util.UseWorkingDir = false
	t.Cleanup(func() {
		util.UseWorkingDir = false
	})

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}

	repoRoot := filepath.Join(t.TempDir(), "repo")
	cloudreveRoot := filepath.Join(repoRoot, "cloudreve")
	if err := os.MkdirAll(cloudreveRoot, 0o755); err != nil {
		t.Fatalf("failed to create working tree: %v", err)
	}
	if err := os.Chdir(cloudreveRoot); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	sidecarPath := filepath.Join(repoRoot, "data", "cloudreve", "fts-sidecar", "1", "2", "4", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(sidecarPath), 0o755); err != nil {
		t.Fatalf("failed to create sidecar dir: %v", err)
	}
	if err := os.WriteFile(sidecarPath, []byte("manifest"), 0o644); err != nil {
		t.Fatalf("failed to write sidecar file: %v", err)
	}

	got := resolveExistingLocalStoragePath("cloudreve/fts-sidecar/1/2/4/manifest.json")
	gotEval, _ := filepath.EvalSymlinks(got)
	wantEval, _ := filepath.EvalSymlinks(sidecarPath)
	if gotEval != wantEval {
		t.Fatalf("unexpected sidecar parent-data fallback: got %q want %q", gotEval, wantEval)
	}
}
