package setting

import (
	"context"
	"testing"
	"time"
)

type testSettingStoreAdapter struct {
	values map[string]any
}

func (s testSettingStoreAdapter) Get(_ context.Context, name string, defaultVal any) any {
	if value, ok := s.values[name]; ok {
		return value
	}

	return defaultVal
}

func TestQueueContentProcessingUsesExpectedKeys(t *testing.T) {
	provider := NewProvider(testSettingStoreAdapter{
		values: map[string]any{
			"queue_content_processing_worker_num":           "23",
			"queue_content_processing_max_execution":        "7200",
			"queue_content_processing_backoff_factor":       "1.5",
			"queue_content_processing_backoff_max_duration": "120",
			"queue_content_processing_max_retry":            "3",
			"queue_content_processing_retry_delay":          "9",
		},
	})

	queue := provider.Queue(context.Background(), QueueTypeContentProcessing)
	if queue.WorkerNum != 23 {
		t.Fatalf("unexpected worker num: got %d want 23", queue.WorkerNum)
	}
	if queue.MaxExecution != 7200*time.Second {
		t.Fatalf("unexpected max execution: got %s want %s", queue.MaxExecution, 7200*time.Second)
	}
	if queue.BackoffFactor != 1.5 {
		t.Fatalf("unexpected backoff factor: got %v want 1.5", queue.BackoffFactor)
	}
	if queue.BackoffMaxDuration != 120*time.Second {
		t.Fatalf("unexpected backoff max duration: got %s want %s", queue.BackoffMaxDuration, 120*time.Second)
	}
	if queue.MaxRetry != 3 {
		t.Fatalf("unexpected max retry: got %d want 3", queue.MaxRetry)
	}
	if queue.RetryDelay != 9*time.Second {
		t.Fatalf("unexpected retry delay: got %s want %s", queue.RetryDelay, 9*time.Second)
	}
}
