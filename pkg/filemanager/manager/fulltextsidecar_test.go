package manager

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
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
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/request"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	tikaextractor "github.com/cloudreve/Cloudreve/v4/pkg/searcher/extractor"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/gofrs/uuid"
)

type memorySidecarHandler struct {
	dir string
}

type openOnlyMemorySidecarHandler struct {
	*memorySidecarHandler
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

func (m *openOnlyMemorySidecarHandler) Capabilities() *driver.Capabilities {
	return &driver.Capabilities{
		StaticFeatures: &boolset.BooleanSet{},
	}
}

type readerAtReadSeekCloser struct {
	*bytes.Reader
}

func (r readerAtReadSeekCloser) Close() error { return nil }

type fakeTikaClient struct {
	responses map[string][]byte
}

func (c *fakeTikaClient) Apply(opts ...request.Option) {}

func (c *fakeTikaClient) Request(method, target string, body io.Reader, opts ...request.Option) *request.Response {
	data := c.responses[target]
	return &request.Response{
		Response: &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(data)),
		},
	}
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

func TestPersistFTSSidecarsToHandlerUsesRMetaRootContentFallback(t *testing.T) {
	tempDir := t.TempDir()
	handler := &memorySidecarHandler{dir: tempDir}
	ctx := context.Background()

	m := &manager{
		l: logging.NewConsoleLogger(logging.LevelError),
		settings: testSettingProvider{
			tikaCfg: &setting.FTSTikaExtractorSetting{
				Endpoint:             "http://tika:9998",
				SidecarEnabled:       true,
				SidecarTextEnabled:   true,
				SidecarAssetsEnabled: true,
				DocumentExts:         []string{"pdf"},
			},
		},
	}

	rmetaRaw := []byte(`[
		{"Content-Type":"application/pdf","X-TIKA:content":"根文档内容"},
		{"X-TIKA:embedded_resource_path":"embedded/note.txt","resourceName":"note.txt","Content-Type":"text/plain","X-TIKA:content":"附件内容"}
	]`)
	client := &fakeTikaClient{
		responses: map[string][]byte{
			"http://tika:9998/tika":   []byte(""),
			"http://tika:9998/rmeta":  rmetaRaw,
			"http://tika:9998/unpack": []byte(""),
		},
	}
	extractor := tikaextractor.NewTikaExtractor(client, m.settings, m.l, m.settings.FTSTikaExtractor(ctx))
	fileModel := &ent.File{ID: 42, OwnerID: 1, Name: "blank.pdf"}
	entity := &testEntity{id: 7, source: "bucket/blank.pdf"}
	policy := &ent.StoragePolicy{ID: 9, BucketName: "bucket"}
	source := bytes.NewReader([]byte("pdf payload"))

	manifest, manifestPath, err := m.persistFTSSidecarsToHandler(ctx, extractor, nil, fileModel, fileModel.Name, entity, policy, handler, readerAtReadSeekCloser{Reader: source}, "")
	if err != nil {
		t.Fatalf("persistFTSSidecarsToHandler returned error: %v", err)
	}
	if manifest == nil || manifestPath == "" {
		t.Fatalf("expected manifest and path, got manifest=%+v path=%q", manifest, manifestPath)
	}
	if !manifest.TextReady {
		t.Fatal("expected text sidecar to be marked ready")
	}
	contentArtifact, ok := manifest.ObjectByName("content.txt")
	if !ok {
		t.Fatal("expected content.txt artifact in manifest")
	}
	if contentArtifact.Size == 0 {
		t.Fatal("expected non-empty content artifact from rmeta root fallback")
	}
	contentRaw, err := os.ReadFile(handler.LocalPath(ctx, contentArtifact.Path))
	if err != nil {
		t.Fatalf("failed to read persisted content artifact: %v", err)
	}
	if got, want := string(contentRaw), "根文档内容"; got != want {
		t.Fatalf("unexpected persisted content artifact: got %q want %q", got, want)
	}
	if _, ok := manifest.ObjectByID("rmeta.json"); ok {
		t.Fatal("did not expect rmeta.json to be persisted in manifest")
	}
}

