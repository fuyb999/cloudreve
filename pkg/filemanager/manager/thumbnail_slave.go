package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager/entitysource"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/thumb"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
)

const slaveContentProcessingKindThumbnailGenerate = "thumbnail_generate"

type SlaveThumbnailGeneratePayload struct {
	FileID  int                `json:"file_id"`
	OwnerID int                `json:"owner_id"`
	Ext     string             `json:"ext"`
	URI     string             `json:"uri,omitempty"`
	Entity  *ent.Entity        `json:"entity"`
	Policy  *ent.StoragePolicy `json:"policy"`
}

type SlaveThumbnailGenerateResult struct {
	FileID       int    `json:"file_id"`
	EntityID     int    `json:"entity_id"`
	SavePath     string `json:"save_path,omitempty"`
	Size         int64  `json:"size,omitempty"`
	NotAvailable bool   `json:"not_available,omitempty"`
}

func (m *manager) buildSlaveThumbnailGeneratePayload(ctx context.Context, uri *fs.URI, ext string, fileID, ownerID int, entity fs.Entity) (*SlaveThumbnailGeneratePayload, error) {
	if uri == nil || entity == nil {
		return nil, fmt.Errorf("invalid thumbnail slave payload source")
	}

	entityModel := entity.Model()
	if entityModel == nil {
		return nil, fmt.Errorf("invalid thumbnail source entity model")
	}

	decodedEntity, err := decryptFTSEntityKeyIfNeeded(ctx, m.dep, entityModel)
	if err != nil {
		return nil, err
	}

	policy, err := m.storagePolicyFromID(ctx, entity.PolicyID())
	if err != nil {
		return nil, fmt.Errorf("failed to resolve thumbnail storage policy: %w", err)
	}

	return &SlaveThumbnailGeneratePayload{
		FileID:  fileID,
		OwnerID: ownerID,
		Ext:     ext,
		URI:     uri.String(),
		Entity:  decodedEntity,
		Policy:  policy,
	}, nil
}

