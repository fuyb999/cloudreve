package setting

import (
	"context"
	"reflect"
	"testing"
)

func TestFTSTikaExtractorBuildsActiveExtsByCategory(t *testing.T) {
	provider := NewProvider(&staticSettingStore{
		settings: map[string]any{
			"fts_tika_document_enabled": "1",
			"fts_tika_document_exts":    "pdf,docx,txt",
			"fts_tika_archive_enabled":  "1",
			"fts_tika_archive_exts":     "zip,rar",
		},
		next: NewDbDefaultStore(nil),
	})

	cfg := provider.FTSTikaExtractor(context.Background())

	if !cfg.DocumentEnabled {
		t.Fatal("expected document extraction to be enabled")
	}
	if !cfg.ArchiveEnabled {
		t.Fatal("expected archive extraction to be enabled")
	}
	if !reflect.DeepEqual(cfg.DocumentExts, []string{"pdf", "docx", "txt"}) {
		t.Fatalf("unexpected document exts: %#v", cfg.DocumentExts)
	}
	if !reflect.DeepEqual(cfg.ArchiveExts, []string{"zip", "rar"}) {
		t.Fatalf("unexpected archive exts: %#v", cfg.ArchiveExts)
	}
	if !reflect.DeepEqual(cfg.Exts, []string{"pdf", "docx", "txt", "zip", "rar"}) {
		t.Fatalf("unexpected active exts: %#v", cfg.Exts)
	}
}

func TestFTSTikaExtractorRespectsCategoryToggles(t *testing.T) {
	provider := NewProvider(&staticSettingStore{
		settings: map[string]any{
			"fts_tika_document_enabled": "0",
			"fts_tika_document_exts":    "pdf,docx",
			"fts_tika_archive_enabled":  "1",
			"fts_tika_archive_exts":     "zip,rar",
		},
		next: NewDbDefaultStore(nil),
	})

	cfg := provider.FTSTikaExtractor(context.Background())

	if cfg.DocumentEnabled {
		t.Fatal("expected document extraction to be disabled")
	}
	if !cfg.ArchiveEnabled {
		t.Fatal("expected archive extraction to be enabled")
	}
	if !reflect.DeepEqual(cfg.Exts, []string{"zip", "rar"}) {
		t.Fatalf("unexpected active exts: %#v", cfg.Exts)
	}
}

func TestNormalizeTikaExtsTrimsDotsSpacesAndDuplicates(t *testing.T) {
	got := normalizeTikaExts([]string{" .PDF ", "pdf", "Docx", ".docx", "", " zip "})
	want := []string{"pdf", "docx", "zip"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected normalized exts: got %#v want %#v", got, want)
	}
}

func TestDefaultFTSTikaDocumentExtsCoverExpandedOfficialFormats(t *testing.T) {
	for _, ext := range []string{
		"fb2", "chm", "mif",
		"hwp", "one", "wpd", "qpw",
		"xlam", "sldx", "vstx", "vssx", "xps", "dwfx",
		"eml", "msg", "pst", "mbox", "tnef",
	} {
		if !containsString(defaultFTSTikaDocumentExts, ext) {
			t.Fatalf("expected default tika document exts to contain %q", ext)
		}
	}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}

	return false
}
