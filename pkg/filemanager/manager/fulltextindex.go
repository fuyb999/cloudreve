package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/cluster"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	tikaextractor "github.com/cloudreve/Cloudreve/v4/pkg/searcher/extractor"
	searchindexer "github.com/cloudreve/Cloudreve/v4/pkg/searcher/indexer"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/samber/lo"
)

type (
	FullTextIndexTask struct {
		*queue.DBTask

		progress queue.Progresses
		state    *FullTextIndexTaskState
	}

	FullTextIndexTaskPhase string

	FullTextIndexTaskItem struct {
		Uri              *fs.URI                       `json:"uri,omitempty"`
		EntityID         int                           `json:"entity_id,omitempty"`
		FileID           int                           `json:"file_id"`
		OwnerID          int                           `json:"owner_id,omitempty"`
		PublicVisibility *publicshare.VisibilityResult `json:"public_visibility,omitempty"`
	}

	FullTextIndexTaskState struct {
		Uri               *fs.URI                 `json:"uri,omitempty"`
		EntityID          int                     `json:"entity_id,omitempty"`
		FileID            int                     `json:"file_id,omitempty"`
		OwnerID           int                     `json:"owner_id,omitempty"`
		FileIDs           []int                   `json:"file_ids,omitempty"`
		Files             []FullTextIndexTaskItem `json:"files,omitempty"`
		Phase             FullTextIndexTaskPhase  `json:"phase,omitempty"`
		NodeID            int                     `json:"node_id,omitempty"`
		LastNodeID        int                     `json:"last_node_id,omitempty"`
		SlaveID           int                     `json:"slave_id,omitempty"`
		ExternalRequestID string                  `json:"external_request_id,omitempty"`
		Active            *FullTextIndexTaskItem  `json:"active,omitempty"`
	}

	ftsFileInfo struct {
		FileID   int
		OwnerID  int
		EntityID int
		FileName string
	}
)

func (i FullTextIndexTaskItem) IsDeleteOnly() bool {
	return i.FileID > 0 && i.Uri == nil && i.EntityID == 0 && i.OwnerID == 0
}

func (i FullTextIndexTaskItem) HasPrimaryEntity() bool {
	return i.EntityID > 0
}

var fullTextMergeableTaskTypes = []string{
	queue.FullTextIndexTaskType,
}

var fullTextEnqueueLocks [64]sync.Mutex
var fullTextPendingMergeLock sync.Mutex
var fullTextPerformIndexing = performIndexing
var fullTextCloneFTSSidecarsForCopiedFile = func(ctx context.Context, fm *manager, originalFileID, targetFileID int) (bool, error) {
	return fm.cloneFTSSidecarsForCopiedFile(ctx, originalFileID, targetFileID)
}
var fullTextSourceExtractionPending = func(ctx context.Context, dep dependency.Dep, originalFileID int) (bool, string, error) {
	return sourceFullTextExtractionPending(ctx, dep, originalFileID)
}

func pauseFullTextIndexingForRetryableError(l logging.Logger, err error) bool {
	if !searchindexer.IsRetryableUnavailableError(err) {
		return false
	}
	if l != nil {
		l.Warning("Full text indexing paused while search indexer recovers: %s", err)
	}

	return true
}

const (
	fullTextMaxFilesPerTask = 64

	fullTextIndexPhasePending       FullTextIndexTaskPhase = ""
	fullTextIndexPhaseAwaitSlave    FullTextIndexTaskPhase = "await_slave_extract"
	fullTextIndexPhaseAwaitExternal FullTextIndexTaskPhase = "await_external_extract"

	fullTextCopyPhasePending     FullTextCopyTaskPhase = ""
	fullTextCopyPhaseAwaitSource FullTextCopyTaskPhase = "await_source_extract"
)

func (m *manager) SearchFullText(ctx context.Context, query string, offset int, base *fs.URI) (*FullTextSearchResults, error) {
	indexer := m.dep.SearchIndexer(ctx)
	if searchindexer.IsNoopIndexer(indexer) {
		return nil, searchindexer.UnavailableError(indexer)
	}
	searchReq := &searcher.SearchRequest{
		Query:   query,
		Offset:  offset,
		OwnerID: &m.user.ID,
	}

	var publicVisibility *publicshare.VisibilityResult

	if base != nil && base.FileSystem() == constants.FileSystemPublic {
		publicService := publicshare.NewService(m.l, m.dep.FileClient(), m.dep.SettingClient(), m.hasher)
		visibility, err := publicService.ResolveVisibility(ctx, m.user)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve public visibility for search: %w", err)
		}
		publicVisibility = visibility

		filter := visibility.Filter
		searchReq.OwnerID = nil
		if !base.IsSame(publicshare.BuildPublicURI(), hashid.EncodeUserID(m.hasher, m.user.ID)) {
			target, err := m.Get(ctx, base)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve public search base: %w", err)
			}

			scope := &publicshare.FileFilterExpr{
				Operator: publicshare.FileFilterOpAnd,
				Children: []*publicshare.FileFilterExpr{
					filter,
				},
			}
			if model, ok := target.(*dbfs.File); ok && model.Model.TreePath != "" {
				scope.Children = append(scope.Children, &publicshare.FileFilterExpr{
					Match: &publicshare.FileFilterMatch{
						Kind:         publicshare.FileFilterMatchTreePathIn,
						StringValues: []string{model.Model.TreePath},
					},
				})
			}
			filter = scope
		}

		searchReq.VisibilityFilter = filter
	} else if base != nil && base.FileSystem() == constants.FileSystemMy {
		if normalized := m.normalizeFullTextSearchBaseURIs(base); len(normalized) > 0 {
			searchReq.SearchBaseURI = normalized[0]
			searchReq.SearchBaseURIs = normalized
		}
	}

	results, total, err := indexer.Search(ctx, searchReq)
	if err != nil {
		return nil, fmt.Errorf("failed to search full text: %w", err)
	}

	if len(results) == 0 {
		// No results.
		return &FullTextSearchResults{}, nil
	}

	// Traverse each file in result
	files := lo.FilterMap(results, func(result searcher.SearchResult, _ int) (FullTextSearchResult, bool) {
		if base != nil && base.FileSystem() == constants.FileSystemPublic {
			file, err := m.resolvePublicSearchResultFile(ctx, result.FileID, publicVisibility)
			if err != nil {
				m.l.Debug("Failed to resolve public file %d for full text search: %s, skipping.", result.FileID, err)
				return FullTextSearchResult{}, false
			}

			return FullTextSearchResult{
				File:    file,
				Content: result.Text,
			}, true
		}

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
		return m.SearchFullText(ctx, query, offset+len(results), base)
	}

	return &FullTextSearchResults{
		Hits:  files,
		Total: total,
	}, nil
}

