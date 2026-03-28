package explorer

import (
	"encoding/base64"
	"net/url"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/pkg/cluster/routes"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
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

	resp := buildFullTextSidecarResponse("cloudreve:///my/report.docx", manifest, func(objectURI string) string {
		_, objectID, _, err := parseFullTextSidecarObjectURI(objectURI)
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}
		return routes.MasterFTSSidecarObjectContentUrl(base, buildFullTextSidecarObjectAccessToken(fullTextSidecarObjectAccess{
			FileID:   manifest.FileID,
			ObjectID: objectID,
		}), false).String()
	})
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
	expectedRootURI := (&url.URL{
		Scheme: constants.CloudreveScheme,
		User:   url.User(base64.RawURLEncoding.EncodeToString([]byte("cloudreve:///my/report.docx"))),
		Host:   fullTextSidecarVirtualFS,
		Path:   "/attachments/archive.zip",
	}).String()
	if root.URI != expectedRootURI {
		t.Fatalf("unexpected root uri: got %q want %q", root.URI, expectedRootURI)
	}
	rootAccess := mustObjectAccessFromURL(t, root.URL)
	if rootAccess.FileID != manifest.FileID {
		t.Fatalf("unexpected root access file id: got %d want %d", rootAccess.FileID, manifest.FileID)
	}
	if rootAccess.ObjectID != root.ID {
		t.Fatalf("unexpected root access object id: got %q want %q", rootAccess.ObjectID, root.ID)
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
	childAccess := mustObjectAccessFromURL(t, child.URL)
	if childAccess.FileID != manifest.FileID {
		t.Fatalf("unexpected child access file id: got %d want %d", childAccess.FileID, manifest.FileID)
	}
	if childAccess.ObjectID != child.ID {
		t.Fatalf("unexpected child access object id: got %q want %q", childAccess.ObjectID, child.ID)
	}
}

func TestBuildAndParseFullTextSidecarObjectURI(t *testing.T) {
	parent := "cloudreve://public/docs/report.zip"
	objectID := "attachments/archive.zip/nested.txt"

	raw := buildFullTextSidecarObjectURI(parent, objectID)
	parentURI, parsedObjectID, isSidecarURI, err := parseFullTextSidecarObjectURI(raw)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !isSidecarURI {
		t.Fatal("expected sidecar uri")
	}
	if parentURI.String() != parent {
		t.Fatalf("unexpected parent uri: got %q want %q", parentURI.String(), parent)
	}
	if parsedObjectID != objectID {
		t.Fatalf("unexpected object id: got %q want %q", parsedObjectID, objectID)
	}
}

func TestParseFullTextSidecarObjectURIIgnoresNormalURI(t *testing.T) {
	raw := "cloudreve://my/docs/report.zip"

	parentURI, parsedObjectID, isSidecarURI, err := parseFullTextSidecarObjectURI(raw)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if isSidecarURI {
		t.Fatal("did not expect sidecar uri")
	}
	if parentURI != nil {
		t.Fatalf("expected nil parent uri, got %v", parentURI)
	}
	if parsedObjectID != "" {
		t.Fatalf("expected empty object id, got %q", parsedObjectID)
	}
}

func TestBuildFullTextSidecarObjectURIHandlesNormalization(t *testing.T) {
	raw := buildFullTextSidecarObjectURI("cloudreve://my/docs/report.zip", "../attachments/./nested.txt")

	parentURI, objectID, isSidecarURI, err := parseFullTextSidecarObjectURI(raw)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !isSidecarURI {
		t.Fatal("expected sidecar uri")
	}
	if parentURI.String() != "cloudreve://my/docs/report.zip" {
		t.Fatalf("unexpected parent uri: %q", parentURI.String())
	}
	if objectID != "attachments/nested.txt" {
		t.Fatalf("unexpected normalized object id: %q", objectID)
	}
}

