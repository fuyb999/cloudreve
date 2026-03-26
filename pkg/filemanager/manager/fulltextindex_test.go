package manager

import (
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"strconv"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	taskModel "github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	inventorytypes "github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/auth"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/cluster"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/mediameta"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	tikaextractor "github.com/cloudreve/Cloudreve/v4/pkg/searcher/extractor"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/thumb"
)

type testTextExtractor struct {
	exts        []string
	maxFileSize int64
}

func (t testTextExtractor) Extract(ctx context.Context, reader io.Reader) (string, error) {
	return "", nil
}

func (t testTextExtractor) Exts() []string {
	return t.exts
}

func (t testTextExtractor) MaxFileSize() int64 {
	return t.maxFileSize
}

func TestFullTextIndexTaskStateUpsertRemoveAndNormalize(t *testing.T) {
	uriA, err := fs.NewUriFromString("cloudreve:///my/a.txt")
	if err != nil {
		t.Fatalf("failed to parse uriA: %v", err)
	}
	uriB, err := fs.NewUriFromString("cloudreve:///my/b.txt")
	if err != nil {
		t.Fatalf("failed to parse uriB: %v", err)
	}

	state := &FullTextIndexTaskState{
		FileID:   1,
		OwnerID:  10,
		EntityID: 100,
		Uri:      uriA,
		Files: []FullTextIndexTaskItem{
			{FileID: 1, OwnerID: 10, EntityID: 100, Uri: uriA},
			{FileID: 2, OwnerID: 20, EntityID: 200, Uri: uriB},
			{FileID: 1, OwnerID: 30, EntityID: 300, Uri: uriB},
		},
	}

	items := state.Items()
	if len(items) != 2 {
		t.Fatalf("unexpected normalized item count: got %d want 2", len(items))
	}
	if !state.Contains(1) || !state.Contains(2) {
		t.Fatalf("expected state to contain file ids 1 and 2: %+v", items)
	}

	// Latest upsert for same file id should win.
	if items[0].OwnerID != 30 || items[0].EntityID != 300 || items[0].Uri.String() != uriB.String() {
		t.Fatalf("unexpected normalized first item: %+v", items[0])
	}

	state.Upsert(FullTextIndexTaskItem{FileID: 2, OwnerID: 22, EntityID: 222, Uri: uriA})
	if got := state.Items()[1]; got.OwnerID != 22 || got.EntityID != 222 || got.Uri.String() != uriA.String() {
		t.Fatalf("unexpected updated second item: %+v", got)
	}

	if !state.Remove(1) {
		t.Fatal("expected remove to return true")
	}
	if state.Contains(1) {
		t.Fatal("expected file id 1 to be removed")
	}
	if state.Len() != 1 {
		t.Fatalf("unexpected len after remove: %d", state.Len())
	}

	if state.Remove(999) {
		t.Fatal("expected remove of unknown file id to return false")
	}
}

func TestFullTextIndexTaskStateStressDeduplicatesHighVolumeUpdates(t *testing.T) {
	state := &FullTextIndexTaskState{}

	for i := 0; i < 5000; i++ {
		fileID := (i % 128) + 1
		uri, err := fs.NewUriFromString("cloudreve:///my/file-" + string(rune('a'+(fileID%26))) + ".txt")
		if err != nil {
			t.Fatalf("failed to parse uri: %v", err)
		}

		state.Upsert(FullTextIndexTaskItem{
			FileID:   fileID,
			OwnerID:  i,
			EntityID: i * 10,
			Uri:      uri,
		})
	}

	if state.Len() != 128 {
		t.Fatalf("unexpected deduplicated len: got %d want 128", state.Len())
	}

	for _, item := range state.Items() {
		if item.FileID <= 0 {
			t.Fatalf("unexpected non-positive file id: %+v", item)
		}
		if item.EntityID == 0 {
			t.Fatalf("expected latest entity id to be kept: %+v", item)
		}
	}
}

func TestFullTextIndexTaskStatePreservesLatestHeadAfterReplacement(t *testing.T) {
	uriA, _ := fs.NewUriFromString("cloudreve:///my/original.txt")
	uriB, _ := fs.NewUriFromString("cloudreve:///my/replaced.txt")

	state := newFullTextIndexTaskState(uriA, 11, 1, 22)
	state.Upsert(FullTextIndexTaskItem{
		FileID:   1,
		OwnerID:  33,
		EntityID: 44,
		Uri:      uriB,
	})

	if state.FileID != 1 || state.OwnerID != 33 || state.EntityID != 44 {
		t.Fatalf("unexpected head fields: %+v", state)
	}
	if state.Uri == nil || state.Uri.String() != uriB.String() {
		t.Fatalf("unexpected head uri: %+v", state.Uri)
	}
}

func TestFullTextIndexTaskStateActiveLifecycle(t *testing.T) {
	uriA := mustURI(t, "cloudreve:///my/a.txt")
	uriB := mustURI(t, "cloudreve:///my/b.txt")

	state := &FullTextIndexTaskState{}
	state.Upsert(FullTextIndexTaskItem{FileID: 1, OwnerID: 10, EntityID: 100, Uri: uriA})
	state.Upsert(FullTextIndexTaskItem{FileID: 2, OwnerID: 20, EntityID: 200, Uri: uriB})

	if !state.ActivateNext() {
		t.Fatal("expected ActivateNext to return true")
	}
	current, ok := state.Current()
	if !ok {
		t.Fatal("expected current item after activation")
	}
	if current.FileID != 1 {
		t.Fatalf("unexpected current file id: %+v", current)
	}

	state.Phase = fullTextIndexPhaseAwaitSlave
	state.NodeID = 7
	state.SlaveID = 8
	state.CompleteActive()

	if state.Active != nil {
		t.Fatalf("expected active item to be cleared, got %+v", state.Active)
	}
	if state.Phase != fullTextIndexPhasePending || state.NodeID != 0 || state.SlaveID != 0 {
		t.Fatalf("expected node/phase state to be reset, got %+v", state)
	}
	items := state.Items()
	if len(items) != 1 || items[0].FileID != 2 {
		t.Fatalf("unexpected remaining items after CompleteActive: %+v", items)
	}
}

func TestShouldExtractTextSupportsWinmailDATWhenTNEFEnabled(t *testing.T) {
	extractor := testTextExtractor{
		exts:        []string{"pdf", "tnef"},
		maxFileSize: 1024,
	}

	if !ShouldExtractText(extractor, "winmail.dat", 128) {
		t.Fatal("expected winmail.dat to be extractable when tnef is enabled")
	}
	if ShouldExtractText(extractor, "payload.dat", 128) {
		t.Fatal("expected generic .dat file to remain disabled")
	}
	if ShouldExtractText(extractor, "winmail.dat", 4096) {
		t.Fatal("expected size limit to still apply")
	}
}

func TestCollectFTSRecursiveFileIDsDeduplicatesOverlappingTrees(t *testing.T) {
	rootA := mustURI(t, "cloudreve:///ops/a")
	rootB := mustURI(t, "cloudreve:///ops/b")
	rootErr := mustURI(t, "cloudreve:///ops/error")

	got := collectFTSRecursiveFileIDs(context.Background(), func(ctx context.Context, path *fs.URI, depth int, walk fs.WalkFunc, opts ...fs.Option) error {
		var ids []int
		switch path.String() {
		case rootA.String():
			ids = []int{10, 11, 12}
		case rootB.String():
			ids = []int{11, 12, 13}
		case rootErr.String():
			return context.Canceled
		default:
			return nil
		}

		for _, id := range ids {
			if err := walk(testWalkFile{id: id}, 0); err != nil {
				return err
			}
		}
		return nil
	}, rootA, nil, rootB, rootErr)

	want := []int{10, 11, 12, 13}
	if len(got) != len(want) {
		t.Fatalf("unexpected collected file ids: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected collected file ids: got %v want %v", got, want)
		}
	}
}

