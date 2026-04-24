package queue

import (
	"context"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	inventorydebug "github.com/cloudreve/Cloudreve/v4/inventory/debug"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/gofrs/uuid"
)

type testQueueDep struct{}

func (d testQueueDep) ForkWithLogger(ctx context.Context, l logging.Logger) context.Context {
	return context.WithValue(ctx, logging.LoggerCtx{}, l)
}

type wakeTask struct {
	*InMemoryTask
	done chan<- time.Time
}

func newWakeTask(done chan<- time.Time) *wakeTask {
	return &wakeTask{
		InMemoryTask: &InMemoryTask{
			DBTask: &DBTask{
				Task: &ent.Task{
					Type:   RemoteDownloadTaskType,
					Status: task.StatusQueued,
					PublicState: &types.TaskPublicState{
						ResumeTime: 0,
					},
				},
				DirectOwner: &ent.User{},
			},
		},
		done: done,
	}
}

func (t *wakeTask) Do(ctx context.Context) (task.Status, error) {
	select {
	case t.done <- time.Now():
	default:
	}
	return task.StatusCompleted, nil
}

func (t *wakeTask) CorrelationID() uuid.UUID {
	return uuid.Nil
}

func (t *wakeTask) Summarize(hasher hashid.Encoder) *Summary {
	return nil
}

func TestQueueTaskWakesSleepingPuller(t *testing.T) {
	l := logging.NewConsoleLogger(logging.LevelError)
	q := New(
		l,
		nil,
		nil,
		testQueueDep{},
		WithName("wake-test"),
		WithWorkerCount(1),
		WithTaskPullInterval(10*time.Second),
	).(*queue)

	q.Start()
	defer q.Shutdown()

	time.Sleep(150 * time.Millisecond)

	done := make(chan time.Time, 1)
	start := time.Now()
	if err := q.QueueTask(context.Background(), newWakeTask(done)); err != nil {
		t.Fatalf("failed to queue task: %v", err)
	}

	select {
	case finishedAt := <-done:
		if delay := finishedAt.Sub(start); delay > time.Second {
			t.Fatalf("queued task was not woken up immediately, delay=%s", delay)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued task did not start in time")
	}
}

func TestQueueNewContextSkipsDBDebugLogging(t *testing.T) {
	l := logging.NewConsoleLogger(logging.LevelError)
	q := New(
		l,
		nil,
		nil,
		testQueueDep{},
		WithName("ctx-test"),
	).(*queue)

	ctx := q.newContext(newWakeTask(make(chan time.Time, 1)))
	skip, ok := ctx.Value(inventorydebug.SkipDbLogging{}).(bool)
	if !ok || !skip {
		t.Fatal("queue task context should skip db debug logging")
	}
}
