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
	elasticsearchMaxRetryShrinkAttempts    = 8
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

	return e.retryUpsertFileDocument(ctx, sanitizeElasticsearchDocument(doc))
}

func (e *ElasticsearchIndexer) upsertFileOnce(ctx context.Context, doc *searcher.SearchFileDocument) error {
	if doc == nil {
		return nil
	}

	body, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("failed to marshal search document: %w", err)
	}

	res, err := e.client.Index(
		e.index,
		bytes.NewReader(body),
		e.client.Index.WithContext(ctx),
		e.client.Index.WithDocumentID(doc.ID),
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

func (e *ElasticsearchIndexer) retryUpsertFileDocument(ctx context.Context, doc *searcher.SearchFileDocument) error {
	if doc == nil {
		return nil
	}

	candidate := cloneSearchFileDocument(doc)
	var lastErr error
	for attempt := 0; attempt < elasticsearchMaxRetryShrinkAttempts; attempt++ {
		lastErr = e.upsertFileOnce(ctx, candidate)
		if lastErr == nil {
			return nil
		}
		if !isElasticsearchOversizedError(lastErr) {
			return lastErr
		}
		if !shrinkElasticsearchDocumentContents(candidate, elasticsearchShrinkRatio(candidate, elasticsearchMaxDocumentPayloadBytes)) {
			return lastErr
		}
	}

	return lastErr
}

func (e *ElasticsearchIndexer) BulkUpsertFiles(ctx context.Context, docs []*searcher.SearchFileDocument) error {
	if len(docs) == 0 {
		return nil
	}

	sanitizedDocs := make([]*searcher.SearchFileDocument, 0, len(docs))
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		sanitizedDocs = append(sanitizedDocs, sanitizeElasticsearchDocument(doc))
	}
	if len(sanitizedDocs) == 0 {
		return nil
	}

	if err := e.bulkUpsertFilesOnce(ctx, sanitizedDocs); err != nil {
		if !isElasticsearchOversizedError(err) {
			return err
		}
		for _, doc := range sanitizedDocs {
			if err := e.retryUpsertFileDocument(ctx, doc); err != nil {
				return err
			}
		}
	}

	return nil
}