func TestProcessIndexDiffQueuesAllOperationTypes(t *testing.T) {
	ctx := context.Background()
	settings := testSettingProvider{enabled: true}
	tasks := &testQueue{}
	dep := testDep{
		settings:   settings,
		taskClient: &testTaskClient{},
		mediaMeta:  tasks,
		registry:   queue.NewTaskRegistry(),
	}
	m := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		user:     &ent.User{ID: 1},
		settings: settings,
		dep:      dep,
	}

	updateURI := mustURI(t, "cloudreve:///ops/update.txt")
	copyURI := mustURI(t, "cloudreve:///ops/copy.txt")
	changeOwnerURI := mustURI(t, "cloudreve:///ops/change-owner.txt")
	renameURI := mustURI(t, "cloudreve:///ops/rename.txt")
	m.processIndexDiff(ctx, &fs.IndexDiff{
		IndexToUpdate: []fs.IndexDiffUpdateDetails{
			{Uri: *updateURI, FileID: 101, OwnerID: 201, EntityID: 301},
		},
		IndexToCopy: []fs.IndexDiffCopyDetails{
			{Uri: *copyURI, FileID: 102, OwnerID: 202, EntityID: 302},
		},
		IndexToChangeOwner: []fs.IndexDiffOwnerChangeDetails{
			{Uri: *changeOwnerURI, FileID: 103, NewOwnerID: 203, EntityID: 303},
		},
		IndexToDelete: []int{104},
		IndexToRename: []fs.IndexDiffRenameDetails{
			{Uri: *renameURI, FileID: 105, EntityID: 305},
		},
	})

	if len(tasks.tasks) != 5 {
		t.Fatalf("unexpected queued task count: got %d want 5", len(tasks.tasks))
	}

	assertQueuedState(t, tasks.tasks[0], 101, 201, 301, updateURI.String())
	assertQueuedState(t, tasks.tasks[1], 102, 202, 302, copyURI.String())
	assertQueuedState(t, tasks.tasks[2], 103, 203, 303, changeOwnerURI.String())
	assertQueuedState(t, tasks.tasks[3], 104, 0, 0, "")
	assertQueuedState(t, tasks.tasks[4], 105, 0, 305, renameURI.String())
}

func TestQueueFullTextReconcileStressMergesIntoPendingTask(t *testing.T) {
	ctx := context.Background()
	settings := testSettingProvider{enabled: true}
	pending := &ent.Task{
		ID:           1,
		Type:         queue.FullTextIndexTaskType,
		Status:       taskModel.StatusQueued,
		UpdatedAt:    time.Now(),
		PrivateState: "",
	}
	taskClient := &testTaskClient{pending: []*ent.Task{pending}}
	tasks := &testQueue{}
	dep := testDep{
		settings:   settings,
		taskClient: taskClient,
		mediaMeta:  tasks,
		registry:   queue.NewTaskRegistry(),
	}
	m := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		user:     &ent.User{ID: 1},
		settings: settings,
		dep:      dep,
	}

	type expectedItem struct {
		ownerID  int
		entityID int
		uri      string
	}
	expected := map[int]expectedItem{}
	for i := 0; i < 5000; i++ {
		fileID := (i % 32) + 1
		uri := mustURI(t, "cloudreve:///stress/file-"+string(rune('a'+(fileID%26)))+".txt")
		ownerID := 1000 + i
		entityID := 2000 + i
		expected[fileID] = expectedItem{
			ownerID:  ownerID,
			entityID: entityID,
			uri:      uri.String(),
		}
		m.queueFullTextReconcile(ctx, uri, fileID, ownerID, entityID)
	}

	if len(tasks.tasks) != 0 {
		t.Fatalf("expected all reconcile requests to merge into pending task, got %d new task(s)", len(tasks.tasks))
	}

	state := mustParseState(t, pending.PrivateState)
	if state.Len() != len(expected) {
		t.Fatalf("unexpected merged pending item count: got %d want %d", state.Len(), len(expected))
	}

	for _, item := range state.Items() {
		want, ok := expected[item.FileID]
		if !ok {
			t.Fatalf("unexpected file id in merged pending state: %+v", item)
		}
		if item.OwnerID != want.ownerID || item.EntityID != want.entityID {
			t.Fatalf("unexpected merged item for file %d: got %+v want owner=%d entity=%d", item.FileID, item, want.ownerID, want.entityID)
		}
		if item.Uri == nil || item.Uri.String() != want.uri {
			t.Fatalf("unexpected merged uri for file %d: got %+v want %s", item.FileID, item.Uri, want.uri)
		}
	}
}

func TestQueueFullTextReconcileLatestOperationWinsForSameFile(t *testing.T) {
	ctx := context.Background()
	settings := testSettingProvider{enabled: true}
	originalURI := mustURI(t, "cloudreve:///seq/original.txt")
	restoredURI := mustURI(t, "cloudreve:///seq/restored.txt")
	pending := newPendingFTSTask(t, 1, time.Now(), FullTextIndexTaskItem{
		FileID:   77,
		OwnerID:  701,
		EntityID: 801,
		Uri:      originalURI,
	})
	taskClient := &testTaskClient{pending: []*ent.Task{pending}}
	tasks := &testQueue{}
	dep := testDep{
		settings:   settings,
		taskClient: taskClient,
		mediaMeta:  tasks,
		registry:   queue.NewTaskRegistry(),
	}
	m := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		user:     &ent.User{ID: 1},
		settings: settings,
		dep:      dep,
	}

	m.queueFullTextDelete(ctx, 77)
	stateAfterDelete := mustParseState(t, pending.PrivateState)
	if stateAfterDelete.Len() != 1 {
		t.Fatalf("unexpected pending item count after delete: %+v", stateAfterDelete.Items())
	}
	assertStateItem(t, stateAfterDelete.Items()[0], 77, 0, 0, "")

	m.queueFullTextReconcile(ctx, restoredURI, 77, 702, 802)
	stateAfterRestore := mustParseState(t, pending.PrivateState)
	if stateAfterRestore.Len() != 1 {
		t.Fatalf("unexpected pending item count after restore: %+v", stateAfterRestore.Items())
	}
	assertStateItem(t, stateAfterRestore.Items()[0], 77, 702, 802, restoredURI.String())

	if len(tasks.tasks) != 0 {
		t.Fatalf("expected sequence to merge into existing pending task, got %d new task(s)", len(tasks.tasks))
	}
}

func TestQueueFullTextReconcileCollapsesDuplicateFileAcrossPendingTasks(t *testing.T) {
	ctx := context.Background()
	settings := testSettingProvider{enabled: true}
	sharedOldURI := mustURI(t, "cloudreve:///dup/old.txt")
	sharedNewURI := mustURI(t, "cloudreve:///dup/new.txt")
	otherURI := mustURI(t, "cloudreve:///dup/other.txt")
	olderTask := newPendingFTSTask(t, 1, time.Now().Add(-time.Minute),
		FullTextIndexTaskItem{FileID: 88, OwnerID: 801, EntityID: 901, Uri: sharedOldURI},
		FullTextIndexTaskItem{FileID: 99, OwnerID: 802, EntityID: 902, Uri: otherURI},
	)
	newerTask := newPendingFTSTask(t, 2, time.Now(),
		FullTextIndexTaskItem{FileID: 88, OwnerID: 803, EntityID: 903, Uri: sharedOldURI},
	)
	taskClient := &testTaskClient{pending: []*ent.Task{olderTask, newerTask}}
	tasks := &testQueue{}
	dep := testDep{
		settings:   settings,
		taskClient: taskClient,
		mediaMeta:  tasks,
		registry:   queue.NewTaskRegistry(),
	}
	m := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		user:     &ent.User{ID: 1},
		settings: settings,
		dep:      dep,
	}

	m.queueFullTextReconcile(ctx, sharedNewURI, 88, 804, 904)

	olderState := mustParseState(t, olderTask.PrivateState)
	if olderState.Len() != 1 {
		t.Fatalf("unexpected older task state after collapse: %+v", olderState.Items())
	}
	assertStateItem(t, olderState.Items()[0], 99, 802, 902, otherURI.String())

	newerState := mustParseState(t, newerTask.PrivateState)
	if newerState.Len() != 1 {
		t.Fatalf("unexpected newer task state after collapse: %+v", newerState.Items())
	}
	assertStateItem(t, newerState.Items()[0], 88, 804, 904, sharedNewURI.String())

	if len(tasks.tasks) != 0 {
		t.Fatalf("expected duplicate file reconciliation to reuse pending tasks, got %d new task(s)", len(tasks.tasks))
	}
}

