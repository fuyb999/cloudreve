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
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	tikaextractor "github.com/cloudreve/Cloudreve/v4/pkg/searcher/extractor"
)

type (
	DocumentInspectTask struct {
		*queue.DBTask
	}

	DocumentInspectTaskPhase string

	DocumentInspectTaskState struct {
		Uri      *fs.URI                  `json:"uri"`
		FileID   int                      `json:"file_id,omitempty"`
		OwnerID  int                      `json:"owner_id,omitempty"`
		EntityID int                      `json:"entity_id"`
		Phase    DocumentInspectTaskPhase `json:"phase,omitempty"`
		NodeID   int                      `json:"node_id,omitempty"`
		SlaveID  int                      `json:"slave_id,omitempty"`
	}

	DocumentInspection struct {
		EntityID int               `json:"entity_id"`
		MimeType string            `json:"mime_type,omitempty"`
		Parser   string            `json:"parser,omitempty"`
		Language string            `json:"language,omitempty"`
		Title    string            `json:"title,omitempty"`
		Author   string            `json:"author,omitempty"`
		Metadata map[string]string `json:"metadata,omitempty"`
	}
)

const (
	DocumentInspectTaskPhasePending    DocumentInspectTaskPhase = ""
	DocumentInspectTaskPhaseAwaitSlave DocumentInspectTaskPhase = "await_slave_inspect"
)

func init() {
	queue.RegisterResumableTaskFactory(queue.DocumentInspectTaskType, NewDocumentInspectTaskFromModel)
}

func NewDocumentInspectTask(ctx context.Context, uri *fs.URI, fileID, ownerID, entityID int, creator *ent.User) (*DocumentInspectTask, error) {
	state := &DocumentInspectTaskState{
		Uri:      uri,
		FileID:   fileID,
		OwnerID:  ownerID,
		EntityID: entityID,
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	return &DocumentInspectTask{
		DBTask: &queue.DBTask{
			DirectOwner: creator,
			Task: &ent.Task{
				Type:          queue.DocumentInspectTaskType,
				CorrelationID: logging.NillableCorrelationID(ctx),
				PrivateState:  string(stateBytes),
				PublicState:   &types.TaskPublicState{},
			},
		},
	}, nil
}

func NewDocumentInspectTaskFromModel(task *ent.Task) queue.Task {
	return &DocumentInspectTask{
		DBTask: &queue.DBTask{Task: task},
	}
}

func (t *DocumentInspectTask) Summarize(hasher hashid.Encoder) *queue.Summary {
	var state DocumentInspectTaskState
	if err := json.Unmarshal([]byte(t.State()), &state); err != nil {
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

func (t *DocumentInspectTask) Do(ctx context.Context) (task.Status, error) {
	dep := dependency.FromContext(ctx)
	fm := NewFileManager(dep, inventory.UserFromContext(ctx)).(*manager)

	var state DocumentInspectTaskState
	if err := json.Unmarshal([]byte(t.State()), &state); err != nil {
		return task.StatusError, fmt.Errorf("failed to unmarshal state: %s (%w)", err, queue.CriticalErr)
	}

	var (
		next task.Status
		err  error
	)
	switch state.Phase {
	case DocumentInspectTaskPhasePending:
		next, err = t.dispatchOrInspect(ctx, fm, &state)
	case DocumentInspectTaskPhaseAwaitSlave:
		next, err = t.awaitSlaveInspection(ctx, fm, &state)
	default:
		return task.StatusError, fmt.Errorf("unknown document inspect task phase %q: %w", state.Phase, queue.CriticalErr)
	}

	stateBytes, marshalErr := json.Marshal(&state)
	if marshalErr != nil {
		return task.StatusError, fmt.Errorf("failed to marshal state: %w", marshalErr)
	}
	t.UpdateState(string(stateBytes))
	return next, err
}

func (t *DocumentInspectTask) dispatchOrInspect(ctx context.Context, fm *manager, state *DocumentInspectTaskState) (task.Status, error) {
	node, err := allocateContentProcessingNode(ctx, fm.dep, state.NodeID)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to allocate content processing node: %w", err)
	}

	state.NodeID = node.ID()
	if node.IsMaster() {
		if _, err := fm.InspectAndSaveDocument(ctx, state.Uri, state.EntityID); err != nil {
			return task.StatusError, err
		}
		return task.StatusCompleted, nil
	}

	payload, err := fm.buildSlaveDocumentInspectPayload(ctx, state.Uri, state.FileID, state.EntityID)
	if err != nil {
		return task.StatusError, err
	}
	if payload == nil {
		return task.StatusCompleted, nil
	}

	stateRaw, err := marshalSlaveContentProcessingState(slaveContentProcessingKindDocumentInspect, payload)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to marshal slave document inspect payload: %w", err)
	}

	taskID, err := node.CreateTask(ctx, queue.SlaveContentProcessingTaskType, stateRaw)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to create slave content processing task: %w", err)
	}

	state.Phase = DocumentInspectTaskPhaseAwaitSlave
	state.SlaveID = taskID
	t.ResumeAfter(10 * time.Second)
	return task.StatusSuspending, nil
}

