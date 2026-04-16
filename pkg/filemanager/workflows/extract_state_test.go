package workflows

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
)

func TestNewExtractArchiveTaskPersistsPublicVisibility(t *testing.T) {
	visibility := &publicshare.VisibilityResult{
		RootGrants: []publicshare.RootGrant{
			{
				RootFileID:   20,
				RootOwnerID:  7,
				RootTreePath: "10.20",
				Actions: map[publicshare.Action]bool{
					publicshare.ActionList:   true,
					publicshare.ActionUpload: true,
				},
			},
		},
	}

	task, err := NewExtractArchiveTask(
		context.Background(),
		"cloudreve://public/source.zip",
		"cloudreve://public/target",
		"",
		"",
		nil,
		visibility,
	)
	if err != nil {
		t.Fatalf("failed to create extract archive task: %v", err)
	}

	state := &ExtractArchiveTaskState{}
	if err := json.Unmarshal([]byte(task.State()), state); err != nil {
		t.Fatalf("failed to decode task state: %v", err)
	}
	if state.PublicVisibility == nil {
		t.Fatal("expected public visibility to be persisted into task state")
	}
	if len(state.PublicVisibility.RootGrants) != 1 || state.PublicVisibility.RootGrants[0].RootFileID != 20 {
		t.Fatalf("unexpected task visibility: %+v", state.PublicVisibility.RootGrants)
	}
}