func TestPersistFTSSidecarsToHandlerAlignsRMetaTextWithUnpackedArchivePath(t *testing.T) {
	tempDir := t.TempDir()
	handler := &memorySidecarHandler{dir: tempDir}
	ctx := context.Background()

	m := &manager{
		l: logging.NewConsoleLogger(logging.LevelError),
		settings: testSettingProvider{
			tikaCfg: &setting.FTSTikaExtractorSetting{
				Endpoint:             "http://tika:9998",
				SidecarEnabled:       true,
				SidecarTextEnabled:   true,
				SidecarAssetsEnabled: true,
				DocumentExts:         []string{"pdf"},
				ArchiveExts:          []string{"zip"},
			},
		},
	}

	rmetaRaw := []byte(`[
		{"Content-Type":"application/zip","X-TIKA:content":"root zip text"},
		{"X-TIKA:embedded_resource_path":"note.txt","resourceName":"note.txt","Content-Type":"text/plain","X-TIKA:content":"nested note text"}
	]`)
	unpackRaw := buildZipForTest(t, map[string][]byte{
		"nested/note.txt": []byte("nested note text"),
	})
	client := &fakeTikaClient{
		responses: map[string][]byte{
			"http://tika:9998/tika":       []byte("root zip text"),
			"http://tika:9998/rmeta":      rmetaRaw,
			"http://tika:9998/unpack/all": unpackRaw,
		},
	}
	extractor := tikaextractor.NewTikaExtractor(client, m.settings, m.l, m.settings.FTSTikaExtractor(ctx))
	fileModel := &ent.File{ID: 42, OwnerID: 1, Name: "archive.zip"}
	entity := &testEntity{id: 7, source: "bucket/archive.zip"}
	policy := &ent.StoragePolicy{ID: 9, BucketName: "bucket"}
	source := bytes.NewReader([]byte("zip payload"))

	manifest, manifestPath, err := m.persistFTSSidecarsToHandler(ctx, extractor, nil, fileModel, fileModel.Name, entity, policy, handler, readerAtReadSeekCloser{Reader: source}, "")
	if err != nil {
		t.Fatalf("persistFTSSidecarsToHandler returned error: %v", err)
	}
	if manifest == nil || manifestPath == "" {
		t.Fatalf("expected manifest and path, got manifest=%+v path=%q", manifest, manifestPath)
	}
	if _, ok := manifest.ObjectByID("content.txt"); ok {
		t.Fatal("did not expect archive root content.txt sidecar to be persisted")
	}
	if _, ok := manifest.ObjectByID("rmeta.json"); ok {
		t.Fatal("did not expect rmeta.json sidecar to be persisted")
	}

	textArtifactID := sidecarAttachmentTextObjectID("attachments/nested/note.txt")
	if _, ok := manifest.ObjectByID(textArtifactID); !ok {
		t.Fatalf("expected manifest to include aligned attachment text artifact %q", textArtifactID)
	}

	attachments := buildEmbeddedSearchAttachmentsFromManifest(fileModel, entity, manifest, rmetaRaw)
	if len(attachments) != 1 {
		t.Fatalf("unexpected manifest attachment count: got %d want 1", len(attachments))
	}
	if got, want := attachments[0].Path, filepath.ToSlash(filepath.Join(filepath.Dir(manifestPath), "attachments/nested/note.txt")); got != want {
		t.Fatalf("unexpected attachment path: got %q want %q", got, want)
	}
	if got, want := attachments[0].Source, filepath.ToSlash(filepath.Join(filepath.Dir(manifestPath), "attachment-text/attachments/nested/note.txt.txt")); got != want {
		t.Fatalf("unexpected attachment source: got %q want %q", got, want)
	}

	hydrated := m.hydrateFTSSidecarAttachmentContents(ctx, handler, attachments)
	if got, want := hydrated[0].Content, "nested note text"; got != want {
		t.Fatalf("unexpected hydrated attachment content: got %q want %q", got, want)
	}
}

