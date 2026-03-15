package inventory

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/file"
	"github.com/cloudreve/Cloudreve/v4/ent/predicate"
	"github.com/cloudreve/Cloudreve/v4/ent/setting"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
)

const (
	fileExtBackfillBatchSize = 1000
	fileExtBackfillMarker    = DBVersionPrefix + "file_ext_backfill_v1"
)

func fileExtValue(name string, fileType int) string {
	if fileType != int(types.FileTypeFile) {
		return ""
	}

	return util.Ext(name)
}

func normalizeSearchExt(ext string) string {
	ext = strings.TrimSpace(strings.ToLower(ext))
	return strings.TrimPrefix(ext, ".")
}

func parseSimpleWildcardExtPattern(pattern string) (string, string, bool) {
	if !strings.HasPrefix(pattern, "*.") || strings.Count(pattern, SearchWildcard) != 1 {
		return "", "", false
	}

	rawExt := strings.TrimPrefix(pattern, "*.")
	if rawExt == "" || strings.Contains(rawExt, SearchWildcard) ||
		strings.Contains(rawExt, ".") ||
		strings.ContainsAny(rawExt, `/\`) {
		return "", "", false
	}

	normalized := normalizeSearchExt(rawExt)
	if normalized == "" {
		return "", "", false
	}

	return normalized, "." + rawExt, true
}

func legacyNameExtPredicate(pattern string, caseFolding bool) (predicate.File, bool) {
	ext, suffix, ok := parseSimpleWildcardExtPattern(pattern)
	if !ok {
		return nil, false
	}

	if caseFolding {
		return file.FileExtEQ(ext), true
	}

	return file.And(file.FileExtEQ(ext), file.NameHasSuffix(suffix)), true
}

func normalizeSearchExts(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	res := make([]string, 0, len(values))
	for _, item := range values {
		normalized := normalizeSearchExt(item)
		if normalized == "" {
			continue
		}

		if _, ok := seen[normalized]; ok {
			continue
		}

		seen[normalized] = struct{}{}
		res = append(res, normalized)
	}

	return res
}

func ensureFileExtSupport(ctx context.Context, l logging.Logger, client *ent.Client) error {
	exists, err := client.Setting.Query().Where(setting.NameEQ(fileExtBackfillMarker)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("failed to inspect file_ext backfill marker: %w", err)
	}

	if exists {
		return nil
	}

	lastID := 0
	updated := 0

	for {
		files, err := client.File.Query().
			Select(file.FieldID, file.FieldName, file.FieldType, file.FieldFileExt).
			Where(
				file.TypeEQ(int(types.FileTypeFile)),
				file.IDGT(lastID),
			).
			Order(file.ByID()).
			Limit(fileExtBackfillBatchSize).
			All(ctx)
		if err != nil {
			return fmt.Errorf("failed to query file_ext backfill batch: %w", err)
		}

		if len(files) == 0 {
			break
		}

		lastID = files[len(files)-1].ID
		groups := make(map[string][]int)
		for _, item := range files {
			expected := fileExtValue(item.Name, item.Type)
			if item.FileExt == expected {
				continue
			}

			groups[expected] = append(groups[expected], item.ID)
		}

		for ext, ids := range groups {
			if _, err := client.File.Update().
				Where(file.IDIn(ids...)).
				SetFileExt(ext).
				Save(ctx); err != nil {
				return fmt.Errorf("failed to backfill file_ext=%q: %w", ext, err)
			}

			updated += len(ids)
		}
	}

	if updated > 0 {
		l.Info("Backfilled file extensions for %d files.", updated)
	}

	if err := client.Setting.Create().
		SetName(fileExtBackfillMarker).
		SetValue("installed").
		OnConflictColumns(setting.FieldName).
		UpdateNewValues().
		Exec(ctx); err != nil {
		return fmt.Errorf("failed to persist file_ext backfill marker: %w", err)
	}

	return nil
}
