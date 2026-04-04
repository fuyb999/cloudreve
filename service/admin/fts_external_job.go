package admin

import (
	"strings"

	"entgo.io/ent/dialect/sql"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/ftsexternaljob"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/gin-gonic/gin"
)

const (
	ftsExternalJobStatusCondition        = "fts_external_job_status"
	ftsExternalJobFileIDCondition        = "fts_external_job_file_id"
	ftsExternalJobOwnerIDCondition       = "fts_external_job_owner_id"
	ftsExternalJobEntityIDCondition      = "fts_external_job_entity_id"
	ftsExternalJobRequestIDCondition     = "fts_external_job_request_id"
	ftsExternalJobModeCondition          = "fts_external_job_mode"
	ftsExternalJobTriggerReasonCondition = "fts_external_job_trigger_reason"
)

type (
	SingleFTSExternalJobService struct {
		ID int `uri:"id" binding:"required"`
	}
	SingleFTSExternalJobParamCtx struct{}
)

func (s *AdminListService) FTSExternalJobs(c *gin.Context) (*ListFTSExternalJobResponse, error) {
	dep := dependency.FromContext(c)
	query := dep.DBClient().FTSExternalJob.Query()

	if status := strings.TrimSpace(s.Conditions[ftsExternalJobStatusCondition]); status != "" {
		query.Where(ftsexternaljob.StatusEQ(status))
	}

	fileID, err := parseOptionalPositiveInt(strings.TrimSpace(s.Conditions[ftsExternalJobFileIDCondition]))
	if err != nil {
		return nil, serializer.NewError(serializer.CodeParamErr, "Invalid external FTS file ID", err)
	}
	if fileID > 0 {
		query.Where(ftsexternaljob.FileIDEQ(fileID))
	}

	ownerID, err := parseOptionalPositiveInt(strings.TrimSpace(s.Conditions[ftsExternalJobOwnerIDCondition]))
	if err != nil {
		return nil, serializer.NewError(serializer.CodeParamErr, "Invalid external FTS owner ID", err)
	}
	if ownerID > 0 {
		query.Where(ftsexternaljob.OwnerIDEQ(ownerID))
	}

	entityID, err := parseOptionalPositiveInt(strings.TrimSpace(s.Conditions[ftsExternalJobEntityIDCondition]))
	if err != nil {
		return nil, serializer.NewError(serializer.CodeParamErr, "Invalid external FTS entity ID", err)
	}
	if entityID > 0 {
		query.Where(ftsexternaljob.EntityIDEQ(entityID))
	}

	if requestID := strings.TrimSpace(s.Conditions[ftsExternalJobRequestIDCondition]); requestID != "" {
		query.Where(ftsexternaljob.RequestIDContainsFold(requestID))
	}

	if mode := strings.TrimSpace(s.Conditions[ftsExternalJobModeCondition]); mode != "" {
		query.Where(ftsexternaljob.ModeEQ(mode))
	}

	if triggerReason := strings.TrimSpace(s.Conditions[ftsExternalJobTriggerReasonCondition]); triggerReason != "" {
		query.Where(ftsexternaljob.TriggerReasonContainsFold(triggerReason))
	}

	pageSize := s.PageSize
	if pageSize <= 0 {
		pageSize = 10
	}

	page := s.Page
	if page <= 0 {
		page = 1
	}

	total, err := query.Clone().Count(c)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to count external FTS jobs", err)
	}

	jobs, err := query.
		Order(getFTSExternalJobOrderOption(s)...).
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		All(c)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to list external FTS jobs", err)
	}

	return &ListFTSExternalJobResponse{
		Pagination: &inventory.PaginationResults{
			Page:       page - 1,
			PageSize:   pageSize,
			TotalItems: total,
		},
		Jobs: buildFTSExternalJobListItems(jobs),
	}, nil
}

func (s *SingleFTSExternalJobService) Get(c *gin.Context) (*GetFTSExternalJobResponse, error) {
	dep := dependency.FromContext(c)

	job, err := dep.DBClient().FTSExternalJob.Query().Where(ftsexternaljob.IDEQ(s.ID)).Only(c)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, serializer.NewError(serializer.CodeNotFound, "External FTS job not found", err)
		}
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to get external FTS job", err)
	}

	return &GetFTSExternalJobResponse{FTSExternalJob: job}, nil
}

