package workflows

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
)

type (
	ContentProcessingTaskKind string

	SlaveContentProcessingTask struct {
		*queue.InMemoryTask

		l        logging.Logger
		state    *SlaveContentProcessingTaskState
		progress queue.Progresses
	}

	SlaveContentProcessingTaskState struct {
		Kind    ContentProcessingTaskKind `json:"kind"`
		Payload json.RawMessage           `json:"payload,omitempty"`
		Result  json.RawMessage           `json:"result,omitempty"`
	}
)

const (
	ContentProcessingTaskKindFullTextExtract   ContentProcessingTaskKind = "full_text_extract"
	ContentProcessingTaskKindMediaMetaExtract  ContentProcessingTaskKind = "media_meta_extract"
	ContentProcessingTaskKindThumbnailGenerate ContentProcessingTaskKind = "thumbnail_generate"
)

// NewSlaveContentProcessingTask creates a new generic content processing task on slave.
func NewSlaveContentProcessingTask(ctx context.Context, props *types.SlaveTaskProps, id int, state string) queue.Task {
	return &SlaveContentProcessingTask{
		InMemoryTask: &queue.InMemoryTask{
			DBTask: &queue.DBTask{
				Task: &ent.Task{
					ID:            id,
					Type:          queue.SlaveContentProcessingTaskType,
					CorrelationID: logging.CorrelationID(ctx),
					PublicState: &types.TaskPublicState{
						SlaveTaskProps: props,
					},
					PrivateState: state,
				},
			},
		},
		progress: make(queue.Progresses),
	}
}

func (t *SlaveContentProcessingTask) Do(ctx context.Context) (task.Status, error) {
	ctx = prepareSlaveTaskCtx(ctx, t.Model().PublicState.SlaveTaskProps)
	dep := dependency.FromContext(ctx)
	t.l = dep.Logger()

	state := &SlaveContentProcessingTaskState{}
	if err := json.Unmarshal([]byte(t.State()), state); err != nil {
		return task.StatusError, fmt.Errorf("failed to unmarshal state: %w", err)
	}

	t.state = state
	switch t.state.Kind {
	case "":
		return task.StatusError, fmt.Errorf("missing content processing kind: %w", queue.CriticalErr)
	case ContentProcessingTaskKindFullTextExtract:
		payload := &manager.SlaveFullTextExtractPayload{}
		if err := json.Unmarshal(t.state.Payload, payload); err != nil {
			return task.StatusError, fmt.Errorf("failed to unmarshal full text extract payload: %w", err)
		}

		result, err := manager.ExecuteSlaveFullTextExtract(ctx, dep, payload)
		if err != nil {
			return task.StatusError, err
		}

		resultRaw, err := json.Marshal(result)
		if err != nil {
			return task.StatusError, fmt.Errorf("failed to marshal full text extract result: %w", err)
		}
		t.state.Result = resultRaw

		newState, err := json.Marshal(t.state)
		if err != nil {
			return task.StatusError, fmt.Errorf("failed to marshal content processing state: %w", err)
		}

		t.Lock()
		t.Task.PrivateState = string(newState)
		t.Unlock()
		return task.StatusCompleted, nil
	case ContentProcessingTaskKindMediaMetaExtract:
		payload := &manager.SlaveMediaMetaExtractPayload{}
		if err := json.Unmarshal(t.state.Payload, payload); err != nil {
			return task.StatusError, fmt.Errorf("failed to unmarshal media meta extract payload: %w", err)
		}

		result, err := manager.ExecuteSlaveMediaMetaExtract(ctx, dep, payload)
		if err != nil {
			return task.StatusError, err
		}

		resultRaw, err := json.Marshal(result)
		if err != nil {
			return task.StatusError, fmt.Errorf("failed to marshal media meta extract result: %w", err)
		}
		t.state.Result = resultRaw

		newState, err := json.Marshal(t.state)
		if err != nil {
			return task.StatusError, fmt.Errorf("failed to marshal content processing state: %w", err)
		}

		t.Lock()
		t.Task.PrivateState = string(newState)
		t.Unlock()
		return task.StatusCompleted, nil
	case ContentProcessingTaskKindThumbnailGenerate:
		payload := &manager.SlaveThumbnailGeneratePayload{}
		if err := json.Unmarshal(t.state.Payload, payload); err != nil {
			return task.StatusError, fmt.Errorf("failed to unmarshal thumbnail generate payload: %w", err)
		}

		result, err := manager.ExecuteSlaveThumbnailGenerate(ctx, dep, payload)
		if err != nil {
			return task.StatusError, err
		}

		resultRaw, err := json.Marshal(result)
		if err != nil {
			return task.StatusError, fmt.Errorf("failed to marshal thumbnail generate result: %w", err)
		}
		t.state.Result = resultRaw

		newState, err := json.Marshal(t.state)
		if err != nil {
			return task.StatusError, fmt.Errorf("failed to marshal content processing state: %w", err)
		}

		t.Lock()
		t.Task.PrivateState = string(newState)
		t.Unlock()
		return task.StatusCompleted, nil
	default:
		return task.StatusError, fmt.Errorf("unknown content processing kind %q: %w", t.state.Kind, queue.CriticalErr)
	}
}

func (t *SlaveContentProcessingTask) Progress(ctx context.Context) queue.Progresses {
	t.Lock()
	defer t.Unlock()

	res := make(queue.Progresses)
	for k, v := range t.progress {
		res[k] = v
	}
	return res
}