func TestPersistFTSSidecarsToHandlerCollapsesNestedDocxRMetaTextToArchiveEntry(t *testing.T) {
	tempDir := t.TempDir()
	handler := &memorySidecarHandler{dir: tempDir}
	ctx := context.Background()

	m := &manager{
		l: logging.NewConsoleLogger(logging.LevelError),
		settings: testSettingProvider{
			tikaCfg: &setting.FTSTikaExtractorSetting{
				Endpoint:             "http://tika:9998",
				SidecarEnabled:       true,
				SidecarTextEnabled:   true,
				SidecarAssetsEnabled: true,
				DocumentExts:         []string{"pdf", "docx"},
				ArchiveExts:          []string{"zip"},
			},
		},
	}

	rmetaRaw := []byte(`[
		{"Content-Type":"application/zip","X-TIKA:content":"root zip text"},
		{"X-TIKA:embedded_resource_path":"docs/report.docx/word/document.xml","resourceName":"word/document.xml","Content-Type":"application/vnd.openxmlformats-officedocument.wordprocessingml.document","X-TIKA:content":"docx正文内容"}
	]`)
	unpackRaw := buildZipForTest(t, map[string][]byte{
		"docs/report.docx": []byte("docx binary payload"),
	})
	client := &fakeTikaClient{
		responses: map[string][]byte{
			"http://tika:9998/tika":       []byte("root zip text"),
			"http://tika:9998/rmeta":      rmetaRaw,
			"http://tika:9998/unpack/all": unpackRaw,
		},
	}
	extractor := tikaextractor.NewTikaExtractor(client, m.settings, m.l, m.settings.FTSTikaExtractor(ctx))
	fileModel := &ent.File{ID: 42, OwnerID: 1, Name: "archive.zip"}
	entity := &testEntity{id: 7, source: "bucket/archive.zip"}
	policy := &ent.StoragePolicy{ID: 9, BucketName: "bucket"}
	source := bytes.NewReader([]byte("zip payload"))

	manifest, manifestPath, err := m.persistFTSSidecarsToHandler(ctx, extractor, nil, fileModel, fileModel.Name, entity, policy, handler, readerAtReadSeekCloser{Reader: source}, "")
	if err != nil {
		t.Fatalf("persistFTSSidecarsToHandler returned error: %v", err)
	}
	if manifest == nil || manifestPath == "" {
		t.Fatalf("expected manifest and path, got manifest=%+v path=%q", manifest, manifestPath)
	}

	textArtifactID := sidecarAttachmentTextObjectID("attachments/docs/report.docx")
	textArtifact, ok := manifest.ObjectByID(textArtifactID)
	if !ok {
		t.Fatalf("expected manifest to include docx attachment text artifact %q", textArtifactID)
	}
	if got, want := textArtifact.ParentID, "attachments/docs/report.docx"; got != want {
		t.Fatalf("unexpected text artifact parent: got %q want %q", got, want)
	}

	attachments := buildEmbeddedSearchAttachmentsFromManifest(fileModel, entity, manifest, rmetaRaw)
	if len(attachments) != 1 {
		t.Fatalf("unexpected manifest attachment count: got %d want 1", len(attachments))
	}
	if got, want := attachments[0].Path, filepath.ToSlash(filepath.Join(filepath.Dir(manifestPath), "attachments/docs/report.docx")); got != want {
		t.Fatalf("unexpected attachment path: got %q want %q", got, want)
	}
	if got, want := attachments[0].Source, filepath.ToSlash(filepath.Join(filepath.Dir(manifestPath), textArtifactID)); got != want {
		t.Fatalf("unexpected attachment source: got %q want %q", got, want)
	}

	hydrated := m.hydrateFTSSidecarAttachmentContents(ctx, handler, attachments)
	if got, want := hydrated[0].Content, "docx正文内容"; got != want {
		t.Fatalf("unexpected hydrated attachment content: got %q want %q", got, want)
	}
}

