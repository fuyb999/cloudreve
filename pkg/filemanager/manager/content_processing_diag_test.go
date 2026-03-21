package manager

import (
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/pkg/cluster"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
)

func TestSlaveTaskDiagnostic(t *testing.T) {
	summary := &cluster.SlaveTaskSummary{
		Status:      task.StatusError,
		DisplayType: queue.ContentProcessingTaskKindThumbnailGenerate,
		Summary: &queue.Summary{
			Props: map[string]any{
				"file_id":       801,
				"src":           "cloudreve:///thumb/source.png",
				"not_available": false,
			},
		},
	}

	got := slaveTaskDiagnostic(summary)
	want := ` [display_type=thumbnail_generate, summary={"file_id":801,"not_available":false,"src":"cloudreve:///thumb/source.png"}]`
	if got != want {
		t.Fatalf("unexpected diagnostic: got %s want %s", got, want)
	}
}
