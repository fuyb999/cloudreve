package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	searchindexer "github.com/cloudreve/Cloudreve/v4/pkg/searcher/indexer"
)

type (
	RebuildIndexTask struct {
		*queue.DBTask

		l        logging.Logger
		state    *RebuildIndexTaskState
		progress queue.Progresses
	}
	RebuildIndexTaskPhase string
	RebuildIndexTaskState struct {
		Phase                 RebuildIndexTaskPhase `json:"phase"`
		Total                 int                   `json:"total"`
		Indexed               int                   `json:"indexed"`
		LastFileID            int                   `json:"last_file_id"`
		Failed                int                   `json:"failed"`
		FilteredStoragePolicy []int                 `json:"filtered_storage_policy"`
		SkipTextExtraction    bool                  `json:"skip_text_extraction,omitempty"`
		SkipAssetExtraction   bool                  `json:"skip_attachment_extraction,omitempty"`
	}
)

const (
	RebuildIndexPhaseNuke  RebuildIndexTaskPhase = "nuke"
	RebuildIndexPhaseIndex RebuildIndexTaskPhase = "index"

	RebuildIndexBatchSize  = 1000
	RebuildIndexConcurrent = 4

	ProgressTypeRebuildIndex = "rebuild_index"
)

func init() {
	queue.RegisterResumableTaskFactory(queue.FullTextRebuildTaskType, NewRebuildIndexTaskFromModel)
}

func NewRebuildIndexTask(
	ctx context.Context,
	u *ent.User,
	filteredStoragePolicy []int,
	skipTextExtraction bool,
	skipAssetExtraction bool,
) (queue.Task, error) {
	state := &RebuildIndexTaskState{
		Phase:                 RebuildIndexPhaseNuke,
		FilteredStoragePolicy: filteredStoragePolicy,
		SkipTextExtraction:    skipTextExtraction,
		SkipAssetExtraction:   skipAssetExtraction,
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	return &RebuildIndexTask{
		DBTask: &queue.DBTask{
			Task: &ent.Task{
				Type:          queue.FullTextRebuildTaskType,
				CorrelationID: logging.NillableCorrelationID(ctx),
				PrivateState:  string(stateBytes),
				PublicState:   &types.TaskPublicState{},
			},
			DirectOwner: u,
		},
	}, nil
}

func NewRebuildIndexTaskFromModel(t *ent.Task) queue.Task {
	return &RebuildIndexTask{
		DBTask: &queue.DBTask{
			Task: t,
		},
	}
}

func (m *RebuildIndexTask) Do(ctx context.Context) (task.Status, error) {
	dep := dependency.FromContext(ctx)
	m.l = dep.Logger()

	m.Lock()
	if m.progress == nil {
		m.progress = make(queue.Progresses)
	}
	m.progress[ProgressTypeRebuildIndex] = &queue.Progress{}
	m.Unlock()

	state := &RebuildIndexTaskState{}
	if err := json.Unmarshal([]byte(m.State()), state); err != nil {
		return task.StatusError, fmt.Errorf("failed to unmarshal state: %s (%w)", err, queue.CriticalErr)
	}
	m.state = state

	var (
		next = task.StatusCompleted
		err  error
	)
	switch m.state.Phase {
	case RebuildIndexPhaseNuke, "":
		next, err = m.nuke(ctx, dep)
	case RebuildIndexPhaseIndex:
		next, err = m.index(ctx, dep)
	default:
		next, err = task.StatusError, fmt.Errorf("unknown phase %q: %w", m.state.Phase, queue.CriticalErr)
	}

	newStateStr, marshalErr := json.Marshal(m.state)
	if marshalErr != nil {
		return task.StatusError, fmt.Errorf("failed to marshal state: %w", marshalErr)
	}

	m.Lock()
	m.Task.PrivateState = string(newStateStr)
	m.Unlock()
	return next, err
}

// nuke deletes all existing index documents and ensures a fresh index exists,
// then counts total indexable files for progress tracking.
func (m *RebuildIndexTask) nuke(ctx context.Context, dep dependency.Dep) (task.Status, error) {
	indexer := dep.SearchIndexer(ctx)
	if searchindexer.IsNoopIndexer(indexer) {
		return task.StatusError, fmt.Errorf("search indexer is unavailable")
	}

	m.l.Info("Deleting all existing index documents...")
	if err := indexer.DeleteAll(ctx); err != nil {
		return task.StatusError, fmt.Errorf("failed to delete all index documents: %w", err)
	}

	if err := dep.FileClient().DeleteAllMetadataByName(ctx, dbfs.FullTextIndexKey); err != nil {
		return task.StatusError, fmt.Errorf("failed to delete all metadata by name: %w", err)
	}

	m.l.Info("Ensuring index exists with correct configuration...")
	if err := indexer.EnsureIndex(ctx); err != nil {
		return task.StatusError, fmt.Errorf("failed to ensure index: %w", err)
	}

	// Count total indexable files
	total, err := dep.FileClient().CountIndexableFiles(ctx)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to count indexable files: %w", err)
	}

	m.state.Total = total
	m.state.Phase = RebuildIndexPhaseIndex
	m.state.LastFileID = 0
	m.state.Indexed = 0

	m.l.Info("Found %d indexable files, starting rebuild...", total)
	m.ResumeAfter(0)
	return task.StatusSuspending, nil
}

