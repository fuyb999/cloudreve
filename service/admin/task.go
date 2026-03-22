package admin

import (
	"context"
	"strconv"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"
	"github.com/samber/lo"
)

func GetQueueMetrics(c *gin.Context) ([]QueueMetric, error) {
	res := []QueueMetric{}
	dep := dependency.FromContext(c)

	contentProcessing := dep.ContentProcessingQueue(c)
	entityRecycle := dep.EntityRecycleQueue(c)
	ioIntense := dep.IoIntenseQueue(c)
	remoteDownload := dep.RemoteDownloadQueue(c)
	thumb := dep.ThumbQueue(c)

	res = append(res, QueueMetric{
		Name:            setting.QueueTypeContentProcessing,
		BusyWorkers:     contentProcessing.BusyWorkers(),
		SuccessTasks:    contentProcessing.SuccessTasks(),
		FailureTasks:    contentProcessing.FailureTasks(),
		SubmittedTasks:  contentProcessing.SubmittedTasks(),
		SuspendingTasks: contentProcessing.SuspendingTasks(),
	})
	res = append(res, QueueMetric{
		Name:            setting.QueueTypeEntityRecycle,
		BusyWorkers:     entityRecycle.BusyWorkers(),
		SuccessTasks:    entityRecycle.SuccessTasks(),
		FailureTasks:    entityRecycle.FailureTasks(),
		SubmittedTasks:  entityRecycle.SubmittedTasks(),
		SuspendingTasks: entityRecycle.SuspendingTasks(),
	})
	res = append(res, QueueMetric{
		Name:            setting.QueueTypeIOIntense,
		BusyWorkers:     ioIntense.BusyWorkers(),
		SuccessTasks:    ioIntense.SuccessTasks(),
		FailureTasks:    ioIntense.FailureTasks(),
		SubmittedTasks:  ioIntense.SubmittedTasks(),
		SuspendingTasks: ioIntense.SuspendingTasks(),
	})
	res = append(res, QueueMetric{
		Name:            setting.QueueTypeRemoteDownload,
		BusyWorkers:     remoteDownload.BusyWorkers(),
		SuccessTasks:    remoteDownload.SuccessTasks(),
		FailureTasks:    remoteDownload.FailureTasks(),
		SubmittedTasks:  remoteDownload.SubmittedTasks(),
		SuspendingTasks: remoteDownload.SuspendingTasks(),
	})
	res = append(res, QueueMetric{
		Name:            setting.QueueTypeThumb,
		BusyWorkers:     thumb.BusyWorkers(),
		SuccessTasks:    thumb.SuccessTasks(),
		FailureTasks:    thumb.FailureTasks(),
		SubmittedTasks:  thumb.SubmittedTasks(),
		SuspendingTasks: thumb.SuspendingTasks(),
	})

	return res, nil
}

const (
	taskTypeCondition          = "task_type"
	taskStatusCondition        = "task_status"
	taskCorrelationIDCondition = "task_correlation_id"
	taskUserIDCondition        = "task_user_id"

	contentProcessingKindFullTextExtract   = `"kind":"full_text_extract"`
	contentProcessingKindMediaMetaExtract  = `"kind":"media_meta_extract"`
	contentProcessingKindThumbnailGenerate = `"kind":"thumbnail_generate"`
	contentProcessingKindDocumentInspect   = `"kind":"document_inspect"`
)

