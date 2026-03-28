package searcher

import (
	"context"
	"io"
	"time"

	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
)

type SearchAttachmentDocument struct {
	ID        string            `json:"id"`
	ParentID  string            `json:"parent_id,omitempty"`
	Depth     int               `json:"depth,omitempty"`
	EntityID  int               `json:"entity_id,omitempty"`
	Type      string            `json:"type,omitempty"`
	Name      string            `json:"name,omitempty"`
	Path      string            `json:"path,omitempty"`
	Bucket    string            `json:"bucket,omitempty"`
	Size      int64             `json:"size,omitempty"`
	MimeType  string            `json:"mime_type,omitempty"`
	Source    string            `json:"source,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Content   string            `json:"content,omitempty"`
	CreatedAt time.Time         `json:"created_at,omitempty"`
	UpdatedAt time.Time         `json:"updated_at,omitempty"`
}

type SearchFileVersionDocument struct {
	ID              string         `json:"id"`
	EntityID        int            `json:"entity_id"`
	EntityType      string         `json:"entity_type"`
	EntityTypeValue int            `json:"entity_type_value"`
	Source          string         `json:"source,omitempty"`
	Size            int64          `json:"size,omitempty"`
	CreatedAt       time.Time      `json:"created_at,omitempty"`
	UpdatedAt       time.Time      `json:"updated_at,omitempty"`
	StoragePolicyID int            `json:"storage_policy_id,omitempty"`
	StorageType     string         `json:"storage_type,omitempty"`
	Bucket          string         `json:"bucket,omitempty"`
	MimeType        string         `json:"mime_type,omitempty"`
	ReferenceCount  int            `json:"reference_count,omitempty"`
	Encrypted       bool           `json:"encrypted,omitempty"`
	Props           map[string]any `json:"props,omitempty"`
}

type SearchFileDocument struct {
	ID              string                     `json:"id"`
	FileID          int                        `json:"file_id"`
	OwnerID         int                        `json:"owner_id"`
	EntityID        int                        `json:"entity_id,omitempty"`
	ParentID        int                        `json:"parent_id,omitempty"`
	FileName        string                     `json:"file_name"`
	FileExt         string                     `json:"file_ext,omitempty"`
	FileType        int                        `json:"file_type"`
	Size            int64                      `json:"size"`
	CreatedAt       time.Time                  `json:"created_at,omitempty"`
	UpdatedAt       time.Time                  `json:"updated_at,omitempty"`
	IsSymbolic      bool                       `json:"is_symbolic,omitempty"`
	Shared          bool                       `json:"shared,omitempty"`
	TreePath        string                     `json:"tree_path,omitempty"`
	StoragePolicyID int                        `json:"storage_policy_id,omitempty"`
	StorageType     string                     `json:"storage_type,omitempty"`
	StorageBucket   string                     `json:"storage_bucket,omitempty"`
	Metadata        map[string]string          `json:"metadata,omitempty"`
	MetadataText    string                     `json:"metadata_text,omitempty"`
	Props           map[string]any             `json:"props,omitempty"`
	PathText        string                     `json:"path_text,omitempty"`
	Content         string                     `json:"content,omitempty"`
	LatestVersion   *SearchFileVersionDocument `json:"latest_version,omitempty"`
	Attachments     []SearchAttachmentDocument `json:"attachments,omitempty"`
	SnapshotVersion int                        `json:"snapshot_version"`
	SynchronizedAt  time.Time                  `json:"synchronized_at,omitempty"`
}

type SearchResult struct {
	FileID   int    `json:"file_id"`
	OwnerID  int    `json:"owner_id"`
	EntityID int    `json:"entity_id"`
	FileName string `json:"file_name"`
	Text     string `json:"text"`
}

type SearchRequest struct {
	Query            string                      `json:"query"`
	Offset           int                         `json:"offset"`
	OwnerID          *int                        `json:"owner_id,omitempty"`
	VisibilityFilter *publicshare.FileFilterExpr `json:"visibility_filter,omitempty"`
}

type SearchIndexer interface {
	UpsertFile(ctx context.Context, doc *SearchFileDocument) error
	BulkUpsertFiles(ctx context.Context, docs []*SearchFileDocument) error
	DeleteByFileIDs(ctx context.Context, fileID ...int) error
	Search(ctx context.Context, req *SearchRequest) ([]SearchResult, int64, error)
	// IndexReady reports whether the search index exists and has the required
	// configuration (filterable/searchable attributes, etc.).
	IndexReady(ctx context.Context) (bool, error)
	EnsureIndex(ctx context.Context) error
	// DeleteAll removes all documents from the index.
	DeleteAll(ctx context.Context) error
	Close() error
}

type TextExtractor interface {
	Exts() []string
	MaxFileSize() int64
	Extract(ctx context.Context, reader io.Reader) (string, error)
}
