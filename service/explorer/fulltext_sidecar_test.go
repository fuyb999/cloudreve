package explorer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/pkg/cluster/routes"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
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
				ID:       "attachment-text/attachments/archive.zip/nested.txt.txt",
				ParentID: "attachments/archive.zip/nested.txt",
				Kind:     "attachment_text",
				Name:     "nested.txt.txt",
				Path:     "cloudreve/fts-sidecar/1/42/7/attachment-text/attachments/archive.zip/nested.txt.txt",
				MimeType: "text/plain; charset=utf-8",
				Size:     6,
			},
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
	if child.PreviewURL == "" {
		t.Fatal("expected child preview_url to point at attachment text sidecar")
	}
	childPreviewAccess := mustObjectAccessFromURL(t, child.PreviewURL)
	if childPreviewAccess.ObjectID != "attachment-text/attachments/archive.zip/nested.txt.txt" {
		t.Fatalf("unexpected child preview access object id: got %q", childPreviewAccess.ObjectID)
	}
	childAccess := mustObjectAccessFromURL(t, child.URL)
	if childAccess.FileID != manifest.FileID {
		t.Fatalf("unexpected child access file id: got %d want %d", childAccess.FileID, manifest.FileID)
	}
	if childAccess.ObjectID != child.ID {
		t.Fatalf("unexpected child access object id: got %q want %q", childAccess.ObjectID, child.ID)
	}
}

func TestBuildFullTextSidecarResponseFiltersHelperArtifacts(t *testing.T) {
	manifest := &manager.FTSSidecarManifest{
		Version:     1,
		FileID:      42,
		EntityID:    7,
		SourcePath:  "cloudreve:///my/report.docx",
		ExtractedAt: time.Unix(1700000000, 0).UTC(),
		Objects: []manager.FTSSidecarArtifact{
			{ID: "manifest.json", Kind: "", Name: "manifest.json"},
			{ID: "rmeta.json", Kind: "metadata", Name: "rmeta.json"},
			{ID: "attachment-text/attachments/nested.txt.txt", Kind: "attachment_text", Name: "nested.txt.txt"},
			{ID: "legacy-diagnostics-artifact.bin", Kind: "diagnostics", Name: "legacy-diagnostics-artifact.bin"},
			{ID: "attachments/nested.txt", Kind: "embedded", Name: "nested.txt", Path: "cloudreve/fts-sidecar/1/42/7/attachments/nested.txt"},
		},
	}

	resp := buildFullTextSidecarResponse("cloudreve:///my/report.docx", manifest, func(objectURI string) string {
		return objectURI
	})
	if resp == nil {
		t.Fatal("expected response")
	}
	if len(resp.Objects) != 1 {
		t.Fatalf("unexpected visible object count: got %d want 1", len(resp.Objects))
	}
	if got, want := resp.Objects[0].ID, "attachments/nested.txt"; got != want {
		t.Fatalf("unexpected visible object id: got %q want %q", got, want)
	}
}

func TestBuildFullTextSidecarResponseKeepsEmptyObjectsArray(t *testing.T) {
	manifest := &manager.FTSSidecarManifest{
		Version:     1,
		FileID:      42,
		EntityID:    7,
		SourcePath:  "cloudreve:///public/install.sh",
		ExtractedAt: time.Unix(1700000000, 0).UTC(),
		Objects: []manager.FTSSidecarArtifact{
			{ID: "manifest.json", Kind: "", Name: "manifest.json"},
			{ID: "rmeta.json", Kind: "metadata", Name: "rmeta.json"},
			{ID: "legacy-diagnostics-artifact.bin", Kind: "diagnostics", Name: "legacy-diagnostics-artifact.bin"},
		},
	}

	resp := buildFullTextSidecarResponse("cloudreve://public/install.sh", manifest, func(objectURI string) string {
		return objectURI
	})
	if resp == nil {
		t.Fatal("expected response")
	}
	if resp.Objects == nil {
		t.Fatal("expected non-nil objects slice")
	}
	if len(resp.Objects) != 0 {
		t.Fatalf("unexpected visible object count: got %d want 0", len(resp.Objects))
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}
	if string(raw) == "" || !containsJSONObjectsArray(raw) {
		t.Fatalf("expected marshaled response to include empty objects array, got %s", string(raw))
	}
}

func TestBuildFullTextSidecarResponseFiltersLegacyArchiveRootContent(t *testing.T) {
	manifest := &manager.FTSSidecarManifest{
		Version:     1,
		Provider:    "tika",
		FileID:      25,
		EntityID:    16,
		SourcePath:  "cloudreve://public/omx-tika-pubzip-20260501-142102.zip",
		ExtractedAt: time.Unix(1700000000, 0).UTC(),
		Objects: []manager.FTSSidecarArtifact{
			{ID: "content.txt", Kind: "text", Name: "content.txt", Path: "cloudreve/fts-sidecar/1/25/16/content.txt"},
			{ID: "attachments/nested/note.txt", Kind: "embedded", Name: "note.txt", Path: "cloudreve/fts-sidecar/1/25/16/attachments/nested/note.txt"},
		},
	}

	resp := buildFullTextSidecarResponse("cloudreve://public/omx-tika-pubzip-20260501-142102.zip", manifest, func(objectURI string) string {
		return objectURI
	})
	if resp == nil {
		t.Fatal("expected response")
	}
	if len(resp.Objects) != 1 {
		t.Fatalf("unexpected visible object count: got %d want 1", len(resp.Objects))
	}
	if got, want := resp.Objects[0].ID, "attachments/nested/note.txt"; got != want {
		t.Fatalf("unexpected visible object id: got %q want %q", got, want)
	}
}