func TestQueueFullTextReconcileQueuesNewTaskWhenPendingTaskAtCapacity(t *testing.T) {
	ctx := context.Background()
	settings := testSettingProvider{enabled: true}
	fullItems := make([]FullTextIndexTaskItem, 0, fullTextMaxFilesPerTask)
	for i := 0; i < fullTextMaxFilesPerTask; i++ {
		fullItems = append(fullItems, FullTextIndexTaskItem{
			FileID:   i + 1,
			OwnerID:  100 + i,
			EntityID: 200 + i,
			Uri:      mustURI(t, "cloudreve:///capacity/file-"+string(rune('a'+(i%26)))+".txt"),
		})
	}

	pending := newPendingFTSTask(t, 1, time.Now(), fullItems...)
	taskClient := &testTaskClient{pending: []*ent.Task{pending}}
	tasks := &testQueue{}
	dep := testDep{
		settings:   settings,
		taskClient: taskClient,
		mediaMeta:  tasks,
		registry:   queue.NewTaskRegistry(),
	}
	m := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		user:     &ent.User{ID: 1},
		settings: settings,
		dep:      dep,
	}

	newURI := mustURI(t, "cloudreve:///capacity/overflow.txt")
	m.queueFullTextReconcile(ctx, newURI, 999, 1999, 2999)

	if len(tasks.tasks) != 1 {
		t.Fatalf("expected a new queued task when pending task is full, got %d", len(tasks.tasks))
	}
	assertQueuedState(t, tasks.tasks[0], 999, 1999, 2999, newURI.String())

	originalState := mustParseState(t, pending.PrivateState)
	if originalState.Len() != fullTextMaxFilesPerTask {
		t.Fatalf("expected original pending task to stay full, got %d", originalState.Len())
	}
	if originalState.Contains(999) {
		t.Fatalf("did not expect overflow file to be merged into full pending task")
	}
}

func TestQueueFullTextReconcileDoesNotMergeIntoAwaitingSlaveTask(t *testing.T) {
	ctx := context.Background()
	settings := testSettingProvider{enabled: true}
	activeURI := mustURI(t, "cloudreve:///await/active.rar")
	newURI := mustURI(t, "cloudreve:///await/new.txt")

	pending := newPendingFTSTask(t, 1, time.Now(), FullTextIndexTaskItem{
		FileID:   701,
		OwnerID:  801,
		EntityID: 901,
		Uri:      activeURI,
	})
	activeState := mustParseState(t, pending.PrivateState)
	activeState.Active = &FullTextIndexTaskItem{
		FileID:   701,
		OwnerID:  801,
		EntityID: 901,
		Uri:      activeURI,
	}
	activeState.Phase = fullTextIndexPhaseAwaitSlave
	activeState.NodeID = 3
	activeState.SlaveID = 15
	stateBytes, err := marshalFullTextIndexTaskState(activeState)
	if err != nil {
		t.Fatalf("failed to marshal awaiting-slave state: %v", err)
	}
	pending.Status = taskModel.StatusSuspending
	pending.PrivateState = string(stateBytes)

	taskClient := &testTaskClient{pending: []*ent.Task{pending}}
	tasks := &testQueue{}
	dep := testDep{
		settings:   settings,
		taskClient: taskClient,
		mediaMeta:  tasks,
		registry:   queue.NewTaskRegistry(),
	}
	m := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		user:     &ent.User{ID: 1},
		settings: settings,
		dep:      dep,
	}

	m.queueFullTextReconcile(ctx, newURI, 702, 802, 902)

	if len(tasks.tasks) != 1 {
		t.Fatalf("expected awaiting-slave task to be skipped for merge, got %d new task(s)", len(tasks.tasks))
	}
	assertQueuedState(t, tasks.tasks[0], 702, 802, 902, newURI.String())

	originalState := mustParseState(t, pending.PrivateState)
	if originalState.Len() != 1 || !originalState.Contains(701) || originalState.Contains(702) {
		t.Fatalf("expected original awaiting-slave task to stay unchanged, got %+v", originalState.Items())
	}
	if originalState.Phase != fullTextIndexPhaseAwaitSlave || originalState.Active == nil || originalState.SlaveID != 15 {
		t.Fatalf("expected awaiting-slave markers to stay intact, got %+v", originalState)
	}
}

func TestProcessIndexDiffSequenceLastOperationWinsForSameFile(t *testing.T) {
	ctx := context.Background()
	settings := testSettingProvider{enabled: true}
	initialURI := mustURI(t, "cloudreve:///sequence/initial.txt")
	renamedURI := mustURI(t, "cloudreve:///sequence/renamed.txt")
	pending := newPendingFTSTask(t, 1, time.Now(), FullTextIndexTaskItem{
		FileID:   501,
		OwnerID:  601,
		EntityID: 701,
		Uri:      initialURI,
	})
	taskClient := &testTaskClient{pending: []*ent.Task{pending}}
	tasks := &testQueue{}
	dep := testDep{
		settings:   settings,
		taskClient: taskClient,
		mediaMeta:  tasks,
		registry:   queue.NewTaskRegistry(),
	}
	m := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		user:     &ent.User{ID: 1},
		settings: settings,
		dep:      dep,
	}

	m.processIndexDiff(ctx, &fs.IndexDiff{
		IndexToUpdate: []fs.IndexDiffUpdateDetails{
			{Uri: *initialURI, FileID: 501, OwnerID: 611, EntityID: 711},
		},
	})
	stateAfterUpdate := mustParseState(t, pending.PrivateState)
	assertStateItem(t, stateAfterUpdate.Items()[0], 501, 611, 711, initialURI.String())

	m.processIndexDiff(ctx, &fs.IndexDiff{
		IndexToDelete: []int{501},
	})
	stateAfterDelete := mustParseState(t, pending.PrivateState)
	assertStateItem(t, stateAfterDelete.Items()[0], 501, 0, 0, "")

	m.processIndexDiff(ctx, &fs.IndexDiff{
		IndexToRename: []fs.IndexDiffRenameDetails{
			{Uri: *renamedURI, FileID: 501, EntityID: 712},
		},
	})
	stateAfterRename := mustParseState(t, pending.PrivateState)
	assertStateItem(t, stateAfterRename.Items()[0], 501, 0, 712, renamedURI.String())

	if len(tasks.tasks) != 0 {
		t.Fatalf("expected diff sequence to stay merged into pending task, got %d new task(s)", len(tasks.tasks))
	}
}

