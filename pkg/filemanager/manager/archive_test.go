package manager

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/request"
	tikaextractor "github.com/cloudreve/Cloudreve/v4/pkg/searcher/extractor"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

func TestTopLevelArchiveURIs(t *testing.T) {
	uris := mustArchiveURIs(t,
		"cloudreve://my/a/b",
		"cloudreve://my/a",
		"cloudreve://my/c/d",
		"cloudreve://my/c",
		"cloudreve://my/c",
	)

	filtered := topLevelArchiveURIs(uris, "current-user")
	if len(filtered) != 2 {
		t.Fatalf("unexpected filtered uri count: %d", len(filtered))
	}

	if filtered[0].String() != "cloudreve://my/a" || filtered[1].String() != "cloudreve://my/c" {
		t.Fatalf("unexpected filtered uris: %s, %s", filtered[0], filtered[1])
	}
}

func TestTopLevelArchiveURIsKeepDifferentOwners(t *testing.T) {
	uris := mustArchiveURIs(t,
		"cloudreve://owner-a@my/a",
		"cloudreve://owner-b@my/a/b",
	)

	filtered := topLevelArchiveURIs(uris, "current-user")
	if len(filtered) != 2 {
		t.Fatalf("unexpected filtered uri count across owners: %d", len(filtered))
	}
}

func TestListArchiveFilesByLocalSupportZIP(t *testing.T) {
	raw := mustArchiveZip(t, map[string]string{
		"docs/":           "",
		"docs/readme.txt": "hello",
	})

	files := mustListArchiveFiles(t, "sample.zip", raw)
	if len(files) != 2 {
		t.Fatalf("unexpected file count: %d", len(files))
	}

	if files[0].Name != "docs" || !files[0].IsDirectory {
		t.Fatalf("unexpected first entry: %+v", files[0])
	}
	if files[1].Name != "docs/readme.txt" || files[1].IsDirectory || files[1].Size != 5 {
		t.Fatalf("unexpected second entry: %+v", files[1])
	}
}

func TestListArchiveFilesByLocalSupportZIPAcceptsUTF8EncodingAlias(t *testing.T) {
	raw := mustArchiveZip(t, map[string]string{
		"docs/readme.txt": "hello",
	})

	enc, ok := ResolveZipTextEncoding("utf8")
	if !ok {
		t.Fatal("expected utf8 alias to be supported")
	}

	reader := bytes.NewReader(raw)
	files, err := listArchiveFilesByLocalSupport(
		context.Background(),
		"sample.zip",
		io.NewSectionReader(reader, 0, int64(len(raw))),
		enc,
	)
	if err != nil {
		t.Fatalf("failed to list archive with utf8 alias: %v", err)
	}

	if len(files) != 1 || files[0].Name != "docs/readme.txt" {
		t.Fatalf("unexpected files: %+v", files)
	}
}

func TestListArchiveFilesByLocalSupportTarGz(t *testing.T) {
	raw := mustArchiveTarGz(t, map[string]string{
		"nested/":           "",
		"nested/report.txt": "payload",
	})

	files := mustListArchiveFiles(t, "sample.tar.gz", raw)
	if len(files) != 2 {
		t.Fatalf("unexpected file count: %d", len(files))
	}

	if files[1].Name != "nested/report.txt" || files[1].Size != int64(len("payload")) {
		t.Fatalf("unexpected tar.gz entry: %+v", files[1])
	}
}

func TestListArchiveFilesByLocalSupportSingleCompressedFile(t *testing.T) {
	raw := mustArchiveGzip(t, "hello world")

	files := mustListArchiveFiles(t, "report.txt.gz", raw)
	if len(files) != 1 {
		t.Fatalf("unexpected file count: %d", len(files))
	}

	if files[0].Name != "report.txt" {
		t.Fatalf("unexpected decompressed entry name: %+v", files[0])
	}
}

