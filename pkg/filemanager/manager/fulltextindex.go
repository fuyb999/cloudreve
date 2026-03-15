package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

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
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/samber/lo"
)

type (
	FullTextIndexTask struct {
		*queue.DBTask
	}

	FullTextIndexTaskItem struct {
		Uri      *fs.URI `json:"uri,omitempty"`
		EntityID int     `json:"entity_id,omitempty"`
		FileID   int     `json:"file_id"`
		OwnerID  int     `json:"owner_id,omitempty"`
	}

	FullTextIndexTaskState struct {
		Uri      *fs.URI                 `json:"uri,omitempty"`
		EntityID int                     `json:"entity_id,omitempty"`
		FileID   int                     `json:"file_id,omitempty"`
		OwnerID  int                     `json:"owner_id,omitempty"`
		FileIDs  []int                   `json:"file_ids,omitempty"`
		Files    []FullTextIndexTaskItem `json:"files,omitempty"`
	}

	ftsFileInfo struct {
		FileID   int
		OwnerID  int
		EntityID int
		FileName string
	}
)

var fullTextMergeableTaskTypes = []string{
	queue.FullTextIndexTaskType,
}

var fullTextEnqueueLocks [64]sync.Mutex
var fullTextPendingMergeLock sync.Mutex

const fullTextMaxFilesPerTask = 64

func (m *manager) SearchFullText(ctx context.Context, query string, offset int) (*FullTextSearchResults, error) {
	indexer := m.dep.SearchIndexer(ctx)
	results, total, err := indexer.Search(ctx, m.user.ID, query, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to search full text: %w", err)
	}

	if len(results) == 0 {
		// No results.
		return &FullTextSearchResults{}, nil
	}

	// Traverse each file in result
	files := lo.FilterMap(results, func(result searcher.SearchResult, _ int) (FullTextSearchResult, bool) {
		file, err := m.TraverseFile(ctx, result.FileID)
		if err != nil {
			m.l.Debug("Failed to traverse file %d for full text search: %s, skipping.", result.FileID, err)
			return FullTextSearchResult{}, false
		}

		return FullTextSearchResult{
			File:    file,
			Content: result.Text,
		}, true
	})

	if len(files) == 0 {
		// No valid files, run next offset
		return m.SearchFullText(ctx, query, offset+len(results))
	}

	return &FullTextSearchResults{
		Hits:  files,
		Total: total,
	}, nil
}

func init() {
	queue.RegisterResumableTaskFactory(queue.FullTextIndexTaskType, NewFullTextIndexTaskFromModel)
	queue.RegisterResumableTaskFactory(queue.FullTextCopyTaskType, NewFullTextCopyTaskFromModel)
	queue.RegisterResumableTaskFactory(queue.FullTextChangeOwnerTaskType, NewFullTextChangeOwnerTaskFromModel)
	queue.RegisterResumableTaskFactory(queue.FullTextDeleteTaskType, NewFullTextDeleteTaskFromModel)
}

func NewFullTextIndexTask(ctx context.Context, uri *fs.URI, entityID, fileID, ownerID int, creator *ent.User) (*FullTextIndexTask, error) {
	state := newFullTextIndexTaskState(uri, entityID, fileID, ownerID)
	stateBytes, err := marshalFullTextIndexTaskState(state)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	return &FullTextIndexTask{
		DBTask: &queue.DBTask{
			DirectOwner: creator,
			Task: &ent.Task{
				Type:          queue.FullTextIndexTaskType,
				CorrelationID: logging.CorrelationID(ctx),
				PrivateState:  string(stateBytes),
				PublicState:   &types.TaskPublicState{},
			},
		},
	}, nil
}

func NewFullTextIndexTaskFromModel(t *ent.Task) queue.Task {
	return &FullTextIndexTask{
		DBTask: &queue.DBTask{
			Task: t,
		},
	}
}