func TestQueueFullTextReconcileUpdatesExistingFileInsideFullPendingTask(t *testing.T) {
	ctx := context.Background()
	settings := testSettingProvider{enabled: true}
	fullItems := make([]FullTextIndexTaskItem, 0, fullTextMaxFilesPerTask)
	targetFileID := 42
	for i := 0; i < fullTextMaxFilesPerTask; i++ {
		fileID := i + 1
		fullItems = append(fullItems, FullTextIndexTaskItem{
			FileID:   fileID,
			OwnerID:  300 + i,
			EntityID: 400 + i,
			Uri:      mustURI(t, "cloudreve:///full/existing-"+string(rune('a'+(i%26)))+".txt"),
		})
	}

	pending := newPendingFTSTask(t, 1, time.Now(), fullItems...)
	taskClient := &testTaskClient{pending: []*ent.Task{pending}}
	tasks := &testQueue{}
	dep := testDep{
		settings:   settings,
		taskClient: taskClient,
		mediaMeta:  tasks,
		registry:   queue.NewTaskRegistry(),
	}
	m := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		user:     &ent.User{ID: 1},
		settings: settings,
		dep:      dep,
	}

	updatedURI := mustURI(t, "cloudreve:///full/updated-target.txt")
	m.queueFullTextReconcile(ctx, updatedURI, targetFileID, 999, 1999)

	if len(tasks.tasks) != 0 {
		t.Fatalf("expected update for existing file in full task to merge in place, got %d new task(s)", len(tasks.tasks))
	}

	state := mustParseState(t, pending.PrivateState)
	if state.Len() != fullTextMaxFilesPerTask {
		t.Fatalf("expected full pending task to keep size %d, got %d", fullTextMaxFilesPerTask, state.Len())
	}

	found := false
	for _, item := range state.Items() {
		if item.FileID != targetFileID {
			continue
		}
		found = true
		assertStateItem(t, item, targetFileID, 999, 1999, updatedURI.String())
	}
	if !found {
		t.Fatalf("expected updated file %d to remain in full pending task", targetFileID)
	}
}

func TestUpdatePendingTaskStateInRegistryUpdatesRegisteredTask(t *testing.T) {
	settings := testSettingProvider{enabled: true}
	finalURI := mustURI(t, "cloudreve:///registry/final.txt")
	registry := queue.NewTaskRegistry()
	registered := &FullTextIndexTask{DBTask: &queue.DBTask{Task: &ent.Task{
		ID:           1,
		Type:         queue.FullTextIndexTaskType,
		PrivateState: "",
	}}}
	registry.Set(1, registered)
	dep := testDep{
		settings:   settings,
		taskClient: &testTaskClient{},
		mediaMeta:  &testQueue{},
		registry:   registry,
	}
	m := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		user:     &ent.User{ID: 1},
		settings: settings,
		dep:      dep,
	}

	stateBytes, err := marshalFullTextIndexTaskState(&FullTextIndexTaskState{
		Files: []FullTextIndexTaskItem{
			{FileID: 700, OwnerID: 799, EntityID: 899, Uri: finalURI},
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal state: %v", err)
	}
	m.updatePendingTaskStateInRegistry(1, string(stateBytes))
	m.updatePendingTaskStateInRegistry(999, "{}")

	regTask, ok := registry.Get(1)
	if !ok {
		t.Fatalf("expected registered task to remain in registry")
	}
	regState := mustParseTaskState(t, regTask)
	if regState.Len() != 1 {
		t.Fatalf("expected registry task to contain updated state, got %+v", regState.Items())
	}
	assertStateItem(t, regState.Items()[0], 700, 799, 899, finalURI.String())
}

func TestProcessIndexDiffMixedOperationStressKeepsLastStatePerFile(t *testing.T) {
	ctx := context.Background()
	settings := testSettingProvider{enabled: true}
	pending := &ent.Task{
		ID:           1,
		Type:         queue.FullTextIndexTaskType,
		Status:       taskModel.StatusQueued,
		UpdatedAt:    time.Now(),
		PrivateState: "",
	}
	taskClient := &testTaskClient{pending: []*ent.Task{pending}}
	tasks := &testQueue{}
	dep := testDep{
		settings:   settings,
		taskClient: taskClient,
		mediaMeta:  tasks,
		registry:   queue.NewTaskRegistry(),
	}
	m := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		user:     &ent.User{ID: 1},
		settings: settings,
		dep:      dep,
	}

	type expectedItem struct {
		ownerID  int
		entityID int
		uri      string
	}
	expected := map[int]expectedItem{}
	rng := rand.New(rand.NewSource(20260321))

	const fileUniverse = 48
	const operations = 4000
	for i := 0; i < operations; i++ {
		fileID := rng.Intn(fileUniverse) + 1
		op := rng.Intn(5)

		switch op {
		case 0:
			uri := mustURI(t, "cloudreve:///mix/update-"+itoa(fileID)+"-"+itoa(i)+".txt")
			ownerID := 1000 + i
			entityID := 2000 + i
			m.processIndexDiff(ctx, &fs.IndexDiff{
				IndexToUpdate: []fs.IndexDiffUpdateDetails{
					{Uri: *uri, FileID: fileID, OwnerID: ownerID, EntityID: entityID},
				},
			})
			expected[fileID] = expectedItem{ownerID: ownerID, entityID: entityID, uri: uri.String()}
		case 1:
			m.processIndexDiff(ctx, &fs.IndexDiff{
				IndexToDelete: []int{fileID},
			})
			expected[fileID] = expectedItem{}
		case 2:
			uri := mustURI(t, "cloudreve:///mix/rename-"+itoa(fileID)+"-"+itoa(i)+".txt")
			entityID := 3000 + i
			m.processIndexDiff(ctx, &fs.IndexDiff{
				IndexToRename: []fs.IndexDiffRenameDetails{
					{Uri: *uri, FileID: fileID, EntityID: entityID},
				},
			})
			expected[fileID] = expectedItem{ownerID: 0, entityID: entityID, uri: uri.String()}
		case 3:
			uri := mustURI(t, "cloudreve:///mix/owner-"+itoa(fileID)+"-"+itoa(i)+".txt")
			ownerID := 4000 + i
			entityID := 5000 + i
			m.processIndexDiff(ctx, &fs.IndexDiff{
				IndexToChangeOwner: []fs.IndexDiffOwnerChangeDetails{
					{Uri: *uri, FileID: fileID, NewOwnerID: ownerID, EntityID: entityID},
				},
			})
			expected[fileID] = expectedItem{ownerID: ownerID, entityID: entityID, uri: uri.String()}
		default:
			uri := mustURI(t, "cloudreve:///mix/copy-"+itoa(fileID)+"-"+itoa(i)+".txt")
			ownerID := 6000 + i
			entityID := 7000 + i
			m.processIndexDiff(ctx, &fs.IndexDiff{
				IndexToCopy: []fs.IndexDiffCopyDetails{
					{Uri: *uri, FileID: fileID, OwnerID: ownerID, EntityID: entityID},
				},
			})
			expected[fileID] = expectedItem{ownerID: ownerID, entityID: entityID, uri: uri.String()}
		}
	}

	if len(tasks.tasks) != 0 {
		t.Fatalf("expected mixed diff stream to merge into pending task, got %d new task(s)", len(tasks.tasks))
	}

	state := mustParseState(t, pending.PrivateState)
	if state.Len() != len(expected) {
		t.Fatalf("unexpected mixed-stream pending size: got %d want %d", state.Len(), len(expected))
	}

	for _, item := range state.Items() {
		want, ok := expected[item.FileID]
		if !ok {
			t.Fatalf("unexpected file id in mixed-stream state: %+v", item)
		}
		assertStateItem(t, item, item.FileID, want.ownerID, want.entityID, want.uri)
	}
}

func TestFullTextDeleteTaskDoDeletesIndexWhenFTSDisabled(t *testing.T) {
	settings := testSettingProvider{enabled: false}
	indexer := &testSearchIndexer{}
	dep := testDep{
		settings:      settings,
		taskClient:    &testTaskClient{},
		mediaMeta:     &testQueue{},
		registry:      queue.NewTaskRegistry(),
		config:        testConfigProvider{},
		searchIndexer: indexer,
	}
	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)

	ftTask, err := NewFullTextDeleteTask(ctx, []int{81, 82, 83}, nil)
	if err != nil {
		t.Fatalf("failed to create full text delete task: %v", err)
	}

	status, err := ftTask.Do(ctx)
	if err != nil {
		t.Fatalf("unexpected delete task error: %v", err)
	}
	if status != task.StatusCompleted {
		t.Fatalf("unexpected delete task status: got %s want %s", status, task.StatusCompleted)
	}
	if len(indexer.deleted) != 3 || indexer.deleted[0] != 81 || indexer.deleted[1] != 82 || indexer.deleted[2] != 83 {
		t.Fatalf("unexpected deleted file ids: %v", indexer.deleted)
	}
	if indexer.upserted != 0 {
		t.Fatalf("expected no upsert calls when FTS disabled, got %d", indexer.upserted)
	}
}