func TestIsFTSDocumentLikeFile(t *testing.T) {
	cfg := &setting.FTSTikaExtractorSetting{
		DocumentExts: []string{"pdf", "docx", "txt"},
		ArchiveExts:  []string{"zip", "rar"},
	}

	extractor := tikaextractor.NewTikaExtractor(nil, nil, logging.NewConsoleLogger(logging.LevelError), cfg)

	if !shouldSaveFTSSidecarRootContent("report.pdf", cfg, extractor) {
		t.Fatal("expected pdf to save root sidecar content")
	}
	if !shouldSaveFTSSidecarRootContent("script.sh", cfg, extractor) {
		t.Fatal("expected shell script to save root sidecar content")
	}
	if shouldSaveFTSSidecarRootContent("archive.zip", cfg, extractor) {
		t.Fatal("did not expect zip to save root sidecar content")
	}
}

func TestPersistFTSSidecarsToHandlerWritesRootContentForShellScript(t *testing.T) {
	tempDir := t.TempDir()
	handler := &memorySidecarHandler{dir: tempDir}
	ctx := context.Background()

	m := &manager{
		l: logging.NewConsoleLogger(logging.LevelError),
		settings: testSettingProvider{
			tikaCfg: &setting.FTSTikaExtractorSetting{
				Endpoint:             "http://tika:9998",
				SidecarEnabled:       true,
				SidecarTextEnabled:   true,
				SidecarAssetsEnabled: true,
				DocumentExts:         []string{"pdf", "txt"},
				ArchiveExts:          []string{"zip"},
			},
		},
	}

	client := &fakeTikaClient{
		responses: map[string][]byte{
			"http://tika:9998/tika":   []byte("#!/usr/bin/env bash\necho hello\n"),
			"http://tika:9998/rmeta":  []byte(`[]`),
			"http://tika:9998/unpack": []byte(""),
		},
	}
	extractor := tikaextractor.NewTikaExtractor(client, m.settings, m.l, m.settings.FTSTikaExtractor(ctx))
	fileModel := &ent.File{ID: 52, OwnerID: 1, Name: "install.sh"}
	entity := &testEntity{id: 9, source: "bucket/install.sh"}
	policy := &ent.StoragePolicy{ID: 9, BucketName: "bucket"}
	source := bytes.NewReader([]byte("#!/usr/bin/env bash\necho hello\n"))

	manifest, _, err := m.persistFTSSidecarsToHandler(ctx, extractor, nil, fileModel, fileModel.Name, entity, policy, handler, readerAtReadSeekCloser{Reader: source}, "")
	if err != nil {
		t.Fatalf("persistFTSSidecarsToHandler returned error: %v", err)
	}
	if manifest == nil || !manifest.TextReady {
		t.Fatalf("expected text-ready manifest, got %+v", manifest)
	}
	contentArtifact, ok := manifest.ObjectByID("content.txt")
	if !ok {
		t.Fatal("expected shell script content.txt artifact in manifest")
	}
	contentRaw, err := os.ReadFile(handler.LocalPath(ctx, contentArtifact.Path))
	if err != nil {
		t.Fatalf("failed to read persisted shell content artifact: %v", err)
	}
	if got, want := string(contentRaw), "#!/usr/bin/env bash\necho hello"; got != want {
		t.Fatalf("unexpected persisted shell content artifact: got %q want %q", got, want)
	}
}

