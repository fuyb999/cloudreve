package workflows

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
)

func TestNewRebuildIndexTaskStoresSkipExtractionOptions(t *testing.T) {
	task, err := NewRebuildIndexTask(context.Background(), &ent.User{ID: 1}, []int{3, 7}, true, true)
	if err != nil {
		t.Fatalf("unexpected new rebuild index task error: %v", err)
	}

	rebuildTask, ok := task.(*RebuildIndexTask)
	if !ok {
		t.Fatalf("unexpected task type: %T", task)
	}

	var state RebuildIndexTaskState
	if err := json.Unmarshal([]byte(rebuildTask.State()), &state); err != nil {
		t.Fatalf("failed to unmarshal rebuild task state: %v", err)
	}

	if state.Phase != RebuildIndexPhaseNuke {
		t.Fatalf("unexpected phase: got %q want %q", state.Phase, RebuildIndexPhaseNuke)
	}
	if len(state.FilteredStoragePolicy) != 2 || state.FilteredStoragePolicy[0] != 3 || state.FilteredStoragePolicy[1] != 7 {
		t.Fatalf("unexpected filtered storage policy: %+v", state.FilteredStoragePolicy)
	}
	if !state.SkipTextExtraction {
		t.Fatal("expected skip text extraction to be true")
	}
	if !state.SkipAssetExtraction {
		t.Fatal("expected skip attachment extraction to be true")
	}
}

func TestRebuildIndexTaskSummarizeIncludesSkipExtractionOptions(t *testing.T) {
	state := RebuildIndexTaskState{
		Phase:               RebuildIndexPhaseIndex,
		Total:               12,
		Failed:              2,
		SkipTextExtraction:  true,
		SkipAssetExtraction: true,
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("failed to marshal state: %v", err)
	}

	task := &RebuildIndexTask{
		DBTask: &queue.DBTask{
			Task: &ent.Task{
				PrivateState: string(raw),
			},
		},
	}

	summary := task.Summarize(nil)
	if summary == nil {
		t.Fatal("expected summary")
	}

	if got := summary.Props[SummaryKeyTotal]; got != 12 {
		t.Fatalf("unexpected total summary prop: %#v", got)
	}
	if got := summary.Props[SummaryKeyFailed]; got != 2 {
		t.Fatalf("unexpected failed summary prop: %#v", got)
	}
	if got := summary.Props["skip_text_extraction"]; got != true {
		t.Fatalf("unexpected skip_text_extraction summary prop: %#v", got)
	}
	if got := summary.Props["skip_attachment_extraction"]; got != true {
		t.Fatalf("unexpected skip_attachment_extraction summary prop: %#v", got)
	}
}
