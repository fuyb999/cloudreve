package manager

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	tikaextractor "github.com/cloudreve/Cloudreve/v4/pkg/searcher/extractor"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

func TestBuildFTSSearchPathTextPrefersPublicPath(t *testing.T) {
	ownerBase, err := fs.NewUriFromString(fs.NewMyUri("owner"))
	if err != nil {
		t.Fatalf("failed to create owner uri: %v", err)
	}
	ownerURI := ownerBase.Join("公共文件", "研发部", "计划 说明.txt")
	publicURI := publicshare.BuildPublicURI().Join("研发部", "计划 说明.txt")

	pathText := buildFTSSearchPathText(ownerURI, publicURI)
	if got, want := pathText, "cloudreve://public/研发部/计划 说明.txt\ncloudreve://owner@my/公共文件/研发部/计划 说明.txt"; got != want {
		t.Fatalf("unexpected path text: got %q want %q", got, want)
	}
}

func TestBuildFTSSearchPathTextKeepsOwnerPathForNonPublicFile(t *testing.T) {
	ownerBase, err := fs.NewUriFromString(fs.NewMyUri("owner"))
	if err != nil {
		t.Fatalf("failed to create owner uri: %v", err)
	}
	ownerURI := ownerBase.Join("docs", "readme.txt")

	pathText := buildFTSSearchPathText(ownerURI, nil)
	if got, want := pathText, "cloudreve://owner@my/docs/readme.txt"; got != want {
		t.Fatalf("unexpected path text: got %q want %q", got, want)
	}
}

func TestResolvePublicSearchURIBuildsPublicPathFromRootAncestors(t *testing.T) {
	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hasher: %v", err)
	}

	fileModel := &ent.File{
		ID:      12,
		OwnerID: 1,
		Name:    "说明.txt",
	}
	fileClient := &testFileClient{
		ancestorByID: map[int][]*ent.File{
			12: {
				{ID: 1, Name: inventory.RootFolderName},
				{ID: 9, Name: publicshare.DefaultRootName},
				{ID: 10, Name: "研发部"},
				{ID: 12, Name: "说明.txt"},
			},
		},
	}
	m := &manager{
		l:      logging.NewConsoleLogger(logging.LevelError),
		dep:    testDep{fileClient: fileClient, settingClient: testSettingClient{values: map[string]string{publicshare.PublicRootFileIDSetting: "9"}}, registry: queue.NewTaskRegistry()},
		hasher: hasher,
	}

	got := m.resolvePublicSearchURI(context.Background(), fileModel)
	if got == nil {
		t.Fatalf("expected public uri")
	}
	want := mustURI(t, "cloudreve://public/研发部/说明.txt")
	if got.String() != want.String() {
		t.Fatalf("unexpected public uri: got %q want %q", got.String(), want.String())
	}
}

