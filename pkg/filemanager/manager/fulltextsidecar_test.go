package manager

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/driver"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	"github.com/gofrs/uuid"
)

type memorySidecarHandler struct {
	dir string
}

func (m *memorySidecarHandler) Put(ctx context.Context, file *fs.UploadRequest) error {
	if file == nil || file.Props == nil {
		return nil
	}

	target := filepath.Join(m.dir, filepath.FromSlash(file.Props.SavePath))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	dst, err := os.Create(target)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, file.File)
	return err
}

func (m *memorySidecarHandler) Delete(ctx context.Context, files ...string) ([]string, error) {
	var failed []string
	for _, item := range files {
		target := filepath.Join(m.dir, filepath.FromSlash(item))
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			failed = append(failed, item)
		}
	}
	if len(failed) > 0 {
		return failed, os.ErrInvalid
	}
	return nil, nil
}

func (m *memorySidecarHandler) Open(ctx context.Context, path string) (*os.File, error) {
	return os.Open(filepath.Join(m.dir, filepath.FromSlash(path)))
}

func (m *memorySidecarHandler) LocalPath(ctx context.Context, path string) string {
	return filepath.Join(m.dir, filepath.FromSlash(path))
}

func (m *memorySidecarHandler) Thumb(ctx context.Context, expire *time.Time, ext string, e fs.Entity) (string, error) {
	return "", nil
}

func (m *memorySidecarHandler) Source(ctx context.Context, e fs.Entity, args *driver.GetSourceArgs) (string, error) {
	return "", nil
}

func (m *memorySidecarHandler) Token(ctx context.Context, uploadSession *fs.UploadSession, file *fs.UploadRequest) (*fs.UploadCredential, error) {
	return nil, nil
}

func (m *memorySidecarHandler) CancelToken(ctx context.Context, uploadSession *fs.UploadSession) error {
	return nil
}

func (m *memorySidecarHandler) CompleteUpload(ctx context.Context, session *fs.UploadSession) error {
	return nil
}

func (m *memorySidecarHandler) List(ctx context.Context, base string, onProgress driver.ListProgressFunc, recursive bool) ([]fs.PhysicalObject, error) {
	return nil, nil
}

func (m *memorySidecarHandler) Capabilities() *driver.Capabilities {
	features := &boolset.BooleanSet{}
	boolset.Set(int(driver.HandlerCapabilityInboundGet), true, features)
	return &driver.Capabilities{
		StaticFeatures: features,
	}
}

func (m *memorySidecarHandler) MediaMeta(ctx context.Context, path, ext, language string) ([]driver.MediaMeta, error) {
	return nil, nil
}

type testEntity struct {
	id int
}

func (e *testEntity) ID() int                     { return e.id }
func (e *testEntity) Type() types.EntityType      { return types.EntityTypeVersion }
func (e *testEntity) Size() int64                 { return 0 }
func (e *testEntity) UpdatedAt() time.Time        { return time.Unix(0, 0) }
func (e *testEntity) CreatedAt() time.Time        { return time.Unix(0, 0) }
func (e *testEntity) Source() string              { return "" }
func (e *testEntity) ReferenceCount() int         { return 0 }
func (e *testEntity) PolicyID() int               { return 0 }
func (e *testEntity) UploadSessionID() *uuid.UUID { return nil }
func (e *testEntity) CreatedBy() *ent.User        { return nil }
func (e *testEntity) Model() *ent.Entity          { return nil }
func (e *testEntity) Props() *types.EntityProps   { return nil }
func (e *testEntity) Encrypted() bool             { return false }

func TestSaveSidecarArchiveRecursiveBuildsTree(t *testing.T) {
	tempDir := t.TempDir()
	handler := &memorySidecarHandler{dir: tempDir}

	innerZip := buildZipForTest(t, map[string][]byte{
		"nested.txt": []byte("nested"),
	})
	outerZip := buildZipForTest(t, map[string][]byte{
		"archive.zip": innerZip,
		"plain.txt":   []byte("plain"),
	})

	artifacts, err := saveSidecarArchive(context.Background(), handler, "cloudreve/fts-sidecar/1/2/3", ftsSidecarEmbeddedDir, outerZip)
	if err != nil {
		t.Fatalf("saveSidecarArchive returned error: %v", err)
	}

	if len(artifacts) != 3 {
		t.Fatalf("unexpected artifact count: got %d want 3", len(artifacts))
	}

	byID := map[string]FTSSidecarArtifact{}
	for _, item := range artifacts {
		byID[item.ID] = item
	}

	archive, ok := byID["attachments/archive.zip"]
	if !ok {
		t.Fatal("missing archive artifact")
	}
	if archive.Kind != "archive" {
		t.Fatalf("unexpected archive kind: %q", archive.Kind)
	}
	if archive.ParentID != "" {
		t.Fatalf("unexpected archive parent: %q", archive.ParentID)
	}
	if archive.Path != "cloudreve/fts-sidecar/1/2/3/attachments/archive.zip/__self__" {
		t.Fatalf("unexpected archive path: %q", archive.Path)
	}

	nested, ok := byID["attachments/archive.zip/nested.txt"]
	if !ok {
		t.Fatal("missing nested artifact")
	}
	if nested.ParentID != "attachments/archive.zip" {
		t.Fatalf("unexpected nested parent: %q", nested.ParentID)
	}
	if nested.Depth != 1 {
		t.Fatalf("unexpected nested depth: %d", nested.Depth)
	}
	if nested.Kind != "embedded" {
		t.Fatalf("unexpected nested kind: %q", nested.Kind)
	}
}