func TestFullTextIndexTaskDoSkipsWhenFTSDisabled(t *testing.T) {
	settings := testSettingProvider{enabled: false}
	indexer := &testSearchIndexer{}
	dep := testDep{
		settings:      settings,
		taskClient:    &testTaskClient{},
		mediaMeta:     &testQueue{},
		registry:      queue.NewTaskRegistry(),
		config:        testConfigProvider{},
		searchIndexer: indexer,
	}
	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)

	uri := mustURI(t, "cloudreve:///skip/index.txt")
	ftTask, err := NewFullTextIndexTask(ctx, uri, 901, 801, 701, nil)
	if err != nil {
		t.Fatalf("failed to create full text index task: %v", err)
	}

	status, err := ftTask.Do(ctx)
	if err != nil {
		t.Fatalf("unexpected index task error: %v", err)
	}
	if status != task.StatusCompleted {
		t.Fatalf("unexpected index task status: got %s want %s", status, task.StatusCompleted)
	}
	if len(indexer.deleted) != 0 || indexer.upserted != 0 {
		t.Fatalf("expected no indexer calls when FTS disabled, got deletes=%v upserts=%d", indexer.deleted, indexer.upserted)
	}
}

func TestFullTextIndexTaskDoDispatchesToSlaveContentProcessing(t *testing.T) {
	settings := testSettingProvider{
		enabled: true,
		tikaCfg: &setting.FTSTikaExtractorSetting{
			Endpoint:           "http://tika:9998",
			Exts:               []string{".pdf"},
			MaxFileSize:        20 << 20,
			SidecarEnabled:     true,
			SidecarTextEnabled: true,
		},
	}
	textExtractor := tikaextractor.NewTikaExtractor(nil, settings, logging.NewConsoleLogger(logging.LevelError), settings.tikaCfg)
	policy := &ent.StoragePolicy{
		ID:       9,
		Type:     inventorytypes.PolicyTypeLocal,
		Settings: &inventorytypes.PolicySetting{},
	}
	fileClient := &testFileClient{
		fileByID: map[int]*ent.File{
			801: {
				ID:            801,
				OwnerID:       701,
				Name:          "dispatch.pdf",
				Size:          1024,
				PrimaryEntity: 901,
				Edges: ent.FileEdges{
					Entities: []*ent.Entity{
						{
							ID:                    901,
							StoragePolicyEntities: 9,
						},
					},
				},
			},
		},
	}
	node := &testClusterNode{
		id:       21,
		isMaster: false,
		createID: 314,
	}
	dep := testDep{
		settings:      settings,
		taskClient:    &testTaskClient{},
		mediaMeta:     &testQueue{},
		registry:      queue.NewTaskRegistry(),
		config:        testConfigProvider{},
		searchIndexer: &testSearchIndexer{},
		fileClient:    fileClient,
		policyClient:  &testPolicyClient{policyByID: map[int]*ent.StoragePolicy{9: policy}},
		nodePool:      &testNodePool{node: node},
		textExtractor: textExtractor,
	}
	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)

	uri := mustURI(t, "cloudreve:///dispatch/dispatch.pdf")
	ftTask, err := NewFullTextIndexTask(ctx, uri, 901, 801, 701, nil)
	if err != nil {
		t.Fatalf("failed to create full text index task: %v", err)
	}

	status, err := ftTask.Do(ctx)
	if err != nil {
		t.Fatalf("unexpected index task error: %v", err)
	}
	if status != task.StatusSuspending {
		t.Fatalf("unexpected index task status: got %s want %s", status, task.StatusSuspending)
	}
	if node.createdTaskType != queue.SlaveContentProcessingTaskType {
		t.Fatalf("unexpected created task type: got %s want %s", node.createdTaskType, queue.SlaveContentProcessingTaskType)
	}

	state := mustParseTaskState(t, ftTask)
	if state.Phase != fullTextIndexPhaseAwaitSlave {
		t.Fatalf("unexpected task phase: got %s want %s", state.Phase, fullTextIndexPhaseAwaitSlave)
	}
	if state.SlaveID != 314 || state.NodeID != 21 {
		t.Fatalf("unexpected slave dispatch state: %+v", state)
	}
	if state.Active == nil || state.Active.FileID != 801 {
		t.Fatalf("expected active file 801 after slave dispatch, got %+v", state.Active)
	}

	wrapper, err := parseSlaveContentProcessingState(node.createdState)
	if err != nil {
		t.Fatalf("failed to parse created slave state: %v", err)
	}
	if wrapper.Kind != slaveContentProcessingKindFullTextExtract {
		t.Fatalf("unexpected slave content processing kind: got %s want %s", wrapper.Kind, slaveContentProcessingKindFullTextExtract)
	}

	payload := &SlaveFullTextExtractPayload{}
	if err := json.Unmarshal(wrapper.Payload, payload); err != nil {
		t.Fatalf("failed to parse slave full text payload: %v", err)
	}
	if payload.FileID != 801 || payload.OwnerID != 701 || payload.Entity == nil || payload.Entity.ID != 901 {
		t.Fatalf("unexpected slave full text payload: %+v", payload)
	}
}

func TestManagerShouldOffloadFullTextToSlaveRequiresTikaAndSidecarText(t *testing.T) {
	tikaSettings := testSettingProvider{
		enabled: true,
		tikaCfg: &setting.FTSTikaExtractorSetting{
			Endpoint:           "http://tika:9998",
			Exts:               []string{".pdf"},
			MaxFileSize:        1024,
			SidecarEnabled:     true,
			SidecarTextEnabled: true,
		},
	}
	tikaManager := &manager{
		settings: tikaSettings,
		dep: testDep{
			settings:      tikaSettings,
			textExtractor: tikaextractor.NewTikaExtractor(nil, tikaSettings, logging.NewConsoleLogger(logging.LevelError), tikaSettings.tikaCfg),
		},
	}
	if !tikaManager.shouldOffloadFullTextToSlave(context.Background()) {
		t.Fatalf("expected manager to offload when tika and text sidecar are enabled")
	}

	plainSettings := testSettingProvider{
		enabled: true,
		tikaCfg: &setting.FTSTikaExtractorSetting{
			SidecarEnabled:     true,
			SidecarTextEnabled: true,
		},
	}
	plainManager := &manager{
		settings: plainSettings,
		dep: testDep{
			settings:      plainSettings,
			textExtractor: testTextExtractor{exts: []string{".txt"}, maxFileSize: 1024},
		},
	}
	if plainManager.shouldOffloadFullTextToSlave(context.Background()) {
		t.Fatalf("expected manager not to offload for non-tika extractor")
	}
}

func TestManagerShouldOffloadFullTextToSlaveAllowsAssetsOnly(t *testing.T) {
	settings := testSettingProvider{
		enabled: true,
		tikaCfg: &setting.FTSTikaExtractorSetting{
			Endpoint:             "http://tika:9998",
			Exts:                 []string{".pdf"},
			MaxFileSize:          1024,
			SidecarEnabled:       true,
			SidecarTextEnabled:   false,
			SidecarAssetsEnabled: true,
		},
	}
	m := &manager{
		settings: settings,
		dep: testDep{
			settings:      settings,
			textExtractor: tikaextractor.NewTikaExtractor(nil, settings, logging.NewConsoleLogger(logging.LevelError), settings.tikaCfg),
		},
	}

	if !m.shouldOffloadFullTextToSlave(context.Background()) {
		t.Fatalf("expected manager to offload when assets sidecar is enabled")
	}
}