// index processes a batch of files and suspends for the next batch.
func (m *RebuildIndexTask) index(ctx context.Context, dep dependency.Dep) (task.Status, error) {
	atomic.StoreInt64(&m.progress[ProgressTypeRebuildIndex].Total, int64(m.state.Total))
	atomic.StoreInt64(&m.progress[ProgressTypeRebuildIndex].Current, int64(m.state.Indexed))

	files, err := dep.FileClient().ListIndexableFiles(ctx, m.state.LastFileID, RebuildIndexBatchSize)
	if err != nil {
		return task.StatusError, fmt.Errorf("failed to list indexable files after ID %d: %w", m.state.LastFileID, err)
	}

	if len(files) == 0 {
		m.l.Info("Rebuild complete. %d files indexed, %d failed.", m.state.Indexed-m.state.Failed, m.state.Failed)
		return task.StatusCompleted, nil
	}

	batchFailed := m.processBatch(ctx, dep, files)
	m.state.Failed += batchFailed
	m.state.Indexed += len(files)
	m.state.LastFileID = files[len(files)-1].ID

	atomic.StoreInt64(&m.progress[ProgressTypeRebuildIndex].Current, int64(m.state.Indexed))

	// Suspend and resume for next batch
	m.ResumeAfter(0)
	return task.StatusSuspending, nil
}

func shouldIndexRebuildURI(uri *fs.URI) bool {
	if uri == nil {
		return true
	}

	return uri.FileSystem() != constants.FileSystemTrash
}

func matchesRebuildStoragePolicy(doc *searcher.SearchFileDocument, filteredStoragePolicy []int) bool {
	if doc == nil || len(filteredStoragePolicy) == 0 {
		return true
	}

	policyID := doc.StoragePolicyID
	if doc.LatestVersion != nil && doc.LatestVersion.StoragePolicyID > 0 {
		policyID = doc.LatestVersion.StoragePolicyID
	}

	return slices.Contains(filteredStoragePolicy, policyID)
}

// processBatch indexes a batch of files concurrently.
func (m *RebuildIndexTask) processBatch(ctx context.Context, dep dependency.Dep, files []*ent.File) int {
	user := inventory.UserFromContext(ctx)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		failed   int
		docs     []*searcher.SearchFileDocument
		fileByID = make(map[int]*ent.File, len(files))
	)

	sem := make(chan struct{}, RebuildIndexConcurrent)
	for _, f := range files {
		fileByID[f.ID] = f

		select {
		case <-ctx.Done():
			return failed
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(f *ent.File) {
			defer func() {
				<-sem
				wg.Done()
			}()

			doc, uri, err := manager.BuildFTSFileDocumentWithOptions(ctx, dep, user, f.ID, manager.FTSBuildOptions{
				SkipTextExtraction:        m.state.SkipTextExtraction,
				SkipAttachmentExtraction:  m.state.SkipAssetExtraction,
				ForceTextExtraction:       !m.state.SkipTextExtraction,
				ForceAttachmentExtraction: !m.state.SkipAssetExtraction,
			})
			if err != nil {
				var notFound *ent.NotFoundError
				if errors.As(err, &notFound) {
					return
				}

				m.l.Warning("Failed to index file %d (%s): %s", f.ID, f.Name, err)
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}

			if !shouldIndexRebuildURI(uri) {
				m.l.Debug("Skip rebuild indexing for trashed file %d (%s)", f.ID, f.Name)
				return
			}

			if !matchesRebuildStoragePolicy(doc, m.state.FilteredStoragePolicy) {
				return
			}

			mu.Lock()
			docs = append(docs, doc)
			mu.Unlock()
		}(f)
	}

	wg.Wait()

	if len(docs) == 0 {
		return failed
	}

	if err := dep.SearchIndexer(ctx).BulkUpsertFiles(ctx, docs); err != nil {
		m.l.Warning("Failed to bulk upsert rebuild batch starting at file %d: %s", files[0].ID, err)
		return failed + len(docs)
	}

	for _, doc := range docs {
		fileModel, ok := fileByID[doc.FileID]
		if !ok {
			continue
		}

		if err := dep.FileClient().UpsertMetadata(ctx, fileModel, map[string]string{
			dbfs.FullTextIndexKey: dbfs.BuildFullTextIndexMetadataValue(dep.HashIDEncoder(), doc.FileID, doc.EntityID),
		}, nil); err != nil {
			m.l.Warning("Failed to upsert metadata for file %d: %s", doc.FileID, err)
		}
	}

	return failed
}

func (m *RebuildIndexTask) Progress(ctx context.Context) queue.Progresses {
	m.Lock()
	defer m.Unlock()
	return m.progress
}

func (m *RebuildIndexTask) Summarize(hasher hashid.Encoder) *queue.Summary {
	if m.state == nil {
		if err := json.Unmarshal([]byte(m.State()), &m.state); err != nil {
			return nil
		}
	}

	return &queue.Summary{
		Phase: string(m.state.Phase),
		Props: map[string]any{
			SummaryKeyFailed:             m.state.Failed,
			SummaryKeyTotal:              m.state.Total,
			"skip_text_extraction":       m.state.SkipTextExtraction,
			"skip_attachment_extraction": m.state.SkipAssetExtraction,
		},
	}
}
