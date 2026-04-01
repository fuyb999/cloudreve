package indexer

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	elasticsearch "github.com/elastic/go-elasticsearch/v8"
)

const elasticsearchDefaultIndexName = "cloudreve_files"

const (
	elasticsearchMaxContentBytes           = 8 << 20
	elasticsearchMaxAttachmentContentBytes = 512 << 10
	elasticsearchMaxDocumentPayloadBytes   = 16 << 20
)

type ElasticsearchIndexer struct {
	client   *elasticsearch.Client
	index    string
	pageSize int
	l        logging.Logger
}

type elasticsearchSearchResponse struct {
	Hits struct {
		Total struct {
			Value int64 `json:"value"`
		} `json:"total"`
		Hits []struct {
			Source struct {
				FileID   int    `json:"file_id"`
				OwnerID  int    `json:"owner_id"`
				EntityID int    `json:"entity_id"`
				FileName string `json:"file_name"`
			} `json:"_source"`
			Highlight map[string][]string `json:"highlight"`
		} `json:"hits"`
	} `json:"hits"`
}

// ElasticsearchTime serializes/deserializes time values using the
// yyyy-MM-dd HH:mm:ss format required by the Elasticsearch index mapping.
type elasticsearchAttachmentDocument struct {
	ID        string              `json:"id"`
	ParentID  string              `json:"parent_id,omitempty"`
	Depth     int                 `json:"depth,omitempty"`
	EntityID  int                 `json:"entity_id,omitempty"`
	Type      string              `json:"type,omitempty"`
	Name      string              `json:"name,omitempty"`
	Path      string              `json:"path,omitempty"`
	Bucket    string              `json:"bucket,omitempty"`
	Size      int64               `json:"size,omitempty"`
	MimeType  string              `json:"mime_type,omitempty"`
	Source    string              `json:"source,omitempty"`
	Metadata  map[string]string   `json:"metadata,omitempty"`
	Content   string              `json:"content,omitempty"`
	CreatedAt util.DateTimeSecond `json:"created_at,omitempty"`
	UpdatedAt util.DateTimeSecond `json:"updated_at,omitempty"`
}

type elasticsearchFileVersionDocument struct {
	ID              string              `json:"id"`
	EntityID        int                 `json:"entity_id"`
	EntityType      string              `json:"entity_type"`
	EntityTypeValue int                 `json:"entity_type_value"`
	Source          string              `json:"source,omitempty"`
	Size            int64               `json:"size,omitempty"`
	CreatedAt       util.DateTimeSecond `json:"created_at,omitempty"`
	UpdatedAt       util.DateTimeSecond `json:"updated_at,omitempty"`
	StoragePolicyID int                 `json:"storage_policy_id,omitempty"`
	StorageType     string              `json:"storage_type,omitempty"`
	Bucket          string              `json:"bucket,omitempty"`
	MimeType        string              `json:"mime_type,omitempty"`
	ReferenceCount  int                 `json:"reference_count,omitempty"`
	Encrypted       bool                `json:"encrypted,omitempty"`
	Props           map[string]any      `json:"props,omitempty"`
}