func (e *ElasticsearchIndexer) bulkUpsertFilesOnce(ctx context.Context, docs []*searcher.SearchFileDocument) error {
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	for _, doc := range docs {
		if doc == nil {
			continue
		}

		if err := encoder.Encode(map[string]any{
			"index": map[string]any{
				"_index": e.index,
				"_id":    doc.ID,
			},
		}); err != nil {
			return fmt.Errorf("failed to encode bulk action: %w", err)
		}

		if err := encoder.Encode(doc); err != nil {
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
		e.client.DeleteByQuery.WithConflicts("proceed"),
		e.client.DeleteByQuery.WithRefresh(true),
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

func compactSearchBaseURIs(values []string, fallback string) []string {
	res := make([]string, 0, len(values)+1)
	seen := map[string]struct{}{}
	appendValue := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}

		seen[value] = struct{}{}
		res = append(res, value)
	}

	for _, value := range values {
		appendValue(value)
	}
	appendValue(fallback)
	return res
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
	if req != nil {
		searchBaseURIs := compactSearchBaseURIs(req.SearchBaseURIs, req.SearchBaseURI)
		if len(searchBaseURIs) == 1 {
			filters = append(filters, map[string]any{
				"term": map[string]any{
					"search_paths": searchBaseURIs[0],
				},
			})
		} else if len(searchBaseURIs) > 1 {
			filters = append(filters, map[string]any{
				"terms": map[string]any{
					"search_paths": searchBaseURIs,
				},
			})
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

	sanitized := cloneSearchFileDocument(doc)
	if sanitized == nil {
		return nil
	}
	sanitized.Content = truncateUTF8ByBytes(strings.TrimSpace(doc.Content), elasticsearchMaxContentBytes)
	if len(doc.Attachments) > 0 {
		for i := range sanitized.Attachments {
			sanitized.Attachments[i].Content = truncateUTF8ByBytes(
				strings.TrimSpace(sanitized.Attachments[i].Content),
				elasticsearchMaxAttachmentContentBytes,
			)
		}
	}

	shrinkElasticsearchDocumentToLimit(sanitized, elasticsearchMaxDocumentPayloadBytes)
	return sanitized
}

func cloneSearchFileDocument(doc *searcher.SearchFileDocument) *searcher.SearchFileDocument {
	if doc == nil {
		return nil
	}

	cloned := *doc
	if len(doc.Attachments) > 0 {
		cloned.Attachments = append([]searcher.SearchAttachmentDocument(nil), doc.Attachments...)
	}
	return &cloned
}

func elasticsearchDocumentSizeWithinLimit(doc *searcher.SearchFileDocument, maxBytes int) bool {
	if doc == nil || maxBytes <= 0 {
		return true
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		return false
	}

	return len(raw) <= maxBytes
}

func shrinkElasticsearchDocumentToLimit(doc *searcher.SearchFileDocument, maxBytes int) {
	if doc == nil || maxBytes <= 0 {
		return
	}

	for attempt := 0; attempt < elasticsearchMaxRetryShrinkAttempts; attempt++ {
		if elasticsearchDocumentSizeWithinLimit(doc, maxBytes) {
			return
		}
		if !shrinkElasticsearchDocumentContents(doc, elasticsearchShrinkRatio(doc, maxBytes)) {
			return
		}
	}
}

func elasticsearchShrinkRatio(doc *searcher.SearchFileDocument, maxBytes int) float64 {
	if doc == nil || maxBytes <= 0 {
		return 0.8
	}

	raw, err := json.Marshal(doc)
	if err != nil || len(raw) == 0 {
		return 0.8
	}

	ratio := (float64(maxBytes) / float64(len(raw))) * 0.95
	switch {
	case ratio <= 0:
		return 0.1
	case ratio >= 0.95:
		return 0.8
	case ratio < 0.1:
		return 0.1
	default:
		return ratio
	}
}

func shrinkElasticsearchDocumentContents(doc *searcher.SearchFileDocument, ratio float64) bool {
	if doc == nil {
		return false
	}

	if ratio <= 0 {
		ratio = 0.1
	}
	if ratio >= 1 {
		ratio = 0.8
	}

	changed := false
	if trimmed, ok := shrinkUTF8ByRatio(doc.Content, ratio); ok {
		doc.Content = trimmed
		changed = true
	}

	for i := range doc.Attachments {
		if trimmed, ok := shrinkUTF8ByRatio(doc.Attachments[i].Content, ratio); ok {
			doc.Attachments[i].Content = trimmed
			changed = true
		}
	}

	return changed
}

func shrinkUTF8ByRatio(value string, ratio float64) (string, bool) {
	if value == "" {
		return "", false
	}

	maxBytes := int(float64(len(value)) * ratio)
	if maxBytes >= len(value) {
		maxBytes = len(value) - 1
	}
	if maxBytes < 0 {
		maxBytes = 0
	}

	trimmed := truncateUTF8ByBytes(value, maxBytes)
	return trimmed, trimmed != value
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

func isElasticsearchOversizedError(err error) bool {
	if err == nil {
		return false
	}

	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"status=413",
		"payload too large",
		"request entity too large",
		"entity too large",
		"document is larger than the configured max",
		"document contains at least one immense term",
		"source is too large",
		"too_large",
		"max_bytes_length_exceeded_exception",
		"content_too_long",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}

	return false
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
				"owner_uri":         map[string]any{"type": "keyword"},
				"public_uri":        map[string]any{"type": "keyword"},
				"search_uris":       map[string]any{"type": "keyword"},
				"search_paths":      map[string]any{"type": "keyword"},
				"storage_policy_id": map[string]any{"type": "integer"},
				"storage_type":      map[string]any{"type": "keyword"},
				"storage_bucket":    textWithKeywordMapping(),
				"metadata":          map[string]any{"type": "flattened"},
				"metadata_keys":     map[string]any{"type": "keyword"},
				"tags":              map[string]any{"type": "keyword"},
				"custom_props":      map[string]any{"type": "flattened"},
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
