package admin

import (
	"context"
	"testing"
)

func TestContentProcessingQueueSettingsRegistered(t *testing.T) {
	keys := []string{
		"queue_media_meta_worker_num",
		"queue_media_meta_max_execution",
		"queue_media_meta_backoff_factor",
		"queue_media_meta_backoff_max_duration",
		"queue_media_meta_max_retry",
		"queue_media_meta_retry_delay",
		"queue_content_processing_worker_num",
		"queue_content_processing_max_execution",
		"queue_content_processing_backoff_factor",
		"queue_content_processing_backoff_max_duration",
		"queue_content_processing_max_retry",
		"queue_content_processing_retry_delay",
	}

	for _, key := range keys {
		if _, ok := postprocessors[key]; !ok {
			t.Fatalf("expected postprocessor for %s to be registered", key)
		}
	}
}

func TestFTSIndexerSettingsRegistered(t *testing.T) {
	keys := []string{
		"fts_enabled",
		"fts_index_type",
		"fts_chunk_size",
	}

	for _, key := range keys {
		if _, ok := postprocessors[key]; !ok {
			t.Fatalf("expected postprocessor for %s to be registered", key)
		}
	}
}

func TestProcessorKeyUsesFunctionIdentity(t *testing.T) {
	first := processorKey(siteUrlPreProcessor)
	second := processorKey(secretKeyPreProcessor)
	third := processorKey(siteUrlPreProcessor)

	if first == "" || second == "" {
		t.Fatal("expected non-empty processor keys")
	}
	if first == second {
		t.Fatalf("expected distinct processor keys, got %q", first)
	}
	if first != third {
		t.Fatalf("expected stable processor key, got %q and %q", first, third)
	}
}

func TestMimeMappingPreProcessorNormalizesMappings(t *testing.T) {
	settings := map[string]string{
		"mime_mapping": `{
			"TS":" text/plain ",
			".RAR":"application/x-rar-compressed",
			"json":"application/json; charset=utf-8"
		}`,
	}

	if err := mimeMappingPreProcessor(context.Background(), settings); err != nil {
		t.Fatalf("unexpected preprocessor error: %v", err)
	}

	want := `{".json":"application/json; charset=utf-8",".rar":"application/x-rar-compressed",".ts":"text/plain"}`
	if got := settings["mime_mapping"]; got != want {
		t.Fatalf("unexpected normalized mime mapping: got %q want %q", got, want)
	}
}

func TestMimeMappingPreProcessorRejectsInvalidJSON(t *testing.T) {
	settings := map[string]string{
		"mime_mapping": `{invalid}`,
	}

	if err := mimeMappingPreProcessor(context.Background(), settings); err == nil {
		t.Fatal("expected error for invalid mime mapping json")
	}
}

func TestMimeMappingPreProcessorRejectsConflictingNormalizedKeys(t *testing.T) {
	settings := map[string]string{
		"mime_mapping": `{
			"TS":"text/plain",
			".ts":"application/typescript"
		}`,
	}

	if err := mimeMappingPreProcessor(context.Background(), settings); err == nil {
		t.Fatal("expected error for conflicting normalized mime mapping keys")
	}
}
