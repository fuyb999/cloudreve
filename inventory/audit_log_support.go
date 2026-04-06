package inventory

import (
	"context"
	"fmt"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/auditlog"
	"github.com/cloudreve/Cloudreve/v4/ent/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

const auditLogSystemUserBackfillMarker = DBVersionPrefix + "audit_log_system_user_backfill_v1"

func ensureAuditLogSystemUserSupport(ctx context.Context, l logging.Logger, client *ent.Client) error {
	exists, err := client.Setting.Query().Where(setting.NameEQ(auditLogSystemUserBackfillMarker)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("failed to inspect audit log system user backfill marker: %w", err)
	}

	if exists {
		return nil
	}

	updated, err := client.AuditLog.Update().
		Where(
			auditlog.TypeEQ(0),
			auditlog.Or(auditlog.UserIDIsNil(), auditlog.UserIDEQ(0)),
		).
		SetUserID(constants.PublicSystemOwnerID).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to backfill audit log system user ids: %w", err)
	}

	if updated > 0 {
		l.Info("Backfilled internal system user id for %d startup audit logs.", updated)
	}

	if err := client.Setting.Create().
		SetName(auditLogSystemUserBackfillMarker).
		SetValue("installed").
		OnConflictColumns(setting.FieldName).
		UpdateNewValues().
		Exec(ctx); err != nil {
		return fmt.Errorf("failed to persist audit log system user backfill marker: %w", err)
	}

	return nil
}