func (m *manager) normalizeFullTextSearchBaseURI(base *fs.URI) string {
	normalized := m.normalizeFullTextSearchBaseURIs(base)
	if len(normalized) == 0 {
		return ""
	}

	return normalized[0]
}

func (m *manager) normalizeFullTextSearchBaseURIs(base *fs.URI) []string {
	if base == nil || base.U == nil {
		return nil
	}

	normalized := base.SetQuery("")
	if normalized.FileSystem() != constants.FileSystemMy || normalized.U.User != nil || m == nil || m.user == nil {
		raw := strings.TrimSuffix(normalized.String(), "/")
		if raw == "" {
			return nil
		}

		return []string{raw}
	}

	ownerless := strings.TrimSuffix(normalized.String(), "/")
	withOwner := normalized
	if withOwner.U.User == nil {
		userID := hashid.EncodeUserID(m.hasher, m.user.ID)
		if userID == "" {
			return compactSearchBaseURIs(ownerless)
		}
		withOwner.U.User = url.User(userID)
	}

	return compactSearchBaseURIs(strings.TrimSuffix(withOwner.String(), "/"), ownerless)
}

func compactSearchBaseURIs(candidates ...string) []string {
	res := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(strings.TrimSuffix(candidate, "/"))
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}

		seen[candidate] = struct{}{}
		res = append(res, candidate)
	}

	return res
}

func (m *manager) resolvePublicSearchResultFile(ctx context.Context, fileID int, visibility *publicshare.VisibilityResult) (fs.File, error) {
	publicService := publicshare.NewService(m.l, m.dep.FileClient(), m.dep.SettingClient(), m.hasher)
	fileModel, err := m.dep.FileClient().GetByID(context.WithValue(ctx, inventory.LoadFileMetadata{}, true), fileID)
	if err != nil {
		return nil, fmt.Errorf("failed to load public file %d: %w", fileID, err)
	}

	publicURI, err := publicService.ResolveVisibleURI(ctx, fileModel, visibility)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve public uri for file %d: %w", fileID, err)
	}

	return m.Get(ctx, publicURI, dbfs.WithFilePublicMetadata())
}

func init() {
	queue.RegisterResumableTaskFactory(queue.FullTextIndexTaskType, NewFullTextIndexTaskFromModel)
	queue.RegisterResumableTaskFactory(queue.FullTextCopyTaskType, NewFullTextCopyTaskFromModel)
	queue.RegisterResumableTaskFactory(queue.FullTextChangeOwnerTaskType, NewFullTextChangeOwnerTaskFromModel)
	queue.RegisterResumableTaskFactory(queue.FullTextDeleteTaskType, NewFullTextDeleteTaskFromModel)
}