func buildFTSExternalJobListItems(jobs []*ent.FTSExternalJob) []GetFTSExternalJobListItem {
	res := make([]GetFTSExternalJobListItem, 0, len(jobs))
	for _, job := range jobs {
		if job == nil {
			continue
		}

		res = append(res, GetFTSExternalJobListItem{
			ID:               job.ID,
			CreatedAt:        job.CreatedAt,
			UpdatedAt:        job.UpdatedAt,
			RequestID:        job.RequestID,
			Status:           job.Status,
			FileID:           job.FileID,
			OwnerID:          job.OwnerID,
			EntityID:         job.EntityID,
			Mode:             job.Mode,
			TriggerReason:    job.TriggerReason,
			Attempt:          job.Attempt,
			ManifestPath:     job.ManifestPath,
			RequestedAt:      job.RequestedAt,
			DeadlineAt:       job.DeadlineAt,
			CompletedAt:      job.CompletedAt,
			HasResultPayload: strings.TrimSpace(job.ResultPayload) != "",
			HasErrorPayload:  strings.TrimSpace(job.ErrorPayload) != "",
			HasQualityReport: strings.TrimSpace(job.QualityReport) != "",
		})
	}

	return res
}

func getFTSExternalJobOrderOption(s *AdminListService) []ftsexternaljob.OrderOption {
	orderTerm := getAdminOrderTerm(inventory.OrderDirection(s.OrderDirection))

	switch normalizedFTSExternalJobOrderBy(s.OrderBy) {
	case ftsexternaljob.FieldRequestedAt:
		return []ftsexternaljob.OrderOption{ftsexternaljob.ByRequestedAt(orderTerm), ftsexternaljob.ByID(orderTerm)}
	case ftsexternaljob.FieldCompletedAt:
		return []ftsexternaljob.OrderOption{ftsexternaljob.ByCompletedAt(orderTerm), ftsexternaljob.ByID(orderTerm)}
	case ftsexternaljob.FieldDeadlineAt:
		return []ftsexternaljob.OrderOption{ftsexternaljob.ByDeadlineAt(orderTerm), ftsexternaljob.ByID(orderTerm)}
	case ftsexternaljob.FieldStatus:
		return []ftsexternaljob.OrderOption{ftsexternaljob.ByStatus(orderTerm), ftsexternaljob.ByID(orderTerm)}
	case ftsexternaljob.FieldFileID:
		return []ftsexternaljob.OrderOption{ftsexternaljob.ByFileID(orderTerm), ftsexternaljob.ByID(orderTerm)}
	case ftsexternaljob.FieldOwnerID:
		return []ftsexternaljob.OrderOption{ftsexternaljob.ByOwnerID(orderTerm), ftsexternaljob.ByID(orderTerm)}
	case ftsexternaljob.FieldEntityID:
		return []ftsexternaljob.OrderOption{ftsexternaljob.ByEntityID(orderTerm), ftsexternaljob.ByID(orderTerm)}
	case ftsexternaljob.FieldAttempt:
		return []ftsexternaljob.OrderOption{ftsexternaljob.ByAttempt(orderTerm), ftsexternaljob.ByID(orderTerm)}
	case ftsexternaljob.FieldMode:
		return []ftsexternaljob.OrderOption{ftsexternaljob.ByMode(orderTerm), ftsexternaljob.ByID(orderTerm)}
	case ftsexternaljob.FieldCreatedAt:
		return []ftsexternaljob.OrderOption{ftsexternaljob.ByCreatedAt(orderTerm), ftsexternaljob.ByID(orderTerm)}
	case ftsexternaljob.FieldUpdatedAt:
		return []ftsexternaljob.OrderOption{ftsexternaljob.ByUpdatedAt(orderTerm), ftsexternaljob.ByID(orderTerm)}
	default:
		return []ftsexternaljob.OrderOption{ftsexternaljob.ByID(orderTerm)}
	}
}

func normalizedFTSExternalJobOrderBy(orderBy string) string {
	switch orderBy {
	case "",
		ftsexternaljob.FieldID,
		ftsexternaljob.FieldCreatedAt,
		ftsexternaljob.FieldUpdatedAt,
		ftsexternaljob.FieldStatus,
		ftsexternaljob.FieldFileID,
		ftsexternaljob.FieldOwnerID,
		ftsexternaljob.FieldEntityID,
		ftsexternaljob.FieldMode,
		ftsexternaljob.FieldAttempt,
		ftsexternaljob.FieldRequestedAt,
		ftsexternaljob.FieldDeadlineAt,
		ftsexternaljob.FieldCompletedAt:
		return orderBy
	default:
		return ""
	}
}

func getAdminOrderTerm(order inventory.OrderDirection) sql.OrderTermOption {
	switch order {
	case inventory.OrderDirectionDesc:
		return sql.OrderDesc()
	default:
		return sql.OrderAsc()
	}
}