func newFullTextIndexTaskState(uri *fs.URI, entityID, fileID, ownerID int) *FullTextIndexTaskState {
	state := &FullTextIndexTaskState{}
	state.Upsert(FullTextIndexTaskItem{
		Uri:      uri,
		EntityID: entityID,
		FileID:   fileID,
		OwnerID:  ownerID,
	})
	return state
}

func parseFullTextIndexTaskState(raw string) (*FullTextIndexTaskState, error) {
	state := &FullTextIndexTaskState{}
	if raw == "" {
		return state, nil
	}

	if err := json.Unmarshal([]byte(raw), state); err != nil {
		return nil, err
	}

	state.normalize()
	return state, nil
}

func marshalFullTextIndexTaskState(state *FullTextIndexTaskState) ([]byte, error) {
	state.normalize()
	return json.Marshal(state)
}

func (s *FullTextIndexTaskState) normalize() {
	items := append([]FullTextIndexTaskItem(nil), s.Files...)
	if len(items) == 0 && s.FileID > 0 {
		items = append(items, FullTextIndexTaskItem{
			Uri:      s.Uri,
			EntityID: s.EntityID,
			FileID:   s.FileID,
			OwnerID:  s.OwnerID,
		})
	}

	normalized := make([]FullTextIndexTaskItem, 0, len(items))
	positions := make(map[int]int, len(items))
	for _, item := range items {
		if item.FileID <= 0 {
			continue
		}

		if idx, ok := positions[item.FileID]; ok {
			normalized[idx] = item
			continue
		}

		positions[item.FileID] = len(normalized)
		normalized = append(normalized, item)
	}

	s.Files = normalized
	if len(normalized) == 0 {
		s.Uri = nil
		s.EntityID = 0
		s.FileID = 0
		s.OwnerID = 0
		s.FileIDs = nil
		return
	}

	s.FileIDs = make([]int, 0, len(normalized))
	for _, item := range normalized {
		s.FileIDs = append(s.FileIDs, item.FileID)
	}

	head := normalized[0]
	s.Uri = head.Uri
	s.EntityID = head.EntityID
	s.FileID = head.FileID
	s.OwnerID = head.OwnerID
}

func (s *FullTextIndexTaskState) Upsert(item FullTextIndexTaskItem) {
	s.normalize()
	if item.FileID <= 0 {
		return
	}

	for i := range s.Files {
		if s.Files[i].FileID == item.FileID {
			s.Files[i] = item
			s.normalize()
			return
		}
	}

	s.Files = append(s.Files, item)
	s.normalize()
}

func (s *FullTextIndexTaskState) Remove(fileID int) bool {
	s.normalize()
	if fileID <= 0 || len(s.Files) == 0 {
		return false
	}

	filtered := s.Files[:0]
	removed := false
	for _, item := range s.Files {
		if item.FileID == fileID {
			removed = true
			continue
		}
		filtered = append(filtered, item)
	}

	if !removed {
		return false
	}

	s.Files = append([]FullTextIndexTaskItem(nil), filtered...)
	s.normalize()
	return true
}

func (s *FullTextIndexTaskState) Contains(fileID int) bool {
	s.normalize()
	for _, item := range s.Files {
		if item.FileID == fileID {
			return true
		}
	}
	return false
}

func (s *FullTextIndexTaskState) Items() []FullTextIndexTaskItem {
	s.normalize()
	return append([]FullTextIndexTaskItem(nil), s.Files...)
}

func (s *FullTextIndexTaskState) Len() int {
	s.normalize()
	return len(s.Files)
}

type (
	FullTextCopyTask struct {
		*queue.DBTask
	}

	FullTextCopyTaskState struct {
		Uri            *fs.URI `json:"uri"`
		OriginalFileID int     `json:"original_file_id"`
		FileID         int     `json:"file_id"`
		OwnerID        int     `json:"owner_id"`
		EntityID       int     `json:"entity_id"`
	}
)