func (s *AdminListService) Tasks(c *gin.Context) (*ListTaskResponse, error) {
	dep := dependency.FromContext(c)
	taskClient := dep.TaskClient()
	hasher := dep.HashIDEncoder()
	var (
		err             error
		userID          int
		correlationID   *uuid.UUID
		status          []task.Status
		taskType        []string
		taskTypeFilters []inventory.TaskTypeFilter
	)

	if s.Conditions[taskTypeCondition] != "" {
		taskType, taskTypeFilters = resolveAdminTaskTypeFilter(s.Conditions[taskTypeCondition])
	}

	if s.Conditions[taskStatusCondition] != "" {
		status = []task.Status{task.Status(s.Conditions[taskStatusCondition])}
	}

	if s.Conditions[taskCorrelationIDCondition] != "" {
		cid, err := uuid.FromString(s.Conditions[taskCorrelationIDCondition])
		if err != nil {
			return nil, serializer.NewError(serializer.CodeParamErr, "Invalid task correlation ID", err)
		}
		correlationID = &cid
	}

	if s.Conditions[taskUserIDCondition] != "" {
		userID, err = strconv.Atoi(s.Conditions[taskUserIDCondition])
		if err != nil {
			return nil, serializer.NewError(serializer.CodeParamErr, "Invalid task user ID", err)
		}
	}

	ctx := context.WithValue(c, inventory.LoadTaskUser{}, true)
	res, err := taskClient.List(ctx, &inventory.ListTaskArgs{
		PaginationArgs: &inventory.PaginationArgs{
			Page:     s.Page - 1,
			PageSize: s.PageSize,
			OrderBy:  s.OrderBy,
			Order:    inventory.OrderDirection(s.OrderDirection),
		},
		UserID:        userID,
		CorrelationID: correlationID,
		Types:         taskType,
		TypeFilters:   taskTypeFilters,
		Status:        status,
	})

	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to list tasks", err)
	}

	tasks := make([]queue.Task, 0, len(res.Tasks))
	nodeMap := make(map[int]*ent.Node)
	for _, t := range res.Tasks {
		task, err := queue.NewTaskFromModel(t)
		if err != nil {
			return nil, serializer.NewError(serializer.CodeDBError, "Failed to parse task", err)
		}

		summary := task.Summarize(hasher)
		if summary != nil && summary.NodeID > 0 {
			if _, ok := nodeMap[summary.NodeID]; !ok {
				nodeMap[summary.NodeID] = nil
			}
		}
		tasks = append(tasks, task)
	}

	// Get nodes
	nodes, err := dep.NodeClient().GetNodeByIds(c, lo.Keys(nodeMap))
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to query nodes", err)
	}
	for _, n := range nodes {
		nodeMap[n.ID] = n
	}

	return &ListTaskResponse{
		Pagination: res.PaginationResults,
		Tasks: lo.Map(res.Tasks, func(task *ent.Task, i int) GetTaskResponse {
			var (
				uid     string
				node    *ent.Node
				summary *queue.Summary
			)

			if task.Edges.User != nil {
				uid = hashid.EncodeUserID(hasher, task.Edges.User.ID)
			}

			t := tasks[i]
			summary = t.Summarize(hasher)
			if summary != nil && summary.NodeID > 0 {
				node = nodeMap[summary.NodeID]
			}

			return GetTaskResponse{
				Task:        task,
				TaskHashID:  hashid.EncodeTaskID(hasher, task.ID),
				UserHashID:  uid,
				DisplayType: queue.DisplayType(task.Type, task.PrivateState),
				Node:        node,
				Summary:     summary,
			}
		}),
	}, nil
}

type (
	SingleTaskService struct {
		ID int `uri:"id" json:"id" binding:"required"`
	}
	SingleTaskParamCtx struct{}
)

func (s *SingleTaskService) Get(c *gin.Context) (*GetTaskResponse, error) {
	dep := dependency.FromContext(c)
	taskClient := dep.TaskClient()
	hasher := dep.HashIDEncoder()

	ctx := context.WithValue(c, inventory.LoadTaskUser{}, true)
	task, err := taskClient.GetTaskByID(ctx, s.ID)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to get task", err)
	}

	t, err := queue.NewTaskFromModel(task)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to parse task", err)
	}

	summary := t.Summarize(hasher)
	var (
		node       *ent.Node
		userHashID string
	)

	if summary != nil && summary.NodeID > 0 {
		node, _ = dep.NodeClient().GetNodeById(c, summary.NodeID)
	}

	if task.Edges.User != nil {
		userHashID = hashid.EncodeUserID(hasher, task.Edges.User.ID)
	}

	return &GetTaskResponse{
		Task:        task,
		Summary:     summary,
		Node:        node,
		UserHashID:  userHashID,
		TaskHashID:  hashid.EncodeTaskID(hasher, task.ID),
		DisplayType: queue.DisplayType(task.Type, task.PrivateState),
	}, nil
}

