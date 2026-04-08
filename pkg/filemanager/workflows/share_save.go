package workflows

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
)

type (
	ShareSaveTask struct {
		*queue.DBTask

		state *ShareSaveTaskState
	}

	ShareSaveTaskState struct {
		Src    string             `json:"src,omitempty"`
		Dst    string             `json:"dst,omitempty"`
		Phase  ShareSaveTaskPhase `json:"phase,omitempty"`
		Failed int                `json:"failed,omitempty"`
	}

	ShareSaveTaskPhase string
)

const (
	ShareSaveTaskPhasePending ShareSaveTaskPhase = ""
	ShareSaveTaskPhaseCopying ShareSaveTaskPhase = "copying"
)

func init() {
	queue.RegisterResumableTaskFactory(queue.ShareSaveTaskType, NewShareSaveTaskFromModel)
}

func NewShareSaveTask(ctx context.Context, u *ent.User, src, dst string) (queue.Task, error) {
	state := &ShareSaveTaskState{
		Src: src,
		Dst: dst,
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	return &ShareSaveTask{
		DBTask: &queue.DBTask{
			Task: &ent.Task{
				Type:          queue.ShareSaveTaskType,
				CorrelationID: logging.NillableCorrelationID(ctx),
				PrivateState:  string(stateBytes),
				PublicState:   &types.TaskPublicState{},
			},
			DirectOwner: u,
		},
	}, nil
}

func NewShareSaveTaskFromModel(task *ent.Task) queue.Task {
	return &ShareSaveTask{
		DBTask: &queue.DBTask{
			Task: task,
		},
	}
}

func (m *ShareSaveTask) Do(ctx context.Context) (task.Status, error) {
	dep := dependency.FromContext(ctx)

	state := &ShareSaveTaskState{}
	if err := json.Unmarshal([]byte(m.State()), state); err != nil {
		return task.StatusError, fmt.Errorf("failed to unmarshal state: %w", err)
	}
	m.state = state
	m.state.Phase = ShareSaveTaskPhaseCopying

	src, err := fs.NewUriFromString(m.state.Src)
	if err != nil {
		m.state.Failed = 1
		return task.StatusError, fmt.Errorf("failed to parse share source: %s (%w)", err, queue.CriticalErr)
	}

	dst, err := fs.NewUriFromString(m.state.Dst)
	if err != nil {
		m.state.Failed = 1
		return task.StatusError, fmt.Errorf("failed to parse destination: %s (%w)", err, queue.CriticalErr)
	}

	fm := manager.NewFileManager(dep, inventory.UserFromContext(ctx))
	defer fm.Recycle()

	if err := fm.MoveOrCopy(ctx, []*fs.URI{src}, dst, true); err != nil {
		m.state.Failed = 1
		return task.StatusError, err
	}

	return task.StatusCompleted, nil
}

func (m *ShareSaveTask) Summarize(hasher hashid.Encoder) *queue.Summary {
	if m.state == nil {
		if err := json.Unmarshal([]byte(m.State()), &m.state); err != nil {
			return nil
		}
	}

	return &queue.Summary{
		Phase: string(m.state.Phase),
		Props: map[string]any{
			SummaryKeySrc:    m.state.Src,
			SummaryKeyDst:    m.state.Dst,
			SummaryKeyFailed: m.state.Failed,
		},
	}
}
