package workflows

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"strings"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/mholt/archives"
)

type fakeArchiveExtractor struct {
	entries []archives.FileInfo
}

func (f fakeArchiveExtractor) Extract(ctx context.Context, reader io.Reader, handle archives.FileHandler) error {
	for _, entry := range f.entries {
		if err := handle(ctx, entry); err != nil {
			return err
		}
	}
	return nil
}

type fakeArchiveEntryInfo struct {
	name    string
	size    int64
	mode    iofs.FileMode
	modTime time.Time
}

func (f fakeArchiveEntryInfo) Name() string        { return f.name }
func (f fakeArchiveEntryInfo) Size() int64         { return f.size }
func (f fakeArchiveEntryInfo) Mode() iofs.FileMode { return f.mode }
func (f fakeArchiveEntryInfo) ModTime() time.Time  { return f.modTime }
func (f fakeArchiveEntryInfo) IsDir() bool         { return f.mode.IsDir() }
func (f fakeArchiveEntryInfo) Sys() any            { return nil }

func TestExtractCompressedArchiveFile(t *testing.T) {
	var payload bytes.Buffer
	writer := gzip.NewWriter(&payload)
	if _, err := writer.Write([]byte("hello archive")); err != nil {
		t.Fatalf("failed to write gzip payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close gzip writer: %v", err)
	}

	dst, err := fs.NewUriFromString("cloudreve://my/extract")
	if err != nil {
		t.Fatalf("failed to parse destination uri: %v", err)
	}

	var (
		cursor    string
		savedPath string
		savedSize int64
		savedBody string
	)

	countProgress := &queue.Progress{}
	err = extractCompressedArchiveFile(
		context.Background(),
		archives.Gz{},
		bytes.NewReader(payload.Bytes()),
		"note.txt.gz",
		".gz",
		dst,
		&cursor,
		nil,
		countProgress,
		logging.NewConsoleLogger(logging.LevelError),
		func(ctx context.Context, savePath *fs.URI, size int64, lastModified *time.Time, file io.ReadCloser) error {
			body, err := io.ReadAll(file)
			if err != nil {
				return err
			}

			savedPath = savePath.String()
			savedSize = size
			savedBody = string(body)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected extraction error: %v", err)
	}

	if savedPath != "cloudreve://my/extract/note.txt" {
		t.Fatalf("unexpected saved path: %q", savedPath)
	}
	if savedSize != int64(len("hello archive")) {
		t.Fatalf("unexpected saved size: %d", savedSize)
	}
	if savedBody != "hello archive" {
		t.Fatalf("unexpected saved body: %q", savedBody)
	}
	if cursor != "note.txt" {
		t.Fatalf("unexpected processed cursor: %q", cursor)
	}
	if countProgress.Current != 1 {
		t.Fatalf("unexpected progress count: %d", countProgress.Current)
	}
}

func TestExtractArchiveEntriesReturnsErrorWhenNoRegularFilesCanBeOpened(t *testing.T) {
	dst, err := fs.NewUriFromString("cloudreve://my/extract")
	if err != nil {
		t.Fatalf("failed to parse destination uri: %v", err)
	}

	err = extractArchiveEntries(
		context.Background(),
		fakeArchiveExtractor{
			entries: []archives.FileInfo{
				{
					FileInfo:      fakeArchiveEntryInfo{name: "docs", mode: iofs.ModeDir | 0o755, modTime: time.Now()},
					NameInArchive: "docs/",
				},
				{
					FileInfo:      fakeArchiveEntryInfo{name: "docs/readme.txt", size: 12, mode: 0o644, modTime: time.Now()},
					NameInArchive: "docs/readme.txt",
					Open: func() (iofs.File, error) {
						return nil, errors.New("boom")
					},
				},
			},
		},
		bytes.NewReader(nil),
		dst,
		nil,
		nil,
		&queue.Progress{},
		&queue.Progress{},
		logging.NewConsoleLogger(logging.LevelError),
		func(ctx context.Context, savePath *fs.URI) error { return nil },
		func(ctx context.Context, savePath *fs.URI, size int64, lastModified *time.Time, file io.ReadCloser) error {
			t.Fatalf("upload should not be invoked when open fails")
			return nil
		},
	)
	if err == nil {
		t.Fatal("expected extraction error when no regular files could be opened")
	}
	if !strings.Contains(err.Error(), `failed to open file "docs/readme.txt"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}