func (m *manager) submitAndAwaitSlaveThumbnailTask(ctx context.Context, uri *fs.URI, ext string, fileID, ownerID int, entity fs.Entity) (fs.Entity, error) {
	node, err := allocateContentProcessingNode(ctx, m.dep, 0)
	if err != nil {
		return nil, err
	}
	if node.IsMaster() {
		return nil, fmt.Errorf("content processing node resolved to master")
	}

	payload, err := m.buildSlaveThumbnailGeneratePayload(ctx, uri, ext, fileID, ownerID, entity)
	if err != nil {
		return nil, err
	}

	stateRaw, err := marshalSlaveContentProcessingState(slaveContentProcessingKindThumbnailGenerate, payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal slave thumbnail payload: %w", err)
	}

	taskID, err := node.CreateTask(ctx, queue.SlaveContentProcessingTaskType, stateRaw)
	if err != nil {
		return nil, fmt.Errorf("failed to create slave thumbnail task: %w", err)
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		summary, err := node.GetTask(ctx, taskID, true)
		if err != nil {
			return nil, fmt.Errorf("failed to get slave thumbnail task: %w", err)
		}
		if summary == nil {
			return nil, fmt.Errorf("slave thumbnail task %d not found", taskID)
		}

		switch summary.Status {
		case task.StatusCompleted:
			wrapper, err := parseSlaveContentProcessingState(summary.PrivateState)
			if err != nil {
				return nil, fmt.Errorf("failed to parse slave thumbnail result: %w", err)
			}

			result := &SlaveThumbnailGenerateResult{}
			if len(wrapper.Result) > 0 {
				if err := json.Unmarshal(wrapper.Result, result); err != nil {
					return nil, fmt.Errorf("failed to unmarshal slave thumbnail result: %w", err)
				}
			}

			return m.applySlaveThumbnailResult(ctx, uri, fileID, entity, result)
		case task.StatusError:
			return nil, fmt.Errorf("slave thumbnail task failed: %s%s (%w)", summary.Error, slaveTaskDiagnostic(summary), queue.CriticalErr)
		case task.StatusCanceled:
			return nil, fmt.Errorf("slave thumbnail task canceled%s (%w)", slaveTaskDiagnostic(summary), queue.CriticalErr)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func ExecuteSlaveThumbnailGenerate(ctx context.Context, dep dependency.Dep, payload *SlaveThumbnailGeneratePayload) (*SlaveThumbnailGenerateResult, error) {
	if payload == nil || payload.Entity == nil {
		return nil, fmt.Errorf("invalid slave thumbnail payload")
	}

	fm := NewFileManager(dep, nil)
	defer fm.Recycle()

	internal, ok := fm.(*manager)
	if !ok {
		return nil, fmt.Errorf("failed to construct stateless file manager")
	}

	entity := fs.NewEntity(payload.Entity)
	policy := internal.CastStoragePolicyOnSlave(ctx, payload.Policy)
	source, err := internal.GetEntitySource(ctx, 0, fs.WithEntity(entity), fs.WithPolicy(policy))
	if err != nil {
		return nil, fmt.Errorf("failed to get thumbnail entity source: %w", err)
	}
	defer source.Close()

	res, err := internal.persistSlaveThumbnail(ctx, payload, source)
	if err != nil {
		if errors.Is(err, thumb.ErrNotAvailable) {
			return &SlaveThumbnailGenerateResult{
				FileID:       payload.FileID,
				EntityID:     payload.Entity.ID,
				NotAvailable: true,
			}, nil
		}

		return nil, err
	}

	return res, nil
}

func (m *manager) persistSlaveThumbnail(ctx context.Context, payload *SlaveThumbnailGeneratePayload, source entitysource.EntitySource) (*SlaveThumbnailGenerateResult, error) {
	res, err := m.dep.ThumbPipeline().Generate(ctx, source, payload.Ext, nil)
	if err != nil {
		if res != nil && res.Path != "" {
			_ = os.Remove(res.Path)
		}
		return nil, fmt.Errorf("failed to generate slave thumbnail: %w", err)
	}
	if res == nil || res.Path == "" {
		return nil, fmt.Errorf("slave thumbnail generator returned empty result")
	}
	defer os.Remove(res.Path)

	thumbFile, err := os.Open(res.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to open temp thumb %q: %w", res.Path, err)
	}
	defer thumbFile.Close()

	info, err := thumbFile.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat temp thumb %q: %w", res.Path, err)
	}

	targetURI, err := fs.NewUriFromString(payload.URI)
	if err != nil {
		return nil, fmt.Errorf("failed to parse thumbnail uri: %w", err)
	}

	savePath := path.Clean(util.ReplaceMagicVar(
		m.settings.ThumbEntitySuffix(ctx),
		fs.Separator,
		true,
		true,
		time.Now(),
		payload.OwnerID,
		targetURI.Name(),
		targetURI.Path(),
		source.Entity().Source(),
	))

	d, err := m.GetStorageDriver(ctx, m.CastStoragePolicyOnSlave(ctx, payload.Policy))
	if err != nil {
		return nil, fmt.Errorf("failed to get thumbnail storage driver: %w", err)
	}

	if err := d.Put(ctx, &fs.UploadRequest{
		File:   thumbFile,
		Seeker: thumbFile,
		Props: &fs.UploadProps{
			SavePath: savePath,
			Size:     info.Size(),
		},
	}); err != nil {
		return nil, fmt.Errorf("failed to save slave thumbnail: %w", err)
	}

	return &SlaveThumbnailGenerateResult{
		FileID:   payload.FileID,
		EntityID: payload.Entity.ID,
		SavePath: savePath,
		Size:     info.Size(),
	}, nil
}

func (m *manager) applySlaveThumbnailResult(ctx context.Context, uri *fs.URI, fileID int, entity fs.Entity, result *SlaveThumbnailGenerateResult) (fs.Entity, error) {
	if result == nil {
		return nil, fmt.Errorf("missing slave thumbnail result")
	}
	if result.NotAvailable {
		if uri != nil {
			if err := disableThumb(ctx, m, uri); err != nil {
				m.l.Warning("Failed to disable thumb after slave generation miss: %v", err)
			}
		}
		return nil, thumb.ErrNotAvailable
	}
	if result.SavePath == "" || result.Size <= 0 {
		return nil, fmt.Errorf("invalid slave thumbnail result")
	}

	ctx = context.WithValue(ctx, inventory.LoadFileUser{}, true)
	fileModel, err := m.dep.FileClient().GetByID(ctx, fileID)
	if err != nil {
		return nil, fmt.Errorf("failed to load file for thumbnail finalize: %w", err)
	}
	if fileModel.PrimaryEntity != entity.ID() {
		return nil, fs.ErrEntityNotExist
	}
	if fileModel.Edges.Owner == nil {
		return nil, fmt.Errorf("failed to resolve thumbnail owner")
	}

	fc, tx, txCtx, err := inventory.WithTx(ctx, m.dep.FileClient())
	if err != nil {
		return nil, fmt.Errorf("failed to start thumbnail finalize transaction: %w", err)
	}

	fileModel, err = fc.GetByID(txCtx, fileID)
	if err != nil {
		_ = inventory.Rollback(tx)
		return nil, fmt.Errorf("failed to reload file for thumbnail finalize: %w", err)
	}

	diff, err := fc.CapEntities(txCtx, fileModel, fileModel.Edges.Owner, 0, types.EntityTypeThumbnail)
	if err != nil {
		_ = inventory.Rollback(tx)
		return nil, fmt.Errorf("failed to cap thumbnail entities: %w", err)
	}
	tx.AppendStorageDiff(diff)

	if err := fc.RemoveMetadata(txCtx, fileModel, dbfs.ThumbDisabledKey); err != nil {
		_ = inventory.Rollback(tx)
		return nil, fmt.Errorf("failed to clear thumbnail disabled metadata: %w", err)
	}

	entityModel, diff, err := fc.CreateEntity(txCtx, fileModel, &inventory.EntityParameters{
		OwnerID:         fileModel.OwnerID,
		EntityType:      types.EntityTypeThumbnail,
		StoragePolicyID: entity.PolicyID(),
		Source:          result.SavePath,
		Size:            result.Size,
	})
	if err != nil {
		_ = inventory.Rollback(tx)
		return nil, fmt.Errorf("failed to create thumbnail entity: %w", err)
	}
	tx.AppendStorageDiff(diff)

	if uc := m.dep.UserClient(); uc != nil {
		if err := inventory.CommitWithStorageDiff(txCtx, tx, m.l, uc); err != nil {
			return nil, fmt.Errorf("failed to commit thumbnail finalize: %w", err)
		}
	} else if err := inventory.Commit(tx); err != nil {
		return nil, fmt.Errorf("failed to commit thumbnail finalize: %w", err)
	}

	return fs.NewEntity(entityModel), nil
}