func TestPersistExternalFTSSidecarsToHandlerWritesExpectedArtifacts(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	handler := &memorySidecarHandler{dir: tempDir}
	m := &manager{}
	policy := &ent.StoragePolicy{ID: 11, BucketName: "cloudreve-test-bucket"}

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

	manifest, manifestPath, err := m.persistExternalFTSSidecarsToHandler(ctx, fileModel, entity, policy, handler, result)
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

	attachmentArtifact, ok := manifest.ObjectByID("attachments/embedded/embedded.txt")
	if !ok {
		t.Fatal("expected attachment artifact to be persisted in manifest objects")
	}
	textArtifactID := sidecarAttachmentTextObjectID("attachments/embedded/embedded.txt")
	if _, ok := manifest.ObjectByID(textArtifactID); !ok {
		t.Fatalf("expected manifest to include attachment text artifact %q", textArtifactID)
	}
	for _, object := range manifest.Objects {
		if isLegacyFTSSidecarAuxiliaryKind(object.Kind) {
			t.Fatalf("expected legacy auxiliary artifacts to be removed from external sidecars, got kind=%q id=%q", object.Kind, object.ID)
		}
	}

	attachments := buildEmbeddedSearchAttachmentsFromManifest(fileModel, entity, manifest, nil)
	if len(attachments) != 1 {
		t.Fatalf("unexpected manifest attachment count: got %d want 1", len(attachments))
	}
	if got, want := attachments[0].ParentID, attachmentRootParentID(42); got != want {
		t.Fatalf("unexpected attachment parent id: got %q want %q", got, want)
	}
	if got, want := attachments[0].Path, attachmentArtifact.Path; got != want {
		t.Fatalf("unexpected attachment artifact path: got %q want %q", got, want)
	}
	attachments = m.hydrateFTSSidecarAttachmentContents(ctx, handler, attachments)
	if got, want := attachments[0].Content, "embedded content"; got != want {
		t.Fatalf("unexpected hydrated attachment content: got %q want %q", got, want)
	}
}

