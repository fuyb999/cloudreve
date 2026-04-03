package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
)

func TestExternalFTSPostProcessorCallsReload(t *testing.T) {
	original := reloadFTSExternalKafka
	defer func() { reloadFTSExternalKafka = original }()

	called := 0
	reloadFTSExternalKafka = func(ctx context.Context, dep dependency.Dep) error {
		called++
		if dep == nil {
			t.Fatal("expected dependency in context")
		}
		return nil
	}

	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dependency.NewDependency())
	if err := externalFTSPostProcessor(ctx, map[string]string{"fts_external_enabled": "1"}); err != nil {
		t.Fatalf("expected reload to succeed, got error: %v", err)
	}
	if called != 1 {
		t.Fatalf("expected reload to be called once, got %d", called)
	}
}

func TestExternalFTSPostProcessorReturnsReloadError(t *testing.T) {
	original := reloadFTSExternalKafka
	defer func() { reloadFTSExternalKafka = original }()

	wantErr := errors.New("reload failed")
	reloadFTSExternalKafka = func(ctx context.Context, dep dependency.Dep) error {
		return wantErr
	}

	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dependency.NewDependency())
	if err := externalFTSPostProcessor(ctx, map[string]string{"fts_external_enabled": "1"}); !errors.Is(err, wantErr) {
		t.Fatalf("expected reload error to be returned, got %v", err)
	}
}

func TestQualityOnlySettingsDoNotTriggerExternalKafkaReload(t *testing.T) {
	if _, ok := postprocessors["fts_external_quality_enabled"]; ok {
		t.Fatal("quality toggle should not trigger kafka reload")
	}
	if _, ok := postprocessors["fts_external_quality_font_box_min_count"]; ok {
		t.Fatal("font box count threshold should not trigger kafka reload")
	}
	if _, ok := postprocessors["fts_external_quality_font_box_min_run"]; ok {
		t.Fatal("font box run threshold should not trigger kafka reload")
	}
	if _, ok := postprocessors["fts_external_quality_font_box_min_ratio"]; ok {
		t.Fatal("font box ratio threshold should not trigger kafka reload")
	}
	if _, ok := postprocessors["fts_external_timeout_seconds"]; ok {
		t.Fatal("timeout threshold should not trigger kafka reload")
	}
	if _, ok := postprocessors["fts_external_enabled"]; !ok {
		t.Fatal("external enable toggle should still trigger kafka reload")
	}
	if _, ok := postprocessors["fts_external_kafka_process_topic"]; !ok {
		t.Fatal("kafka topic changes should still trigger kafka reload")
	}
}