func (t *DocumentInspectTask) awaitSlaveInspection(ctx context.Context, fm *manager, state *DocumentInspectTaskState) (task.Status, error) {
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
			return task.StatusError, fmt.Errorf("failed to parse slave document inspect result: %w", err)
		}

		result := &DocumentInspection{}
		if len(wrapper.Result) > 0 {
			if err := json.Unmarshal(wrapper.Result, result); err != nil {
				return task.StatusError, fmt.Errorf("failed to unmarshal slave document inspect result: %w", err)
			}
		}

		if err := fm.applyDocumentInspection(ctx, state.Uri, state.FileID, state.OwnerID, state.EntityID, result); err != nil {
			return task.StatusError, err
		}

		state.Phase = DocumentInspectTaskPhasePending
		state.NodeID = 0
		state.SlaveID = 0
		return task.StatusCompleted, nil
	case task.StatusError:
		return task.StatusError, fmt.Errorf("slave content processing task failed: %s%s (%w)", summary.Error, slaveTaskDiagnostic(summary), queue.CriticalErr)
	case task.StatusCanceled:
		return task.StatusError, fmt.Errorf("slave content processing task canceled%s (%w)", slaveTaskDiagnostic(summary), queue.CriticalErr)
	default:
		t.ResumeAfter(30 * time.Second)
		return task.StatusSuspending, nil
	}
}

func (m *manager) documentInspectForNewEntity(ctx context.Context, session *fs.UploadSession) {
	if session.Props.EntityType != nil && *session.Props.EntityType != types.EntityTypeVersion {
		return
	}
	if !m.shouldInspectDocument(ctx, session.Props.Uri.Name(), session.Props.Size) {
		return
	}

	task, err := NewDocumentInspectTask(ctx, session.Props.Uri, session.FileID, m.user.ID, session.EntityID, m.user)
	if err != nil {
		m.l.Warning("Failed to create document inspect task: %s", err)
		return
	}
	if err := m.dep.ContentProcessingQueue(ctx).QueueTask(ctx, task); err != nil {
		m.l.Warning("Failed to queue document inspect task: %s", err)
	}
}

func (m *manager) shouldInspectDocument(ctx context.Context, fileName string, fileSize int64) bool {
	extractor := m.dep.TextExtractor(ctx)
	tika, ok := extractor.(*tikaextractor.TikaExtractor)
	if !ok {
		return false
	}

	return ShouldExtractText(tika, fileName, fileSize)
}

