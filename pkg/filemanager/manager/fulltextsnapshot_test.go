package manager

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	inventorytypes "github.com/cloudreve/Cloudreve/v4/inventory/types"
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

func TestBuildFTSSearchPathsIncludesAncestorScopes(t *testing.T) {
	ownerBase, err := fs.NewUriFromString(fs.NewMyUri("owner"))
	if err != nil {
		t.Fatalf("failed to create owner uri: %v", err)
	}
	ownerURI := ownerBase.Join("docs", "reports", "readme.txt")
	publicURI := publicshare.BuildPublicURI().Join("reports", "readme.txt")

	got := buildFTSSearchPaths(ownerURI, publicURI)
	want := []string{
		"cloudreve://public",
		"cloudreve://public/reports",
		"cloudreve://public/reports/readme.txt",
		"cloudreve://owner@my",
		"cloudreve://owner@my/docs",
		"cloudreve://owner@my/docs/reports",
		"cloudreve://owner@my/docs/reports/readme.txt",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected search paths: got %#v want %#v", got, want)
	}
}

func TestMetadataSearchFieldsExtractTagsAndCustomProps(t *testing.T) {
	metadata := map[string]string{
		"tag:important": "1",
		"props:review":  "approved",
		"music:title":   "Song",
	}

	if got, want := metadataKeys(metadata), []string{"music:title", "props:review", "tag:important"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected metadata keys: got %#v want %#v", got, want)
	}
	if got, want := metadataTags(metadata), []string{"important"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected tags: got %#v want %#v", got, want)
	}
	if got := metadataCustomProps(metadata); !reflect.DeepEqual(got, map[string]any{"review": "approved"}) {
		t.Fatalf("unexpected custom props: got %#v", got)
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

func TestBuildEmbeddedSearchAttachmentsFromManifestPrefersAttachmentTextSidecar(t *testing.T) {
	rmetaRaw, err := json.Marshal([]map[string]any{
		{
			"X-TIKA:embedded_resource_path": "nested/note.txt",
			"resourceName":                  "note.txt",
			"Content-Type":                  "text/plain",
			"X-TIKA:content":                "inline content should stay out of manifest docs",
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal rmeta payload: %v", err)
	}

	textSidecarPath := "cloudreve/fts-sidecar/1/12/4/attachment-text/attachments/nested/note.txt.txt"
	attachments := buildEmbeddedSearchAttachmentsFromManifest(
		&ent.File{ID: 12},
		&testEntity{id: 4},
		&FTSSidecarManifest{
			Objects: []FTSSidecarArtifact{
				{
					ID:       "attachments/nested/note.txt",
					Kind:     "embedded",
					Name:     "note.txt",
					Path:     "cloudreve/fts-sidecar/1/12/4/attachments/nested/note.txt",
					MimeType: "text/plain",
				},
				{
					ID:       sidecarAttachmentTextObjectID("attachments/nested/note.txt"),
					ParentID: "attachments/nested/note.txt",
					Kind:     "attachment_text",
					Name:     "note.txt.txt",
					Path:     textSidecarPath,
					MimeType: "text/plain; charset=utf-8",
				},
			},
		},
		rmetaRaw,
	)

	if len(attachments) != 1 {
		t.Fatalf("unexpected attachment count: got %d want 1", len(attachments))
	}
	if got, want := attachments[0].Source, textSidecarPath; got != want {
		t.Fatalf("unexpected attachment source: got %q want %q", got, want)
	}
	if got := attachments[0].Content; got != "" {
		t.Fatalf("expected inline rmeta content to be skipped when sidecar text exists, got %q", got)
	}
}

func TestBuildEmbeddedSearchAttachmentsFromManifestSkipsOCRCandidateArtifact(t *testing.T) {
	attachments := buildEmbeddedSearchAttachmentsFromManifest(
		&ent.File{ID: 12},
		&testEntity{id: 4},
		&FTSSidecarManifest{
			Objects: []FTSSidecarArtifact{
				{
					ID:       "attachments/nested/image1.png",
					Kind:     "embedded",
					Name:     "image1.png",
					Path:     "cloudreve/fts-sidecar/1/12/4/attachments/nested/image1.png",
					MimeType: "image/png",
					Size:     1024,
				},
				{
					ID:       "legacy-ocr-artifact.bin",
					Kind:     "ocr_candidates",
					Name:     "legacy-ocr-artifact.bin",
					Path:     "cloudreve/fts-sidecar/1/12/4/legacy-ocr-artifact.bin",
					MimeType: "application/json",
					Size:     256,
				},
				{
					ID:       "legacy-diagnostics-artifact.bin",
					Kind:     "diagnostics",
					Name:     "legacy-diagnostics-artifact.bin",
					Path:     "cloudreve/fts-sidecar/1/12/4/legacy-diagnostics-artifact.bin",
					MimeType: "application/json",
					Size:     128,
				},
				{
					ID:       "legacy-external-attachments-artifact.bin",
					Kind:     "external_attachments",
					Name:     "legacy-external-attachments-artifact.bin",
					Path:     "cloudreve/fts-sidecar/1/12/4/legacy-external-attachments-artifact.bin",
					MimeType: "application/json",
					Size:     512,
				},
			},
		},
		nil,
	)

	if len(attachments) != 1 {
		t.Fatalf("unexpected attachment count: got %d want 1", len(attachments))
	}
	if got, want := attachments[0].Name, "image1.png"; got != want {
		t.Fatalf("unexpected attachment name: got %q want %q", got, want)
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

func TestBuildFTSExtractionPlanReusesExternalSidecarWithoutLocalFallback(t *testing.T) {
	plan := buildFTSExtractionPlan(
		FTSBuildOptions{},
		testTextExtractor{exts: []string{".txt"}, maxFileSize: 20 << 20},
		&ent.File{Name: "probe.txt", Size: 40},
		"",
		nil,
		true,
		&FTSSidecarManifest{
			Provider: ftsSidecarProviderExternal,
		},
		true,
		true,
		true,
		true,
	)

	if !plan.ReuseSidecarText {
		t.Fatal("expected external sidecar text to be reused")
	}
	if !plan.ReuseSidecarAttachments {
		t.Fatal("expected external sidecar attachments to be reused")
	}
	if plan.NeedTextExtraction {
		t.Fatal("expected no local text extraction when external sidecar already exists")
	}
	if plan.NeedAttachmentExtraction {
		t.Fatal("expected no local attachment extraction when external sidecar already exists")
	}
	if plan.ShouldPersistSidecar {
		t.Fatal("expected no sidecar persistence refresh for external sidecar reuse")
	}
}

func TestResolveOwnerFTSURIPreservesOwnerPathForPublicSubtree(t *testing.T) {
	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hasher: %v", err)
	}

	fileModel := &ent.File{
		ID:      12,
		OwnerID: 7,
		Name:    "说明.txt",
	}
	ownerRoot := &ent.File{ID: 1, Name: inventory.RootFolderName, OwnerID: 7, Type: int(inventorytypes.FileTypeFolder)}
	dept := &ent.File{ID: 10, Name: "研发部", OwnerID: 7, Type: int(inventorytypes.FileTypeFolder)}
	target := &ent.File{ID: 12, Name: "说明.txt", OwnerID: 7, Type: int(inventorytypes.FileTypeFile)}
	fileClient := &testFileClient{
		ancestorByID: map[int][]*ent.File{
			12: {
				{ID: 1, Name: inventory.RootFolderName},
				{ID: 9, Name: publicshare.DefaultRootName},
				{ID: 10, Name: "研发部"},
				{ID: 12, Name: "说明.txt"},
			},
		},
		fileByID: map[int]*ent.File{
			12: target,
		},
		rootByOwner: map[int]*ent.File{
			7: ownerRoot,
		},
		childByParentName: map[int]map[string]*ent.File{
			ownerRoot.ID: {dept.Name: dept},
			dept.ID:      {target.Name: target},
		},
	}
	m := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		user:     &ent.User{ID: 7},
		hasher:   hasher,
		settings: testSettingProvider{},
		config:   testConfigProvider{},
		dep: testDep{
			settings:      testSettingProvider{},
			config:        testConfigProvider{},
			fileClient:    fileClient,
			settingClient: testSettingClient{values: map[string]string{publicshare.PublicRootFileIDSetting: "9"}},
			registry:      queue.NewTaskRegistry(),
		},
	}

	ownerManager, err := m.fileManagerForOwner(context.Background(), fileModel.OwnerID)
	if err != nil {
		t.Fatalf("failed to construct owner manager: %v", err)
	}
	defer ownerManager.Recycle()

	got, err := m.resolveOwnerFTSURI(context.Background(), fileModel, ownerManager)
	if err != nil {
		t.Fatalf("resolveOwnerFTSURI returned error: %v", err)
	}
	want := mustURI(t, "cloudreve://my/公共文件/研发部/说明.txt")
	if got == nil || got.String() != want.String() {
		t.Fatalf("unexpected owner uri: got %v want %s", got, want.String())
	}
}

func TestBuildFTSExtractionPlanSkipsEmptyFileExtractionAndSidecarPersistence(t *testing.T) {
	settings := testSettingProvider{
		tikaCfg: &setting.FTSTikaExtractorSetting{
			Endpoint:    "http://tika:9998",
			MaxFileSize: 20 << 20,
		},
	}

	plan := buildFTSExtractionPlan(
		FTSBuildOptions{},
		tikaextractor.NewTikaExtractor(nil, settings, logging.NewConsoleLogger(logging.LevelError), settings.tikaCfg),
		&ent.File{Name: "empty.pdf", Size: 0},
		"",
		nil,
		false,
		nil,
		true,
		true,
		true,
		true,
	)

	if plan.NeedTextExtraction {
		t.Fatal("expected empty file to skip text extraction")
	}
	if plan.NeedAttachmentExtraction {
		t.Fatal("expected empty file to skip attachment extraction")
	}
	if plan.ShouldPersistSidecar {
		t.Fatal("expected empty file to skip sidecar persistence")
	}
}