func TestFullTextIndexTaskAwaitSlaveExtractionHandlesRunningAndError(t *testing.T) {
	runningNode := &testClusterNode{
		id:          51,
		slaveTask:   &cluster.SlaveTaskSummary{Status: task.StatusProcessing, Progress: queue.Progresses{"slave": &queue.Progress{Current: 2, Total: 5}}},
		isMaster:    false,
		clearCalled: false,
	}
	runningTask := &FullTextIndexTask{
		DBTask: &queue.DBTask{
			Task: &ent.Task{
				Type:        queue.FullTextIndexTaskType,
				PublicState: &inventorytypes.TaskPublicState{},
			},
		},
	}
	runningState := &FullTextIndexTaskState{
		Phase:   fullTextIndexPhaseAwaitSlave,
		NodeID:  51,
		SlaveID: 1001,
		Active:  &FullTextIndexTaskItem{FileID: 801},
	}
	runningManager := &manager{
		settings: testSettingProvider{enabled: true},
		dep: testDep{
			settings: testSettingProvider{enabled: true},
			nodePool: &testNodePool{node: runningNode},
		},
	}

	status, err := runningTask.awaitSlaveExtraction(context.Background(), runningManager, runningState)
	if err != nil {
		t.Fatalf("unexpected running await error: %v", err)
	}
	if status != task.StatusSuspending {
		t.Fatalf("unexpected running await status: got %s want %s", status, task.StatusSuspending)
	}
	if runningTask.ResumeTime() == 0 {
		t.Fatalf("expected resume time to be set while slave task is still running")
	}
	progress := runningTask.Progress(context.Background())
	if progress["slave"] == nil || progress["slave"].Current != 2 || progress["slave"].Total != 5 {
		t.Fatalf("unexpected slave progress relay: %+v", progress)
	}
	if runningNode.getTaskID != 1001 || !runningNode.clearCalled {
		t.Fatalf("unexpected running node getTask call: id=%d clear=%v", runningNode.getTaskID, runningNode.clearCalled)
	}

	failedNode := &testClusterNode{
		id:        52,
		isMaster:  false,
		slaveTask: &cluster.SlaveTaskSummary{Status: task.StatusError, Error: "boom"},
	}
	failedTask := &FullTextIndexTask{
		DBTask: &queue.DBTask{
			Task: &ent.Task{
				Type:        queue.FullTextIndexTaskType,
				PublicState: &inventorytypes.TaskPublicState{},
			},
		},
	}
	failedState := &FullTextIndexTaskState{
		Phase:   fullTextIndexPhaseAwaitSlave,
		NodeID:  52,
		SlaveID: 1002,
		Active:  &FullTextIndexTaskItem{FileID: 802},
	}
	failedManager := &manager{
		settings: testSettingProvider{enabled: true},
		dep: testDep{
			settings: testSettingProvider{enabled: true},
			nodePool: &testNodePool{node: failedNode},
		},
	}

	status, err = failedTask.awaitSlaveExtraction(context.Background(), failedManager, failedState)
	if err == nil {
		t.Fatalf("expected slave failure to be returned")
	}
	if status != task.StatusError {
		t.Fatalf("unexpected failed await status: got %s want %s", status, task.StatusError)
	}
}

func TestFullTextIndexTaskSummarizeReportsPhaseNodeAndCurrentFile(t *testing.T) {
	uri := mustURI(t, "cloudreve:///summary/current.pdf")
	taskModel := &ent.Task{
		Type:        queue.FullTextIndexTaskType,
		PublicState: &inventorytypes.TaskPublicState{},
	}
	ftTask := &FullTextIndexTask{
		DBTask: &queue.DBTask{Task: taskModel},
		state: &FullTextIndexTaskState{
			Phase:  fullTextIndexPhaseAwaitSlave,
			NodeID: 61,
			Active: &FullTextIndexTaskItem{
				Uri:      uri,
				FileID:   810,
				OwnerID:  710,
				EntityID: 910,
			},
			Files: []FullTextIndexTaskItem{{FileID: 810}},
		},
	}

	summary := ftTask.Summarize(nil)
	if summary == nil {
		t.Fatal("expected summary")
	}
	if summary.NodeID != 61 || summary.Phase != string(fullTextIndexPhaseAwaitSlave) {
		t.Fatalf("unexpected summary basic fields: %+v", summary)
	}
	if summary.Props["file_id"] != 810 || summary.Props["owner_id"] != 710 || summary.Props["entity_id"] != 910 {
		t.Fatalf("unexpected summary props: %+v", summary.Props)
	}
	if summary.Props["src"] != uri {
		t.Fatalf("unexpected summary src: %+v", summary.Props["src"])
	}
}

func TestExecuteSlaveFullTextExtractValidatesExtractorAndEligibility(t *testing.T) {
	payload := &SlaveFullTextExtractPayload{
		FileID:   900,
		OwnerID:  901,
		FileName: "sample.bin",
		FileSize: 128,
		Entity:   &ent.Entity{ID: 902},
	}

	plainDep := testDep{
		settings: testSettingProvider{
			enabled: true,
			tikaCfg: &setting.FTSTikaExtractorSetting{
				SidecarEnabled:     true,
				SidecarTextEnabled: true,
			},
		},
		textExtractor: testTextExtractor{exts: []string{".txt"}, maxFileSize: 1024},
	}
	if _, err := ExecuteSlaveFullTextExtract(context.WithValue(context.Background(), dependency.DepCtx{}, plainDep), plainDep, payload); err == nil {
		t.Fatalf("expected non-tika extractor to be rejected")
	}

	tikaSettings := testSettingProvider{
		enabled: true,
		tikaCfg: &setting.FTSTikaExtractorSetting{
			Endpoint:           "http://tika:9998",
			Exts:               []string{".pdf"},
			MaxFileSize:        1024,
			SidecarEnabled:     true,
			SidecarTextEnabled: true,
		},
	}
	tikaDep := testDep{
		settings:      tikaSettings,
		textExtractor: tikaextractor.NewTikaExtractor(nil, tikaSettings, logging.NewConsoleLogger(logging.LevelError), tikaSettings.tikaCfg),
	}
	result, err := ExecuteSlaveFullTextExtract(context.WithValue(context.Background(), dependency.DepCtx{}, tikaDep), tikaDep, payload)
	if err != nil {
		t.Fatalf("expected unsupported extension to short-circuit without error, got %v", err)
	}
	if result == nil || result.EntityID != 902 || result.ManifestPath != "" {
		t.Fatalf("unexpected short-circuit result: %+v", result)
	}
}

func TestExecuteSlaveFullTextExtractAllowsAssetsOnlyConfig(t *testing.T) {
	payload := &SlaveFullTextExtractPayload{
		FileID:   901,
		OwnerID:  902,
		FileName: "sample.bin",
		FileSize: 128,
		Entity:   &ent.Entity{ID: 903},
	}
	settings := testSettingProvider{
		enabled: true,
		tikaCfg: &setting.FTSTikaExtractorSetting{
			Endpoint:             "http://tika:9998",
			Exts:                 []string{".pdf"},
			MaxFileSize:          1024,
			SidecarEnabled:       true,
			SidecarTextEnabled:   false,
			SidecarAssetsEnabled: true,
		},
	}
	dep := testDep{
		settings:      settings,
		textExtractor: tikaextractor.NewTikaExtractor(nil, settings, logging.NewConsoleLogger(logging.LevelError), settings.tikaCfg),
	}

	result, err := ExecuteSlaveFullTextExtract(context.WithValue(context.Background(), dependency.DepCtx{}, dep), dep, payload)
	if err != nil {
		t.Fatalf("expected assets-only config to be allowed, got %v", err)
	}
	if result == nil || result.EntityID != 903 || result.ManifestPath != "" {
		t.Fatalf("unexpected assets-only short-circuit result: %+v", result)
	}
}

