package explorer

import (
	"net/url"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
)

func TestBuildFullTextSidecarResponseIncludesHierarchyAndIDBasedURLs(t *testing.T) {
	base, err := url.Parse("https://cloudreve.example")
	if err != nil {
		t.Fatalf("failed to parse base url: %v", err)
	}

	extractedAt := time.Unix(1700000000, 0).UTC()
	manifest := &manager.FTSSidecarManifest{
		Version:     1,
		FileID:      42,
		EntityID:    7,
		SourcePath:  "cloudreve:///my/report.docx",
		ExtractedAt: extractedAt,
		Objects: []manager.FTSSidecarArtifact{
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

	resp := buildFullTextSidecarResponse(base, "cloudreve:///my/report.docx", manifest)
	if resp == nil {
		t.Fatal("expected response")
	}
	if resp.FileID != manifest.FileID || resp.EntityID != manifest.EntityID {
		t.Fatalf("unexpected file or entity id: %+v", resp)
	}
	if !resp.ExtractedAt.Equal(extractedAt) {
		t.Fatalf("unexpected extracted_at: got %v want %v", resp.ExtractedAt, extractedAt)
	}
	if len(resp.Objects) != 2 {
		t.Fatalf("unexpected object count: got %d want 2", len(resp.Objects))
	}

	root := resp.Objects[0]
	if root.ID != "attachments/archive.zip" {
		t.Fatalf("unexpected root id: %q", root.ID)
	}
	if root.ParentID != "" {
		t.Fatalf("unexpected root parent id: %q", root.ParentID)
	}
	if root.Kind != "archive" {
		t.Fatalf("unexpected root kind: %q", root.Kind)
	}
	if got := mustQueryValue(t, root.URL, "name"); got != root.ID {
		t.Fatalf("unexpected root url name query: %q", got)
	}
	if got := mustQueryValue(t, root.URL, "uri"); got != "cloudreve:///my/report.docx" {
		t.Fatalf("unexpected root url uri query: %q", got)
	}

	child := resp.Objects[1]
	if child.ParentID != "attachments/archive.zip" {
		t.Fatalf("unexpected child parent id: %q", child.ParentID)
	}
	if child.Depth != 1 {
		t.Fatalf("unexpected child depth: %d", child.Depth)
	}
	if child.Kind != "embedded" {
		t.Fatalf("unexpected child kind: %q", child.Kind)
	}
	if got := mustQueryValue(t, child.URL, "name"); got != child.ID {
		t.Fatalf("unexpected child url name query: %q", got)
	}
}

func mustQueryValue(t *testing.T, rawURL, key string) string {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("failed to parse url %q: %v", rawURL, err)
	}

	return parsed.Query().Get(key)
}