func TestListArchiveFilesWithTikaFallback(t *testing.T) {
	fallbackZip := mustArchiveZip(t, map[string]string{
		"unpacked/result.txt": "fallback",
	})

	tika := tikaextractor.NewTikaExtractor(
		testArchiveRequestClient{responseBody: fallbackZip},
		nil,
		logging.NewConsoleLogger(logging.LevelError),
		&setting.FTSTikaExtractorSetting{Endpoint: "http://tika"},
	)

	files, err := listArchiveFilesWithTikaFallback(context.Background(), tika, "sample.arj", bytes.NewReader([]byte("opaque-archive")))
	if err != nil {
		t.Fatalf("unexpected fallback error: %v", err)
	}

	if len(files) != 1 || files[0].Name != "unpacked/result.txt" {
		t.Fatalf("unexpected fallback files: %+v", files)
	}
}

func TestSingleCompressedArchiveEntryName(t *testing.T) {
	if got := SingleCompressedArchiveEntryName("backup.sql.gz", ".gz"); got != "backup.sql" {
		t.Fatalf("unexpected entry name: %q", got)
	}

	if got := SingleCompressedArchiveEntryName("payload", ".gz"); got != "payload" {
		t.Fatalf("unexpected entry name without suffix: %q", got)
	}
}

func TestArchiveTikaFallbackEnabled(t *testing.T) {
	if archiveTikaFallbackEnabled(nil) {
		t.Fatal("expected nil config to disable tika fallback")
	}

	if archiveTikaFallbackEnabled(&setting.FTSTikaExtractorSetting{
		Endpoint:       "http://tika",
		ArchiveEnabled: false,
	}) {
		t.Fatal("expected disabled archive support to disable tika fallback")
	}

	if !archiveTikaFallbackEnabled(&setting.FTSTikaExtractorSetting{
		Endpoint:       "http://tika",
		ArchiveEnabled: true,
	}) {
		t.Fatal("expected enabled archive support with endpoint to enable tika fallback")
	}
}

func mustArchiveURIs(t *testing.T, raw ...string) []*fs.URI {
	t.Helper()

	res, err := fs.NewUriFromStrings(raw...)
	if err != nil {
		t.Fatalf("failed to parse uris: %v", err)
	}

	return res
}

func mustListArchiveFiles(t *testing.T, fileName string, raw []byte) []ArchivedFile {
	t.Helper()

	reader := bytes.NewReader(raw)
	files, err := listArchiveFilesByLocalSupport(context.Background(), fileName, io.NewSectionReader(reader, 0, int64(len(raw))), nil)
	if err != nil {
		t.Fatalf("failed to list archive %q: %v", fileName, err)
	}

	return files
}

func mustArchiveZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		content := entries[name]
		header := &zip.FileHeader{
			Name:     name,
			Method:   zip.Deflate,
			Modified: time.Unix(1700000000, 0),
		}
		if name[len(name)-1] == '/' {
			header.Method = zip.Store
		}

		writer, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatalf("failed to create zip entry %q: %v", name, err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatalf("failed to write zip entry %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("failed to close zip writer: %v", err)
	}

	return buf.Bytes()
}

func mustArchiveTarGz(t *testing.T, entries map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		content := entries[name]
		header := &tar.Header{
			Name:    name,
			ModTime: time.Unix(1700000000, 0),
			Mode:    0o644,
			Size:    int64(len(content)),
		}
		if name[len(name)-1] == '/' {
			header.Typeflag = tar.TypeDir
			header.Mode = 0o755
			header.Size = 0
		}

		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("failed to write tar header %q: %v", name, err)
		}
		if header.Typeflag != tar.TypeDir {
			if _, err := tw.Write([]byte(content)); err != nil {
				t.Fatalf("failed to write tar entry %q: %v", name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("failed to close tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("failed to close gzip writer: %v", err)
	}

	return buf.Bytes()
}

func mustArchiveGzip(t *testing.T, body string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write([]byte(body)); err != nil {
		t.Fatalf("failed to write gzip payload: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("failed to close gzip writer: %v", err)
	}

	return buf.Bytes()
}

type testArchiveRequestClient struct {
	responseBody []byte
}

func (c testArchiveRequestClient) Apply(opts ...request.Option) {}

func (c testArchiveRequestClient) Request(method, target string, body io.Reader, opts ...request.Option) *request.Response {
	return &request.Response{
		Response: &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(c.responseBody)),
		},
	}
}