func TestApplySlaveFTSSidecarResultPatchesMetadata(t *testing.T) {
	fileClient := &testFileClient{
		fileByID: map[int]*ent.File{
			801: {
				ID:            801,
				OwnerID:       701,
				Name:          "finalize.pdf",
				PrimaryEntity: 901,
				Edges: ent.FileEdges{
					Metadata: []*ent.Metadata{},
				},
			},
		},
	}
	backend := &testMetadataFS{}
	m := &manager{
		fs:       backend,
		settings: testSettingProvider{enabled: true},
		dep: testDep{
			settings:   testSettingProvider{enabled: true},
			fileClient: fileClient,
		},
	}

	uri := mustURI(t, "cloudreve:///finalize/finalize.pdf")
	err := m.applySlaveFTSSidecarResult(context.Background(), 801, uri, &SlaveFullTextExtractResult{
		EntityID:     901,
		ManifestPath: "cloudreve/fts-sidecar/701/801/901/manifest.json",
	})
	if err != nil {
		t.Fatalf("unexpected applySlaveFTSSidecarResult error: %v", err)
	}

	if len(backend.paths) != 1 || backend.paths[0].String() != uri.String() {
		t.Fatalf("unexpected metadata patch paths: %+v", backend.paths)
	}
	if len(backend.patches) != 2 {
		t.Fatalf("unexpected metadata patch count: %+v", backend.patches)
	}

	if backend.patches[0].Key != dbfs.FTSSidecarManifestKey || backend.patches[0].Value == "" || !backend.patches[0].Private {
		t.Fatalf("unexpected manifest metadata patch: %+v", backend.patches[0])
	}
	if backend.patches[1].Key != dbfs.FTSSidecarEntityIDKey || backend.patches[1].Value != "901" || !backend.patches[1].Private {
		t.Fatalf("unexpected entity metadata patch: %+v", backend.patches[1])
	}
}

func TestApplySlaveFTSSidecarResultRemovesMetadataWhenResultEmpty(t *testing.T) {
	fileClient := &testFileClient{
		fileByID: map[int]*ent.File{
			802: {
				ID:            802,
				OwnerID:       702,
				Name:          "cleanup.pdf",
				PrimaryEntity: 902,
				Edges: ent.FileEdges{
					Metadata: []*ent.Metadata{
						{Name: dbfs.FTSSidecarManifestKey, Value: "old/manifest.json"},
						{Name: dbfs.FTSSidecarEntityIDKey, Value: "902"},
					},
				},
			},
		},
	}
	backend := &testMetadataFS{}
	m := &manager{
		fs:       backend,
		settings: testSettingProvider{enabled: true},
		dep: testDep{
			settings:   testSettingProvider{enabled: true},
			fileClient: fileClient,
		},
	}

	uri := mustURI(t, "cloudreve:///cleanup/cleanup.pdf")
	err := m.applySlaveFTSSidecarResult(context.Background(), 802, uri, nil)
	if err != nil {
		t.Fatalf("unexpected applySlaveFTSSidecarResult remove error: %v", err)
	}

	if len(backend.patches) != 2 {
		t.Fatalf("unexpected metadata patch count: %+v", backend.patches)
	}
	if backend.patches[0].Key != dbfs.FTSSidecarManifestKey || !backend.patches[0].Remove || !backend.patches[0].Private {
		t.Fatalf("unexpected manifest remove patch: %+v", backend.patches[0])
	}
	if backend.patches[1].Key != dbfs.FTSSidecarEntityIDKey || !backend.patches[1].Remove || !backend.patches[1].Private {
		t.Fatalf("unexpected entity remove patch: %+v", backend.patches[1])
	}
}

func mustURI(t *testing.T, raw string) *fs.URI {
	t.Helper()
	uri, err := fs.NewUriFromString(raw)
	if err != nil {
		t.Fatalf("failed to parse uri %q: %v", raw, err)
	}
	return uri
}

func mustParseTaskState(t *testing.T, task queue.Task) *FullTextIndexTaskState {
	t.Helper()
	return mustParseState(t, task.State())
}

func mustParseState(t *testing.T, raw string) *FullTextIndexTaskState {
	t.Helper()
	state, err := parseFullTextIndexTaskState(raw)
	if err != nil {
		t.Fatalf("failed to parse full text task state: %v", err)
	}
	return state
}

func itoa(v int) string {
	return strconv.Itoa(v)
}

func newPendingFTSTask(t *testing.T, id int, updatedAt time.Time, items ...FullTextIndexTaskItem) *ent.Task {
	t.Helper()
	state := &FullTextIndexTaskState{}
	for _, item := range items {
		state.Upsert(item)
	}
	stateBytes, err := marshalFullTextIndexTaskState(state)
	if err != nil {
		t.Fatalf("failed to marshal pending full text state: %v", err)
	}
	return &ent.Task{
		ID:           id,
		Type:         queue.FullTextIndexTaskType,
		Status:       taskModel.StatusQueued,
		UpdatedAt:    updatedAt,
		PrivateState: string(stateBytes),
	}
}

func assertQueuedState(t *testing.T, task queue.Task, fileID, ownerID, entityID int, uri string) {
	t.Helper()
	if task.Type() != queue.FullTextIndexTaskType {
		t.Fatalf("unexpected queued task type: got %s want %s", task.Type(), queue.FullTextIndexTaskType)
	}

	state := mustParseTaskState(t, task)
	items := state.Items()
	if len(items) != 1 {
		t.Fatalf("unexpected queued task item count: %+v", items)
	}

	item := items[0]
	assertStateItem(t, item, fileID, ownerID, entityID, uri)
}

func assertStateItem(t *testing.T, item FullTextIndexTaskItem, fileID, ownerID, entityID int, uri string) {
	t.Helper()
	if item.FileID != fileID || item.OwnerID != ownerID || item.EntityID != entityID {
		t.Fatalf("unexpected queued task item: got %+v want file=%d owner=%d entity=%d", item, fileID, ownerID, entityID)
	}

	if uri == "" {
		if item.Uri != nil {
			t.Fatalf("expected nil uri for file %d, got %s", fileID, item.Uri.String())
		}
		return
	}

	if item.Uri == nil || item.Uri.String() != uri {
		t.Fatalf("unexpected queued task uri: got %+v want %s", item.Uri, uri)
	}
}

type testWalkFile struct {
	fs.File
	id int
}

func (f testWalkFile) ID() int {
	return f.id
}

type testSettingProvider struct {
	setting.Provider
	enabled bool
	tikaCfg *setting.FTSTikaExtractorSetting
}

func (s testSettingProvider) FTSEnabled(ctx context.Context) bool {
	return s.enabled
}

func (s testSettingProvider) FTSTikaExtractor(ctx context.Context) *setting.FTSTikaExtractorSetting {
	if s.tikaCfg != nil {
		return s.tikaCfg
	}
	return &setting.FTSTikaExtractorSetting{}
}

func (s testSettingProvider) MediaMetaExifEnabled(ctx context.Context) bool {
	return false
}

func (s testSettingProvider) MediaMetaMusicEnabled(ctx context.Context) bool {
	return false
}

func (s testSettingProvider) MediaMetaFFProbeEnabled(ctx context.Context) bool {
	return false
}

func (s testSettingProvider) MediaMetaGeocodingEnabled(ctx context.Context) bool {
	return false
}

type testDep struct {
	dependency.Dep
	settings      setting.Provider
	settingClient inventory.SettingClient
	taskClient    inventory.TaskClient
	contentQueue  queue.Queue
	mediaMeta     queue.Queue
	registry      queue.TaskRegistry
	config        conf.ConfigProvider
	searchIndexer searcher.SearchIndexer
	fileClient    inventory.FileClient
	policyClient  inventory.StoragePolicyClient
	userClient    inventory.UserClient
	nodePool      cluster.NodePool
	textExtractor searcher.TextExtractor
	mediaMetaExt  mediameta.Extractor
	thumbGen      thumb.Generator
}