func (m *manager) InspectAndSaveDocument(ctx context.Context, uri *fs.URI, entityID int) (*DocumentInspection, error) {
	file, targetVersion, shouldProcess, err := m.resolveDocumentInspectTarget(ctx, uri, 0, entityID)
	if err != nil {
		return nil, err
	}
	if !shouldProcess {
		return nil, nil
	}

	result, err := m.inspectDocumentEntity(ctx, file.Name(), targetVersion, nil)
	if err != nil {
		return nil, err
	}
	if err := m.applyDocumentInspection(ctx, uri, file.ID(), file.OwnerID(), file.PrimaryEntityID(), result); err != nil {
		return nil, err
	}

	return result, nil
}

func (m *manager) applyDocumentInspection(ctx context.Context, uri *fs.URI, fileID, ownerID, entityID int, result *DocumentInspection) error {
	if uri == nil {
		return nil
	}

	patches := []fs.MetadataPatch{
		{Key: dbfs.DocInspectMimeKey, Remove: true, Private: true},
		{Key: dbfs.DocInspectParserKey, Remove: true, Private: true},
		{Key: dbfs.DocInspectLanguageKey, Remove: true, Private: true},
		{Key: dbfs.DocInspectTitleKey, Remove: true, Private: true},
		{Key: dbfs.DocInspectAuthorKey, Remove: true, Private: true},
		{Key: dbfs.DocInspectEntityIDKey, Remove: true, Private: true},
	}
	if result != nil {
		if result.MimeType != "" {
			patches = append(patches, fs.MetadataPatch{Key: dbfs.DocInspectMimeKey, Value: result.MimeType, Private: true})
		}
		if result.Parser != "" {
			patches = append(patches, fs.MetadataPatch{Key: dbfs.DocInspectParserKey, Value: result.Parser, Private: true})
		}
		if result.Language != "" {
			patches = append(patches, fs.MetadataPatch{Key: dbfs.DocInspectLanguageKey, Value: result.Language, Private: true})
		}
		if result.Title != "" {
			patches = append(patches, fs.MetadataPatch{Key: dbfs.DocInspectTitleKey, Value: result.Title, Private: true})
		}
		if result.Author != "" {
			patches = append(patches, fs.MetadataPatch{Key: dbfs.DocInspectAuthorKey, Value: result.Author, Private: true})
		}
		if result.EntityID > 0 {
			patches = append(patches, fs.MetadataPatch{Key: dbfs.DocInspectEntityIDKey, Value: fmt.Sprintf("%d", result.EntityID), Private: true})
		}
	}

	if err := m.fs.PatchMetadata(ctx, []*fs.URI{uri}, patches...); err != nil {
		return fmt.Errorf("failed to save document inspection metadata: %s (%w)", err, queue.CriticalErr)
	}

	m.queueFullTextSync(ctx, uri, fileID, ownerID, entityID)
	return nil
}

func (m *manager) inspectDocumentEntity(ctx context.Context, fileName string, entity fs.Entity, policyOverride *ent.StoragePolicy) (*DocumentInspection, error) {
	extractor := m.dep.TextExtractor(ctx)
	tika, ok := extractor.(*tikaextractor.TikaExtractor)
	if !ok {
		return nil, nil
	}

	return m.inspectDocumentEntityWithExtractor(ctx, tika, fileName, entity, policyOverride)
}

func (m *manager) inspectDocumentEntityWithExtractor(ctx context.Context, tika *tikaextractor.TikaExtractor, fileName string, entity fs.Entity, policyOverride *ent.StoragePolicy) (*DocumentInspection, error) {
	if tika == nil {
		return nil, nil
	}

	source, err := m.GetEntitySource(ctx, 0, fs.WithEntity(entity), fs.WithPolicy(policyOverride))
	if err != nil {
		return nil, fmt.Errorf("failed to get entity source: %w", err)
	}
	defer source.Close()

	raw, err := tika.RMetaFile(ctx, source, fileName, tikaextractor.ArtifactOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to inspect document using tika: %w", err)
	}

	result := parseTikaDocumentInspection(raw)
	if result != nil {
		result.EntityID = entity.ID()
	}
	return result, nil
}
