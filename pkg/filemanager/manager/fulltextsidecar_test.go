package manager

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
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
	id     int
	source string
}

func (e *testEntity) ID() int                     { return e.id }
func (e *testEntity) Type() types.EntityType      { return types.EntityTypeVersion }
func (e *testEntity) Size() int64                 { return 0 }
func (e *testEntity) UpdatedAt() time.Time        { return time.Unix(0, 0) }
func (e *testEntity) CreatedAt() time.Time        { return time.Unix(0, 0) }
func (e *testEntity) Source() string              { return e.source }
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

func TestSaveSidecarArchiveRecursiveSkipsSyntheticTikaArtifacts(t *testing.T) {
	tempDir := t.TempDir()
	handler := &memorySidecarHandler{dir: tempDir}

	raw := buildZipForTest(t, map[string][]byte{
		"__TEXT__":     []byte("redundant"),
		"__METADATA__": []byte(`{"k":"v"}`),
		"plain.txt":    []byte("plain"),
	})

	artifacts, err := saveSidecarArchive(context.Background(), handler, "cloudreve/fts-sidecar/1/2/3", ftsSidecarEmbeddedDir, raw)
	if err != nil {
		t.Fatalf("saveSidecarArchive returned error: %v", err)
	}

	if len(artifacts) != 1 {
		t.Fatalf("unexpected artifact count: got %d want 1", len(artifacts))
	}
	if got, want := artifacts[0].ID, "attachments/plain.txt"; got != want {
		t.Fatalf("unexpected artifact id: got %q want %q", got, want)
	}

	if _, err := os.Stat(handler.LocalPath(context.Background(), "cloudreve/fts-sidecar/1/2/3/attachments/__TEXT__")); !os.IsNotExist(err) {
		t.Fatalf("expected synthetic __TEXT__ artifact to be skipped, stat err=%v", err)
	}
	if _, err := os.Stat(handler.LocalPath(context.Background(), "cloudreve/fts-sidecar/1/2/3/attachments/__METADATA__")); !os.IsNotExist(err) {
		t.Fatalf("expected synthetic __METADATA__ artifact to be skipped, stat err=%v", err)
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
	if got, want := nested.ParentID, embeddedAttachmentDocID(42, "attachments/archive.zip"); got != want {
		t.Fatalf("unexpected parent id: got %q want %q", got, want)
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

func TestPersistExternalFTSSidecarsToHandlerWritesExpectedArtifacts(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	handler := &memorySidecarHandler{dir: tempDir}
	m := &manager{}

	fileModel := &ent.File{ID: 42, OwnerID: 9, Name: "report.pdf"}
	entity := &testEntity{id: 7, source: "tenant-a/u9/report.pdf"}
	result := &externalFTSResultMessage{
		SnapshotToken: "snapshot-42",
		Provider: externalFTSProviderInfo{
			Name:    "vendor-x",
			Version: "1.0.0",
		},
		Root: externalFTSResultRoot{
			Content:      "hello from external extractor",
			Warnings:     []string{"font fallback used"},
			QualityScore: 0.97,
		},
		Attachments: []externalFTSAttachment{{
			ID:       "att-1",
			ParentID: "file:42",
			Depth:    1,
			Type:     "attachment",
			Name:     "embedded.txt",
			Path:     "embedded/embedded.txt",
			MimeType: "text/plain",
			Content:  "embedded content",
		}},
	}

	manifest, manifestPath, err := m.persistExternalFTSSidecarsToHandler(ctx, fileModel, entity, handler, result)
	if err != nil {
		t.Fatalf("persistExternalFTSSidecarsToHandler returned error: %v", err)
	}
	if manifest == nil {
		t.Fatal("expected manifest")
	}
	if got, want := manifest.Provider, ftsSidecarProviderExternal; got != want {
		t.Fatalf("unexpected manifest provider: got %q want %q", got, want)
	}
	if !manifest.TextReady || !manifest.AssetsReady {
		t.Fatalf("expected text and assets to be ready: %+v", manifest)
	}
	if got, want := manifest.SourcePath, entity.Source(); got != want {
		t.Fatalf("unexpected source path: got %q want %q", got, want)
	}
	if manifestPath == "" {
		t.Fatal("expected manifest path")
	}

	loaded := loadFTSSidecarManifestByPath(ctx, handler, manifestPath)
	if loaded == nil {
		t.Fatal("expected manifest to be readable from sidecar storage")
	}
	if got, want := loaded.SnapshotToken, "snapshot-42"; got != want {
		t.Fatalf("unexpected loaded snapshot token: got %q want %q", got, want)
	}

	contentRaw, err := os.ReadFile(handler.LocalPath(ctx, filepath.ToSlash(filepath.Join(filepath.Dir(manifestPath), "content.txt"))))
	if err != nil {
		t.Fatalf("failed to read content sidecar: %v", err)
	}
	if got, want := string(contentRaw), "hello from external extractor"; got != want {
		t.Fatalf("unexpected content sidecar: got %q want %q", got, want)
	}

	attachmentsRaw, err := os.ReadFile(handler.LocalPath(ctx, filepath.ToSlash(filepath.Join(filepath.Dir(manifestPath), "attachments.json"))))
	if err != nil {
		t.Fatalf("failed to read attachments sidecar: %v", err)
	}
	var attachments []searcher.SearchAttachmentDocument
	if err := json.Unmarshal(attachmentsRaw, &attachments); err != nil {
		t.Fatalf("failed to unmarshal attachments sidecar: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("unexpected attachments sidecar count: got %d want 1", len(attachments))
	}
	if got, want := attachments[0].ParentID, attachmentRootParentID(42); got != want {
		t.Fatalf("unexpected attachment parent id: got %q want %q", got, want)
	}

	diagnosticsRaw, err := os.ReadFile(handler.LocalPath(ctx, filepath.ToSlash(filepath.Join(filepath.Dir(manifestPath), "diagnostics.json"))))
	if err != nil {
		t.Fatalf("failed to read diagnostics sidecar: %v", err)
	}
	var diagnostics externalFTSDiagnostics
	if err := json.Unmarshal(diagnosticsRaw, &diagnostics); err != nil {
		t.Fatalf("failed to unmarshal diagnostics sidecar: %v", err)
	}
	if got, want := diagnostics.Provider.Name, "vendor-x"; got != want {
		t.Fatalf("unexpected diagnostics provider: got %q want %q", got, want)
	}
	if got, want := diagnostics.SnapshotToken, "snapshot-42"; got != want {
		t.Fatalf("unexpected diagnostics snapshot token: got %q want %q", got, want)
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
