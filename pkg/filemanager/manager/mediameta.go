package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/driver"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
)

type (
	MediaMetaTask struct {
		*queue.DBTask
	}

	MediaMetaTaskPhase string

	MediaMetaTaskState struct {
		Uri      *fs.URI            `json:"uri"`
		FileID   int                `json:"file_id,omitempty"`
		OwnerID  int                `json:"owner_id,omitempty"`
		EntityID int                `json:"entity_id"`
		Phase    MediaMetaTaskPhase `json:"phase,omitempty"`
		NodeID   int                `json:"node_id,omitempty"`
		SlaveID  int                `json:"slave_id,omitempty"`
	}
)

const (
	MediaMetaTaskPhasePending    MediaMetaTaskPhase = ""
	MediaMetaTaskPhaseAwaitSlave MediaMetaTaskPhase = "await_slave_extract"
)

func init() {
	queue.RegisterResumableTaskFactory(queue.MediaMetaTaskType, NewMediaMetaTaskFromModel)
}

// NewMediaMetaTask creates a new MediaMetaTask to
func NewMediaMetaTask(ctx context.Context, uri *fs.URI, fileID, ownerID, entityID int, creator *ent.User) (*MediaMetaTask, error) {
	state := &MediaMetaTaskState{
		Uri:      uri,
		FileID:   fileID,
		OwnerID:  ownerID,
		EntityID: entityID,
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	return &MediaMetaTask{
		DBTask: &queue.DBTask{
			DirectOwner: creator,
			Task: &ent.Task{
				Type:          queue.MediaMetaTaskType,
				CorrelationID: logging.CorrelationID(ctx),
				PrivateState:  string(stateBytes),
				PublicState:   &types.TaskPublicState{},
			},
		},
	}, nil
}

func NewMediaMetaTaskFromModel(task *ent.Task) queue.Task {
	return &MediaMetaTask{
		DBTask: &queue.DBTask{
			Task: task,
		},
	}
}

func (m *MediaMetaTask) Summarize(hasher hashid.Encoder) *queue.Summary {
	var state MediaMetaTaskState
	if err := json.Unmarshal([]byte(m.State()), &state); err != nil {
		return nil
	}

	props := map[string]any{
		"file_id":   state.FileID,
		"owner_id":  state.OwnerID,
		"entity_id": state.EntityID,
	}
	if state.Uri != nil {
		props["src"] = state.Uri.String()
	}

	return &queue.Summary{
		NodeID: state.NodeID,
		Phase:  string(state.Phase),
		Props:  props,
	}
}

func (m *MediaMetaTask) Do(ctx context.Context) (task.Status, error) {
	dep := dependency.FromContext(ctx)
	fm := NewFileManager(dep, inventory.UserFromContext(ctx)).(*manager)

	// unmarshal state
	var state MediaMetaTaskState
	if err := json.Unmarshal([]byte(m.State()), &state); err != nil {
		return task.StatusError, fmt.Errorf("failed to unmarshal state: %s (%w)", err, queue.CriticalErr)
	}

	var (
		next task.Status
		err  error
	)
	switch state.Phase {
	case MediaMetaTaskPhasePending:
		next, err = m.dispatchOrExtract(ctx, fm, &state)
	case MediaMetaTaskPhaseAwaitSlave:
		next, err = m.awaitSlaveExtraction(ctx, fm, &state)
	default:
		return task.StatusError, fmt.Errorf("unknown media meta task phase %q: %w", state.Phase, queue.CriticalErr)
	}

	stateBytes, marshalErr := json.Marshal(&state)
	if marshalErr != nil {
		return task.StatusError, fmt.Errorf("failed to marshal state: %w", marshalErr)
	}
	m.UpdateState(string(stateBytes))
	return next, err
}

func (m *MediaMetaTask) dispatchOrExtract(ctx context.Context, fm *manager, state *MediaMetaTaskState) (task.Status, error) {
	node, err := allocateContentProcessingNode(ctx, fm.dep, state.NodeID)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to allocate content processing node: %w", err)
	}

	state.NodeID = node.ID()
	if node.IsMaster() {
		if err := fm.ExtractAndSaveMediaMeta(ctx, state.Uri, state.EntityID); err != nil {
			return task.StatusError, err
		}
		return task.StatusCompleted, nil
	}

	payload, err := fm.buildSlaveMediaMetaExtractPayload(ctx, state.Uri, state.FileID, state.EntityID)
	if err != nil {
		return task.StatusError, err
	}
	if payload == nil {
		return task.StatusCompleted, nil
	}

	stateRaw, err := marshalSlaveContentProcessingState(slaveContentProcessingKindMediaMetaExtract, payload)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to marshal slave media meta payload: %w", err)
	}

	taskID, err := node.CreateTask(ctx, queue.SlaveContentProcessingTaskType, stateRaw)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to create slave content processing task: %w", err)
	}

	state.Phase = MediaMetaTaskPhaseAwaitSlave
	state.SlaveID = taskID
	m.ResumeAfter(10 * time.Second)
	return task.StatusSuspending, nil
}

