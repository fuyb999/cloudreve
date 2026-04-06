package admin

import (
	"encoding/gob"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

type ListShareResponse struct {
	Pagination *inventory.PaginationResults `json:"pagination"`
	Shares     []GetShareResponse           `json:"shares"`
}

type GetShareResponse struct {
	*ent.Share
	UserHashID string `json:"user_hash_id,omitempty"`
	ShareLink  string `json:"share_link,omitempty"`
}

type ListTaskResponse struct {
	Pagination *inventory.PaginationResults `json:"pagination"`
	Tasks      []GetTaskResponse            `json:"tasks"`
}

type ListFTSExternalJobResponse struct {
	Pagination *inventory.PaginationResults `json:"pagination"`
	Jobs       []GetFTSExternalJobListItem  `json:"jobs"`
}

type GetFTSExternalJobListItem struct {
	ID               int        `json:"id,omitempty"`
	CreatedAt        time.Time  `json:"created_at,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at,omitempty"`
	RequestID        string     `json:"request_id,omitempty"`
	Status           string     `json:"status,omitempty"`
	FileID           int        `json:"file_id,omitempty"`
	OwnerID          int        `json:"owner_id,omitempty"`
	EntityID         int        `json:"entity_id,omitempty"`
	Mode             string     `json:"mode,omitempty"`
	TriggerReason    string     `json:"trigger_reason,omitempty"`
	Attempt          int        `json:"attempt,omitempty"`
	ManifestPath     string     `json:"manifest_path,omitempty"`
	RequestedAt      time.Time  `json:"requested_at,omitempty"`
	DeadlineAt       time.Time  `json:"deadline_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	HasResultPayload bool       `json:"has_result_payload,omitempty"`
	HasErrorPayload  bool       `json:"has_error_payload,omitempty"`
	HasQualityReport bool       `json:"has_quality_report,omitempty"`
}

type GetFTSExternalJobResponse struct {
	*ent.FTSExternalJob
}

type ListAuditLogResponse struct {
	Pagination *inventory.PaginationResults `json:"pagination"`
	Logs       []GetAuditLogResponse        `json:"logs"`
}

type AuditLogPayload struct {
	ID            int                    `json:"id"`
	CreatedAt     time.Time              `json:"created_at,omitempty"`
	UpdatedAt     time.Time              `json:"updated_at,omitempty"`
	DeletedAt     *time.Time             `json:"deleted_at,omitempty"`
	Type          int                    `json:"type"`
	CorrelationID string                 `json:"correlation_id,omitempty"`
	IP            string                 `json:"ip,omitempty"`
	Content       map[string]interface{} `json:"content,omitempty"`
	UserID        int                    `json:"user_id,omitempty"`
	FileID        int                    `json:"file_id,omitempty"`
	EntityID      int                    `json:"entity_id,omitempty"`
	ShareID       int                    `json:"share_id,omitempty"`
	Edges         ent.AuditLogEdges      `json:"edges"`
}

type GetAuditLogResponse struct {
	AuditLogPayload
	UserHashID string `json:"user_hash_id,omitempty"`
}

type GetTaskResponse struct {
	*ent.Task
	UserHashID  string         `json:"user_hash_id,omitempty"`
	TaskHashID  string         `json:"task_hash_id,omitempty"`
	DisplayType string         `json:"display_type,omitempty"`
	Summary     *queue.Summary `json:"summary,omitempty"`
	Node        *ent.Node      `json:"node,omitempty"`
}

type ListEntityResponse struct {
	Pagination *inventory.PaginationResults `json:"pagination"`
	Entities   []GetEntityResponse          `json:"entities"`
}

type GetEntityResponse struct {
	*ent.Entity
	UserHashID    string         `json:"user_hash_id,omitempty"`
	UserHashIDMap map[int]string `json:"user_hash_id_map,omitempty"`
}

type ListFileResponse struct {
	Pagination *inventory.PaginationResults `json:"pagination"`
	Files      []GetFileResponse            `json:"files"`
}

type GetFileResponse struct {
	*ent.File
	UserHashID    string         `json:"user_hash_id,omitempty"`
	FileHashID    string         `json:"file_hash_id,omitempty"`
	DirectLinkMap map[int]string `json:"direct_link_map,omitempty"`
}

type ListUserResponse struct {
	Pagination *inventory.PaginationResults `json:"pagination"`
	Users      []GetUserResponse            `json:"users"`
}

type GetUserResponse struct {
	*ent.User
	HashID       string       `json:"hash_id,omitempty"`
	TwoFAEnabled bool         `json:"two_fa_enabled,omitempty"`
	Capacity     *fs.Capacity `json:"capacity,omitempty"`
}

type GetNodeResponse struct {
	*ent.Node
}

type GetGroupResponse struct {
	*ent.Group
	TotalUsers int `json:"total_users"`
}

type OauthCredentialStatus struct {
	Valid           bool       `json:"valid"`
	LastRefreshTime *time.Time `json:"last_refresh_time"`
}

type GetStoragePolicyResponse struct {
	*ent.StoragePolicy
	EntitiesCount int `json:"entities_count,omitempty"`
	EntitiesSize  int `json:"entities_size,omitempty"`
}

type ListNodeResponse struct {
	Pagination *inventory.PaginationResults `json:"pagination"`
	Nodes      []*ent.Node                  `json:"nodes"`
}

type ListPolicyResponse struct {
	Pagination *inventory.PaginationResults `json:"pagination"`
	Policies   []*ent.StoragePolicy         `json:"policies"`
}

type QueueMetric struct {
	Name            setting.QueueType `json:"name"`
	BusyWorkers     int               `json:"busy_workers"`
	SuccessTasks    int               `json:"success_tasks"`
	FailureTasks    int               `json:"failure_tasks"`
	SubmittedTasks  int               `json:"submitted_tasks"`
	SuspendingTasks int               `json:"suspending_tasks"`
}

type ListGroupResponse struct {
	Groups     []*ent.Group                 `json:"groups"`
	Pagination *inventory.PaginationResults `json:"pagination"`
}

type ListOAuthClientResponse struct {
	Clients    []GetOAuthClientResponse     `json:"clients"`
	Pagination *inventory.PaginationResults `json:"pagination"`
}

type GetOAuthClientResponse struct {
	*ent.OAuthClient
	IsSystem    bool `json:"is_system"`
	TotalGrants int  `json:"total_grants,omitempty"`
}

type HomepageSummary struct {
	MetricsSummary *MetricsSummary `json:"metrics_summary"`
	SiteURls       []string        `json:"site_urls"`
	Version        *Version        `json:"version"`
}

type UserUploadStat struct {
	UserID      int    `json:"user_id"`
	DisplayName string `json:"display_name"`
	FileCount   int    `json:"file_count"`
}

type MetricsSummary struct {
	Dates          []time.Time      `json:"dates"`
	Files          []int            `json:"files"`
	Users          []int            `json:"users"`
	Shares         []int            `json:"shares"`
	TopUploadUsers []UserUploadStat `json:"top_upload_users"`
	FileTotal      int              `json:"file_total"`
	UserTotal      int              `json:"user_total"`
	ShareTotal     int              `json:"share_total"`
	EntitiesTotal  int              `json:"entities_total"`
	GeneratedAt    time.Time        `json:"generated_at"`
}

type Version struct {
	Version string `json:"version"`
	Pro     bool   `json:"pro"`
	Commit  string `json:"commit"`
}

func init() {
	gob.Register(MetricsSummary{})
}