func NewFullTextCopyTask(ctx context.Context, uri *fs.URI, originalFileID, fileID, ownerID, entityID int, creator *ent.User) (*FullTextCopyTask, error) {
	state := &FullTextCopyTaskState{
		Uri:            uri,
		OriginalFileID: originalFileID,
		FileID:         fileID,
		OwnerID:        ownerID,
		EntityID:       entityID,
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	return &FullTextCopyTask{
		DBTask: &queue.DBTask{
			DirectOwner: creator,
			Task: &ent.Task{
				Type:          queue.FullTextCopyTaskType,
				CorrelationID: logging.CorrelationID(ctx),
				PrivateState:  string(stateBytes),
				PublicState:   &types.TaskPublicState{},
			},
		},
	}, nil
}

func NewFullTextCopyTaskFromModel(t *ent.Task) queue.Task {
	return &FullTextCopyTask{
		DBTask: &queue.DBTask{
			Task: t,
		},
	}
}

func (t *FullTextCopyTask) Do(ctx context.Context) (task.Status, error) {
	dep := dependency.FromContext(ctx)
	l := dep.Logger()
	fm := NewFileManager(dep, inventory.UserFromContext(ctx)).(*manager)

	if !fm.settings.FTSEnabled(ctx) {
		l.Debug("FTS disabled, skipping full text copy task.")
		return task.StatusCompleted, nil
	}

	var state FullTextCopyTaskState
	if err := json.Unmarshal([]byte(t.State()), &state); err != nil {
		return task.StatusError, fmt.Errorf("failed to unmarshal state: %s (%w)", err, queue.CriticalErr)
	}

	status, err := performIndexing(ctx, fm, state.FileID)
	if err == nil {
		l.Debug("Successfully rebuilt full text index for copied file %d.", state.FileID)
	}
	return status, err
}

type (
	FullTextChangeOwnerTask struct {
		*queue.DBTask
	}

	FullTextChangeOwnerTaskState struct {
		Uri             *fs.URI `json:"uri"`
		EntityID        int     `json:"entity_id"`
		FileID          int     `json:"file_id"`
		OriginalOwnerID int     `json:"original_owner_id"`
		NewOwnerID      int     `json:"new_owner_id"`
	}
)

func NewFullTextChangeOwnerTask(ctx context.Context, uri *fs.URI, entityID, fileID, originalOwnerID, newOwnerID int, creator *ent.User) (*FullTextChangeOwnerTask, error) {
	state := &FullTextChangeOwnerTaskState{
		Uri:             uri,
		EntityID:        entityID,
		FileID:          fileID,
		OriginalOwnerID: originalOwnerID,
		NewOwnerID:      newOwnerID,
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	return &FullTextChangeOwnerTask{
		DBTask: &queue.DBTask{
			DirectOwner: creator,
			Task: &ent.Task{
				Type:          queue.FullTextChangeOwnerTaskType,
				CorrelationID: logging.CorrelationID(ctx),
				PrivateState:  string(stateBytes),
				PublicState:   &types.TaskPublicState{},
			},
		},
	}, nil
}

func NewFullTextChangeOwnerTaskFromModel(t *ent.Task) queue.Task {
	return &FullTextChangeOwnerTask{
		DBTask: &queue.DBTask{
			Task: t,
		},
	}
}

func (t *FullTextChangeOwnerTask) Do(ctx context.Context) (task.Status, error) {
	dep := dependency.FromContext(ctx)
	l := dep.Logger()
	fm := NewFileManager(dep, inventory.UserFromContext(ctx)).(*manager)

	if !fm.settings.FTSEnabled(ctx) {
		l.Debug("FTS disabled, skipping full text change owner task.")
		return task.StatusCompleted, nil
	}

	var state FullTextChangeOwnerTaskState
	if err := json.Unmarshal([]byte(t.State()), &state); err != nil {
		return task.StatusError, fmt.Errorf("failed to unmarshal state: %s (%w)", err, queue.CriticalErr)
	}

	status, err := performIndexing(ctx, fm, state.FileID)
	if err == nil {
		l.Debug("Successfully rebuilt full text index for owner-updated file %d.", state.FileID)
	}
	return status, err
}

type (
	FullTextDeleteTask struct {
		*queue.DBTask
	}

	FullTextDeleteTaskState struct {
		FileIDs []int `json:"file_ids"`
	}
)

func NewFullTextDeleteTask(ctx context.Context, fileIDs []int, creator *ent.User) (*FullTextDeleteTask, error) {
	state := &FullTextDeleteTaskState{
		FileIDs: fileIDs,
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	return &FullTextDeleteTask{
		DBTask: &queue.DBTask{
			DirectOwner: creator,
			Task: &ent.Task{
				Type:          queue.FullTextDeleteTaskType,
				CorrelationID: logging.CorrelationID(ctx),
				PrivateState:  string(stateBytes),
				PublicState:   &types.TaskPublicState{},
			},
		},
	}, nil
}

func NewFullTextDeleteTaskFromModel(t *ent.Task) queue.Task {
	return &FullTextDeleteTask{
		DBTask: &queue.DBTask{
			Task: t,
		},
	}
}

func (t *FullTextDeleteTask) Do(ctx context.Context) (task.Status, error) {
	dep := dependency.FromContext(ctx)
	l := dep.Logger()
	fm := NewFileManager(dep, inventory.UserFromContext(ctx)).(*manager)

	var state FullTextDeleteTaskState
	if err := json.Unmarshal([]byte(t.State()), &state); err != nil {
		return task.StatusError, fmt.Errorf("failed to unmarshal state: %s (%w)", err, queue.CriticalErr)
	}

	if fm.settings.FTSEnabled(ctx) {
		for _, fileID := range state.FileIDs {
			status, err := performIndexing(ctx, fm, fileID)
			if err != nil {
				return status, err
			}
		}

		l.Debug("Successfully reconciled full text index for %d file(s) from legacy delete task.", len(state.FileIDs))
		return task.StatusCompleted, nil
	}

	indexer := dep.SearchIndexer(ctx)
	if err := indexer.DeleteByFileIDs(ctx, state.FileIDs...); err != nil {
		return task.StatusError, fmt.Errorf("failed to delete index for %d file(s): %w", len(state.FileIDs), err)
	}

	l.Debug("Successfully deleted index for %d file(s).", len(state.FileIDs))
	return task.StatusCompleted, nil
}

func (t *FullTextIndexTask) Do(ctx context.Context) (task.Status, error) {
	dep := dependency.FromContext(ctx)
	l := dep.Logger()
	fm := NewFileManager(dep, inventory.UserFromContext(ctx)).(*manager)

	// Check FTS enabled
	if !fm.settings.FTSEnabled(ctx) {
		l.Debug("FTS disabled, skipping full text index task.")
		return task.StatusCompleted, nil
	}

	// Unmarshal state
	state, err := parseFullTextIndexTaskState(t.State())
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to unmarshal state: %s (%w)", err, queue.CriticalErr)
	}

	items := state.Items()
	if len(items) == 0 {
		l.Debug("No files left in full text reconcile task, skipping.")
		return task.StatusCompleted, nil
	}

	for _, item := range items {
		status, err := performIndexing(ctx, fm, item.FileID)
		if err != nil {
			return status, err
		}
	}

	l.Debug("Successfully reconciled full text index for %d file(s).", len(items))
	return task.StatusCompleted, nil
}

func performIndexing(ctx context.Context, fm *manager, fileID int) (task.Status, error) {
	dep := dependency.FromContext(ctx)
	l := dep.Logger()
	indexer := dep.SearchIndexer(ctx)

	doc, uri, err := fm.buildFTSFileDocument(ctx, fileID)
	if err != nil {
		if shouldIgnoreFTSSyncError(err) {
			if err := indexer.DeleteByFileIDs(ctx, fileID); err != nil {
				return task.StatusError, fmt.Errorf("failed to delete stale index for file %d: %w", fileID, err)
			}

			l.Debug("File %d disappeared before full text sync finished, removed stale index entry.", fileID)
			return task.StatusCompleted, nil
		}
		return task.StatusError, fmt.Errorf("failed to build search document for file %d: %w", fileID, err)
	}

	if err := indexer.UpsertFile(ctx, doc); err != nil {
		return task.StatusError, fmt.Errorf("failed to index file %d: %w", fileID, err)
	}

	if doc.EntityID > 0 && uri != nil {
		if err := fm.fs.PatchMetadata(ctx, []*fs.URI{uri}, fs.MetadataPatch{
			Key:   dbfs.FullTextIndexKey,
			Value: hashid.EncodeEntityID(fm.hasher, doc.EntityID),
		}); err != nil {
			return task.StatusError, fmt.Errorf("failed to patch metadata: %w", err)
		}
	}

	l.Debug("Successfully indexed file %d for owner %d.", fileID, doc.OwnerID)
	return task.StatusCompleted, nil
}

func shouldIgnoreFTSSyncError(err error) bool {
	var notFound *ent.NotFoundError
	return errors.As(err, &notFound)
}

// ShouldExtractText checks if a file is eligible for text extraction based on
// the extractor's supported extensions and max file size. This is exported for
// use by the rebuild index workflow.
func ShouldExtractText(extractor searcher.TextExtractor, fileName string, size int64) bool {
	return util.IsInExtensionList(extractor.Exts(), fileName) && extractor.MaxFileSize() > size
}

// shouldIndexFullText checks if a file should be indexed for full-text search.
func (m *manager) shouldIndexFullText(ctx context.Context, fileName string, size int64) bool {
	if !m.settings.FTSEnabled(ctx) {
		return false
	}

	extractor := m.dep.TextExtractor(ctx)
	return ShouldExtractText(extractor, fileName, size)
}

// fullTextIndexForNewEntity creates and queues a full text index task for a newly uploaded entity.
func (m *manager) fullTextIndexForNewEntity(ctx context.Context, session *fs.UploadSession, owner int) {
	if session.Props.EntityType != nil && *session.Props.EntityType != types.EntityTypeVersion {
		return
	}

	if !m.settings.FTSEnabled(ctx) {
		return
	}

	m.queueFullTextReconcile(ctx, session.Props.Uri, session.FileID, owner, session.EntityID)
}

func (m *manager) queueFullTextSync(ctx context.Context, uri *fs.URI, fileID, ownerID, entityID int) {
	m.queueFullTextReconcile(ctx, uri, fileID, ownerID, entityID)
}

func (m *manager) queueFullTextDelete(ctx context.Context, fileID int) {
	m.queueFullTextReconcile(ctx, nil, fileID, 0, 0)
}

func (m *manager) queueFullTextReconcile(ctx context.Context, uri *fs.URI, fileID, ownerID, entityID int) {
	if !m.settings.FTSEnabled(ctx) || fileID <= 0 {
		return
	}

	lock := &fullTextEnqueueLocks[fileID%len(fullTextEnqueueLocks)]
	lock.Lock()
	defer lock.Unlock()

	state := newFullTextIndexTaskState(uri, entityID, fileID, ownerID)
	merged, err := m.mergePendingFullTextTask(ctx, state)
	if err != nil {
		m.l.Warning("Failed to merge pending full text reconcile task for file %d: %s", fileID, err)
	}
	if merged {
		return
	}

	t, err := NewFullTextIndexTask(ctx, uri, entityID, fileID, ownerID, m.user)
	if err != nil {
		m.l.Warning("Failed to create full text reconcile task: %s", err)
		return
	}

	if err := m.dep.MediaMetaQueue(ctx).QueueTask(ctx, t); err != nil {
		m.l.Warning("Failed to queue full text reconcile task: %s", err)
	}
}

func (m *manager) mergePendingFullTextTask(ctx context.Context, state *FullTextIndexTaskState) (bool, error) {
	items := state.Items()
	if len(items) == 0 {
		return false, nil
	}
	item := items[0]

	fullTextPendingMergeLock.Lock()
	defer fullTextPendingMergeLock.Unlock()

	candidates, err := m.dep.TaskClient().GetPendingTasks(ctx, fullTextMergeableTaskTypes...)
	if err != nil {
		return false, err
	}

	type pendingTaskState struct {
		task  *ent.Task
		state *FullTextIndexTaskState
	}

	pending := make([]pendingTaskState, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Status != task.StatusQueued && candidate.Status != task.StatusSuspending {
			continue
		}

		parsed, err := parseFullTextIndexTaskState(candidate.PrivateState)
		if err != nil {
			m.l.Warning("Failed to parse pending full text task %d state: %s", candidate.ID, err)
			continue
		}

		pending = append(pending, pendingTaskState{task: candidate, state: parsed})
	}

	if len(pending) == 0 {
		return false, nil
	}

	targetIndex := -1
	for i := range pending {
		if !pending[i].state.Contains(item.FileID) {
			continue
		}
		if targetIndex == -1 || pending[i].task.UpdatedAt.After(pending[targetIndex].task.UpdatedAt) {
			targetIndex = i
		}
	}

	if targetIndex == -1 {
		for i := range pending {
			if pending[i].state.Len() >= fullTextMaxFilesPerTask {
				continue
			}
			if targetIndex == -1 || pending[i].task.UpdatedAt.After(pending[targetIndex].task.UpdatedAt) {
				targetIndex = i
			}
		}
	}

	if targetIndex == -1 {
		return false, nil
	}

	for i := range pending {
		if i == targetIndex {
			continue
		}
		if !pending[i].state.Remove(item.FileID) {
			continue
		}

		stateBytes, err := marshalFullTextIndexTaskState(pending[i].state)
		if err != nil {
			return false, err
		}

		updated, err := m.dep.TaskClient().UpdatePrivateState(ctx, pending[i].task, string(stateBytes))
		if err != nil {
			return false, err
		}
		m.updatePendingTaskStateInRegistry(updated.ID, updated.PrivateState)
	}

	pending[targetIndex].state.Upsert(item)
	stateBytes, err := marshalFullTextIndexTaskState(pending[targetIndex].state)
	if err != nil {
		return false, err
	}

	updated, err := m.dep.TaskClient().UpdatePrivateState(ctx, pending[targetIndex].task, string(stateBytes))
	if err != nil {
		return false, err
	}
	m.updatePendingTaskStateInRegistry(updated.ID, updated.PrivateState)
	m.l.Debug(
		"Merged full text reconcile task for file %d into pending task %d with %d file(s).",
		item.FileID,
		updated.ID,
		pending[targetIndex].state.Len(),
	)
	return true, nil
}

func (m *manager) updatePendingTaskStateInRegistry(taskID int, privateState string) {
	registry := m.dep.TaskRegistry()
	if registry == nil {
		return
	}

	pending, ok := registry.Get(taskID)
	if !ok {
		return
	}
	pending.UpdateState(privateState)
}

func (m *manager) processIndexDiff(ctx context.Context, diff *fs.IndexDiff) {
	if diff == nil {
		return
	}

	for _, update := range diff.IndexToUpdate {
		m.queueFullTextSync(ctx, &update.Uri, update.FileID, update.OwnerID, update.EntityID)
	}

	for _, cp := range diff.IndexToCopy {
		m.queueFullTextSync(ctx, &cp.Uri, cp.FileID, cp.OwnerID, cp.EntityID)
	}

	for _, change := range diff.IndexToChangeOwner {
		m.queueFullTextSync(ctx, &change.Uri, change.FileID, change.NewOwnerID, change.EntityID)
	}

	if len(diff.IndexToDelete) > 0 && m.dep.SettingProvider().FTSEnabled(ctx) {
		for _, fileID := range diff.IndexToDelete {
			m.queueFullTextDelete(ctx, fileID)
		}
	}

	for _, rename := range diff.IndexToRename {
		m.queueFullTextSync(ctx, &rename.Uri, rename.FileID, 0, rename.EntityID)
	}
}