func (m *MediaMetaTask) awaitSlaveExtraction(ctx context.Context, fm *manager, state *MediaMetaTaskState) (task.Status, error) {
	node, err := allocateContentProcessingNode(ctx, fm.dep, state.NodeID)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to resolve content processing node: %w", err)
	}

	summary, err := node.GetTask(ctx, state.SlaveID, true)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to get slave task: %w", err)
	}

	switch summary.Status {
	case task.StatusCompleted:
		wrapper, err := parseSlaveContentProcessingState(summary.PrivateState)
		if err != nil {
			return task.StatusError, fmt.Errorf("failed to parse slave media meta result: %w", err)
		}

		result := &SlaveMediaMetaExtractResult{}
		if len(wrapper.Result) > 0 {
			if err := json.Unmarshal(wrapper.Result, result); err != nil {
				return task.StatusError, fmt.Errorf("failed to unmarshal slave media meta result: %w", err)
			}
		}

		if err := fm.applySlaveMediaMetaResult(ctx, state.Uri, state.FileID, state.OwnerID, state.EntityID, result); err != nil {
			return task.StatusError, err
		}

		state.Phase = MediaMetaTaskPhasePending
		state.NodeID = 0
		state.SlaveID = 0
		return task.StatusCompleted, nil
	case task.StatusError:
		return task.StatusError, fmt.Errorf("slave content processing task failed: %s (%w)", summary.Error, queue.CriticalErr)
	case task.StatusCanceled:
		return task.StatusError, fmt.Errorf("slave content processing task canceled (%w)", queue.CriticalErr)
	default:
		m.ResumeAfter(30 * time.Second)
		return task.StatusSuspending, nil
	}
}

func (m *manager) ExtractAndSaveMediaMeta(ctx context.Context, uri *fs.URI, entityID int) error {
	file, targetVersion, language, shouldProcess, err := m.resolveMediaMetaTarget(ctx, uri, 0, entityID)
	if err != nil {
		return err
	}
	if !shouldProcess {
		return nil
	}

	metas, err := m.extractMediaMetaForEntity(ctx, targetVersion, file.Name(), file.Ext(), language, nil)
	if err != nil {
		return err
	}

	return m.saveMediaMeta(ctx, uri, file.ID(), file.OwnerID(), file.PrimaryEntityID(), metas)
}

func (m *manager) shouldGenerateMediaMeta(ctx context.Context, d driver.Handler, fileName string) bool {
	driverCaps := d.Capabilities()
	if util.IsInExtensionList(driverCaps.MediaMetaSupportedExts, fileName) {
		// Handler support it natively
		return true
	}

	if driverCaps.MediaMetaProxy && util.IsInExtensionList(m.dep.MediaMetaExtractor(ctx).Exts(), fileName) {
		// Handler does not support. but proxy is enabled.
		return true
	}

	return false
}

func (m *manager) mediaMetaForNewEntity(ctx context.Context, session *fs.UploadSession, d driver.Handler) {
	if session.Props.EntityType == nil || *session.Props.EntityType == types.EntityTypeVersion {
		if !m.shouldGenerateMediaMeta(ctx, d, session.Props.Uri.Name()) {
			return
		}

		mediaMetaTask, err := NewMediaMetaTask(ctx, session.Props.Uri, session.FileID, m.user.ID, session.EntityID, m.user)
		if err != nil {
			m.l.Warning("Failed to create media meta task: %s", err)
			return
		}
		if err := m.dep.ContentProcessingQueue(ctx).QueueTask(ctx, mediaMetaTask); err != nil {
			m.l.Warning("Failed to queue media meta task: %s", err)
		}

		return
	}
}
