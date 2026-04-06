package manager

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	inventorytypes "github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/cluster"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/driver"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/mediameta"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
)

func TestMediaMetaTaskDispatchesSlaveContentProcessing(t *testing.T) {
	policy := &ent.StoragePolicy{ID: 9, Type: inventorytypes.PolicyTypeLocal, Settings: &inventorytypes.PolicySetting{}}
	fileClient := &testFileClient{
		fileByID: map[int]*ent.File{
			801: {
				ID:            801,
				OwnerID:       701,
				Name:          "dispatch.mp4",
				PrimaryEntity: 901,
				Edges: ent.FileEdges{
					Entities: []*ent.Entity{
						{
							ID:                    901,
							Type:                  int(inventorytypes.EntityTypeVersion),
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
		settings:     testSettingProvider{enabled: false},
		config:       testConfigProvider{},
		fileClient:   fileClient,
		policyClient: &testPolicyClient{policyByID: map[int]*ent.StoragePolicy{9: policy}},
		nodePool:     &testNodePool{node: node},
	}
	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)

	uri := mustURI(t, "cloudreve:///dispatch/dispatch.mp4")
	metaTask, err := NewMediaMetaTask(ctx, uri, 801, 701, 901, nil)
	if err != nil {
		t.Fatalf("failed to create media meta task: %v", err)
	}

	status, err := metaTask.Do(ctx)
	if err != nil {
		t.Fatalf("unexpected media meta task error: %v", err)
	}
	if status != task.StatusSuspending {
		t.Fatalf("unexpected media meta task status: got %s want %s", status, task.StatusSuspending)
	}
	if node.createdTaskType != queue.SlaveContentProcessingTaskType {
		t.Fatalf("unexpected created task type: got %s want %s", node.createdTaskType, queue.SlaveContentProcessingTaskType)
	}

	state := &MediaMetaTaskState{}
	if err := json.Unmarshal([]byte(metaTask.State()), state); err != nil {
		t.Fatalf("failed to parse media meta state: %v", err)
	}
	if state.Phase != MediaMetaTaskPhaseAwaitSlave || state.NodeID != 21 || state.SlaveID != 314 {
		t.Fatalf("unexpected slave dispatch state: %+v", state)
	}

	wrapper, err := parseSlaveContentProcessingState(node.createdState)
	if err != nil {
		t.Fatalf("failed to parse created slave state: %v", err)
	}
	if wrapper.Kind != slaveContentProcessingKindMediaMetaExtract {
		t.Fatalf("unexpected slave content processing kind: got %s want %s", wrapper.Kind, slaveContentProcessingKindMediaMetaExtract)
	}

	payload := &SlaveMediaMetaExtractPayload{}
	if err := json.Unmarshal(wrapper.Payload, payload); err != nil {
		t.Fatalf("failed to parse slave media meta payload: %v", err)
	}
	if payload.FileName != "dispatch.mp4" || payload.FileExt != "mp4" || payload.Entity == nil || payload.Entity.ID != 901 {
		t.Fatalf("unexpected slave media meta payload: %+v", payload)
	}
	if payload.Policy == nil || payload.Policy.ID != 9 {
		t.Fatalf("unexpected slave media meta policy: %+v", payload.Policy)
	}
}

func TestMediaMetaTaskAwaitSlaveExtractionAppliesMetadata(t *testing.T) {
	resultRaw, err := json.Marshal(&SlaveMediaMetaExtractResult{
		EntityID: 901,
		Metas: []driver.MediaMeta{
			{Type: "video", Key: "duration", Value: "123"},
			{Type: "video", Key: "codec", Value: "h264"},
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal result: %v", err)
	}

	stateRaw, err := json.Marshal(&SlaveContentProcessingTaskState{
		Kind:   slaveContentProcessingKindMediaMetaExtract,
		Result: resultRaw,
	})
	if err != nil {
		t.Fatalf("failed to marshal wrapper: %v", err)
	}

	node := &testClusterNode{
		id:       22,
		isMaster: false,
		slaveTask: &cluster.SlaveTaskSummary{
			Status:       task.StatusCompleted,
			PrivateState: string(stateRaw),
		},
	}
	metaFS := &testMetadataFS{}
	manager := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		fs:       metaFS,
		settings: testSettingProvider{enabled: false},
		dep: testDep{
			settings: testSettingProvider{enabled: false},
			nodePool: &testNodePool{node: node},
		},
	}
	metaTask := &MediaMetaTask{
		DBTask: &queue.DBTask{
			Task: &ent.Task{
				Type:        queue.MediaMetaTaskType,
				PublicState: &inventorytypes.TaskPublicState{},
			},
		},
	}
	state := &MediaMetaTaskState{
		Uri:      mustURI(t, "cloudreve:///media/video.mp4"),
		FileID:   801,
		OwnerID:  701,
		EntityID: 901,
		Phase:    MediaMetaTaskPhaseAwaitSlave,
		NodeID:   22,
		SlaveID:  315,
	}

	status, err := metaTask.awaitSlaveExtraction(context.Background(), manager, state)
	if err != nil {
		t.Fatalf("unexpected await slave error: %v", err)
	}
	if status != task.StatusCompleted {
		t.Fatalf("unexpected await slave status: got %s want %s", status, task.StatusCompleted)
	}
	if state.Phase != MediaMetaTaskPhasePending || state.NodeID != 0 || state.SlaveID != 0 {
		t.Fatalf("expected state to be reset after apply, got %+v", state)
	}
	if node.getTaskID != 315 || !node.clearCalled {
		t.Fatalf("unexpected node getTask call: id=%d clear=%v", node.getTaskID, node.clearCalled)
	}
	if len(node.getTaskCalls) != 2 || node.getTaskCalls[0] || !node.getTaskCalls[1] {
		t.Fatalf("expected slave task to be fetched before clear, got %+v", node.getTaskCalls)
	}
	if len(metaFS.paths) != 1 || metaFS.paths[0].String() != "cloudreve:///media/video.mp4" {
		t.Fatalf("unexpected metadata patch target: %+v", metaFS.paths)
	}
	if len(metaFS.patches) != 2 {
		t.Fatalf("unexpected metadata patch count: %+v", metaFS.patches)
	}
	if metaFS.patches[0].Key != "video:duration" || metaFS.patches[0].Value != "123" {
		t.Fatalf("unexpected first metadata patch: %+v", metaFS.patches[0])
	}
	if metaFS.patches[1].Key != "video:codec" || metaFS.patches[1].Value != "h264" {
		t.Fatalf("unexpected second metadata patch: %+v", metaFS.patches[1])
	}
}

func TestExecuteSlaveMediaMetaExtractSkipsUnsupportedExt(t *testing.T) {
	dep := testDep{
		settings:     testSettingProvider{enabled: false},
		config:       testConfigProvider{},
		mediaMetaExt: mediameta.NewExtractorManager(context.Background(), testSettingProvider{enabled: false}, logging.NewConsoleLogger(logging.LevelError), nil),
	}
	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)

	result, err := ExecuteSlaveMediaMetaExtract(ctx, dep, &SlaveMediaMetaExtractPayload{
		FileName: "sample.bin",
		FileExt:  "bin",
		Entity:   &ent.Entity{ID: 902},
		Policy:   &ent.StoragePolicy{Type: inventorytypes.PolicyTypeLocal, Settings: &inventorytypes.PolicySetting{}},
	})
	if err != nil {
		t.Fatalf("expected unsupported ext to short-circuit without error, got %v", err)
	}
	if result == nil || result.EntityID != 902 || len(result.Metas) != 0 {
		t.Fatalf("unexpected slave media meta result: %+v", result)
	}
}

func TestMediaMetaTaskSummarize(t *testing.T) {
	stateRaw, err := json.Marshal(&MediaMetaTaskState{
		Uri:      mustURI(t, "cloudreve:///media/summary.mp4"),
		FileID:   801,
		OwnerID:  701,
		EntityID: 901,
		Phase:    MediaMetaTaskPhaseAwaitSlave,
		NodeID:   22,
		SlaveID:  315,
	})
	if err != nil {
		t.Fatalf("failed to marshal state: %v", err)
	}

	task := &MediaMetaTask{
		DBTask: &queue.DBTask{
			Task: &ent.Task{
				Type:         queue.MediaMetaTaskType,
				PrivateState: string(stateRaw),
			},
		},
	}

	summary := task.Summarize(nil)
	if summary == nil {
		t.Fatal("expected summary")
	}
	if summary.NodeID != 22 {
		t.Fatalf("unexpected node id: got %d want %d", summary.NodeID, 22)
	}
	if summary.Phase != string(MediaMetaTaskPhaseAwaitSlave) {
		t.Fatalf("unexpected phase: got %s", summary.Phase)
	}
	if summary.Props["src"] != "cloudreve:///media/summary.mp4" {
		t.Fatalf("unexpected src: %+v", summary.Props["src"])
	}
	if summary.Props["file_id"] != 801 {
		t.Fatalf("unexpected file id: %+v", summary.Props["file_id"])
	}
	if summary.Props["owner_id"] != 701 {
		t.Fatalf("unexpected owner id: %+v", summary.Props["owner_id"])
	}
	if summary.Props["entity_id"] != 901 {
		t.Fatalf("unexpected entity id: %+v", summary.Props["entity_id"])
	}
}
