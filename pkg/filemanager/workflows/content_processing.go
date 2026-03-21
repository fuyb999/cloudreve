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
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
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
	ContentProcessingTaskKindDocumentInspect   ContentProcessingTaskKind = "document_inspect"
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
	case ContentProcessingTaskKindDocumentInspect:
		payload := &manager.SlaveDocumentInspectPayload{}
		if err := json.Unmarshal(t.state.Payload, payload); err != nil {
			return task.StatusError, fmt.Errorf("failed to unmarshal document inspect payload: %w", err)
		}

		result, err := manager.ExecuteSlaveDocumentInspect(ctx, dep, payload)
		if err != nil {
			return task.StatusError, err
		}

		resultRaw, err := json.Marshal(result)
		if err != nil {
			return task.StatusError, fmt.Errorf("failed to marshal document inspect result: %w", err)
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

func (t *SlaveContentProcessingTask) Summarize(hasher hashid.Encoder) *queue.Summary {
	state, err := t.stateForSummary()
	if err != nil {
		return nil
	}

	props := map[string]any{
		"kind": string(state.Kind),
	}
	mergeSummaryProps(props, summarizeContentProcessingPayload(state.Kind, state.Payload))
	mergeSummaryProps(props, summarizeContentProcessingResult(state.Kind, state.Result))

	return &queue.Summary{
		Props: props,
	}
}

func (t *SlaveContentProcessingTask) stateForSummary() (*SlaveContentProcessingTaskState, error) {
	if t.state != nil {
		stateCopy := *t.state
		return &stateCopy, nil
	}

	state := &SlaveContentProcessingTaskState{}
	if err := json.Unmarshal([]byte(t.State()), state); err != nil {
		return nil, err
	}
	return state, nil
}

func summarizeContentProcessingPayload(kind ContentProcessingTaskKind, payloadRaw json.RawMessage) map[string]any {
	if len(payloadRaw) == 0 {
		return nil
	}

	switch kind {
	case ContentProcessingTaskKindFullTextExtract:
		payload := &manager.SlaveFullTextExtractPayload{}
		if err := json.Unmarshal(payloadRaw, payload); err != nil {
			return nil
		}

		props := map[string]any{
			"file_id":   payload.FileID,
			"owner_id":  payload.OwnerID,
			"file_name": payload.FileName,
			"file_size": payload.FileSize,
		}
		if payload.Entity != nil {
			props["entity_id"] = payload.Entity.ID
		}
		if payload.Policy != nil {
			props["policy_id"] = payload.Policy.ID
		}
		return props
	case ContentProcessingTaskKindMediaMetaExtract:
		payload := &manager.SlaveMediaMetaExtractPayload{}
		if err := json.Unmarshal(payloadRaw, payload); err != nil {
			return nil
		}

		props := map[string]any{
			"file_name": payload.FileName,
			"file_ext":  payload.FileExt,
		}
		if payload.Language != "" {
			props["language"] = payload.Language
		}
		if payload.Entity != nil {
			props["entity_id"] = payload.Entity.ID
		}
		if payload.Policy != nil {
			props["policy_id"] = payload.Policy.ID
		}
		return props
	case ContentProcessingTaskKindThumbnailGenerate:
		payload := &manager.SlaveThumbnailGeneratePayload{}
		if err := json.Unmarshal(payloadRaw, payload); err != nil {
			return nil
		}

		props := map[string]any{
			"file_id":  payload.FileID,
			"owner_id": payload.OwnerID,
			"ext":      payload.Ext,
		}
		if payload.URI != "" {
			props["src"] = payload.URI
		}
		if payload.Entity != nil {
			props["entity_id"] = payload.Entity.ID
		}
		if payload.Policy != nil {
			props["policy_id"] = payload.Policy.ID
		}
		return props
	case ContentProcessingTaskKindDocumentInspect:
		payload := &manager.SlaveDocumentInspectPayload{}
		if err := json.Unmarshal(payloadRaw, payload); err != nil {
			return nil
		}

		props := map[string]any{
			"file_name": payload.FileName,
			"file_size": payload.FileSize,
		}
		if payload.Entity != nil {
			props["entity_id"] = payload.Entity.ID
		}
		if payload.Policy != nil {
			props["policy_id"] = payload.Policy.ID
		}
		return props
	default:
		return nil
	}
}

func summarizeContentProcessingResult(kind ContentProcessingTaskKind, resultRaw json.RawMessage) map[string]any {
	if len(resultRaw) == 0 {
		return nil
	}

	switch kind {
	case ContentProcessingTaskKindFullTextExtract:
		result := &manager.SlaveFullTextExtractResult{}
		if err := json.Unmarshal(resultRaw, result); err != nil {
			return nil
		}

		props := map[string]any{
			"entity_id": result.EntityID,
		}
		if result.ManifestPath != "" {
			props["manifest_path"] = result.ManifestPath
		}
		return props
	case ContentProcessingTaskKindMediaMetaExtract:
		result := &manager.SlaveMediaMetaExtractResult{}
		if err := json.Unmarshal(resultRaw, result); err != nil {
			return nil
		}

		props := map[string]any{
			"entity_id":  result.EntityID,
			"meta_count": len(result.Metas),
		}
		return props
	case ContentProcessingTaskKindThumbnailGenerate:
		result := &manager.SlaveThumbnailGenerateResult{}
		if err := json.Unmarshal(resultRaw, result); err != nil {
			return nil
		}

		props := map[string]any{
			"file_id":       result.FileID,
			"entity_id":     result.EntityID,
			"not_available": result.NotAvailable,
		}
		if result.SavePath != "" {
			props["save_path"] = result.SavePath
		}
		if result.Size > 0 {
			props["size"] = result.Size
		}
		return props
	case ContentProcessingTaskKindDocumentInspect:
		result := &manager.DocumentInspection{}
		if err := json.Unmarshal(resultRaw, result); err != nil {
			return nil
		}

		props := map[string]any{
			"entity_id": result.EntityID,
		}
		if result.MimeType != "" {
			props["mime_type"] = result.MimeType
		}
		if result.Parser != "" {
			props["parser"] = result.Parser
		}
		if result.Language != "" {
			props["language"] = result.Language
		}
		if result.Title != "" {
			props["title"] = result.Title
		}
		if result.Author != "" {
			props["author"] = result.Author
		}
		if len(result.Metadata) > 0 {
			props["metadata_count"] = len(result.Metadata)
		}
		return props
	default:
		return nil
	}
}

func mergeSummaryProps(dst, src map[string]any) {
	for key, value := range src {
		dst[key] = value
	}
}
