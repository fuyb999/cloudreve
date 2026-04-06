package admin

import (
	"context"
	"fmt"
	"strconv"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/auditlog"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

const (
	auditLogTypeCondition          = "audit_log_type"
	auditLogUserIDCondition        = "audit_log_user_id"
	auditLogFileIDCondition        = "audit_log_file_id"
	auditLogEntityIDCondition      = "audit_log_entity_id"
	auditLogShareIDCondition       = "audit_log_share_id"
	auditLogIPCondition            = "audit_log_ip"
	auditLogCorrelationIDCondition = "audit_log_correlation_id"
)

func (s *AdminListService) AuditLogs(c *gin.Context) (*ListAuditLogResponse, error) {
	dep := dependency.FromContext(c)

	types, err := inventory.ParseAuditLogTypeList(s.Conditions[auditLogTypeCondition])
	if err != nil {
		return nil, serializer.NewError(serializer.CodeParamErr, "Invalid audit log type", err)
	}

	userID, err := parseOptionalPositiveInt(s.Conditions[auditLogUserIDCondition])
	if err != nil {
		return nil, serializer.NewError(serializer.CodeParamErr, "Invalid audit log user ID", err)
	}

	fileID, err := parseOptionalPositiveInt(s.Conditions[auditLogFileIDCondition])
	if err != nil {
		return nil, serializer.NewError(serializer.CodeParamErr, "Invalid audit log file ID", err)
	}

	entityID, err := parseOptionalPositiveInt(s.Conditions[auditLogEntityIDCondition])
	if err != nil {
		return nil, serializer.NewError(serializer.CodeParamErr, "Invalid audit log entity ID", err)
	}

	shareID, err := parseOptionalPositiveInt(s.Conditions[auditLogShareIDCondition])
	if err != nil {
		return nil, serializer.NewError(serializer.CodeParamErr, "Invalid audit log share ID", err)
	}

	ctx := context.WithValue(c, inventory.LoadAuditLogUser{}, true)
	ctx = context.WithValue(ctx, inventory.LoadAuditLogFile{}, true)
	ctx = context.WithValue(ctx, inventory.LoadAuditLogEntity{}, true)
	ctx = context.WithValue(ctx, inventory.LoadAuditLogShare{}, true)
	res, err := dep.AuditLogClient().List(ctx, &inventory.ListAuditLogArgs{
		PaginationArgs: &inventory.PaginationArgs{
			Page:     s.Page - 1,
			PageSize: s.PageSize,
			OrderBy:  s.normalizedAuditLogOrderBy(),
			Order:    inventory.OrderDirection(s.OrderDirection),
		},
		Types:         types,
		UserID:        userID,
		FileID:        fileID,
		EntityID:      entityID,
		ShareID:       shareID,
		IP:            s.Conditions[auditLogIPCondition],
		CorrelationID: s.Conditions[auditLogCorrelationIDCondition],
	})
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to list audit logs", err)
	}

	hasher := dep.HashIDEncoder()
	return &ListAuditLogResponse{
		Pagination: res.PaginationResults,
		Logs: lo.Map(res.Logs, func(item *ent.AuditLog, _ int) GetAuditLogResponse {
			return buildAuditLogResponse(item, hasher)
		}),
	}, nil
}

func (s *AdminListService) normalizedAuditLogOrderBy() string {
	switch s.OrderBy {
	case "", "id":
		return ""
	case auditlog.FieldType, auditlog.FieldCreatedAt, auditlog.FieldIP:
		return s.OrderBy
	default:
		return ""
	}
}

func buildAuditLogResponse(item *ent.AuditLog, hasher hashid.Encoder) GetAuditLogResponse {
	if item == nil {
		return GetAuditLogResponse{}
	}

	res := GetAuditLogResponse{
		AuditLogPayload: AuditLogPayload{
			ID:            item.ID,
			CreatedAt:     item.CreatedAt,
			UpdatedAt:     item.UpdatedAt,
			DeletedAt:     item.DeletedAt,
			Type:          item.Type,
			CorrelationID: item.CorrelationID,
			IP:            item.IP,
			Content:       item.Content,
			UserID:        item.UserID,
			FileID:        item.FileID,
			EntityID:      item.EntityID,
			ShareID:       item.ShareID,
			Edges:         item.Edges,
		},
	}
	if item.Edges.User != nil {
		if inventory.IsInternalSystemUser(item.Edges.User) {
			res.AuditLogPayload.Edges.User = nil
			return res
		}
		res.UserHashID = hashid.EncodeUserID(hasher, item.Edges.User.ID)
	}

	return res
}

func parseOptionalPositiveInt(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}

	res, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if res <= 0 {
		return 0, fmt.Errorf("value must be positive")
	}

	return res, nil
}
