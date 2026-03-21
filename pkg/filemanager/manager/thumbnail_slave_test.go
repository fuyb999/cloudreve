package manager

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	inventorytypes "github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/cluster"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/thumb"
)

func TestManagerSubmitAndAwaitSlaveThumbnailTaskDisablesUnavailableThumb(t *testing.T) {
	resultRaw, err := json.Marshal(&SlaveThumbnailGenerateResult{
		FileID:       801,
		EntityID:     901,
		NotAvailable: true,
	})
	if err != nil {
		t.Fatalf("failed to marshal result: %v", err)
	}

	stateRaw, err := json.Marshal(&SlaveContentProcessingTaskState{
		Kind:   slaveContentProcessingKindThumbnailGenerate,
		Result: resultRaw,
	})
	if err != nil {
		t.Fatalf("failed to marshal wrapper: %v", err)
	}

	node := &testClusterNode{
		id:       31,
		isMaster: false,
		createID: 401,
		slaveTask: &cluster.SlaveTaskSummary{
			Status:       task.StatusCompleted,
			PrivateState: string(stateRaw),
		},
	}
	metaFS := &testMetadataFS{}
	policy := &ent.StoragePolicy{ID: 9, Type: inventorytypes.PolicyTypeLocal, Settings: &inventorytypes.PolicySetting{}}
	manager := &manager{
		l:        logging.NewConsoleLogger(logging.LevelError),
		fs:       metaFS,
		settings: testSettingProvider{enabled: false},
		dep: testDep{
			settings:     testSettingProvider{enabled: false},
			config:       testConfigProvider{},
			nodePool:     &testNodePool{node: node},
			policyClient: &testPolicyClient{policyByID: map[int]*ent.StoragePolicy{9: policy}},
		},
	}
	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, manager.dep)

	uri := mustURI(t, "cloudreve:///thumb/source.png")
	entity := fs.NewEntity(&ent.Entity{
		ID:                    901,
		Type:                  int(inventorytypes.EntityTypeVersion),
		StoragePolicyEntities: 9,
	})
	thumbEntity, err := manager.submitAndAwaitSlaveThumbnailTask(ctx, uri, "png", 801, 701, entity)
	if !errors.Is(err, thumb.ErrNotAvailable) {
		t.Fatalf("expected thumb not available error, got entity=%v err=%v", thumbEntity, err)
	}
	if thumbEntity != nil {
		t.Fatalf("expected no thumbnail entity, got %+v", thumbEntity)
	}

	wrapper, err := parseSlaveContentProcessingState(node.createdState)
	if err != nil {
		t.Fatalf("failed to parse created state: %v", err)
	}
	if wrapper.Kind != slaveContentProcessingKindThumbnailGenerate {
		t.Fatalf("unexpected content processing kind: got %s want %s", wrapper.Kind, slaveContentProcessingKindThumbnailGenerate)
	}

	payload := &SlaveThumbnailGeneratePayload{}
	if err := json.Unmarshal(wrapper.Payload, payload); err != nil {
		t.Fatalf("failed to unmarshal thumbnail payload: %v", err)
	}
	if payload.FileID != 801 || payload.OwnerID != 701 || payload.Ext != "png" || payload.URI != uri.String() {
		t.Fatalf("unexpected thumbnail payload: %+v", payload)
	}
	if payload.Entity == nil || payload.Entity.ID != 901 {
		t.Fatalf("unexpected thumbnail payload entity: %+v", payload.Entity)
	}
	if payload.Policy == nil || payload.Policy.ID != 9 {
		t.Fatalf("unexpected thumbnail payload policy: %+v", payload.Policy)
	}

	if len(metaFS.paths) != 1 || metaFS.paths[0].String() != uri.String() {
		t.Fatalf("unexpected thumb disabled path: %+v", metaFS.paths)
	}
	if len(metaFS.patches) != 1 || metaFS.patches[0].Key != dbfs.ThumbDisabledKey {
		t.Fatalf("unexpected thumb disabled patch: %+v", metaFS.patches)
	}
}
