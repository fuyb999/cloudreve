package manager

import (
	"context"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
)

func TestDeleteSoftDeleteQueuesRecursiveFullTextDeletes(t *testing.T) {
	ctx := context.Background()
	rootA := mustURI(t, "cloudreve:///trash/a")
	rootB := mustURI(t, "cloudreve:///trash/b")
	backend := &testOperationFS{
		walkIDs: map[string][]int{
			rootA.String(): {11, 12},
			rootB.String(): {12, 13},
		},
	}
	tasks := &testQueue{}
	settings := testSettingProvider{enabled: true}
	m := &manager{
		l:         logging.NewConsoleLogger(logging.LevelError),
		user:      &ent.User{ID: 1},
		fs:        backend,
		settings:  settings,
		stateless: true,
		dep: testDep{
			settings:   settings,
			taskClient: &testTaskClient{},
			mediaMeta:  tasks,
			registry:   queue.NewTaskRegistry(),
		},
	}

	if err := m.Delete(ctx, []*fs.URI{rootA, rootB}); err != nil {
		t.Fatalf("unexpected delete error: %v", err)
	}

	if len(backend.softDeleted) != 2 {
		t.Fatalf("unexpected soft delete call paths: %v", backend.softDeleted)
	}
	if backend.walkCalls != 2 {
		t.Fatalf("unexpected walk call count: got %d want 2", backend.walkCalls)
	}
	if len(tasks.tasks) != 3 {
		t.Fatalf("unexpected queued task count: got %d want 3", len(tasks.tasks))
	}

	assertQueuedState(t, tasks.tasks[0], 11, 0, 0, "")
	assertQueuedState(t, tasks.tasks[1], 12, 0, 0, "")
	assertQueuedState(t, tasks.tasks[2], 13, 0, 0, "")
}

func TestRestoreQueuesRecursiveFullTextReconcile(t *testing.T) {
	ctx := context.Background()
	root := mustURI(t, "cloudreve:///trash/restore")
	backend := &testOperationFS{
		walkIDs: map[string][]int{
			root.String(): {21, 22, 23},
		},
	}
	tasks := &testQueue{}
	settings := testSettingProvider{enabled: true}
	m := &manager{
		l:         logging.NewConsoleLogger(logging.LevelError),
		user:      &ent.User{ID: 1},
		fs:        backend,
		settings:  settings,
		stateless: true,
		dep: testDep{
			settings:   settings,
			taskClient: &testTaskClient{},
			mediaMeta:  tasks,
			registry:   queue.NewTaskRegistry(),
		},
	}

	if err := m.Restore(ctx, root); err != nil {
		t.Fatalf("unexpected restore error: %v", err)
	}

	if len(backend.restored) != 1 || backend.restored[0] != root.String() {
		t.Fatalf("unexpected restore call paths: %v", backend.restored)
	}
	if backend.walkCalls != 1 {
		t.Fatalf("unexpected walk call count: got %d want 1", backend.walkCalls)
	}
	if len(tasks.tasks) != 3 {
		t.Fatalf("unexpected queued task count: got %d want 3", len(tasks.tasks))
	}

	assertQueuedState(t, tasks.tasks[0], 21, 0, 0, "")
	assertQueuedState(t, tasks.tasks[1], 22, 0, 0, "")
	assertQueuedState(t, tasks.tasks[2], 23, 0, 0, "")
}

func TestDeleteAndRestoreSkipFullTextTraversalWhenDisabled(t *testing.T) {
	ctx := context.Background()
	root := mustURI(t, "cloudreve:///trash/disabled")
	backend := &testOperationFS{
		walkIDs: map[string][]int{
			root.String(): {31, 32},
		},
	}
	tasks := &testQueue{}
	settings := testSettingProvider{enabled: false}
	m := &manager{
		l:         logging.NewConsoleLogger(logging.LevelError),
		user:      &ent.User{ID: 1},
		fs:        backend,
		settings:  settings,
		stateless: true,
		dep: testDep{
			settings:   settings,
			taskClient: &testTaskClient{},
			mediaMeta:  tasks,
			registry:   queue.NewTaskRegistry(),
		},
	}

	if err := m.Delete(ctx, []*fs.URI{root}); err != nil {
		t.Fatalf("unexpected delete error: %v", err)
	}
	if err := m.Restore(ctx, root); err != nil {
		t.Fatalf("unexpected restore error: %v", err)
	}

	if backend.walkCalls != 0 {
		t.Fatalf("expected no walk calls when FTS disabled, got %d", backend.walkCalls)
	}
	if len(tasks.tasks) != 0 {
		t.Fatalf("expected no full text tasks when FTS disabled, got %d", len(tasks.tasks))
	}
}

type testOperationFS struct {
	fs.FileSystem
	walkIDs     map[string][]int
	walkCalls   int
	softDeleted []string
	restored    []string
}

func (f *testOperationFS) SoftDelete(ctx context.Context, path ...*fs.URI) error {
	for _, item := range path {
		if item != nil {
			f.softDeleted = append(f.softDeleted, item.String())
		}
	}
	return nil
}

func (f *testOperationFS) Restore(ctx context.Context, path ...*fs.URI) error {
	for _, item := range path {
		if item != nil {
			f.restored = append(f.restored, item.String())
		}
	}
	return nil
}

func (f *testOperationFS) Walk(ctx context.Context, path *fs.URI, depth int, walk fs.WalkFunc, opts ...fs.Option) error {
	f.walkCalls++
	for _, id := range f.walkIDs[path.String()] {
		if err := walk(testWalkFile{id: id}, 0); err != nil {
			return err
		}
	}
	return nil
}