func TestParseTikaRMetaAttachmentsDropsMarkupOnlyImageContent(t *testing.T) {
	raw, err := json.Marshal([]map[string]any{
		{
			"X-TIKA:embedded_resource_path": "images/image1.png",
			"resourceName":                  "image1.png",
			"Content-Type":                  "image/png",
			"X-TIKA:content":                "<html xmlns=\"http://www.w3.org/1999/xhtml\"><head><meta name=\"dummy\" /></head><body><p/></body></html>",
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal rmeta payload: %v", err)
	}

	attachments := parseTikaRMetaAttachments(raw)
	if len(attachments) != 1 {
		t.Fatalf("unexpected attachment count: got %d want 1", len(attachments))
	}
	if attachments[0].Content != "" {
		t.Fatalf("expected image attachment content to be empty, got %q", attachments[0].Content)
	}
}

func TestParseTikaRMetaAttachmentsConvertsMarkupToPlainText(t *testing.T) {
	raw, err := json.Marshal([]map[string]any{
		{
			"X-TIKA:embedded_resource_path": "embedded/note.txt",
			"resourceName":                  "note.txt",
			"Content-Type":                  "text/plain",
			"X-TIKA:content":                "<html><body><p>Hello <b>Cloudreve</b></p><p>Search</p></body></html>",
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal rmeta payload: %v", err)
	}

	attachments := parseTikaRMetaAttachments(raw)
	if len(attachments) != 1 {
		t.Fatalf("unexpected attachment count: got %d want 1", len(attachments))
	}
	if got, want := attachments[0].Content, "Hello Cloudreve Search"; got != want {
		t.Fatalf("unexpected attachment content: got %q want %q", got, want)
	}
}

func TestBuildEmbeddedSearchAttachmentsSkipsSyntheticTikaArtifacts(t *testing.T) {
	rmetaRaw, err := json.Marshal([]map[string]any{
		{
			"X-TIKA:embedded_resource_path": "__TEXT__",
			"resourceName":                  "__TEXT__",
			"Content-Type":                  "text/plain",
			"X-TIKA:content":                "redundant",
		},
		{
			"X-TIKA:embedded_resource_path": "__METADATA__",
			"resourceName":                  "__METADATA__",
			"Content-Type":                  "application/json",
		},
		{
			"X-TIKA:embedded_resource_path": "nested/note.txt",
			"resourceName":                  "note.txt",
			"Content-Type":                  "text/plain",
			"X-TIKA:content":                "hello cloudreve",
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal rmeta payload: %v", err)
	}

	unpackRaw := buildZipForTest(t, map[string][]byte{
		"__TEXT__":        []byte("redundant"),
		"__METADATA__":    []byte(`{"k":"v"}`),
		"nested/note.txt": []byte("hello cloudreve"),
	})

	attachments := buildEmbeddedSearchAttachments(
		&ent.File{ID: 12, OwnerID: 1, Name: "archive.zip"},
		mustURI(t, "cloudreve:///docs/archive.zip"),
		&testEntity{id: 4},
		rmetaRaw,
		unpackRaw,
		nil,
	)

	if len(attachments) != 1 {
		t.Fatalf("unexpected attachment count: got %d want 1", len(attachments))
	}
	if got, want := attachments[0].Name, "note.txt"; got != want {
		t.Fatalf("unexpected attachment name: got %q want %q", got, want)
	}
	if got, want := attachments[0].Content, "hello cloudreve"; got != want {
		t.Fatalf("unexpected attachment content: got %q want %q", got, want)
	}
	if got, want := attachments[0].ParentID, attachmentRootParentID(12); got != want {
		t.Fatalf("unexpected attachment parent id: got %q want %q", got, want)
	}
	if got, want := attachments[0].ParentID, "12"; got != want {
		t.Fatalf("unexpected root attachment parent id: got %q want %q", got, want)
	}
}

func TestBuildEmbeddedSearchAttachmentsFromManifestSkipsSyntheticTikaArtifacts(t *testing.T) {
	rmetaRaw, err := json.Marshal([]map[string]any{
		{
			"X-TIKA:embedded_resource_path": "nested/note.txt",
			"resourceName":                  "note.txt",
			"Content-Type":                  "text/plain",
			"X-TIKA:content":                "hello cloudreve",
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal rmeta payload: %v", err)
	}

	attachments := buildEmbeddedSearchAttachmentsFromManifest(
		&ent.File{ID: 12},
		&testEntity{id: 4},
		&FTSSidecarManifest{
			Objects: []FTSSidecarArtifact{
				{
					ID:   "attachments/__TEXT__",
					Kind: "embedded",
					Name: "__TEXT__",
					Path: "cloudreve/fts-sidecar/1/12/4/attachments/__TEXT__",
				},
				{
					ID:   "attachments/__METADATA__",
					Kind: "embedded",
					Name: "__METADATA__",
					Path: "cloudreve/fts-sidecar/1/12/4/attachments/__METADATA__",
				},
				{
					ID:       "attachments/nested/note.txt",
					Kind:     "embedded",
					Name:     "note.txt",
					Path:     "cloudreve/fts-sidecar/1/12/4/attachments/nested/note.txt",
					MimeType: "text/plain",
				},
			},
		},
		rmetaRaw,
	)

	if len(attachments) != 1 {
		t.Fatalf("unexpected attachment count: got %d want 1", len(attachments))
	}
	if got, want := attachments[0].Path, "cloudreve/fts-sidecar/1/12/4/attachments/nested/note.txt"; got != want {
		t.Fatalf("unexpected attachment path: got %q want %q", got, want)
	}
	if got, want := attachments[0].Content, "hello cloudreve"; got != want {
		t.Fatalf("unexpected attachment content: got %q want %q", got, want)
	}
	if got, want := attachments[0].ParentID, attachmentRootParentID(12); got != want {
		t.Fatalf("unexpected attachment parent id: got %q want %q", got, want)
	}
	if got, want := attachments[0].ParentID, "12"; got != want {
		t.Fatalf("unexpected root attachment parent id: got %q want %q", got, want)
	}
}

func TestBuildFTSExtractionPlanForcesFreshExtractionWhenRebuildDoesNotSkip(t *testing.T) {
	settings := testSettingProvider{
		tikaCfg: &setting.FTSTikaExtractorSetting{
			Endpoint:    "http://tika:9998",
			MaxFileSize: 20 << 20,
		},
	}

	plan := buildFTSExtractionPlan(
		FTSBuildOptions{
			ForceTextExtraction:       true,
			ForceAttachmentExtraction: true,
		},
		tikaextractor.NewTikaExtractor(nil, settings, logging.NewConsoleLogger(logging.LevelError), settings.tikaCfg),
		&ent.File{Name: "report.pdf", Size: 1024},
		"cached text",
		[]searcher.SearchAttachmentDocument{{ID: "cached-attachment"}},
		true,
		&FTSSidecarManifest{TextReady: true, AssetsReady: true},
		true,
		true,
	)

	if plan.ReuseSidecarText {
		t.Fatal("expected text sidecar reuse to be disabled when forcing fresh extraction")
	}
	if plan.ReuseSidecarAttachments {
		t.Fatal("expected attachment sidecar reuse to be disabled when forcing fresh extraction")
	}
	if !plan.NeedTextExtraction {
		t.Fatal("expected text extraction to be required when forcing fresh extraction")
	}
	if !plan.NeedAttachmentExtraction {
		t.Fatal("expected attachment extraction to be required when forcing fresh extraction")
	}
	if !plan.ShouldPersistSidecar {
		t.Fatal("expected sidecar to be refreshed when forcing fresh extraction")
	}
}

func TestBuildFTSExtractionPlanReusesReadySidecarWhenSkipEnabled(t *testing.T) {
	plan := buildFTSExtractionPlan(
		FTSBuildOptions{
			SkipTextExtraction:       true,
			SkipAttachmentExtraction: true,
		},
		testTextExtractor{exts: []string{".pdf"}, maxFileSize: 20 << 20},
		&ent.File{Name: "report.pdf", Size: 1024},
		"cached text",
		[]searcher.SearchAttachmentDocument{{ID: "cached-attachment"}},
		true,
		&FTSSidecarManifest{TextReady: true, AssetsReady: true},
		true,
		true,
	)

	if !plan.ReuseSidecarText {
		t.Fatal("expected text sidecar reuse to stay enabled when skipping extraction")
	}
	if !plan.ReuseSidecarAttachments {
		t.Fatal("expected attachment sidecar reuse to stay enabled when skipping extraction")
	}
	if plan.NeedTextExtraction {
		t.Fatal("expected text extraction to be skipped")
	}
	if plan.NeedAttachmentExtraction {
		t.Fatal("expected attachment extraction to be skipped")
	}
	if plan.ShouldPersistSidecar {
		t.Fatal("expected no sidecar refresh when both extraction steps are skipped")
	}
}

func TestBuildFTSExtractionPlanExtractsAttachmentsWithoutSidecarPersistence(t *testing.T) {
	settings := testSettingProvider{
		tikaCfg: &setting.FTSTikaExtractorSetting{
			Endpoint:    "http://tika:9998",
			MaxFileSize: 20 << 20,
		},
	}

	plan := buildFTSExtractionPlan(
		FTSBuildOptions{},
		tikaextractor.NewTikaExtractor(nil, settings, logging.NewConsoleLogger(logging.LevelError), settings.tikaCfg),
		&ent.File{Name: "archive.zip", Size: 1024},
		"",
		nil,
		false,
		nil,
		false,
		false,
	)

	if !plan.NeedAttachmentExtraction {
		t.Fatal("expected embedded attachments to be extracted even when sidecar persistence is disabled")
	}
	if plan.ShouldPersistSidecar {
		t.Fatal("expected no sidecar persistence when sidecar switches are disabled")
	}
}
