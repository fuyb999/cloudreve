package manager

import (
	"context"
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
	"github.com/cloudreve/Cloudreve/v4/pkg/auth"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
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
}

func (s testSettingProvider) FTSEnabled(ctx context.Context) bool {
	return s.enabled
}

type testDep struct {
	dependency.Dep
	settings      setting.Provider
	taskClient    inventory.TaskClient
	mediaMeta     queue.Queue
	registry      queue.TaskRegistry
	config        conf.ConfigProvider
	searchIndexer searcher.SearchIndexer
}

func (d testDep) SettingProvider() setting.Provider {
	return d.settings
}

func (d testDep) TaskClient() inventory.TaskClient {
	return d.taskClient
}

func (d testDep) MediaMetaQueue(ctx context.Context) queue.Queue {
	return d.mediaMeta
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

func (d testDep) KV() cache.Driver {
	return nil
}

func (d testDep) GeneralAuth() auth.Auth {
	return nil
}

func (d testDep) HashIDEncoder() hashid.Encoder {
	return nil
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