type (
	BatchTaskService struct {
		IDs []int `json:"ids" binding:"required"`
	}
	BatchTaskParamCtx struct{}
)

func (s *BatchTaskService) Delete(c *gin.Context) error {
	dep := dependency.FromContext(c)
	taskClient := dep.TaskClient()

	err := taskClient.DeleteByIDs(c, s.IDs...)
	if err != nil {
		return serializer.NewError(serializer.CodeDBError, "Failed to delete tasks", err)
	}

	return nil
}

type (
	CleanupTaskService struct {
		NotAfter time.Time     `json:"not_after" binding:"required"`
		Types    []string      `json:"types"`
		Status   []task.Status `json:"status"`
	}
	CleanupTaskParameterCtx struct{}
)

func (s *CleanupTaskService) CleanupTask(c *gin.Context) error {
	dep := dependency.FromContext(c)
	taskClient := dep.TaskClient()

	if len(s.Status) == 0 {
		s.Status = []task.Status{task.StatusCanceled, task.StatusCompleted, task.StatusError}
	}

	if err := taskClient.DeleteBy(c, &inventory.DeleteTaskArgs{
		NotAfter:    s.NotAfter,
		Types:       expandCleanupTaskTypes(s.Types),
		TypeFilters: expandCleanupTaskTypeFilters(s.Types),
		Status:      s.Status,
	}); err != nil {
		return serializer.NewError(serializer.CodeDBError, "Failed to cleanup tasks", err)
	}

	return nil
}

func resolveAdminTaskTypeFilter(taskType string) ([]string, []inventory.TaskTypeFilter) {
	switch taskType {
	case "content_processing":
		return []string{
				queue.FullTextIndexTaskType,
				queue.FullTextDeleteTaskType,
				queue.MediaMetaTaskType,
				queue.DocumentInspectTaskType,
			}, []inventory.TaskTypeFilter{
				{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindFullTextExtract},
				{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindMediaMetaExtract},
				{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindDocumentInspect},
				{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindThumbnailGenerate},
			}
	case queue.FullTextIndexTaskType:
		return []string{queue.FullTextIndexTaskType, queue.FullTextDeleteTaskType}, []inventory.TaskTypeFilter{
			{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindFullTextExtract},
		}
	case queue.MediaMetaTaskType:
		return []string{queue.MediaMetaTaskType}, []inventory.TaskTypeFilter{
			{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindMediaMetaExtract},
		}
	case queue.DocumentInspectTaskType:
		return []string{queue.DocumentInspectTaskType}, []inventory.TaskTypeFilter{
			{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindDocumentInspect},
		}
	case "thumbnail_generate":
		return nil, []inventory.TaskTypeFilter{
			{Type: queue.SlaveContentProcessingTaskType, PrivateStateContains: contentProcessingKindThumbnailGenerate},
		}
	case "":
		return nil, nil
	default:
		return []string{taskType}, nil
	}
}

func expandCleanupTaskTypes(taskTypes []string) []string {
	expanded := make([]string, 0, len(taskTypes))
	for _, taskType := range taskTypes {
		rawTypes, _ := resolveAdminTaskTypeFilter(taskType)
		expanded = append(expanded, rawTypes...)
	}

	return lo.Uniq(expanded)
}

func expandCleanupTaskTypeFilters(taskTypes []string) []inventory.TaskTypeFilter {
	expanded := make([]inventory.TaskTypeFilter, 0, len(taskTypes))
	for _, taskType := range taskTypes {
		_, filters := resolveAdminTaskTypeFilter(taskType)
		expanded = append(expanded, filters...)
	}

	return expanded
}
