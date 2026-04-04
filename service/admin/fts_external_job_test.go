package admin

import (
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/ftsexternaljob"
	"github.com/cloudreve/Cloudreve/v4/inventory"
)

func TestBuildFTSExternalJobListItemsOmitsPayloadBodies(t *testing.T) {
	t.Parallel()

	completedAt := testTimePtr()
	items := buildFTSExternalJobListItems([]*ent.FTSExternalJob{
		{
			ID:            11,
			RequestID:     "req-11",
			Status:        "success",
			FileID:        21,
			OwnerID:       31,
			EntityID:      41,
			Mode:          "primary",
			TriggerReason: "quality_rejected",
			Attempt:       2,
			ManifestPath:  "cloudreve/fts-sidecar/demo/manifest.json",
			ResultPayload: "{\"ok\":true}",
			QualityReport: "{\"accepted\":false}",
			CompletedAt:   completedAt,
		},
	})

	if len(items) != 1 {
		t.Fatalf("unexpected item count: got %d want 1", len(items))
	}

	item := items[0]
	if !item.HasResultPayload {
		t.Fatal("expected result payload flag to be true")
	}
	if item.HasErrorPayload {
		t.Fatal("expected error payload flag to be false")
	}
	if !item.HasQualityReport {
		t.Fatal("expected quality report flag to be true")
	}
	if item.RequestID != "req-11" || item.ManifestPath == "" || item.CompletedAt == nil {
		t.Fatalf("unexpected list item: %+v", item)
	}
}

func TestNormalizedFTSExternalJobOrderBy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "default", input: "", want: ""},
		{name: "requested at", input: ftsexternaljob.FieldRequestedAt, want: ftsexternaljob.FieldRequestedAt},
		{name: "completed at", input: ftsexternaljob.FieldCompletedAt, want: ftsexternaljob.FieldCompletedAt},
		{name: "invalid", input: "result_payload", want: ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := normalizedFTSExternalJobOrderBy(tt.input)
			if got != tt.want {
				t.Fatalf("unexpected normalized order by: got %q want %q", got, tt.want)
			}
		})
	}

	opts := getFTSExternalJobOrderOption(&AdminListService{
		OrderBy:        ftsexternaljob.FieldRequestedAt,
		OrderDirection: string(inventory.OrderDirectionDesc),
	})
	if len(opts) != 2 {
		t.Fatalf("unexpected order option count: got %d want 2", len(opts))
	}
}

func testTimePtr() *time.Time {
	tm := time.Unix(1712131200, 0).UTC()
	return &tm
}