func (d testDep) SettingProvider() setting.Provider {
	return d.settings
}

func (d testDep) SettingClient() inventory.SettingClient {
	return d.settingClient
}

func (d testDep) TaskClient() inventory.TaskClient {
	return d.taskClient
}

func (d testDep) ContentProcessingQueue(ctx context.Context) queue.Queue {
	if d.contentQueue != nil {
		return d.contentQueue
	}
	return d.mediaMeta
}

func (d testDep) MediaMetaQueue(ctx context.Context) queue.Queue {
	if d.mediaMeta != nil {
		return d.mediaMeta
	}
	return d.contentQueue
}

func (d testDep) TaskRegistry() queue.TaskRegistry {
	return d.registry
}

func (d testDep) ConfigProvider() conf.ConfigProvider {
	return d.config
}

func (d testDep) SearchIndexer(ctx context.Context) searcher.SearchIndexer {
	return d.searchIndexer
}

func (d testDep) Logger() logging.Logger {
	return logging.NewConsoleLogger(logging.LevelError)
}

func (d testDep) FileClient() inventory.FileClient {
	return d.fileClient
}

func (d testDep) UserClient() inventory.UserClient {
	return d.userClient
}

func (d testDep) StoragePolicyClient() inventory.StoragePolicyClient {
	return d.policyClient
}

func (d testDep) NodePool(ctx context.Context) (cluster.NodePool, error) {
	return d.nodePool, nil
}

func (d testDep) KV() cache.Driver {
	return nil
}

func (d testDep) GeneralAuth() auth.Auth {
	return nil
}

func (d testDep) HashIDEncoder() hashid.Encoder {
	return nil
}

func (d testDep) TextExtractor(ctx context.Context) searcher.TextExtractor {
	return d.textExtractor
}

func (d testDep) MediaMetaExtractor(ctx context.Context) mediameta.Extractor {
	return d.mediaMetaExt
}

func (d testDep) ThumbPipeline() thumb.Generator {
	return d.thumbGen
}

type testTaskClient struct {
	inventory.TaskClient
	pending []*ent.Task
}

func (c *testTaskClient) GetPendingTasks(ctx context.Context, taskType ...string) ([]*ent.Task, error) {
	return append([]*ent.Task(nil), c.pending...), nil
}

func (c *testTaskClient) UpdatePrivateState(ctx context.Context, taskModel *ent.Task, privateState string) (*ent.Task, error) {
	taskModel.PrivateState = privateState
	taskModel.UpdatedAt = time.Now()
	return taskModel, nil
}

type testQueue struct {
	queue.Queue
	tasks []queue.Task
}

func (q *testQueue) QueueTask(ctx context.Context, t queue.Task) error {
	q.tasks = append(q.tasks, t)
	return nil
}

type testConfigProvider struct {
	conf.ConfigProvider
}

func (c testConfigProvider) System() *conf.System {
	return &conf.System{}
}

func (c testConfigProvider) Slave() *conf.Slave {
	return &conf.Slave{}
}

type testSearchIndexer struct {
	searcher.SearchIndexer
	deleted  []int
	upserted int
}

func (s *testSearchIndexer) UpsertFile(ctx context.Context, doc *searcher.SearchFileDocument) error {
	s.upserted++
	return nil
}

func (s *testSearchIndexer) BulkUpsertFiles(ctx context.Context, docs []*searcher.SearchFileDocument) error {
	s.upserted += len(docs)
	return nil
}

func (s *testSearchIndexer) DeleteByFileIDs(ctx context.Context, fileID ...int) error {
	s.deleted = append(s.deleted, fileID...)
	return nil
}

func (s *testSearchIndexer) Search(ctx context.Context, req *searcher.SearchRequest) ([]searcher.SearchResult, int64, error) {
	return nil, 0, nil
}

func (s *testSearchIndexer) IndexReady(ctx context.Context) (bool, error) {
	return true, nil
}

func (s *testSearchIndexer) EnsureIndex(ctx context.Context) error {
	return nil
}

func (s *testSearchIndexer) DeleteAll(ctx context.Context) error {
	return nil
}

func (s *testSearchIndexer) Close() error {
	return nil
}

type testFileClient struct {
	inventory.FileClient
	fileByID     map[int]*ent.File
	ancestorByID map[int][]*ent.File
}

func (c *testFileClient) GetByID(ctx context.Context, id int) (*ent.File, error) {
	if file, ok := c.fileByID[id]; ok {
		return file, nil
	}
	return nil, &ent.NotFoundError{}
}

func (c *testFileClient) GetAncestorFiles(ctx context.Context, target *ent.File) ([]*ent.File, error) {
	if target != nil && c.ancestorByID != nil {
		if items, ok := c.ancestorByID[target.ID]; ok {
			return append([]*ent.File(nil), items...), nil
		}
	}
	return nil, inventory.ErrTreePathQueryUnavailable
}

type testSettingClient struct {
	inventory.SettingClient
	values map[string]string
}

func (c testSettingClient) Get(ctx context.Context, name string) (string, error) {
	if value, ok := c.values[name]; ok {
		return value, nil
	}
	return "", &ent.NotFoundError{}
}

func (c testSettingClient) Set(ctx context.Context, settings map[string]string) error {
	if c.values == nil {
		c.values = map[string]string{}
	}
	for k, v := range settings {
		c.values[k] = v
	}
	return nil
}

func (c testSettingClient) Gets(ctx context.Context, names []string) (map[string]string, error) {
	res := make(map[string]string, len(names))
	for _, name := range names {
		if value, ok := c.values[name]; ok {
			res[name] = value
		}
	}
	return res, nil
}

type testPolicyClient struct {
	inventory.StoragePolicyClient
	policyByID map[int]*ent.StoragePolicy
}

func (c *testPolicyClient) GetPolicyByID(ctx context.Context, id int) (*ent.StoragePolicy, error) {
	if policy, ok := c.policyByID[id]; ok {
		return policy, nil
	}
	return nil, &ent.NotFoundError{}
}

type testNodePool struct {
	cluster.NodePool
	node cluster.Node
}

func (p *testNodePool) Get(ctx context.Context, capability inventorytypes.NodeCapability, preferred int) (cluster.Node, error) {
	return p.node, nil
}

type testClusterNode struct {
	cluster.Node
	id              int
	isMaster        bool
	createID        int
	createdTaskType string
	createdState    string
	slaveTask       *cluster.SlaveTaskSummary
	getTaskID       int
	clearCalled     bool
}

func (n *testClusterNode) ID() int {
	return n.id
}

func (n *testClusterNode) IsMaster() bool {
	return n.isMaster
}

func (n *testClusterNode) CreateTask(ctx context.Context, taskType string, state string) (int, error) {
	n.createdTaskType = taskType
	n.createdState = state
	return n.createID, nil
}

func (n *testClusterNode) GetTask(ctx context.Context, id int, clearOnComplete bool) (*cluster.SlaveTaskSummary, error) {
	n.getTaskID = id
	n.clearCalled = clearOnComplete
	if n.slaveTask == nil {
		return nil, nil
	}
	return n.slaveTask, nil
}

type testMetadataFS struct {
	fs.FileSystem
	paths   []*fs.URI
	patches []fs.MetadataPatch
}

func (f *testMetadataFS) PatchMetadata(ctx context.Context, path []*fs.URI, metas ...fs.MetadataPatch) error {
	f.paths = append(f.paths, path...)
	f.patches = append(f.patches, metas...)
	return nil
}

func (f *testMetadataFS) GetEntity(ctx context.Context, entityID int) (fs.Entity, error) {
	return nil, nil
}