func NewFullTextIndexTask(ctx context.Context, uri *fs.URI, entityID, fileID, ownerID int, creator *ent.User) (*FullTextIndexTask, error) {
	state := newFullTextIndexTaskState(ctx, uri, entityID, fileID, ownerID)
	stateBytes, err := marshalFullTextIndexTaskState(state)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	return &FullTextIndexTask{
		DBTask: &queue.DBTask{
			DirectOwner: creator,
			Task: &ent.Task{
				Type:          queue.FullTextIndexTaskType,
				CorrelationID: logging.NillableCorrelationID(ctx),
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

func newFullTextIndexTaskState(ctx context.Context, uri *fs.URI, entityID, fileID, ownerID int) *FullTextIndexTaskState {
	state := &FullTextIndexTaskState{}
	state.Upsert(FullTextIndexTaskItem{
		Uri:              uri,
		EntityID:         entityID,
		FileID:           fileID,
		OwnerID:          ownerID,
		PublicVisibility: publicshare.VisibilityOverrideFromContext(ctx),
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

	removedActive := s.Active != nil && s.Active.FileID == fileID
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
	if removedActive {
		s.Active = nil
		s.Phase = fullTextIndexPhasePending
		s.SlaveID = 0
		s.NodeID = 0
		s.ExternalRequestID = ""
	}
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

func (s *FullTextIndexTaskState) Mergeable() bool {
	s.normalize()
	return s.Active == nil && s.Phase == fullTextIndexPhasePending && s.NodeID == 0 && s.SlaveID == 0 && s.ExternalRequestID == ""
}

func (s *FullTextIndexTaskState) Items() []FullTextIndexTaskItem {
	s.normalize()
	return append([]FullTextIndexTaskItem(nil), s.Files...)
}

func (s *FullTextIndexTaskState) Len() int {
	s.normalize()
	return len(s.Files)
}

func (s *FullTextIndexTaskState) Current() (FullTextIndexTaskItem, bool) {
	s.normalize()
	if s.Active != nil && s.Active.FileID > 0 {
		return *s.Active, true
	}
	if len(s.Files) == 0 {
		return FullTextIndexTaskItem{}, false
	}
	return s.Files[0], true
}

func (s *FullTextIndexTaskState) ActivateNext() bool {
	s.normalize()
	if s.Active != nil && s.Active.FileID > 0 {
		return true
	}
	if len(s.Files) == 0 {
		return false
	}
	item := s.Files[0]
	s.Active = &item
	return true
}

func (s *FullTextIndexTaskState) CompleteActive() {
	s.normalize()
	if s.Active != nil {
		filtered := make([]FullTextIndexTaskItem, 0, len(s.Files))
		for _, item := range s.Files {
			if item.FileID == s.Active.FileID {
				continue
			}
			filtered = append(filtered, item)
		}
		s.Files = filtered
	}
	if s.NodeID > 0 {
		s.LastNodeID = s.NodeID
	}

	// Clear legacy head fields before normalize() so a completed active item
	// won't be synthesized back from the deprecated single-file fields.
	s.Uri = nil
	s.EntityID = 0
	s.FileID = 0
	s.OwnerID = 0
	s.FileIDs = nil
	s.Active = nil
	s.Phase = fullTextIndexPhasePending
	s.SlaveID = 0
	s.NodeID = 0
	s.ExternalRequestID = ""
	s.normalize()
}

type (
	FullTextCopyTask struct {
		*queue.DBTask
	}

	FullTextCopyTaskPhase string

	FullTextCopyTaskState struct {
		Uri            *fs.URI               `json:"uri"`
		OriginalFileID int                   `json:"original_file_id"`
		FileID         int                   `json:"file_id"`
		OwnerID        int                   `json:"owner_id"`
		EntityID       int                   `json:"entity_id"`
		Phase          FullTextCopyTaskPhase `json:"phase,omitempty"`
		WaitReason     string                `json:"wait_reason,omitempty"`
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
	stateBytes, err := marshalFullTextCopyTaskState(state)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	return &FullTextCopyTask{
		DBTask: &queue.DBTask{
			DirectOwner: creator,
			Task: &ent.Task{
				Type:          queue.FullTextCopyTaskType,
				CorrelationID: logging.NillableCorrelationID(ctx),
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

func parseFullTextCopyTaskState(raw string) (*FullTextCopyTaskState, error) {
	state := &FullTextCopyTaskState{}
	if raw == "" {
		return state, nil
	}

	if err := json.Unmarshal([]byte(raw), state); err != nil {
		return nil, err
	}

	return state, nil
}

func marshalFullTextCopyTaskState(state *FullTextCopyTaskState) ([]byte, error) {
	if state == nil {
		state = &FullTextCopyTaskState{}
	}

	return json.Marshal(state)
}

func (t *FullTextCopyTask) Do(ctx context.Context) (task.Status, error) {
	dep := dependency.FromContext(ctx)
	l := dep.Logger()
	fm := NewFileManager(dep, inventory.UserFromContext(ctx)).(*manager)
	defer fm.Recycle()

	if !fm.settings.FTSEnabled(ctx) {
		l.Debug("FTS disabled, skipping full text copy task.")
		return task.StatusCompleted, nil
	}

	state, err := parseFullTextCopyTaskState(t.State())
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to unmarshal state: %s (%w)", err, queue.CriticalErr)
	}

	cloned, cloneErr := fullTextCloneFTSSidecarsForCopiedFile(ctx, fm, state.OriginalFileID, state.FileID)
	if !cloned && cloneErr == nil {
		pending, reason, err := fullTextSourceExtractionPending(ctx, fm.dep, state.OriginalFileID)
		if err != nil {
			l.Warning(
				"Failed to inspect pending full text extraction for source file %d of copied file %d, falling back to rebuild: %s",
				state.OriginalFileID,
				state.FileID,
				err,
			)
		} else if pending {
			l.Debug(
				"Source file %d full text extraction is still pending for copied file %d (%s), waiting for reusable sidecar.",
				state.OriginalFileID,
				state.FileID,
				reason,
			)
			return t.suspendForSourceExtraction(state, reason)
		}
	}

	if cloneErr != nil {
		l.Warning(
			"Failed to clone full text sidecar from file %d to copied file %d, falling back to rebuild: %s",
			state.OriginalFileID,
			state.FileID,
			cloneErr,
		)
	}

	state.Phase = fullTextCopyPhasePending
	state.WaitReason = ""

	status, err := fullTextPerformIndexing(ctx, fm, state.FileID)
	if status == task.StatusSuspending && err == nil {
		t.ResumeAfter(searchindexer.RetryableUnavailableDelay)
	}
	if err == nil {
		if cloned {
			l.Debug("Successfully rebuilt full text index for copied file %d using cloned sidecar.", state.FileID)
		} else {
			l.Debug("Successfully rebuilt full text index for copied file %d.", state.FileID)
		}
	}
	return status, err
}

func (t *FullTextCopyTask) suspendForSourceExtraction(state *FullTextCopyTaskState, reason string) (task.Status, error) {
	state.Phase = fullTextCopyPhaseAwaitSource
	state.WaitReason = reason
	stateBytes, err := marshalFullTextCopyTaskState(state)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to marshal copy task state: %w", err)
	}

	t.UpdateState(string(stateBytes))
	t.ResumeAfter(10 * time.Second)
	return task.StatusSuspending, nil
}

func (t *FullTextCopyTask) Summarize(hasher hashid.Encoder) *queue.Summary {
	state, err := parseFullTextCopyTaskState(t.State())
	if err != nil {
		return nil
	}

	props := map[string]any{
		"src":              state.Uri,
		"file_id":          state.FileID,
		"owner_id":         state.OwnerID,
		"entity_id":        state.EntityID,
		"original_file_id": state.OriginalFileID,
	}
	if state.WaitReason != "" {
		props["wait_reason"] = state.WaitReason
	}

	return &queue.Summary{
		Phase: string(state.Phase),
		Props: props,
	}
}

func sourceFullTextExtractionPending(ctx context.Context, dep dependency.Dep, originalFileID int) (bool, string, error) {
	if dep == nil || dep.TaskClient() == nil || originalFileID <= 0 {
		return false, "", nil
	}

	candidates, err := dep.TaskClient().GetPendingTasks(ctx, queue.FullTextIndexTaskType)
	if err != nil {
		return false, "", err
	}

	for _, candidate := range candidates {
		if candidate == nil || candidate.Type != queue.FullTextIndexTaskType {
			continue
		}

		if candidate.Status != task.StatusQueued && candidate.Status != task.StatusProcessing && candidate.Status != task.StatusSuspending {
			continue
		}

		state, err := parseFullTextIndexTaskState(candidate.PrivateState)
		if err != nil {
			dep.Logger().Warning("Failed to parse pending full text task %d while checking source file %d: %s", candidate.ID, originalFileID, err)
			continue
		}

		if pending, reason := fullTextIndexTaskContainsActiveExtraction(state, originalFileID, candidate.Status); pending {
			return true, reason, nil
		}
	}

	return false, "", nil
}

func fullTextIndexTaskContainsActiveExtraction(state *FullTextIndexTaskState, fileID int, status task.Status) (bool, string) {
	if state == nil || fileID <= 0 {
		return false, ""
	}

	if state.Active != nil && state.Active.FileID == fileID {
		if state.Active.IsDeleteOnly() {
			return false, ""
		}
		return true, fullTextCopyWaitReasonForSource(status, state.Phase)
	}

	for _, item := range state.Items() {
		if item.FileID != fileID {
			continue
		}
		if item.IsDeleteOnly() {
			return false, ""
		}
		return true, fullTextCopyWaitReasonForSource(status, fullTextIndexPhasePending)
	}

	return false, ""
}

func fullTextCopyWaitReasonForSource(status task.Status, phase FullTextIndexTaskPhase) string {
	switch phase {
	case fullTextIndexPhaseAwaitSlave, fullTextIndexPhaseAwaitExternal:
		return string(phase)
	}

	switch status {
	case task.StatusProcessing:
		return "processing_source_extract"
	case task.StatusQueued:
		return "queued_source_extract"
	case task.StatusSuspending:
		return "suspending_source_extract"
	default:
		return string(fullTextCopyPhaseAwaitSource)
	}
}

func fullTextIndexTaskItemURI(item FullTextIndexTaskItem) string {
	if item.Uri == nil {
		return ""
	}

	return item.Uri.String()
}

func sameFullTextIndexTaskItem(a, b FullTextIndexTaskItem) bool {
	if a.FileID != b.FileID || a.OwnerID != b.OwnerID || a.EntityID != b.EntityID {
		return false
	}
	if a.IsDeleteOnly() != b.IsDeleteOnly() {
		return false
	}

	return fullTextIndexTaskItemURI(a) == fullTextIndexTaskItemURI(b)
}

func fullTextTaskContainsEquivalentItem(state *FullTextIndexTaskState, item FullTextIndexTaskItem) bool {
	if state == nil || item.FileID <= 0 {
		return false
	}

	if state.Active != nil && sameFullTextIndexTaskItem(*state.Active, item) {
		return true
	}

	for _, existing := range state.Items() {
		if sameFullTextIndexTaskItem(existing, item) {
			return true
		}
	}

	return false
}

func fullTextTaskSharesCorrelationScope(ctx context.Context, pendingTask *ent.Task) bool {
	if pendingTask == nil || pendingTask.CorrelationID == nil {
		return false
	}

	currentCorrelationID := logging.NillableCorrelationID(ctx)
	if currentCorrelationID == nil {
		return false
	}

	return *currentCorrelationID == *pendingTask.CorrelationID
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
				CorrelationID: logging.NillableCorrelationID(ctx),
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
	defer fm.Recycle()

	if !fm.settings.FTSEnabled(ctx) {
		l.Debug("FTS disabled, skipping full text change owner task.")
		return task.StatusCompleted, nil
	}

	var state FullTextChangeOwnerTaskState
	if err := json.Unmarshal([]byte(t.State()), &state); err != nil {
		return task.StatusError, fmt.Errorf("failed to unmarshal state: %s (%w)", err, queue.CriticalErr)
	}

	status, err := fullTextPerformIndexing(ctx, fm, state.FileID)
	if status == task.StatusSuspending && err == nil {
		t.ResumeAfter(searchindexer.RetryableUnavailableDelay)
	}
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
				CorrelationID: logging.NillableCorrelationID(ctx),
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
	defer fm.Recycle()

	var state FullTextDeleteTaskState
	if err := json.Unmarshal([]byte(t.State()), &state); err != nil {
		return task.StatusError, fmt.Errorf("failed to unmarshal state: %s (%w)", err, queue.CriticalErr)
	}

	if fm.settings.FTSEnabled(ctx) {
		for _, fileID := range state.FileIDs {
			status, err := fullTextPerformIndexing(ctx, fm, fileID)
			if err != nil {
				return status, err
			}
			if status == task.StatusSuspending {
				t.ResumeAfter(searchindexer.RetryableUnavailableDelay)
				return status, nil
			}
		}

		l.Debug("Successfully reconciled full text index for %d file(s) from legacy delete task.", len(state.FileIDs))
		return task.StatusCompleted, nil
	}

	indexer := dep.SearchIndexer(ctx)
	if err := indexer.DeleteByFileIDs(ctx, state.FileIDs...); err != nil {
		if pauseFullTextIndexingForRetryableError(l, err) {
			t.ResumeAfter(searchindexer.RetryableUnavailableDelay)
			return task.StatusSuspending, nil
		}

		return task.StatusError, fmt.Errorf("failed to delete index for %d file(s): %w", len(state.FileIDs), err)
	}

	l.Debug("Successfully deleted index for %d file(s).", len(state.FileIDs))
	return task.StatusCompleted, nil
}

func (t *FullTextIndexTask) Do(ctx context.Context) (task.Status, error) {
	dep := dependency.FromContext(ctx)
	l := dep.Logger()
	fm := NewFileManager(dep, inventory.UserFromContext(ctx)).(*manager)
	baseCtx := ctx

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
	t.state = state

	if state.Len() == 0 && state.Active == nil {
		l.Debug("No files left in full text reconcile task, skipping.")
		return task.StatusCompleted, nil
	}

	for {
		switch state.Phase {
		case fullTextIndexPhasePending:
			if !state.ActivateNext() {
				stateBytes, err := marshalFullTextIndexTaskState(state)
				if err != nil {
					return task.StatusError, fmt.Errorf("failed to marshal state: %w", err)
				}
				t.UpdateState(string(stateBytes))
				l.Debug("Successfully reconciled full text index.")
				return task.StatusCompleted, nil
			}

			item, _ := state.Current()
			itemCtx := baseCtx
			if item.PublicVisibility != nil {
				itemCtx = context.WithValue(baseCtx, publicshare.VisibilityOverrideCtx{}, item.PublicVisibility)
			}
			next, err := t.dispatchOrIndexLocally(itemCtx, fm, state, item)
			if err != nil {
				return task.StatusError, err
			}
			if next == task.StatusSuspending {
				return t.persistAndSuspend(state)
			}
		case fullTextIndexPhaseAwaitSlave:
			itemCtx := baseCtx
			if state.Active != nil && state.Active.PublicVisibility != nil {
				itemCtx = context.WithValue(baseCtx, publicshare.VisibilityOverrideCtx{}, state.Active.PublicVisibility)
			}
			next, err := t.awaitSlaveExtraction(itemCtx, fm, state)
			if err != nil {
				return task.StatusError, err
			}
			if next == task.StatusSuspending {
				return t.persistAndSuspend(state)
			}
		case fullTextIndexPhaseAwaitExternal:
			itemCtx := baseCtx
			if state.Active != nil && state.Active.PublicVisibility != nil {
				itemCtx = context.WithValue(baseCtx, publicshare.VisibilityOverrideCtx{}, state.Active.PublicVisibility)
			}
			next, err := t.awaitExternalExtraction(itemCtx, fm, state)
			if err != nil {
				return task.StatusError, err
			}
			if next == task.StatusSuspending {
				return t.persistAndSuspend(state)
			}
		default:
			return task.StatusError, fmt.Errorf("unknown full text task phase %q: %w", state.Phase, queue.CriticalErr)
		}
	}
}

func (t *FullTextIndexTask) dispatchOrIndexLocally(ctx context.Context, fm *manager, state *FullTextIndexTaskState, item FullTextIndexTaskItem) (task.Status, error) {
	if item.IsDeleteOnly() {
		status, err := fullTextPerformIndexing(ctx, fm, item.FileID)
		if status == task.StatusSuspending && err == nil {
			t.ResumeAfter(searchindexer.RetryableUnavailableDelay)
		}
		if err != nil {
			return status, err
		}
		if status == task.StatusSuspending {
			return status, nil
		}

		state.CompleteActive()
		return t.persistAndContinue(state)
	}

	if !item.HasPrimaryEntity() {
		status, err := fullTextPerformIndexing(ctx, fm, item.FileID)
		if status == task.StatusSuspending && err == nil {
			t.ResumeAfter(searchindexer.RetryableUnavailableDelay)
		}
		if err != nil {
			return status, err
		}
		if status == task.StatusSuspending {
			return status, nil
		}

		state.CompleteActive()
		return t.persistAndContinue(state)
	}

	node, err := allocateContentProcessingNode(ctx, fm.dep, state.NodeID)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to allocate content processing node: %w", err)
	}

	state.NodeID = node.ID()
	if state.NodeID > 0 {
		state.LastNodeID = state.NodeID
	}
	t.Lock()
	t.progress = nil
	t.Unlock()

	if !node.IsMaster() && fm.shouldOffloadFullTextToSlave(ctx) {
		payload, err := fm.buildSlaveFullTextExtractPayload(ctx, item.FileID)
		if err == nil && payload != nil && payload.Policy != nil {
			stateRaw, err := marshalSlaveContentProcessingState(slaveContentProcessingKindFullTextExtract, payload)
			if err != nil {
				return task.StatusError, fmt.Errorf("failed to marshal slave full text payload: %w", err)
			}

			taskID, err := node.CreateTask(ctx, queue.SlaveContentProcessingTaskType, stateRaw)
			if err != nil {
				return task.StatusError, fmt.Errorf("failed to create slave content processing task: %w", err)
			}

			state.Phase = fullTextIndexPhaseAwaitSlave
			state.SlaveID = taskID
			t.ResumeAfter(10 * time.Second)
			return task.StatusSuspending, nil
		}

		if err != nil {
			fm.l.Warning("Failed to build slave full text payload for file %d, falling back to local orchestration: %s", item.FileID, err)
		}
	}

	if next, handled, err := t.dispatchExternalIfConfigured(ctx, fm, state, item); handled || err != nil {
		return next, err
	}

	status, err := fullTextPerformIndexing(ctx, fm, item.FileID)
	if status == task.StatusSuspending && err == nil {
		t.ResumeAfter(searchindexer.RetryableUnavailableDelay)
	}
	if err != nil {
		return status, err
	}
	if status == task.StatusSuspending {
		return status, nil
	}

	state.CompleteActive()
	return t.persistAndContinue(state)
}

func (t *FullTextIndexTask) awaitSlaveExtraction(ctx context.Context, fm *manager, state *FullTextIndexTaskState) (task.Status, error) {
	if state.SlaveID == 0 {
		return task.StatusError, fmt.Errorf("missing slave task id in full text await phase: %w", queue.CriticalErr)
	}

	node, err := allocateContentProcessingNode(ctx, fm.dep, state.NodeID)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to resolve content processing node: %w", err)
	}

	summary, err := node.GetTask(ctx, state.SlaveID, false)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to get slave task: %w", err)
	}

	switch summary.Status {
	case task.StatusCompleted:
		slaveTaskID := state.SlaveID
		t.Lock()
		t.progress = summary.Progress
		t.Unlock()
		wrapper, err := parseSlaveContentProcessingState(summary.PrivateState)
		if err != nil {
			return task.StatusError, fmt.Errorf("failed to parse slave content processing result: %w", err)
		}

		result := &SlaveFullTextExtractResult{}
		if len(wrapper.Result) > 0 {
			if err := json.Unmarshal(wrapper.Result, result); err != nil {
				return task.StatusError, fmt.Errorf("failed to unmarshal slave full text result: %w", err)
			}
		}
		if strings.TrimSpace(result.ExternalRequestID) != "" {
			next, err := t.suspendForExternalJob(state, result.ExternalRequestID)
			if err == nil {
				clearSlaveTaskBestEffort(ctx, fm.l, node, slaveTaskID)
			}
			return next, err
		}

		item, ok := state.Current()
		if !ok {
			return task.StatusError, fmt.Errorf("missing active file in full text await phase: %w", queue.CriticalErr)
		}

		status, err := finalizeSlaveIndexedFile(ctx, fm, item.FileID, result)
		if status == task.StatusSuspending && err == nil {
			t.ResumeAfter(searchindexer.RetryableUnavailableDelay)
		}
		if err != nil {
			return status, err
		}
		if status == task.StatusSuspending {
			return status, nil
		}

		fm.l.Info(
			"Full text await finalize before complete file=%d len=%d phase=%s active=%+v",
			item.FileID,
			state.Len(),
			state.Phase,
			state.Active,
		)
		state.CompleteActive()
		clearSlaveTaskBestEffort(ctx, fm.l, node, slaveTaskID)
		fm.l.Info(
			"Full text await finalize after complete file=%d len=%d phase=%s active=%+v",
			item.FileID,
			state.Len(),
			state.Phase,
			state.Active,
		)
		return t.persistAndContinue(state)
	case task.StatusError:
		slaveTaskID := state.SlaveID
		t.Lock()
		t.progress = summary.Progress
		t.Unlock()
		item, ok := state.Current()
		if !ok {
			return task.StatusError, fmt.Errorf("missing active file in full text await phase: %w", queue.CriticalErr)
		}

		fm.l.Warning("Slave full text extraction failed for file %d, falling back to local indexing: %s%s", item.FileID, summary.Error, slaveTaskDiagnostic(summary))
		status, localErr := fullTextPerformIndexing(ctx, fm, item.FileID)
		if status == task.StatusSuspending && localErr == nil {
			t.ResumeAfter(searchindexer.RetryableUnavailableDelay)
			return status, nil
		}
		if localErr != nil {
			return status, fmt.Errorf("slave content processing task failed: %s%s; local fallback failed: %w", summary.Error, slaveTaskDiagnostic(summary), localErr)
		}

		state.CompleteActive()
		clearSlaveTaskBestEffort(ctx, fm.l, node, slaveTaskID)
		return t.persistAndContinue(state)
	case task.StatusCanceled:
		slaveTaskID := state.SlaveID
		t.Lock()
		t.progress = summary.Progress
		t.Unlock()
		item, ok := state.Current()
		if !ok {
			return task.StatusError, fmt.Errorf("missing active file in full text await phase: %w", queue.CriticalErr)
		}

		fm.l.Warning("Slave full text extraction canceled for file %d, falling back to local indexing%s", item.FileID, slaveTaskDiagnostic(summary))
		status, localErr := fullTextPerformIndexing(ctx, fm, item.FileID)
		if status == task.StatusSuspending && localErr == nil {
			t.ResumeAfter(searchindexer.RetryableUnavailableDelay)
			return status, nil
		}
		if localErr != nil {
			return status, fmt.Errorf("slave content processing task canceled%s; local fallback failed: %w", slaveTaskDiagnostic(summary), localErr)
		}

		state.CompleteActive()
		clearSlaveTaskBestEffort(ctx, fm.l, node, slaveTaskID)
		return t.persistAndContinue(state)
	default:
		t.Lock()
		t.progress = summary.Progress
		t.Unlock()
		t.ResumeAfter(30 * time.Second)
		return task.StatusSuspending, nil
	}
}

func (t *FullTextIndexTask) persistAndSuspend(state *FullTextIndexTaskState) (task.Status, error) {
	stateBytes, err := marshalFullTextIndexTaskState(state)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to marshal state: %w", err)
	}

	t.UpdateState(string(stateBytes))
	return task.StatusSuspending, nil
}

func (t *FullTextIndexTask) persistAndContinue(state *FullTextIndexTaskState) (task.Status, error) {
	stateBytes, err := marshalFullTextIndexTaskState(state)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to marshal state: %w", err)
	}

	t.UpdateState(string(stateBytes))
	return task.StatusProcessing, nil
}

func performIndexing(ctx context.Context, fm *manager, fileID int) (task.Status, error) {
	dep := dependency.FromContext(ctx)
	l := dep.Logger()
	searchIdx := dep.SearchIndexer(ctx)
	if searchindexer.IsNoopIndexer(searchIdx) {
		if searchindexer.IsRetryableNoopIndexer(searchIdx) {
			l.Warning("Full text indexing paused while search indexer recovers: %s", searchindexer.UnavailableError(searchIdx))
			return task.StatusSuspending, nil
		}
		return task.StatusError, searchindexer.UnavailableError(searchIdx)
	}

	fileModel, err := fm.loadFTSFileModel(ctx, fileID)
	if err != nil {
		if shouldIgnoreFTSSyncError(err) {
			if err := searchIdx.DeleteByFileIDs(ctx, fileID); err != nil {
				if pauseFullTextIndexingForRetryableError(l, err) {
					return task.StatusSuspending, nil
				}

				return task.StatusError, fmt.Errorf("failed to delete stale index for file %d: %w", fileID, err)
			}

			l.Debug("File %d disappeared before full text sync finished, removed stale index entry.", fileID)
			return task.StatusCompleted, nil
		}
		return task.StatusError, fmt.Errorf("failed to load search file %d: %w", fileID, err)
	}

	if fileModel.Type == int(types.FileTypeFolder) && !fm.settings.FTSSyncFolders(ctx) {
		if err := searchIdx.DeleteByFileIDs(ctx, fileID); err != nil {
			if pauseFullTextIndexingForRetryableError(l, err) {
				return task.StatusSuspending, nil
			}

			return task.StatusError, fmt.Errorf("failed to delete index for folder %d: %w", fileID, err)
		}

		if fm.canResolveFTSFileURI() {
			if uri, err := fm.resolveFTSFileURIByModel(ctx, fileModel); err != nil {
				l.Warning("Failed to resolve uri for skipped folder %d when clearing full text metadata: %s", fileID, err)
			} else {
				clearFullTextIndexMetadataBestEffort(ctx, fm, uri)
			}
		}

		l.Debug("File %d is a folder and folder synchronization is disabled, removed full text index entry.", fileID)
		return task.StatusCompleted, nil
	}

	uri, err := fm.resolveFTSFileURIByModel(ctx, fileModel)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to resolve search uri for file %d: %w", fileID, err)
	}

	if uri != nil && uri.FileSystem() == constants.FileSystemTrash {
		if err := searchIdx.DeleteByFileIDs(ctx, fileID); err != nil {
			if pauseFullTextIndexingForRetryableError(l, err) {
				return task.StatusSuspending, nil
			}

			return task.StatusError, fmt.Errorf("failed to delete index for trashed file %d: %w", fileID, err)
		}

		l.Debug("File %d is in trash, removed full text index entry.", fileID)
		return task.StatusCompleted, nil
	}

	doc, currentURI, err := fm.buildFTSFileDocument(ctx, fileID)
	if err != nil {
		if shouldIgnoreFTSSyncError(err) {
			if err := searchIdx.DeleteByFileIDs(ctx, fileID); err != nil {
				if pauseFullTextIndexingForRetryableError(l, err) {
					return task.StatusSuspending, nil
				}

				return task.StatusError, fmt.Errorf("failed to delete stale index for file %d: %w", fileID, err)
			}

			l.Debug("File %d disappeared before full text sync finished, removed stale index entry.", fileID)
			return task.StatusCompleted, nil
		}
		clearFullTextIndexMetadataBestEffort(ctx, fm, uri)
		return task.StatusError, fmt.Errorf("failed to build search document for file %d: %w", fileID, err)
	}
	if refreshedURI := metadataURIForFTSDocument(doc, currentURI); refreshedURI != nil {
		uri = refreshedURI
	}
	if uri != nil && uri.FileSystem() == constants.FileSystemTrash {
		if err := searchIdx.DeleteByFileIDs(ctx, fileID); err != nil {
			if pauseFullTextIndexingForRetryableError(l, err) {
				return task.StatusSuspending, nil
			}

			return task.StatusError, fmt.Errorf("failed to delete index for trashed file %d: %w", fileID, err)
		}

		l.Debug("File %d moved to trash before full text index upsert, removed index entry.", fileID)
		return task.StatusCompleted, nil
	}

	if err := searchIdx.UpsertFile(ctx, doc); err != nil {
		if pauseFullTextIndexingForRetryableError(l, err) {
			return task.StatusSuspending, nil
		}

		clearFullTextIndexMetadataBestEffort(ctx, fm, uri)
		return task.StatusError, fmt.Errorf("failed to index file %d: %w", fileID, err)
	}

	if uri != nil {
		if err := fm.fs.PatchMetadata(withPublicBypass(ctx, uri), []*fs.URI{uri}, fs.MetadataPatch{
			Key:   dbfs.FullTextIndexKey,
			Value: dbfs.BuildFullTextIndexMetadataValue(fm.hasher, doc.FileID, doc.EntityID),
		}); err != nil {
			if shouldIgnoreFTSSyncError(err) {
				if refreshErr := refreshFullTextIndexAfterStaleMetadata(ctx, fm, searchIdx, fileID); refreshErr == nil {
					l.Debug("Refreshed full text index for file %d after metadata uri became stale.", fileID)
					return task.StatusCompleted, nil
				} else if pauseFullTextIndexingForRetryableError(l, refreshErr) {
					return task.StatusSuspending, nil
				} else if !shouldIgnoreFTSSyncError(refreshErr) {
					return task.StatusError, fmt.Errorf("failed to refresh full text index after metadata uri became stale: %w", refreshErr)
				}

				if err := deleteStaleFullTextIndex(ctx, searchIdx, fileID); err != nil {
					if pauseFullTextIndexingForRetryableError(l, err) {
						return task.StatusSuspending, nil
					}

					l.Warning("Failed to delete stale index for file %d after metadata target disappeared: %s", fileID, err)
				}

				l.Debug("File %d disappeared before full text metadata was patched, removed stale index entry.", fileID)
				return task.StatusCompleted, nil
			}
			return task.StatusError, fmt.Errorf("failed to patch metadata: %w", err)
		}
	}

	l.Debug("Successfully indexed file %d for owner %d.", fileID, doc.OwnerID)
	return task.StatusCompleted, nil
}

func refreshFullTextIndexAfterStaleMetadata(ctx context.Context, fm *manager, searchIdx searcher.SearchIndexer, fileID int) error {
	doc, _, err := fm.buildFTSFileDocument(ctx, fileID)
	if err != nil {
		return err
	}

	if err := searchIdx.UpsertFile(ctx, doc); err != nil {
		return fmt.Errorf("failed to index refreshed file %d: %w", fileID, err)
	}

	return patchFullTextIndexMetadataByFileID(ctx, fm, fileID, doc)
}

func patchFullTextIndexMetadataByFileID(ctx context.Context, fm *manager, fileID int, doc *searcher.SearchFileDocument) error {
	if fm == nil || fm.dep == nil || fm.dep.FileClient() == nil || doc == nil {
		return fmt.Errorf("file metadata patch dependencies unavailable")
	}

	fileModel, err := fm.loadFTSFileModel(ctx, fileID)
	if err != nil {
		return err
	}

	return fm.dep.FileClient().UpsertMetadata(ctx, fileModel, map[string]string{
		dbfs.FullTextIndexKey: dbfs.BuildFullTextIndexMetadataValue(fm.hasher, doc.FileID, doc.EntityID),
	}, nil)
}

func deleteStaleFullTextIndex(ctx context.Context, searchIdx searcher.SearchIndexer, fileID int) error {
	if searchIdx == nil {
		return nil
	}

	return searchIdx.DeleteByFileIDs(ctx, fileID)
}

func metadataURIForFTSDocument(doc *searcher.SearchFileDocument, currentURI *fs.URI) *fs.URI {
	if doc != nil && strings.TrimSpace(doc.PublicURI) != "" {
		if publicURI, err := fs.NewUriFromString(doc.PublicURI); err == nil && publicURI != nil {
			return publicURI
		}
	}

	return currentURI
}

func clearFullTextIndexMetadataBestEffort(ctx context.Context, fm *manager, uri *fs.URI) {
	if fm == nil || fm.fs == nil || uri == nil {
		return
	}

	if err := fm.fs.PatchMetadata(withPublicBypass(ctx, uri), []*fs.URI{uri}, fs.MetadataPatch{
		Key:    dbfs.FullTextIndexKey,
		Remove: true,
	}); err != nil {
		fm.l.Warning("Failed to clear full text index metadata for %s: %s", uri.String(), err)
	}
}

func allocateContentProcessingNode(ctx context.Context, dep dependency.Dep, nodeID int) (cluster.Node, error) {
	np, err := dep.NodePool(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get node pool: %w", err)
	}

	node, err := np.Get(ctx, types.NodeCapabilityContentProcessing, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get content processing node: %w", err)
	}

	return node, nil
}

func (m *manager) shouldOffloadFullTextToSlave(ctx context.Context) bool {
	extractor := m.dep.TextExtractor(ctx)
	if _, ok := extractor.(*tikaextractor.TikaExtractor); !ok {
		return false
	}

	cfg := m.settings.FTSTikaExtractor(ctx)
	return cfg.SidecarEnabled && (cfg.SidecarTextEnabled || cfg.SidecarAssetsEnabled)
}

func finalizeSlaveIndexedFile(ctx context.Context, fm *manager, fileID int, result *SlaveFullTextExtractResult) (task.Status, error) {
	dep := dependency.FromContext(ctx)
	searchIdx := dep.SearchIndexer(ctx)
	if searchindexer.IsNoopIndexer(searchIdx) {
		if searchindexer.IsRetryableNoopIndexer(searchIdx) {
			dep.Logger().Warning("Full text indexing paused while search indexer recovers: %s", searchindexer.UnavailableError(searchIdx))
			return task.StatusSuspending, nil
		}
		return task.StatusError, searchindexer.UnavailableError(searchIdx)
	}

	uri, err := fm.resolveFTSFileURI(ctx, fileID)
	if err != nil {
		if shouldIgnoreFTSSyncError(err) {
			if err := searchIdx.DeleteByFileIDs(ctx, fileID); err != nil {
				if pauseFullTextIndexingForRetryableError(dep.Logger(), err) {
					return task.StatusSuspending, nil
				}

				return task.StatusError, fmt.Errorf("failed to delete stale index for file %d: %w", fileID, err)
			}
			return task.StatusCompleted, nil
		}
		return task.StatusError, fmt.Errorf("failed to resolve search uri for file %d: %w", fileID, err)
	}

	if uri != nil && uri.FileSystem() == constants.FileSystemTrash {
		if err := searchIdx.DeleteByFileIDs(ctx, fileID); err != nil {
			if pauseFullTextIndexingForRetryableError(dep.Logger(), err) {
				return task.StatusSuspending, nil
			}

			return task.StatusError, fmt.Errorf("failed to delete index for trashed file %d: %w", fileID, err)
		}
		return task.StatusCompleted, nil
	}

	if err := fm.applySlaveFTSSidecarResult(ctx, fileID, uri, result); err != nil {
		return task.StatusError, err
	}

	return fullTextPerformIndexing(ctx, fm, fileID)
}

func (t *FullTextIndexTask) Progress(ctx context.Context) queue.Progresses {
	t.Lock()
	defer t.Unlock()

	res := make(queue.Progresses)
	for k, v := range t.progress {
		res[k] = v
	}
	return res
}

func (t *FullTextIndexTask) Summarize(hasher hashid.Encoder) *queue.Summary {
	if t.state == nil {
		state, err := parseFullTextIndexTaskState(t.State())
		if err != nil {
			return nil
		}
		t.state = state
	}

	props := map[string]any{
		"total": t.state.Len(),
	}
	if item, ok := t.state.Current(); ok {
		props["src"] = item.Uri
		props["file_id"] = item.FileID
		props["owner_id"] = item.OwnerID
		props["entity_id"] = item.EntityID
	}

	return &queue.Summary{
		NodeID: effectiveTaskSummaryNodeID(t.Model().Status, t.state.NodeID, t.state.LastNodeID),
		Phase:  string(t.state.Phase),
		Props:  props,
	}
}

func (m *manager) resolveFTSFileURI(ctx context.Context, fileID int) (*fs.URI, error) {
	fileModel, err := m.loadFTSFileModel(ctx, fileID)
	if err != nil {
		return nil, fmt.Errorf("failed to load file model: %w", err)
	}

	return m.resolveFTSFileURIByModel(ctx, fileModel)
}

func (m *manager) resolveFTSFileURIByModel(ctx context.Context, fileModel *ent.File) (*fs.URI, error) {
	if fileModel == nil {
		return nil, fmt.Errorf("failed to resolve file uri: file model is nil")
	}

	if publicURI := m.resolvePublicSearchURI(ctx, fileModel); publicURI != nil {
		return publicURI, nil
	}

	ownerManager, err := m.fileManagerForOwner(ctx, fileModel.OwnerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load file owner context: %w", err)
	}
	defer ownerManager.Recycle()

	traversed, err := ownerManager.TraverseFile(ctx, fileModel.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve file uri: %w", err)
	}

	uri := traversed.Uri(true)
	if uri == nil {
		return nil, fmt.Errorf("failed to resolve file uri")
	}

	return uri, nil
}

func (m *manager) canResolveFTSFileURI() bool {
	if m == nil || m.dep == nil {
		return false
	}
	if m.dep.FileClient() == nil || m.dep.ConfigProvider() == nil || m.dep.HashIDEncoder() == nil {
		return false
	}
	if m.user == nil && m.dep.UserClient() == nil {
		return false
	}

	return true
}

func shouldIgnoreFTSSyncError(err error) bool {
	var notFound *ent.NotFoundError
	if errors.As(err, &notFound) {
		return true
	}

	var aggregate *serializer.AggregateError
	if errors.As(err, &aggregate) {
		raw := aggregate.Raw()
		if len(raw) == 0 {
			return false
		}
		for _, item := range raw {
			if !shouldIgnoreFTSSyncError(item) {
				return false
			}
		}
		return true
	}

	var appErr serializer.AppError
	if !errors.As(err, &appErr) {
		return false
	}

	switch appErr.ErrCode() {
	case serializer.CodeNotFound,
		serializer.CodeParentNotExist,
		serializer.CodeEntityNotExist,
		serializer.CodeFileDeleted:
		return true
	default:
		return false
	}
}

// ShouldExtractText checks if a file is eligible for text extraction.
// Tika only applies size-based prefiltering and defers parseability detection
// to its detector/parser chain, while non-Tika extractors still honor the
// configured extension allow-list.
func ShouldExtractText(extractor searcher.TextExtractor, fileName string, size int64) bool {
	if extractor == nil || size <= 0 {
		return false
	}

	if extractor.MaxFileSize() <= size {
		return false
	}

	if _, ok := extractor.(*tikaextractor.TikaExtractor); ok {
		return true
	}

	if util.IsInExtensionList(extractor.Exts(), fileName) {
		return true
	}

	return supportsTNEFWinmailDAT(extractor.Exts(), fileName)
}

func supportsTNEFWinmailDAT(exts []string, fileName string) bool {
	return strings.EqualFold(filepath.Base(strings.TrimSpace(fileName)), "winmail.dat") &&
		util.ContainsString(exts, "tnef")
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
		m.l.Debug("Skipping full text queue for file %d: entity type %v is not version.", session.FileID, session.Props.EntityType)
		return
	}

	if inventory.SkipNativeFTSEnqueueFromContext(ctx) {
		m.l.Debug("Skipping full text queue for file %d: native FTS enqueue disabled in context.", session.FileID)
		return
	}

	if !m.settings.FTSEnabled(ctx) {
		m.l.Debug("Skipping full text queue for file %d: FTS disabled.", session.FileID)
		return
	}

	m.l.Debug(
		"Queueing full text reconcile for new entity file %d entity %d owner %d uri %s.",
		session.FileID,
		session.EntityID,
		owner,
		session.Props.Uri,
	)
	m.queueFullTextReconcile(ctx, session.Props.Uri, session.FileID, owner, session.EntityID)
}

func (m *manager) queueFullTextSync(ctx context.Context, uri *fs.URI, fileID, ownerID, entityID int) {
	m.queueFullTextReconcile(ctx, uri, fileID, ownerID, entityID)
}

func (m *manager) queueFullTextCopy(ctx context.Context, uri *fs.URI, originalFileID, fileID, ownerID, entityID int) {
	if !m.settings.FTSEnabled(ctx) || fileID <= 0 {
		return
	}
	if originalFileID <= 0 {
		m.queueFullTextReconcile(ctx, uri, fileID, ownerID, entityID)
		return
	}

	t, err := NewFullTextCopyTask(ctx, uri, originalFileID, fileID, ownerID, entityID, m.user)
	if err != nil {
		m.l.Warning("Failed to create full text copy task: %s", err)
		return
	}

	if err := m.dep.ContentProcessingQueue(ctx).QueueTask(ctx, t); err != nil {
		m.l.Warning("Failed to queue full text copy task: %s", err)
		return
	}

	m.l.Debug(
		"Queued full text copy task for file %d from original file %d entity %d owner %d uri %v.",
		fileID,
		originalFileID,
		entityID,
		ownerID,
		uri,
	)
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

	state := newFullTextIndexTaskState(ctx, uri, entityID, fileID, ownerID)
	merged, err := m.mergePendingFullTextTask(ctx, state)
	if err != nil {
		m.l.Warning("Failed to merge pending full text reconcile task for file %d: %s", fileID, err)
	}
	if merged {
		return
	}

	items := state.Items()
	if len(items) > 0 {
		duplicated, err := m.hasEquivalentPendingFullTextTask(ctx, items[0])
		if err != nil {
			m.l.Warning("Failed to detect duplicated full text reconcile task for file %d: %s", fileID, err)
		}
		if duplicated {
			return
		}
	}

	t, err := NewFullTextIndexTask(ctx, uri, entityID, fileID, ownerID, m.user)
	if err != nil {
		m.l.Warning("Failed to create full text reconcile task: %s", err)
		return
	}

	if err := m.dep.ContentProcessingQueue(ctx).QueueTask(ctx, t); err != nil {
		m.l.Warning("Failed to queue full text reconcile task: %s", err)
		return
	}

	m.l.Debug(
		"Queued new full text reconcile task for file %d entity %d owner %d uri %v.",
		fileID,
		entityID,
		ownerID,
		uri,
	)
}

func (m *manager) hasEquivalentPendingFullTextTask(ctx context.Context, item FullTextIndexTaskItem) (bool, error) {
	if item.FileID <= 0 {
		return false, nil
	}

	candidates, err := m.dep.TaskClient().GetPendingTasks(ctx, fullTextMergeableTaskTypes...)
	if err != nil {
		return false, err
	}

	for _, candidate := range candidates {
		if candidate == nil || candidate.Type != queue.FullTextIndexTaskType {
			continue
		}

		parsed, err := parseFullTextIndexTaskState(candidate.PrivateState)
		if err != nil {
			m.l.Warning("Failed to parse pending full text task %d while checking duplicate file %d: %s", candidate.ID, item.FileID, err)
			continue
		}
		if fullTextTaskContainsEquivalentItem(parsed, item) {
			return true, nil
		}
	}

	return false, nil
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
		if !pending[i].state.Mergeable() {
			continue
		}
		if !pending[i].state.Contains(item.FileID) {
			continue
		}
		if targetIndex == -1 || pending[i].task.UpdatedAt.After(pending[targetIndex].task.UpdatedAt) {
			targetIndex = i
		}
	}

	if targetIndex == -1 {
		for i := range pending {
			if !pending[i].state.Mergeable() {
				continue
			}
			if pending[i].state.Len() >= fullTextMaxFilesPerTask {
				continue
			}
			if !fullTextTaskSharesCorrelationScope(ctx, pending[i].task) {
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
		m.queueFullTextCopy(ctx, &cp.Uri, cp.OriginalFileID, cp.FileID, cp.OwnerID, cp.EntityID)
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