type elasticsearchFileDocument struct {
	ID              string                            `json:"id"`
	FileID          int                               `json:"file_id"`
	OwnerID         int                               `json:"owner_id"`
	EntityID        int                               `json:"entity_id,omitempty"`
	ParentID        int                               `json:"parent_id,omitempty"`
	FileName        string                            `json:"file_name"`
	FileExt         string                            `json:"file_ext,omitempty"`
	FileType        int                               `json:"file_type"`
	Size            int64                             `json:"size"`
	CreatedAt       util.DateTimeSecond               `json:"created_at,omitempty"`
	UpdatedAt       util.DateTimeSecond               `json:"updated_at,omitempty"`
	IsSymbolic      bool                              `json:"is_symbolic,omitempty"`
	Shared          bool                              `json:"shared,omitempty"`
	TreePath        string                            `json:"tree_path,omitempty"`
	StoragePolicyID int                               `json:"storage_policy_id,omitempty"`
	StorageType     string                            `json:"storage_type,omitempty"`
	StorageBucket   string                            `json:"storage_bucket,omitempty"`
	Metadata        map[string]string                 `json:"metadata,omitempty"`
	MetadataText    string                            `json:"metadata_text,omitempty"`
	Props           map[string]any                    `json:"props,omitempty"`
	PathText        string                            `json:"path_text,omitempty"`
	Content         string                            `json:"content,omitempty"`
	LatestVersion   *elasticsearchFileVersionDocument `json:"latest_version,omitempty"`
	Attachments     []elasticsearchAttachmentDocument `json:"attachments,omitempty"`
	SnapshotVersion int                               `json:"snapshot_version"`
	SynchronizedAt  util.DateTimeSecond               `json:"synchronized_at,omitempty"`
}

func NewElasticsearchIndexer(cfg *setting.FTSIndexElasticsearchSetting, l logging.Logger) (*ElasticsearchIndexer, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.SkipTLSVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	esCfg := elasticsearch.Config{
		Addresses: []string{cfg.Endpoint},
		APIKey:    cfg.APIKey,
		Username:  cfg.Username,
		Password:  cfg.Password,
		CloudID:   cfg.CloudID,
		Transport: transport,
	}

	client, err := elasticsearch.NewClient(esCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create elasticsearch client: %w", err)
	}

	indexName := cfg.Index
	if indexName == "" {
		indexName = elasticsearchDefaultIndexName
	}

	pageSize := cfg.PageSize
	if pageSize <= 0 {
		pageSize = 5
	}

	return &ElasticsearchIndexer{
		client:   client,
		index:    indexName,
		pageSize: pageSize,
		l:        l,
	}, nil
}

func (e *ElasticsearchIndexer) IndexReady(ctx context.Context) (bool, error) {
	res, err := e.client.Indices.Exists([]string{e.index}, e.client.Indices.Exists.WithContext(ctx))
	if err != nil {
		return false, fmt.Errorf("failed to check index: %w", err)
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, parseElasticsearchError("failed to check index", fmt.Sprintf("%d", res.StatusCode), res.Body)
	}
}

