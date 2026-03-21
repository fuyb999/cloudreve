package cluster

import (
	"bytes"
	"encoding/gob"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
)

func TestSlaveTaskSummaryGobRoundTrip(t *testing.T) {
	input := &SlaveTaskSummary{
		Status:      task.StatusCompleted,
		DisplayType: queue.ContentProcessingTaskKindThumbnailGenerate,
		Summary: &queue.Summary{
			Props: map[string]any{
				"kind":          queue.ContentProcessingTaskKindThumbnailGenerate,
				"file_id":       801,
				"src":           "cloudreve:///thumb/source.png",
				"size":          int64(2048),
				"not_available": false,
			},
		},
		PrivateState: `{"kind":"thumbnail_generate"}`,
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(input); err != nil {
		t.Fatalf("failed to gob encode slave task summary: %v", err)
	}

	var output SlaveTaskSummary
	if err := gob.NewDecoder(&buf).Decode(&output); err != nil {
		t.Fatalf("failed to gob decode slave task summary: %v", err)
	}

	if output.Status != task.StatusCompleted {
		t.Fatalf("unexpected status: got %s", output.Status)
	}
	if output.DisplayType != queue.ContentProcessingTaskKindThumbnailGenerate {
		t.Fatalf("unexpected display type: got %s", output.DisplayType)
	}
	if output.Summary == nil {
		t.Fatal("expected summary")
	}
	if output.Summary.Props["file_id"] != 801 {
		t.Fatalf("unexpected file id: %+v", output.Summary.Props["file_id"])
	}
	if output.Summary.Props["src"] != "cloudreve:///thumb/source.png" {
		t.Fatalf("unexpected src: %+v", output.Summary.Props["src"])
	}
	if output.Summary.Props["size"] != int64(2048) {
		t.Fatalf("unexpected size: %+v", output.Summary.Props["size"])
	}
	if output.Summary.Props["not_available"] != false {
		t.Fatalf("unexpected not_available: %+v", output.Summary.Props["not_available"])
	}
}
