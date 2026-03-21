package admin

import "testing"

func TestContentProcessingQueueSettingsRegistered(t *testing.T) {
	keys := []string{
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