func TestParseFullTextSidecarObjectURIRejectsMissingParent(t *testing.T) {
	raw := (&url.URL{
		Scheme: constants.CloudreveScheme,
		Host:   fullTextSidecarVirtualFS,
		Path:   "/attachments/archive.zip",
	}).String()

	parentURI, objectID, isSidecarURI, err := parseFullTextSidecarObjectURI(raw)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !isSidecarURI {
		t.Fatal("expected sidecar uri")
	}
	if parentURI != nil {
		t.Fatalf("expected nil parent uri, got %v", parentURI)
	}
	if objectID != "" {
		t.Fatalf("expected empty object id, got %q", objectID)
	}
}

func TestParseFullTextSidecarObjectURIRejectsInvalidParent(t *testing.T) {
	raw := (&url.URL{
		Scheme: constants.CloudreveScheme,
		User:   url.User("%%%"),
		Host:   fullTextSidecarVirtualFS,
		Path:   "/attachments/archive.zip",
	}).String()

	parentURI, objectID, isSidecarURI, err := parseFullTextSidecarObjectURI(raw)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !isSidecarURI {
		t.Fatal("expected sidecar uri")
	}
	if parentURI != nil {
		t.Fatalf("expected nil parent uri, got %v", parentURI)
	}
	if objectID != "" {
		t.Fatalf("expected empty object id, got %q", objectID)
	}
}

func TestNormalizeFullTextSidecarObjectID(t *testing.T) {
	cases := map[string]string{
		"":                        "",
		".":                       "",
		"/":                       "",
		"attachments//nested.txt": "attachments/nested.txt",
		"./attachments/file.txt":  "attachments/file.txt",
	}

	for input, expected := range cases {
		if got := normalizeFullTextSidecarObjectID(input); got != expected {
			t.Fatalf("unexpected normalized id for %q: got %q want %q", input, got, expected)
		}
	}
}

func TestParseFullTextSidecarObjectURIReturnsParentAsFsURI(t *testing.T) {
	raw := buildFullTextSidecarObjectURI("cloudreve://user@my/docs/report.zip", "attachments/file.txt")

	parentURI, _, isSidecarURI, err := parseFullTextSidecarObjectURI(raw)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !isSidecarURI {
		t.Fatal("expected sidecar uri")
	}
	if _, ok := any(parentURI).(*fs.URI); !ok {
		t.Fatalf("expected fs.URI parent, got %T", parentURI)
	}
}

func TestBuildAndParseFullTextSidecarObjectAccessToken(t *testing.T) {
	token := buildFullTextSidecarObjectAccessToken(fullTextSidecarObjectAccess{
		FileID:   42,
		ObjectID: "attachments/nested.txt",
	})

	decoded, err := parseFullTextSidecarObjectAccessToken(token)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if decoded.FileID != 42 {
		t.Fatalf("unexpected decoded file id: got %d want %d", decoded.FileID, 42)
	}
	if decoded.ObjectID != "attachments/nested.txt" {
		t.Fatalf("unexpected decoded object id: got %q want %q", decoded.ObjectID, "attachments/nested.txt")
	}
}

func TestParseFullTextSidecarObjectAccessTokenRejectsInvalidInput(t *testing.T) {
	if _, err := parseFullTextSidecarObjectAccessToken("%%%"); err == nil {
		t.Fatal("expected parse error")
	}
}

func mustObjectAccessFromURL(t *testing.T, rawURL string) *fullTextSidecarObjectAccess {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("failed to parse url %q: %v", rawURL, err)
	}
	token := pathBase(parsed.Path)
	access, err := parseFullTextSidecarObjectAccessToken(token)
	if err != nil {
		t.Fatalf("failed to parse access token from %q: %v", rawURL, err)
	}

	return access
}

func pathBase(p string) string {
	if idx := len(p) - 1; idx >= 0 && p[idx] == '/' {
		p = p[:idx]
	}
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}

	return p
}
