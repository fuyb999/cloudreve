package setting

import (
	"context"
	"reflect"
	"testing"
)

func TestFTSExternalExtractorUsesExpectedKeys(t *testing.T) {
	provider := NewProvider(testSettingStoreAdapter{
		values: map[string]any{
			"fts_external_enabled":                        "1",
			"fts_external_mode":                           "primary",
			"fts_external_timeout_seconds":                "123",
			"fts_external_retry_max":                      "4",
			"fts_external_recursive_attachments":          "0",
			"fts_external_ocr_enabled":                    "1",
			"fts_external_skip_encrypted_files":           "0",
			"fts_external_use_global_kafka":               "0",
			"fts_external_kafka_brokers":                  " broker-a:9092 , broker-b:9092 ",
			"fts_external_kafka_security_protocol":        "SASL_SSL",
			"fts_external_kafka_sasl_mechanism":           "PLAIN",
			"fts_external_kafka_username":                 "extractor",
			"fts_external_kafka_password":                 "secret",
			"fts_external_kafka_tls_skip_verify":          "1",
			"fts_external_kafka_process_topic":            "process-topic",
			"fts_external_kafka_result_topic":             "result-topic",
			"fts_external_kafka_error_topic":              "error-topic",
			"fts_external_kafka_consumer_group":           "group-a",
			"fts_external_quality_enabled":                "1",
			"fts_external_quality_min_text_length":        "64",
			"fts_external_quality_max_replacement_ratio":  "0.2",
			"fts_external_quality_max_control_char_ratio": "0.1",
			"fts_external_quality_min_printable_ratio":    "0.9",
			"fts_external_quality_font_box_min_count":     "6",
			"fts_external_quality_font_box_min_run":       "4",
			"fts_external_quality_font_box_min_ratio":     "0.45",
		},
	})

	cfg := provider.FTSExternalExtractor(context.Background())
	if !cfg.Enabled {
		t.Fatal("expected external extractor to be enabled")
	}
	if cfg.Mode != FTSExternalModePrimary {
		t.Fatalf("unexpected mode: %s", cfg.Mode)
	}
	if cfg.TimeoutSeconds != 123 || cfg.RetryMax != 4 {
		t.Fatalf("unexpected timeout/retry: %+v", cfg)
	}
	if cfg.RecursiveAttachments || cfg.SkipEncryptedFiles {
		t.Fatalf("unexpected recursive/encrypted flags: %+v", cfg)
	}
	if !cfg.OCREnabled {
		t.Fatalf("expected OCR to be enabled: %+v", cfg)
	}
	if cfg.Kafka.UseGlobalKafka {
		t.Fatal("expected dedicated kafka config")
	}
	if !reflect.DeepEqual(cfg.Kafka.Brokers, []string{"broker-a:9092", "broker-b:9092"}) {
		t.Fatalf("unexpected brokers: %#v", cfg.Kafka.Brokers)
	}
	if cfg.Kafka.SecurityProtocol != "SASL_SSL" || cfg.Kafka.Username != "extractor" || cfg.Kafka.Password != "secret" {
		t.Fatalf("unexpected kafka auth config: %+v", cfg.Kafka)
	}
	if !cfg.Kafka.TLSSkipVerify {
		t.Fatal("expected tls skip verify to be enabled")
	}
	if cfg.Kafka.ProcessTopic != "process-topic" || cfg.Kafka.ResultTopic != "result-topic" || cfg.Kafka.ErrorTopic != "error-topic" || cfg.Kafka.ConsumerGroup != "group-a" {
		t.Fatalf("unexpected kafka topics/group: %+v", cfg.Kafka)
	}
	if !cfg.Quality.Enabled || cfg.Quality.MinTextLength != 64 {
		t.Fatalf("unexpected quality config: %+v", cfg.Quality)
	}
	if cfg.Quality.MaxReplacementRatio != 0.2 || cfg.Quality.MaxControlCharRatio != 0.1 || cfg.Quality.MinPrintableRatio != 0.9 {
		t.Fatalf("unexpected quality thresholds: %+v", cfg.Quality)
	}
	if cfg.Quality.FontBoxMinCount != 6 || cfg.Quality.FontBoxMinRun != 4 || cfg.Quality.FontBoxMinRatio != 0.45 {
		t.Fatalf("unexpected font box thresholds: %+v", cfg.Quality)
	}
}