func containsJSONObjectsArray(raw []byte) bool {
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return false
	}

	value, ok := parsed["objects"]
	if !ok {
		return false
	}

	items, ok := value.([]any)
	return ok && len(items) == 0
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
	visibility := publicshare.EncodeVisibilityOverride(&publicshare.VisibilityResult{
		RootGrants: []publicshare.RootGrant{
			{
				RootFileID:   42,
				RootOwnerID:  7,
				RootTreePath: "1.42",
				Actions: map[publicshare.Action]bool{
					publicshare.ActionList:     true,
					publicshare.ActionDownload: true,
				},
			},
		},
	})
	token := buildFullTextSidecarObjectAccessToken(fullTextSidecarObjectAccess{
		FileID:           42,
		ObjectID:         "attachments/nested.txt",
		ParentURI:        "cloudreve://public/install.sh__daxfb",
		PublicVisibility: visibility,
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
	if decoded.ParentURI != "cloudreve://public/install.sh__daxfb" {
		t.Fatalf("unexpected decoded parent uri: got %q", decoded.ParentURI)
	}
	if decoded.PublicVisibility == "" {
		t.Fatal("expected public visibility to round trip")
	}
	decodedVisibility, err := publicshare.DecodeVisibilityOverride(decoded.PublicVisibility)
	if err != nil {
		t.Fatalf("failed to decode public visibility: %v", err)
	}
	if decodedVisibility == nil || len(decodedVisibility.RootGrants) != 1 || decodedVisibility.RootGrants[0].RootFileID != 42 {
		t.Fatalf("unexpected decoded public visibility: %+v", decodedVisibility)
	}
}

func TestFullTextSidecarObjectAccessCarriesPublicVisibilityFromContext(t *testing.T) {
	visibility := &publicshare.VisibilityResult{
		RootGrants: []publicshare.RootGrant{
			{
				RootFileID:   42,
				RootOwnerID:  7,
				RootTreePath: "1.42",
				Actions: map[publicshare.Action]bool{
					publicshare.ActionList:     true,
					publicshare.ActionDownload: true,
				},
			},
		},
	}

	access := withFullTextSidecarPublicVisibility(
		withPublicVisibilityContext(visibility),
		fullTextSidecarObjectAccess{
			FileID:    42,
			ObjectID:  "attachments/nested.txt",
			ParentURI: "cloudreve://public/install.sh",
		},
	)

	if access.PublicVisibility == "" {
		t.Fatal("expected public visibility to be carried for public parent uri")
	}
	decoded, err := publicshare.DecodeVisibilityOverride(access.PublicVisibility)
	if err != nil {
		t.Fatalf("failed to decode public visibility: %v", err)
	}
	if decoded == nil || len(decoded.RootGrants) != 1 || decoded.RootGrants[0].RootFileID != 42 {
		t.Fatalf("unexpected public visibility: %+v", decoded)
	}
}

func TestFullTextSidecarObjectAccessSkipsPublicVisibilityForPrivateParent(t *testing.T) {
	visibility := &publicshare.VisibilityResult{
		RootGrants: []publicshare.RootGrant{{RootFileID: 42}},
	}

	access := withFullTextSidecarPublicVisibility(
		withPublicVisibilityContext(visibility),
		fullTextSidecarObjectAccess{
			FileID:    42,
			ObjectID:  "attachments/nested.txt",
			ParentURI: "cloudreve://my/install.sh",
		},
	)

	if access.PublicVisibility != "" {
		t.Fatalf("did not expect public visibility for private parent uri, got %q", access.PublicVisibility)
	}
}

func withPublicVisibilityContext(visibility *publicshare.VisibilityResult) context.Context {
	return context.WithValue(context.Background(), publicshare.VisibilityOverrideCtx{}, visibility)
}

func TestParseFullTextSidecarObjectAccessTokenRejectsInvalidInput(t *testing.T) {
	if _, err := parseFullTextSidecarObjectAccessToken("%%%"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestResolveFullTextSidecarParentURI(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: ""},
		{name: "public", raw: "cloudreve://public/install.sh__daxfb", want: "cloudreve://public/install.sh__daxfb"},
	}

	for _, test := range tests {
		got, err := resolveFullTextSidecarParentURI(test.raw)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", test.name, err)
		}
		if test.want == "" {
			if got != nil {
				t.Fatalf("%s: expected nil uri, got %v", test.name, got)
			}
			continue
		}
		if got == nil || got.String() != test.want {
			t.Fatalf("%s: unexpected parent uri: got %v want %q", test.name, got, test.want)
		}
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