func TestPersistExternalFTSSidecarsToHandlerReadsReferencedContentByPolicyID(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	handler := &memorySidecarHandler{dir: tempDir}
	m := &manager{}
	policy := &ent.StoragePolicy{ID: 17, BucketName: "cloudreve-extract-bucket"}

	if err := putSidecarBytes(ctx, handler, "third-party/root.txt", "root.txt", "text/plain; charset=utf-8", []byte("hello from referenced object")); err != nil {
		t.Fatalf("failed to seed referenced root content: %v", err)
	}
	if err := putSidecarBytes(ctx, handler, "third-party/attachments/embedded.txt", "embedded.txt", "text/plain; charset=utf-8", []byte("hello from referenced attachment")); err != nil {
		t.Fatalf("failed to seed referenced attachment content: %v", err)
	}

	fileModel := &ent.File{ID: 43, OwnerID: 9, Name: "report.pdf"}
	entity := &testEntity{id: 8, source: "tenant-a/u9/report.pdf"}
	result := &externalFTSResultMessage{
		SnapshotToken: "snapshot-43",
		Provider: externalFTSProviderInfo{
			Name:    "vendor-x",
			Version: "1.0.1",
		},
		Root: externalFTSResultRoot{
			ContentRef: &externalFTSObjectReference{
				PolicyID: policy.ID,
				Bucket:   policy.BucketName,
				Path:     "third-party/root.txt",
			},
		},
		Attachments: []externalFTSAttachment{{
			ID:       "att-1",
			ParentID: "file:43",
			Depth:    1,
			Type:     "attachment",
			Name:     "embedded.txt",
			Path:     "embedded/embedded.txt",
			MimeType: "text/plain",
			ContentRef: &externalFTSObjectReference{
				PolicyID: policy.ID,
				Bucket:   policy.BucketName,
				Path:     "third-party/attachments/embedded.txt",
			},
		}},
	}

	manifest, manifestPath, err := m.persistExternalFTSSidecarsToHandler(ctx, fileModel, entity, policy, handler, result)
	if err != nil {
		t.Fatalf("persistExternalFTSSidecarsToHandler returned error: %v", err)
	}
	if manifest == nil {
		t.Fatal("expected manifest")
	}

	contentRaw, err := os.ReadFile(handler.LocalPath(ctx, filepath.ToSlash(filepath.Join(filepath.Dir(manifestPath), "content.txt"))))
	if err != nil {
		t.Fatalf("failed to read content sidecar: %v", err)
	}
	if got, want := string(contentRaw), "hello from referenced object"; got != want {
		t.Fatalf("unexpected content sidecar: got %q want %q", got, want)
	}

	attachments := buildEmbeddedSearchAttachmentsFromManifest(fileModel, entity, manifest, nil)
	if len(attachments) != 1 {
		t.Fatalf("unexpected manifest attachment count: got %d want 1", len(attachments))
	}
	if got, want := attachments[0].Path, filepath.ToSlash(filepath.Join(filepath.Dir(manifestPath), "attachments/embedded/embedded.txt")); got != want {
		t.Fatalf("unexpected attachment placeholder path: got %q want %q", got, want)
	}
	if got, want := attachments[0].Source, filepath.ToSlash(filepath.Join(filepath.Dir(manifestPath), "attachment-text/attachments/embedded/embedded.txt.txt")); got != want {
		t.Fatalf("unexpected attachment source: got %q want %q", got, want)
	}
	textSidecarRaw, err := os.ReadFile(handler.LocalPath(ctx, attachments[0].Source))
	if err != nil {
		t.Fatalf("failed to read attachment text sidecar: %v", err)
	}
	if got, want := string(textSidecarRaw), "hello from referenced attachment"; got != want {
		t.Fatalf("unexpected attachment text sidecar: got %q want %q", got, want)
	}

	hydrated := m.hydrateFTSSidecarAttachmentContents(ctx, handler, attachments)
	if got, want := hydrated[0].Content, "hello from referenced attachment"; got != want {
		t.Fatalf("unexpected hydrated attachment content: got %q want %q", got, want)
	}

	if _, ok := manifest.ObjectByID("attachments/embedded/embedded.txt"); !ok {
		t.Fatal("expected manifest to include external attachment artifact")
	}
	if _, ok := manifest.ObjectByName(sidecarAttachmentTextObjectIDFromDocID(attachments[0].ID)); !ok {
		t.Fatalf("expected manifest to include attachment text artifact for %q", attachments[0].ID)
	}
}

func TestHydrateFTSSidecarAttachmentContentsLoadsAttachmentTextSidecar(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	handler := &memorySidecarHandler{dir: tempDir}
	m := &manager{}

	savePath := "cloudreve/fts-sidecar/1/42/7/attachment-text/attachments/nested/note.txt.txt"
	if err := putSidecarBytes(ctx, handler, savePath, "note.txt.txt", "text/plain; charset=utf-8", []byte("hello from sidecar attachment")); err != nil {
		t.Fatalf("failed to seed attachment text sidecar: %v", err)
	}

	attachments := []searcher.SearchAttachmentDocument{{
		ID:     embeddedAttachmentDocID(42, "attachments/nested/note.txt"),
		Source: savePath,
	}}
	hydrated := m.hydrateFTSSidecarAttachmentContents(ctx, handler, attachments)
	if len(hydrated) != 1 {
		t.Fatalf("unexpected hydrated attachment count: got %d want 1", len(hydrated))
	}
	if got, want := hydrated[0].Content, "hello from sidecar attachment"; got != want {
		t.Fatalf("unexpected hydrated attachment content: got %q want %q", got, want)
	}
}

