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
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	tikaextractor "github.com/cloudreve/Cloudreve/v4/pkg/searcher/extractor"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

func TestDocumentInspectTaskDispatchesSlaveContentProcessing(t *testing.T) {
	settings := testSettingProvider{
		enabled: true,
		tikaCfg: &setting.FTSTikaExtractorSetting{
			Endpoint:    "http://tika:9998",
			Exts:        []string{"pdf"},
			MaxFileSize: 1024,
		},
	}
	fileClient := &testFileClient{
		fileByID: map[int]*ent.File{
			801: {
				ID:            801,
				OwnerID:       701,
				Name:          "dispatch.pdf",
				Size:          512,
				PrimaryEntity: 901,
				Edges: ent.FileEdges{
					Owner: &ent.User{ID: 701},
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
	node := &testClusterNode{id: 21, isMaster: false, createID: 314}
	dep := testDep{
		settings:      settings,
		config:        testConfigProvider{},
		fileClient:    fileClient,
		policyClient:  &testPolicyClient{policyByID: map[int]*ent.StoragePolicy{9: {ID: 9, Type: inventorytypes.PolicyTypeLocal, Settings: &inventorytypes.PolicySetting{}}}},
		nodePool:      &testNodePool{node: node},
		textExtractor: tikaextractor.NewTikaExtractor(nil, settings, logging.NewConsoleLogger(logging.LevelError), settings.tikaCfg),
	}
	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)

	inspectTask, err := NewDocumentInspectTask(ctx, mustURI(t, "cloudreve:///inspect/dispatch.pdf"), 801, 701, 901, nil)
	if err != nil {
		t.Fatalf("failed to create document inspect task: %v", err)
	}

	status, err := inspectTask.Do(ctx)
	if err != nil {
		t.Fatalf("unexpected document inspect task error: %v", err)
	}
	if status != task.StatusSuspending {
		t.Fatalf("unexpected document inspect status: got %s want %s", status, task.StatusSuspending)
	}
	if node.createdTaskType != queue.SlaveContentProcessingTaskType {
		t.Fatalf("unexpected created task type: got %s want %s", node.createdTaskType, queue.SlaveContentProcessingTaskType)
	}

	state := &DocumentInspectTaskState{}
	if err := json.Unmarshal([]byte(inspectTask.State()), state); err != nil {
		t.Fatalf("failed to parse document inspect state: %v", err)
	}
	if state.Phase != DocumentInspectTaskPhaseAwaitSlave || state.NodeID != 21 || state.SlaveID != 314 {
		t.Fatalf("unexpected dispatch state: %+v", state)
	}

	wrapper, err := parseSlaveContentProcessingState(node.createdState)
	if err != nil {
		t.Fatalf("failed to parse slave state: %v", err)
	}
	if wrapper.Kind != slaveContentProcessingKindDocumentInspect {
		t.Fatalf("unexpected content processing kind: got %s want %s", wrapper.Kind, slaveContentProcessingKindDocumentInspect)
	}

	payload := &SlaveDocumentInspectPayload{}
	if err := json.Unmarshal(wrapper.Payload, payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.FileName != "dispatch.pdf" || payload.FileSize != 512 || payload.Entity == nil || payload.Entity.ID != 901 {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if payload.Policy == nil || payload.Policy.ID != 9 {
		t.Fatalf("unexpected payload policy: %+v", payload.Policy)
	}
}

func TestDocumentInspectTaskAwaitSlaveInspectionAppliesMetadata(t *testing.T) {
	resultRaw, err := json.Marshal(&DocumentInspection{
		EntityID: 901,
		MimeType: "application/pdf",
		Parser:   "org.apache.tika.parser.pdf.PDFParser",
		Language: "zh",
		Title:    "设计文档",
		Author:   "alice",
	})
	if err != nil {
		t.Fatalf("failed to marshal result: %v", err)
	}

	stateRaw, err := json.Marshal(&SlaveContentProcessingTaskState{
		Kind:   slaveContentProcessingKindDocumentInspect,
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
	inspectTask := &DocumentInspectTask{
		DBTask: &queue.DBTask{
			Task: &ent.Task{
				Type:        queue.DocumentInspectTaskType,
				PublicState: &inventorytypes.TaskPublicState{},
			},
		},
	}
	state := &DocumentInspectTaskState{
		Uri:      mustURI(t, "cloudreve:///inspect/design.pdf"),
		FileID:   801,
		OwnerID:  701,
		EntityID: 901,
		Phase:    DocumentInspectTaskPhaseAwaitSlave,
		NodeID:   22,
		SlaveID:  315,
	}

	status, err := inspectTask.awaitSlaveInspection(context.Background(), manager, state)
	if err != nil {
		t.Fatalf("unexpected await slave error: %v", err)
	}
	if status != task.StatusCompleted {
		t.Fatalf("unexpected await slave status: got %s want %s", status, task.StatusCompleted)
	}
	if state.Phase != DocumentInspectTaskPhasePending || state.NodeID != 0 || state.SlaveID != 0 {
		t.Fatalf("expected state reset after apply, got %+v", state)
	}
	if len(node.getTaskCalls) != 2 || node.getTaskCalls[0] || !node.getTaskCalls[1] {
		t.Fatalf("expected slave task to be fetched before clear, got %+v", node.getTaskCalls)
	}
	if len(metaFS.paths) != 1 || metaFS.paths[0].String() != "cloudreve:///inspect/design.pdf" {
		t.Fatalf("unexpected metadata patch target: %+v", metaFS.paths)
	}

	patchMap := map[string]fs.MetadataPatch{}
	for _, patch := range metaFS.patches {
		patchMap[patch.Key] = patch
	}
	if patchMap[dbfs.DocInspectMimeKey].Value != "application/pdf" {
		t.Fatalf("unexpected mime patch: %+v", patchMap[dbfs.DocInspectMimeKey])
	}
	if patchMap[dbfs.DocInspectParserKey].Value != "org.apache.tika.parser.pdf.PDFParser" {
		t.Fatalf("unexpected parser patch: %+v", patchMap[dbfs.DocInspectParserKey])
	}
	if patchMap[dbfs.DocInspectLanguageKey].Value != "zh" {
		t.Fatalf("unexpected language patch: %+v", patchMap[dbfs.DocInspectLanguageKey])
	}
	if patchMap[dbfs.DocInspectTitleKey].Value != "设计文档" {
		t.Fatalf("unexpected title patch: %+v", patchMap[dbfs.DocInspectTitleKey])
	}
	if patchMap[dbfs.DocInspectAuthorKey].Value != "alice" {
		t.Fatalf("unexpected author patch: %+v", patchMap[dbfs.DocInspectAuthorKey])
	}
	if patchMap[dbfs.DocInspectEntityIDKey].Value != "901" {
		t.Fatalf("unexpected entity patch: %+v", patchMap[dbfs.DocInspectEntityIDKey])
	}
}

func TestExecuteSlaveDocumentInspectSkipsWhenFileTooLarge(t *testing.T) {
	settings := testSettingProvider{
		enabled: true,
		tikaCfg: &setting.FTSTikaExtractorSetting{
			Endpoint:    "http://tika:9998",
			Exts:        []string{"pdf"},
			MaxFileSize: 1024,
		},
	}
	dep := testDep{
		settings:      settings,
		textExtractor: tikaextractor.NewTikaExtractor(nil, settings, logging.NewConsoleLogger(logging.LevelError), settings.tikaCfg),
	}
	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)

	result, err := ExecuteSlaveDocumentInspect(ctx, dep, &SlaveDocumentInspectPayload{
		FileName: "sample.bin",
		FileSize: 4096,
		Entity:   &ent.Entity{ID: 902},
	})
	if err != nil {
		t.Fatalf("expected oversized file to short-circuit without error, got %v", err)
	}
	if result == nil || result.EntityID != 902 || result.MimeType != "" || result.Parser != "" {
		t.Fatalf("unexpected document inspect result: %+v", result)
	}
}

func TestDocumentInspectForNewEntityClearsMetadataWhenExtractionSkipped(t *testing.T) {
	settings := testSettingProvider{
		enabled: true,
		tikaCfg: &setting.FTSTikaExtractorSetting{
			Endpoint:    "http://tika:9998",
			Exts:        []string{"pdf"},
			MaxFileSize: 1024,
		},
	}
	metaFS := &testMetadataFS{}
	dep := testDep{
		settings:      settings,
		textExtractor: tikaextractor.NewTikaExtractor(nil, settings, logging.NewConsoleLogger(logging.LevelError), settings.tikaCfg),
	}
	m := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		fs:       metaFS,
		settings: settings,
		dep:      dep,
	}

	entityType := inventorytypes.EntityTypeVersion
	session := &fs.UploadSession{
		FileID:   801,
		EntityID: 901,
		Props: &fs.UploadProps{
			Uri:        mustURI(t, "cloudreve:///inspect/empty.pdf"),
			Size:       0,
			EntityType: &entityType,
		},
	}

	m.documentInspectForNewEntity(context.Background(), session)

	if len(metaFS.paths) != 1 || metaFS.paths[0].String() != "cloudreve:///inspect/empty.pdf" {
		t.Fatalf("unexpected metadata patch target: %+v", metaFS.paths)
	}

	patchMap := map[string]fs.MetadataPatch{}
	for _, patch := range metaFS.patches {
		patchMap[patch.Key] = patch
	}

	for _, key := range []string{
		dbfs.DocInspectMimeKey,
		dbfs.DocInspectParserKey,
		dbfs.DocInspectLanguageKey,
		dbfs.DocInspectTitleKey,
		dbfs.DocInspectAuthorKey,
		dbfs.DocInspectEntityIDKey,
	} {
		patch, ok := patchMap[key]
		if !ok {
			t.Fatalf("expected patch for key %s", key)
		}
		if !patch.Remove {
			t.Fatalf("expected patch for key %s to remove metadata, got %+v", key, patch)
		}
	}
}

func TestDocumentInspectTaskSummarize(t *testing.T) {
	stateRaw, err := json.Marshal(&DocumentInspectTaskState{
		Uri:      mustURI(t, "cloudreve:///inspect/summary.pdf"),
		FileID:   801,
		OwnerID:  701,
		EntityID: 901,
		Phase:    DocumentInspectTaskPhaseAwaitSlave,
		NodeID:   22,
		SlaveID:  315,
	})
	if err != nil {
		t.Fatalf("failed to marshal state: %v", err)
	}

	task := &DocumentInspectTask{
		DBTask: &queue.DBTask{
			Task: &ent.Task{
				Type:         queue.DocumentInspectTaskType,
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
	if summary.Phase != string(DocumentInspectTaskPhaseAwaitSlave) {
		t.Fatalf("unexpected phase: got %s", summary.Phase)
	}
	if summary.Props["src"] != "cloudreve:///inspect/summary.pdf" {
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