func (e *ElasticsearchIndexer) EnsureIndex(ctx context.Context) error {
	ready, err := e.IndexReady(ctx)
	if err != nil {
		return err
	}

	if !ready {
		body, err := json.Marshal(elasticsearchIndexDefinition())
		if err != nil {
			return fmt.Errorf("failed to marshal index definition: %w", err)
		}

		res, err := e.client.Indices.Create(
			e.index,
			e.client.Indices.Create.WithContext(ctx),
			e.client.Indices.Create.WithBody(bytes.NewReader(body)),
		)
		if err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
		defer res.Body.Close()

		if res.IsError() && res.StatusCode != http.StatusOK {
			return parseElasticsearchError("failed to create index", fmt.Sprintf("%d", res.StatusCode), res.Body)
		}
	}

	body, err := json.Marshal(elasticsearchIndexDefinition()["mappings"])
	if err != nil {
		return fmt.Errorf("failed to marshal mappings: %w", err)
	}

	res, err := e.client.Indices.PutMapping(
		[]string{e.index},
		bytes.NewReader(body),
		e.client.Indices.PutMapping.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("failed to update index mappings: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return parseElasticsearchError("failed to update index mappings", fmt.Sprintf("%d", res.StatusCode), res.Body)
	}

	return nil
}

func (e *ElasticsearchIndexer) UpsertFile(ctx context.Context, doc *searcher.SearchFileDocument) error {
	if doc == nil {
		return nil
	}

	sanitized := sanitizeElasticsearchDocument(doc)

	body, err := json.Marshal(newElasticsearchDocument(sanitized))
	if err != nil {
		return fmt.Errorf("failed to marshal search document: %w", err)
	}

	res, err := e.client.Index(
		e.index,
		bytes.NewReader(body),
		e.client.Index.WithContext(ctx),
		e.client.Index.WithDocumentID(sanitized.ID),
	)
	if err != nil {
		return fmt.Errorf("failed to upsert file document: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return parseElasticsearchError("failed to upsert file document", fmt.Sprintf("%d", res.StatusCode), res.Body)
	}

	return nil
}

func (e *ElasticsearchIndexer) BulkUpsertFiles(ctx context.Context, docs []*searcher.SearchFileDocument) error {
	if len(docs) == 0 {
		return nil
	}

	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	for _, doc := range docs {
		if doc == nil {
			continue
		}

		sanitized := sanitizeElasticsearchDocument(doc)

		if err := encoder.Encode(map[string]any{
			"index": map[string]any{
				"_index": e.index,
				"_id":    sanitized.ID,
			},
		}); err != nil {
			return fmt.Errorf("failed to encode bulk action: %w", err)
		}

		if err := encoder.Encode(newElasticsearchDocument(sanitized)); err != nil {
			return fmt.Errorf("failed to encode bulk document: %w", err)
		}
	}

	if payload.Len() == 0 {
		return nil
	}

	res, err := e.client.Bulk(
		&payload,
		e.client.Bulk.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("failed to bulk upsert file documents: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return parseElasticsearchError("failed to bulk upsert file documents", fmt.Sprintf("%d", res.StatusCode), res.Body)
	}

	var bulkRes struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			Status int `json:"status"`
			Error  any `json:"error"`
		} `json:"items"`
	}
	if err := json.NewDecoder(res.Body).Decode(&bulkRes); err != nil {
		return fmt.Errorf("failed to decode bulk response: %w", err)
	}

	if !bulkRes.Errors {
		return nil
	}

	for _, item := range bulkRes.Items {
		if indexItem, ok := item["index"]; ok && indexItem.Error != nil {
			return fmt.Errorf("bulk item failed with status %d: %v", indexItem.Status, indexItem.Error)
		}
	}

	return nil
}

func (e *ElasticsearchIndexer) DeleteByFileIDs(ctx context.Context, fileID ...int) error {
	if len(fileID) == 0 {
		return nil
	}

	body, err := json.Marshal(map[string]any{
		"query": map[string]any{
			"terms": map[string]any{
				"file_id": fileID,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to marshal delete query: %w", err)
	}

	res, err := e.client.DeleteByQuery(
		[]string{e.index},
		bytes.NewReader(body),
		e.client.DeleteByQuery.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("failed to delete file documents: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound {
		return nil
	}
	if res.IsError() {
		return parseElasticsearchError("failed to delete file documents", fmt.Sprintf("%d", res.StatusCode), res.Body)
	}

	return nil
}

func (e *ElasticsearchIndexer) Search(ctx context.Context, req *searcher.SearchRequest) ([]searcher.SearchResult, int64, error) {
	filters := make([]any, 0, 2)
	if req != nil && req.OwnerID != nil {
		filters = append(filters, map[string]any{
			"term": map[string]any{
				"owner_id": *req.OwnerID,
			},
		})
	}
	if req != nil && req.VisibilityFilter != nil {
		if filter := publicshare.ToElasticsearchFilter(req.VisibilityFilter); filter != nil {
			filters = append(filters, filter)
		}
	}

	queryString := ""
	offset := 0
	if req != nil {
		queryString = req.Query
		offset = req.Offset
	}

	body, err := json.Marshal(map[string]any{
		"from":             offset,
		"size":             e.pageSize,
		"track_total_hits": true,
		"_source": []string{
			"file_id",
			"owner_id",
			"entity_id",
			"file_name",
		},
		"query": map[string]any{
			"bool": map[string]any{
				"filter": filters,
				"must": []any{
					map[string]any{
						"simple_query_string": map[string]any{
							"query":            queryString,
							"default_operator": "and",
							"fields": []string{
								"file_name^5",
								"file_ext^2",
								"path_text^4",
								"metadata_text^3",
								"content",
								"latest_version.source",
								"latest_version.bucket",
								"latest_version.mime_type",
								"attachments.name^2",
								"attachments.path^2",
								"attachments.content",
							},
						},
					},
				},
			},
		},
		"highlight": map[string]any{
			"pre_tags":  []string{"<em>"},
			"post_tags": []string{"</em>"},
			"fields": map[string]any{
				"content":             map[string]any{"fragment_size": 160, "number_of_fragments": 1},
				"metadata_text":       map[string]any{"fragment_size": 160, "number_of_fragments": 1},
				"path_text":           map[string]any{"fragment_size": 160, "number_of_fragments": 1},
				"file_name":           map[string]any{"number_of_fragments": 0},
				"attachments.content": map[string]any{"fragment_size": 160, "number_of_fragments": 1},
				"attachments.name":    map[string]any{"number_of_fragments": 0},
				"attachments.path":    map[string]any{"number_of_fragments": 0},
			},
		},
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal search request: %w", err)
	}

	res, err := e.client.Search(
		e.client.Search.WithContext(ctx),
		e.client.Search.WithIndex(e.index),
		e.client.Search.WithBody(bytes.NewReader(body)),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to execute search: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return nil, 0, parseElasticsearchError("failed to execute search", fmt.Sprintf("%d", res.StatusCode), res.Body)
	}

	var searchRes elasticsearchSearchResponse
	if err := json.NewDecoder(res.Body).Decode(&searchRes); err != nil {
		return nil, 0, fmt.Errorf("failed to decode search response: %w", err)
	}

	results := make([]searcher.SearchResult, 0, len(searchRes.Hits.Hits))
	for _, hit := range searchRes.Hits.Hits {
		results = append(results, searcher.SearchResult{
			FileID:   hit.Source.FileID,
			OwnerID:  hit.Source.OwnerID,
			EntityID: hit.Source.EntityID,
			FileName: hit.Source.FileName,
			Text:     bestHighlightSnippet(hit.Highlight, hit.Source.FileName),
		})
	}

	return results, searchRes.Hits.Total.Value, nil
}

func (e *ElasticsearchIndexer) DeleteAll(ctx context.Context) error {
	res, err := e.client.Indices.Delete([]string{e.index}, e.client.Indices.Delete.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("failed to delete index: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound {
		return nil
	}
	if res.IsError() {
		return parseElasticsearchError("failed to delete index", fmt.Sprintf("%d", res.StatusCode), res.Body)
	}

	return nil
}

func (e *ElasticsearchIndexer) Close() error {
	return nil
}

func sanitizeElasticsearchDocument(doc *searcher.SearchFileDocument) *searcher.SearchFileDocument {
	if doc == nil {
		return nil
	}

	sanitized := *doc
	sanitized.Content = truncateUTF8ByBytes(strings.TrimSpace(doc.Content), elasticsearchMaxContentBytes)
	if len(doc.Attachments) > 0 {
		sanitized.Attachments = make([]searcher.SearchAttachmentDocument, len(doc.Attachments))
		copy(sanitized.Attachments, doc.Attachments)
		for i := range sanitized.Attachments {
			sanitized.Attachments[i].Content = truncateUTF8ByBytes(
				strings.TrimSpace(sanitized.Attachments[i].Content),
				elasticsearchMaxAttachmentContentBytes,
			)
		}
	}

	if elasticsearchDocumentSizeWithinLimit(&sanitized, elasticsearchMaxDocumentPayloadBytes) {
		return &sanitized
	}

	for i := range sanitized.Attachments {
		sanitized.Attachments[i].Content = ""
	}
	if elasticsearchDocumentSizeWithinLimit(&sanitized, elasticsearchMaxDocumentPayloadBytes) {
		return &sanitized
	}

	sanitized.Content = ""
	return &sanitized
}

func elasticsearchDocumentSizeWithinLimit(doc *searcher.SearchFileDocument, maxBytes int) bool {
	if doc == nil || maxBytes <= 0 {
		return true
	}

	raw, err := json.Marshal(newElasticsearchDocument(doc))
	if err != nil {
		return false
	}

	return len(raw) <= maxBytes
}

func truncateUTF8ByBytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}

	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}

	return value
}

func bestHighlightSnippet(highlight map[string][]string, fallback ...string) string {
	order := []string{
		"content",
		"metadata_text",
		"path_text",
		"attachments.content",
		"attachments.name",
		"attachments.path",
		"file_name",
	}

	for _, field := range order {
		if values := highlight[field]; len(values) > 0 && strings.TrimSpace(values[0]) != "" {
			return values[0]
		}
	}

	for _, candidate := range fallback {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return candidate
		}
	}

	return ""
}

func newElasticsearchDocument(doc *searcher.SearchFileDocument) *elasticsearchFileDocument {
	if doc == nil {
		return nil
	}

	res := &elasticsearchFileDocument{
		ID:              doc.ID,
		FileID:          doc.FileID,
		OwnerID:         doc.OwnerID,
		EntityID:        doc.EntityID,
		ParentID:        doc.ParentID,
		FileName:        doc.FileName,
		FileExt:         doc.FileExt,
		FileType:        doc.FileType,
		Size:            doc.Size,
		CreatedAt:       util.NewDateTimeSecond(doc.CreatedAt),
		UpdatedAt:       util.NewDateTimeSecond(doc.UpdatedAt),
		IsSymbolic:      doc.IsSymbolic,
		Shared:          doc.Shared,
		TreePath:        doc.TreePath,
		StoragePolicyID: doc.StoragePolicyID,
		StorageType:     doc.StorageType,
		StorageBucket:   doc.StorageBucket,
		Metadata:        doc.Metadata,
		MetadataText:    doc.MetadataText,
		Props:           doc.Props,
		PathText:        doc.PathText,
		Content:         doc.Content,
		SnapshotVersion: doc.SnapshotVersion,
		SynchronizedAt:  util.NewDateTimeSecond(doc.SynchronizedAt),
	}

	if doc.LatestVersion != nil {
		res.LatestVersion = &elasticsearchFileVersionDocument{
			ID:              doc.LatestVersion.ID,
			EntityID:        doc.LatestVersion.EntityID,
			EntityType:      doc.LatestVersion.EntityType,
			EntityTypeValue: doc.LatestVersion.EntityTypeValue,
			Source:          doc.LatestVersion.Source,
			Size:            doc.LatestVersion.Size,
			CreatedAt:       util.NewDateTimeSecond(doc.LatestVersion.CreatedAt),
			UpdatedAt:       util.NewDateTimeSecond(doc.LatestVersion.UpdatedAt),
			StoragePolicyID: doc.LatestVersion.StoragePolicyID,
			StorageType:     doc.LatestVersion.StorageType,
			Bucket:          doc.LatestVersion.Bucket,
			MimeType:        doc.LatestVersion.MimeType,
			ReferenceCount:  doc.LatestVersion.ReferenceCount,
			Encrypted:       doc.LatestVersion.Encrypted,
			Props:           doc.LatestVersion.Props,
		}
	}

	if len(doc.Attachments) > 0 {
		res.Attachments = make([]elasticsearchAttachmentDocument, 0, len(doc.Attachments))
		for _, attachment := range doc.Attachments {
			res.Attachments = append(res.Attachments, elasticsearchAttachmentDocument{
				ID:        attachment.ID,
				ParentID:  attachment.ParentID,
				Depth:     attachment.Depth,
				EntityID:  attachment.EntityID,
				Type:      attachment.Type,
				Name:      attachment.Name,
				Path:      attachment.Path,
				Bucket:    attachment.Bucket,
				Size:      attachment.Size,
				MimeType:  attachment.MimeType,
				Source:    attachment.Source,
				Metadata:  attachment.Metadata,
				Content:   attachment.Content,
				CreatedAt: util.NewDateTimeSecond(attachment.CreatedAt),
				UpdatedAt: util.NewDateTimeSecond(attachment.UpdatedAt),
			})
		}
	}

	return res
}

func (d *elasticsearchFileDocument) toSearchFileDocument() *searcher.SearchFileDocument {
	if d == nil {
		return nil
	}

	res := &searcher.SearchFileDocument{
		ID:              d.ID,
		FileID:          d.FileID,
		OwnerID:         d.OwnerID,
		EntityID:        d.EntityID,
		ParentID:        d.ParentID,
		FileName:        d.FileName,
		FileExt:         d.FileExt,
		FileType:        d.FileType,
		Size:            d.Size,
		CreatedAt:       d.CreatedAt.Time(),
		UpdatedAt:       d.UpdatedAt.Time(),
		IsSymbolic:      d.IsSymbolic,
		Shared:          d.Shared,
		TreePath:        d.TreePath,
		StoragePolicyID: d.StoragePolicyID,
		StorageType:     d.StorageType,
		StorageBucket:   d.StorageBucket,
		Metadata:        d.Metadata,
		MetadataText:    d.MetadataText,
		Props:           d.Props,
		PathText:        d.PathText,
		Content:         d.Content,
		SnapshotVersion: d.SnapshotVersion,
		SynchronizedAt:  d.SynchronizedAt.Time(),
	}

	if d.LatestVersion != nil {
		res.LatestVersion = &searcher.SearchFileVersionDocument{
			ID:              d.LatestVersion.ID,
			EntityID:        d.LatestVersion.EntityID,
			EntityType:      d.LatestVersion.EntityType,
			EntityTypeValue: d.LatestVersion.EntityTypeValue,
			Source:          d.LatestVersion.Source,
			Size:            d.LatestVersion.Size,
			CreatedAt:       d.LatestVersion.CreatedAt.Time(),
			UpdatedAt:       d.LatestVersion.UpdatedAt.Time(),
			StoragePolicyID: d.LatestVersion.StoragePolicyID,
			StorageType:     d.LatestVersion.StorageType,
			Bucket:          d.LatestVersion.Bucket,
			MimeType:        d.LatestVersion.MimeType,
			ReferenceCount:  d.LatestVersion.ReferenceCount,
			Encrypted:       d.LatestVersion.Encrypted,
			Props:           d.LatestVersion.Props,
		}
	}

	if len(d.Attachments) > 0 {
		res.Attachments = make([]searcher.SearchAttachmentDocument, 0, len(d.Attachments))
		for _, attachment := range d.Attachments {
			res.Attachments = append(res.Attachments, searcher.SearchAttachmentDocument{
				ID:        attachment.ID,
				ParentID:  attachment.ParentID,
				Depth:     attachment.Depth,
				EntityID:  attachment.EntityID,
				Type:      attachment.Type,
				Name:      attachment.Name,
				Path:      attachment.Path,
				Bucket:    attachment.Bucket,
				Size:      attachment.Size,
				MimeType:  attachment.MimeType,
				Source:    attachment.Source,
				Metadata:  attachment.Metadata,
				Content:   attachment.Content,
				CreatedAt: attachment.CreatedAt.Time(),
				UpdatedAt: attachment.UpdatedAt.Time(),
			})
		}
	}

	return res
}

// NewElasticsearchTime converts a standard time to ElasticsearchTime.
func elasticsearchIndexDefinition() map[string]any {
	return map[string]any{
		"mappings": map[string]any{
			"dynamic": false,
			"properties": map[string]any{
				"id":                map[string]any{"type": "keyword"},
				"file_id":           map[string]any{"type": "integer"},
				"owner_id":          map[string]any{"type": "integer"},
				"entity_id":         map[string]any{"type": "integer"},
				"parent_id":         map[string]any{"type": "integer"},
				"file_name":         textWithKeywordMapping(),
				"file_ext":          map[string]any{"type": "keyword"},
				"file_type":         map[string]any{"type": "integer"},
				"size":              map[string]any{"type": "long"},
				"created_at":        elasticsearchDateMapping(),
				"updated_at":        elasticsearchDateMapping(),
				"is_symbolic":       map[string]any{"type": "boolean"},
				"shared":            map[string]any{"type": "boolean"},
				"tree_path":         map[string]any{"type": "keyword"},
				"storage_policy_id": map[string]any{"type": "integer"},
				"storage_type":      map[string]any{"type": "keyword"},
				"storage_bucket":    textWithKeywordMapping(),
				"metadata":          map[string]any{"type": "flattened"},
				"metadata_text":     map[string]any{"type": "text"},
				"props":             map[string]any{"type": "flattened"},
				"path_text":         map[string]any{"type": "text"},
				"content":           map[string]any{"type": "text"},
				"snapshot_version":  map[string]any{"type": "integer"},
				"synchronized_at":   elasticsearchDateMapping(),
				"latest_version": map[string]any{
					"properties": map[string]any{
						"id":                map[string]any{"type": "keyword"},
						"entity_id":         map[string]any{"type": "integer"},
						"entity_type":       map[string]any{"type": "keyword"},
						"entity_type_value": map[string]any{"type": "integer"},
						"source":            textWithKeywordMapping(),
						"size":              map[string]any{"type": "long"},
						"created_at":        elasticsearchDateMapping(),
						"updated_at":        elasticsearchDateMapping(),
						"storage_policy_id": map[string]any{"type": "integer"},
						"storage_type":      map[string]any{"type": "keyword"},
						"bucket":            textWithKeywordMapping(),
						"mime_type":         map[string]any{"type": "keyword"},
						"reference_count":   map[string]any{"type": "integer"},
						"encrypted":         map[string]any{"type": "boolean"},
						"props":             map[string]any{"type": "flattened"},
					},
				},
				"attachments": map[string]any{
					"properties": map[string]any{
						"id":         map[string]any{"type": "keyword"},
						"parent_id":  map[string]any{"type": "keyword"},
						"depth":      map[string]any{"type": "integer"},
						"entity_id":  map[string]any{"type": "integer"},
						"type":       map[string]any{"type": "keyword"},
						"name":       textWithKeywordMapping(),
						"path":       textWithKeywordMapping(),
						"bucket":     textWithKeywordMapping(),
						"size":       map[string]any{"type": "long"},
						"mime_type":  map[string]any{"type": "keyword"},
						"source":     textWithKeywordMapping(),
						"metadata":   map[string]any{"type": "flattened"},
						"content":    map[string]any{"type": "text"},
						"created_at": elasticsearchDateMapping(),
						"updated_at": elasticsearchDateMapping(),
					},
				},
			},
		},
	}
}

func elasticsearchDateMapping() map[string]any {
	return map[string]any{
		"type":   "date",
		"format": util.DateTimeSecondFormat,
	}
}

func textWithKeywordMapping() map[string]any {
	return map[string]any{
		"type": "text",
		"fields": map[string]any{
			"keyword": map[string]any{
				"type":         "keyword",
				"ignore_above": 1024,
			},
		},
	}
}

func parseElasticsearchError(prefix, status string, bodyReader io.Reader) error {
	body, _ := io.ReadAll(bodyReader)
	if len(body) == 0 {
		return fmt.Errorf("%s: status=%s", prefix, status)
	}

	return fmt.Errorf("%s: status=%s body=%s", prefix, status, strings.TrimSpace(string(body)))
}