func TestBuildFTSSidecarOCRCandidatesIncludesRootAndAttachmentImages(t *testing.T) {
	fileModel := &ent.File{ID: 42, OwnerID: 9, Name: "poster.png", Size: 4096}
	entity := &testEntity{id: 7, source: "tenant-a/u9/poster.png"}
	policy := &ent.StoragePolicy{ID: 11, BucketName: "cloudreve-fts"}
	manifest := &FTSSidecarManifest{
		Objects: []FTSSidecarArtifact{
			{
				ID:       "attachments/slide/image1.png",
				Kind:     "embedded",
				Name:     "image1.png",
				Path:     "cloudreve/fts-sidecar/9/42/7/attachments/slide/image1.png",
				MimeType: "image/png",
				Size:     2048,
			},
			{
				ID:       "attachments/slide/image2.png",
				Kind:     "embedded",
				Name:     "image2.png",
				Path:     "cloudreve/fts-sidecar/9/42/7/attachments/slide/image2.png",
				MimeType: "image/png",
				Size:     2048,
			},
			{
				ID:       sidecarAttachmentTextObjectID("attachments/slide/image2.png"),
				ParentID: "attachments/slide/image2.png",
				Kind:     "attachment_text",
				Name:     "image2.png.txt",
				Path:     "cloudreve/fts-sidecar/9/42/7/attachment-text/attachments/slide/image2.png.txt",
				MimeType: "text/plain; charset=utf-8",
				Size:     32,
			},
			{
				ID:       "attachments/slide/note.txt",
				Kind:     "embedded",
				Name:     "note.txt",
				Path:     "cloudreve/fts-sidecar/9/42/7/attachments/slide/note.txt",
				MimeType: "text/plain",
				Size:     128,
			},
		},
	}

	candidates := buildFTSSidecarOCRCandidates(fileModel, entity, policy, "", manifest)
	if len(candidates) != 2 {
		t.Fatalf("unexpected ocr candidate count: got %d want 2", len(candidates))
	}
	if got, want := candidates[0].Scope, "file"; got != want {
		t.Fatalf("unexpected root candidate scope: got %q want %q", got, want)
	}
	if got, want := candidates[0].Path, entity.Source(); got != want {
		t.Fatalf("unexpected root candidate path: got %q want %q", got, want)
	}
	if got, want := candidates[0].PolicyID, policy.ID; got != want {
		t.Fatalf("unexpected root candidate policy id: got %d want %d", got, want)
	}
	if got, want := candidates[1].Scope, "attachment"; got != want {
		t.Fatalf("unexpected attachment candidate scope: got %q want %q", got, want)
	}
	if got, want := candidates[1].AttachmentID, embeddedAttachmentDocID(42, "attachments/slide/image1.png"); got != want {
		t.Fatalf("unexpected attachment candidate id: got %q want %q", got, want)
	}
	if got, want := candidates[1].Path, "cloudreve/fts-sidecar/9/42/7/attachments/slide/image1.png"; got != want {
		t.Fatalf("unexpected attachment candidate path: got %q want %q", got, want)
	}
}

func TestReadFTSSidecarBytesFallsBackToOpenWithoutInboundCapability(t *testing.T) {
	tempDir := t.TempDir()
	base := &memorySidecarHandler{dir: tempDir}
	handler := &openOnlyMemorySidecarHandler{memorySidecarHandler: base}

	savePath := "cloudreve/fts-sidecar/1/2/3/content.txt"
	if err := putSidecarBytes(context.Background(), base, savePath, "content.txt", "text/plain; charset=utf-8", []byte("sidecar via open")); err != nil {
		t.Fatalf("failed to seed sidecar content: %v", err)
	}

	raw, err := readFTSSidecarBytes(context.Background(), nil, handler, savePath)
	if err != nil {
		t.Fatalf("readFTSSidecarBytes returned error: %v", err)
	}
	if got, want := string(raw), "sidecar via open"; got != want {
		t.Fatalf("unexpected sidecar content: got %q want %q", got, want)
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
