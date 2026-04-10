package inventory

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/setting"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/samber/lo"
)

const archiveViewerExtBackfillMarker = DBVersionPrefix + "archive_viewer_ext_backfill_v1"

func ensureArchiveViewerExtSupport(ctx context.Context, l logging.Logger, client *ent.Client) error {
	exists, err := client.Setting.Query().Where(setting.NameEQ(archiveViewerExtBackfillMarker)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("failed to inspect archive viewer ext backfill marker: %w", err)
	}

	if exists {
		return nil
	}

	fileViewersSetting, err := client.Setting.Query().Where(setting.NameEQ("file_viewers")).Only(ctx)
	if err != nil {
		return fmt.Errorf("failed to query file_viewers setting: %w", err)
	}

	var fileViewers []types.ViewerGroup
	if err := json.Unmarshal([]byte(fileViewersSetting.Value), &fileViewers); err != nil {
		return fmt.Errorf("failed to unmarshal file_viewers setting: %w", err)
	}

	updated := false
	for groupIndex := range fileViewers {
		for viewerIndex := range fileViewers[groupIndex].Viewers {
			viewer := &fileViewers[groupIndex].Viewers[viewerIndex]
			if viewer.ID != "archive" {
				continue
			}

			mergedExts := lo.Uniq(append(append([]string{}, viewer.Exts...), defaultArchiveViewerExts...))
			if len(mergedExts) == len(viewer.Exts) {
				continue
			}

			viewer.Exts = mergedExts
			updated = true
		}
	}

	if updated {
		raw, err := json.Marshal(fileViewers)
		if err != nil {
			return fmt.Errorf("failed to marshal updated file_viewers setting: %w", err)
		}

		if _, err := client.Setting.UpdateOne(fileViewersSetting).SetValue(string(raw)).Save(ctx); err != nil {
			return fmt.Errorf("failed to update file_viewers setting: %w", err)
		}

		l.Info("Expanded archive viewer extensions to %d items.", len(defaultArchiveViewerExts))
	}

	if err := client.Setting.Create().
		SetName(archiveViewerExtBackfillMarker).
		SetValue("installed").
		OnConflictColumns(setting.FieldName).
		UpdateNewValues().
		Exec(ctx); err != nil {
		return fmt.Errorf("failed to persist archive viewer ext backfill marker: %w", err)
	}

	return nil
}
