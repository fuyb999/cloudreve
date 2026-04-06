package inventory

import (
	"context"
	"fmt"
	"strconv"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/auditlog"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
)

type (
	LoadAuditLogUser   struct{}
	LoadAuditLogFile   struct{}
	LoadAuditLogEntity struct{}
	LoadAuditLogShare  struct{}

	CreateAuditLogArgs struct {
		Type          int
		CorrelationID string
		IP            string
		Content       map[string]any
		UserID        int
		FileID        int
		EntityID      int
		ShareID       int
	}

	ListAuditLogArgs struct {
		*PaginationArgs
		Types         []int
		UserID        int
		FileID        int
		EntityID      int
		ShareID       int
		IP            string
		CorrelationID string
	}

	ListAuditLogResult struct {
		*PaginationResults
		Logs []*ent.AuditLog
	}

	AuditLogClient interface {
		TxOperator
		Create(ctx context.Context, args *CreateAuditLogArgs) (*ent.AuditLog, error)
		List(ctx context.Context, args *ListAuditLogArgs) (*ListAuditLogResult, error)
	}
)

func NewAuditLogClient(client *ent.Client, dbType conf.DBType) AuditLogClient {
	return &auditLogClient{
		client:      client,
		maxSQlParam: sqlParamLimit(dbType),
	}
}

type auditLogClient struct {
	client      *ent.Client
	maxSQlParam int
}

func (c *auditLogClient) SetClient(newClient *ent.Client) TxOperator {
	return &auditLogClient{client: newClient, maxSQlParam: c.maxSQlParam}
}

func (c *auditLogClient) GetClient() *ent.Client {
	return c.client
}

func (c *auditLogClient) Create(ctx context.Context, args *CreateAuditLogArgs) (*ent.AuditLog, error) {
	stm := c.client.AuditLog.Create().
		SetType(args.Type)

	if args.CorrelationID != "" {
		stm.SetCorrelationID(args.CorrelationID)
	}
	if args.IP != "" {
		stm.SetIP(args.IP)
	}
	if args.Content != nil {
		stm.SetContent(args.Content)
	}
	if args.UserID != 0 {
		stm.SetUserID(args.UserID)
	}
	if args.FileID > 0 {
		stm.SetFileID(args.FileID)
	}
	if args.EntityID > 0 {
		stm.SetEntityID(args.EntityID)
	}
	if args.ShareID > 0 {
		stm.SetShareID(args.ShareID)
	}

	log, err := stm.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create audit log: %w", err)
	}

	return log, nil
}

func (c *auditLogClient) List(ctx context.Context, args *ListAuditLogArgs) (*ListAuditLogResult, error) {
	query := c.client.AuditLog.Query()

	if len(args.Types) > 0 {
		query.Where(auditlog.TypeIn(args.Types...))
	}
	if args.UserID > 0 {
		query.Where(auditlog.UserID(args.UserID))
	}
	if args.FileID > 0 {
		query.Where(auditlog.FileID(args.FileID))
	}
	if args.EntityID > 0 {
		query.Where(auditlog.EntityID(args.EntityID))
	}
	if args.ShareID > 0 {
		query.Where(auditlog.ShareID(args.ShareID))
	}
	if args.IP != "" {
		query.Where(auditlog.IPContainsFold(args.IP))
	}
	if args.CorrelationID != "" {
		query.Where(auditlog.CorrelationIDEQ(args.CorrelationID))
	}

	query.Order(getAuditLogOrderOption(args)...)

	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, err
	}

	pageSize := capPageSize(c.maxSQlParam, args.PageSize, 1)
	logs, err := withAuditLogEagerLoading(ctx, query).
		Limit(pageSize).
		Offset(args.Page * pageSize).
		All(ctx)
	if err != nil {
		return nil, err
	}

	return &ListAuditLogResult{
		PaginationResults: &PaginationResults{
			TotalItems: total,
			Page:       args.Page,
			PageSize:   pageSize,
		},
		Logs: logs,
	}, nil
}

func getAuditLogOrderOption(args *ListAuditLogArgs) []auditlog.OrderOption {
	orderTerm := getOrderTerm(args.Order)
	switch args.OrderBy {
	case auditlog.FieldType:
		return []auditlog.OrderOption{auditlog.ByType(orderTerm), auditlog.ByID(orderTerm)}
	case auditlog.FieldCreatedAt:
		return []auditlog.OrderOption{auditlog.ByCreatedAt(orderTerm), auditlog.ByID(orderTerm)}
	case auditlog.FieldIP:
		return []auditlog.OrderOption{auditlog.ByIP(orderTerm), auditlog.ByID(orderTerm)}
	default:
		return []auditlog.OrderOption{auditlog.ByID(orderTerm)}
	}
}

func withAuditLogEagerLoading(ctx context.Context, query *ent.AuditLogQuery) *ent.AuditLogQuery {
	if _, ok := ctx.Value(LoadAuditLogUser{}).(bool); ok {
		query.WithUser()
	}
	if _, ok := ctx.Value(LoadAuditLogFile{}).(bool); ok {
		query.WithFile()
	}
	if _, ok := ctx.Value(LoadAuditLogEntity{}).(bool); ok {
		query.WithEntity()
	}
	if _, ok := ctx.Value(LoadAuditLogShare{}).(bool); ok {
		query.WithShare()
	}

	return query
}

func ParseAuditLogTypeList(raw string) ([]int, error) {
	if raw == "" {
		return nil, nil
	}

	res := make([]int, 0, 1)
	for _, item := range splitAndTrim(raw, ",") {
		eventType, err := strconv.Atoi(item)
		if err != nil {
			return nil, err
		}
		res = append(res, eventType)
	}

	return res, nil
}

func splitAndTrim(raw, separator string) []string {
	parts := make([]string, 0)
	start := 0
	for start <= len(raw) {
		next := len(raw)
		for i := start; i < len(raw); i++ {
			if string(raw[i]) == separator {
				next = i
				break
			}
		}

		part := raw[start:next]
		if part != "" {
			parts = append(parts, part)
		}

		if next == len(raw) {
			break
		}
		start = next + len(separator)
	}

	return parts
}