func TestSaveSidecarArchiveRecursiveSupportsTar(t *testing.T) {
	tempDir := t.TempDir()
	handler := &memorySidecarHandler{dir: tempDir}

	innerZip := buildZipForTest(t, map[string][]byte{
		"nested.txt": []byte("nested"),
	})
	outerTar := buildTarForTest(t, map[string][]byte{
		"archive.zip": innerZip,
		"plain.txt":   []byte("plain"),
	})

	artifacts, err := saveSidecarArchive(context.Background(), handler, "cloudreve/fts-sidecar/1/2/3", ftsSidecarEmbeddedDir, outerTar)
	if err != nil {
		t.Fatalf("saveSidecarArchive returned error: %v", err)
	}

	if len(artifacts) != 3 {
		t.Fatalf("unexpected artifact count: got %d want 3", len(artifacts))
	}

	byID := map[string]FTSSidecarArtifact{}
	for _, item := range artifacts {
		byID[item.ID] = item
	}

	archive, ok := byID["attachments/archive.zip"]
	if !ok {
		t.Fatal("missing archive artifact")
	}
	if archive.Kind != "archive" {
		t.Fatalf("unexpected archive kind: %q", archive.Kind)
	}

	nested, ok := byID["attachments/archive.zip/nested.txt"]
	if !ok {
		t.Fatal("missing nested artifact")
	}
	if nested.ParentID != "attachments/archive.zip" {
		t.Fatalf("unexpected nested parent: %q", nested.ParentID)
	}
	if nested.Depth != 1 {
		t.Fatalf("unexpected nested depth: %d", nested.Depth)
	}
}

func TestBuildEmbeddedSearchAttachmentsFromManifestKeepsHierarchy(t *testing.T) {
	fileModel := &ent.File{ID: 42}
	entity := &testEntity{id: 7}
	manifest := &FTSSidecarManifest{
		Objects: []FTSSidecarArtifact{
			{
				ID:       "attachments/archive.zip",
				Kind:     "archive",
				Name:     "archive.zip",
				Path:     "cloudreve/fts-sidecar/1/42/7/attachments/archive.zip/__self__",
				MimeType: "application/zip",
				Size:     128,
			},
			{
				ID:       "attachments/archive.zip/nested.txt",
				ParentID: "attachments/archive.zip",
				Depth:    1,
				Kind:     "embedded",
				Name:     "nested.txt",
				Path:     "cloudreve/fts-sidecar/1/42/7/attachments/archive.zip/nested.txt",
				MimeType: "text/plain",
				Size:     6,
			},
		},
	}

	attachments := buildEmbeddedSearchAttachmentsFromManifest(fileModel, entity, manifest, nil)
	if len(attachments) != 2 {
		t.Fatalf("unexpected attachment count: got %d want 2", len(attachments))
	}

	var nested searcher.SearchAttachmentDocument
	for _, item := range attachments {
		if item.Name == "nested.txt" {
			nested = item
			break
		}
	}

	if nested.ID == "" {
		t.Fatal("missing nested attachment")
	}
	if nested.ParentAttachmentID != "attachments/archive.zip" {
		t.Fatalf("unexpected parent attachment id: %q", nested.ParentAttachmentID)
	}
	if nested.Depth != 1 {
		t.Fatalf("unexpected nested depth: %d", nested.Depth)
	}
	if nested.Type != "embedded" {
		t.Fatalf("unexpected nested type: %q", nested.Type)
	}
}

func TestFTSSidecarCleanupDirectoriesIncludesNestedParents(t *testing.T) {
	manifest := &FTSSidecarManifest{
		Objects: []FTSSidecarArtifact{
			{
				ID:   "attachments/archive.zip",
				Path: "cloudreve/fts-sidecar/1/42/7/attachments/archive.zip/__self__",
			},
			{
				ID:   "attachments/archive.zip/nested.txt",
				Path: "cloudreve/fts-sidecar/1/42/7/attachments/archive.zip/nested.txt",
			},
			{
				ID:   "docx-media/image1.png",
				Path: "cloudreve/fts-sidecar/1/42/7/docx-media/image1.png",
			},
		},
	}

	got := ftsSidecarCleanupDirectories("cloudreve/fts-sidecar/1/42/7/manifest.json", manifest)
	want := []string{
		"cloudreve/fts-sidecar/1/42/7/attachments/archive.zip",
		"cloudreve/fts-sidecar/1/42/7/attachments",
		"cloudreve/fts-sidecar/1/42/7/docx-media",
		"cloudreve/fts-sidecar/1/42/7",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected cleanup directories: got %#v want %#v", got, want)
	}
}

func TestFTSSidecarCleanupDirectoriesFallsBackToBaseDirs(t *testing.T) {
	got := ftsSidecarCleanupDirectories("cloudreve/fts-sidecar/1/42/7/manifest.json", nil)
	want := []string{
		"cloudreve/fts-sidecar/1/42/7/attachments",
		"cloudreve/fts-sidecar/1/42/7/docx-media",
		"cloudreve/fts-sidecar/1/42/7",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected cleanup directories without manifest: got %#v want %#v", got, want)
	}
}

func buildZipForTest(t *testing.T, files map[string][]byte) []byte {
	t.Helper()

	var buffer bytes.Buffer
	zw := zip.NewWriter(&buffer)
	for name, data := range files {
		writer, err := zw.Create(name)
		if err != nil {
			t.Fatalf("failed to create zip entry %q: %v", name, err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatalf("failed to write zip entry %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("failed to close zip writer: %v", err)
	}

	return buffer.Bytes()
}

func buildTarForTest(t *testing.T, files map[string][]byte) []byte {
	t.Helper()

	var buffer bytes.Buffer
	tw := tar.NewWriter(&buffer)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(data)),
		}); err != nil {
			t.Fatalf("failed to create tar entry %q: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("failed to write tar entry %q: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("failed to close tar writer: %v", err)
	}

	return buffer.Bytes()
}
