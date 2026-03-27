package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/meilisearch/meilisearch-go"
)

const (
	indexName         = "cloudreve_files"
	embedderName      = "cr-text"
	embeddingTemplate = "Chunk #{{doc.chunk_idx}} in a file named '{{doc.file_name}}': {{ doc.text }}"
)

type meilisearchDocument struct {
	ID       string                   `json:"id"`
	FileID   int                      `json:"file_id"`
	OwnerID  int                      `json:"owner_id"`
	EntityID int                      `json:"entity_id"`
	TreePath string                   `json:"tree_path,omitempty"`
	ChunkIdx int                      `json:"chunk_idx"`
	FileName string                   `json:"file_name"`
	Text     string                   `json:"text"`
	Formated *meilisearchFormattedHit `json:"_formatted,omitempty"`
}

type meilisearchFormattedHit struct {
	Text string `json:"text"`
}

// MeilisearchIndexer implements SearchIndexer using Meilisearch.
type MeilisearchIndexer struct {
	client    meilisearch.ServiceManager
	l         logging.Logger
	pageSize  int
	chunkSize int
	cfg       *setting.FTSIndexMeilisearchSetting
}

// NewMeilisearchIndexer creates a new MeilisearchIndexer.
func NewMeilisearchIndexer(msCfg *setting.FTSIndexMeilisearchSetting, chunkSize int, l logging.Logger) *MeilisearchIndexer {
	client := meilisearch.New(msCfg.Endpoint, meilisearch.WithAPIKey(msCfg.APIKey))
	return &MeilisearchIndexer{
		client:    client,
		l:         l,
		pageSize:  msCfg.PageSize,
		chunkSize: chunkSize,
		cfg:       msCfg,
	}
}

var (
	requiredFilterable = []string{"owner_id", "file_id", "entity_id", "tree_path"}
	requiredSearchable = []string{"text", "file_name"}
	requiredDistinct   = "file_id"
)

func (m *MeilisearchIndexer) IndexReady(ctx context.Context) (bool, error) {
	index := m.client.Index(indexName)

	settings, err := index.GetSettingsWithContext(ctx)
	if err != nil {
		return false, nil
	}

	for _, attr := range requiredFilterable {
		if !slices.Contains(settings.FilterableAttributes, attr) {
			return false, nil
		}
	}

	for _, attr := range requiredSearchable {
		if !slices.Contains(settings.SearchableAttributes, attr) {
			return false, nil
		}
	}

	if settings.DistinctAttribute == nil || *settings.DistinctAttribute != requiredDistinct {
		return false, nil
	}

	if m.cfg.EmbeddingEnbaled {
		if settings.Embedders == nil {
			return false, nil
		}
		if _, ok := settings.Embedders[embedderName]; !ok {
			return false, nil
		}
	}

	return true, nil
}

func (m *MeilisearchIndexer) EnsureIndex(ctx context.Context) error {
	_, err := m.client.CreateIndexWithContext(ctx, &meilisearch.IndexConfig{
		Uid:        indexName,
		PrimaryKey: "id",
	})
	if err != nil {
		m.l.Debug("Create index returned (may already exist): %s", err)
	}

	index := m.client.Index(indexName)

	filterableAttrs := []any{"owner_id", "file_id", "entity_id", "tree_path"}
	if _, err := index.UpdateFilterableAttributesWithContext(ctx, &filterableAttrs); err != nil {
		return fmt.Errorf("failed to set filterable attributes: %w", err)
	}

	searchableAttrs := []string{"text", "file_name"}
	if _, err := index.UpdateSearchableAttributesWithContext(ctx, &searchableAttrs); err != nil {
		return fmt.Errorf("failed to set searchable attributes: %w", err)
	}

	if _, err := index.UpdateDistinctAttributeWithContext(ctx, "file_id"); err != nil {
		return fmt.Errorf("failed to set distinct attribute: %w", err)
	}

	if m.cfg.EmbeddingEnbaled {
		var embedder meilisearch.Embedder
		if err := json.Unmarshal([]byte(m.cfg.EmbeddingSetting), &embedder); err != nil {
			m.cfg.EmbeddingEnbaled = false
			m.l.Warning("Failed to unmarshal embedding setting: %s, fallback to disable embedding", err)
			return nil
		}

		embedder.DocumentTemplate = embeddingTemplate
		_, err := index.UpdateEmbeddersWithContext(ctx, map[string]meilisearch.Embedder{
			embedderName: embedder,
		})
		if err != nil {
			return fmt.Errorf("failed to set embedders: %w", err)
		}
	} else {
		_, err := index.ResetEmbeddersWithContext(ctx)
		if err != nil {
			m.l.Warning("Failed to reset embedder: %w", err)
		}
	}

	return nil
}

func (m *MeilisearchIndexer) UpsertFile(ctx context.Context, doc *searcher.SearchFileDocument) error {
	if doc == nil {
		return nil
	}

	if err := m.DeleteByFileIDs(ctx, doc.FileID); err != nil {
		return err
	}

	docs := m.buildDocuments(doc)
	if len(docs) == 0 {
		return nil
	}

	index := m.client.Index(indexName)
	pk := "id"
	if _, err := index.AddDocumentsWithContext(ctx, docs, &meilisearch.DocumentOptions{PrimaryKey: &pk}); err != nil {
		return fmt.Errorf("failed to add documents: %w", err)
	}

	return nil
}

func (m *MeilisearchIndexer) BulkUpsertFiles(ctx context.Context, docs []*searcher.SearchFileDocument) error {
	if len(docs) == 0 {
		return nil
	}

	ids := make([]int, 0, len(docs))
	meiliDocs := make([]meilisearchDocument, 0)
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		ids = append(ids, doc.FileID)
		meiliDocs = append(meiliDocs, m.buildDocuments(doc)...)
	}

	if err := m.DeleteByFileIDs(ctx, ids...); err != nil {
		return err
	}

	if len(meiliDocs) == 0 {
		return nil
	}

	index := m.client.Index(indexName)
	pk := "id"
	if _, err := index.AddDocumentsWithContext(ctx, meiliDocs, &meilisearch.DocumentOptions{PrimaryKey: &pk}); err != nil {
		return fmt.Errorf("failed to add documents: %w", err)
	}

	return nil
}

func (m *MeilisearchIndexer) DeleteByFileIDs(ctx context.Context, fileID ...int) error {
	if len(fileID) == 0 {
		return nil
	}

	index := m.client.Index(indexName)
	strs := make([]string, len(fileID))
	for i, id := range fileID {
		strs[i] = fmt.Sprintf("%d", id)
	}
	filter := fmt.Sprintf("file_id IN [%s]", strings.Join(strs, ", "))
	if _, err := index.DeleteDocumentsByFilterWithContext(ctx, filter, nil); err != nil {
		return fmt.Errorf("failed to delete documents by file_ids: %w", err)
	}
	return nil
}

func (m *MeilisearchIndexer) Search(ctx context.Context, req *searcher.SearchRequest) ([]searcher.SearchResult, int64, error) {
	index := m.client.Index(indexName)

	filters := make([]string, 0, 2)
	if req != nil && req.OwnerID != nil {
		filters = append(filters, fmt.Sprintf("owner_id = %d", *req.OwnerID))
	}
	if req != nil && req.VisibilityFilter != nil {
		if filter := publicshare.ToMeilisearchFilter(req.VisibilityFilter); filter != "" {
			filters = append(filters, filter)
		}
	}

	offset := 0
	query := ""
	if req != nil {
		offset = req.Offset
		query = req.Query
	}

	searchReq := &meilisearch.SearchRequest{
		Filter:                strings.Join(filters, " AND "),
		Limit:                 int64(m.pageSize),
		Offset:                int64(offset),
		AttributesToHighlight: []string{"text"},
	}

	if m.cfg.EmbeddingEnbaled {
		searchReq.Hybrid = &meilisearch.SearchRequestHybrid{
			Embedder: embedderName,
		}
	}

	resp, err := index.SearchWithContext(ctx, query, searchReq)
	if err != nil {
		return nil, 0, fmt.Errorf("search failed: %w", err)
	}

	results := make([]searcher.SearchResult, 0, len(resp.Hits))
	seen := make(map[int]struct{})
	for _, hit := range resp.Hits {
		var doc meilisearchDocument
		if err := hit.DecodeInto(&doc); err != nil {
			continue
		}

		if _, exists := seen[doc.FileID]; exists {
			continue
		}
		seen[doc.FileID] = struct{}{}

		textStr := doc.Text
		if doc.Formated != nil && doc.Formated.Text != "" {
			textStr = doc.Formated.Text
		}

		results = append(results, searcher.SearchResult{
			FileID:   doc.FileID,
			OwnerID:  doc.OwnerID,
			EntityID: doc.EntityID,
			FileName: doc.FileName,
			Text:     textStr,
		})
	}

	return results, resp.EstimatedTotalHits, nil
}

func (m *MeilisearchIndexer) DeleteAll(ctx context.Context) error {
	index := m.client.Index(indexName)
	if _, err := index.DeleteAllDocumentsWithContext(ctx, nil); err != nil {
		return fmt.Errorf("failed to delete all documents: %w", err)
	}
	return nil
}

func (m *MeilisearchIndexer) Close() error {
	return nil
}

func (m *MeilisearchIndexer) buildDocuments(doc *searcher.SearchFileDocument) []meilisearchDocument {
	searchable := buildSearchableText(doc)
	chunks := ChunkText(searchable, m.chunkSize)
	if len(chunks) == 0 {
		chunks = []string{doc.FileName}
	}

	docs := make([]meilisearchDocument, 0, len(chunks))
	for i, chunk := range chunks {
		docs = append(docs, meilisearchDocument{
			ID:       fmt.Sprintf("%d_%d", doc.FileID, i),
			FileID:   doc.FileID,
			OwnerID:  doc.OwnerID,
			EntityID: doc.EntityID,
			TreePath: doc.TreePath,
			ChunkIdx: i,
			FileName: doc.FileName,
			Text:     chunk,
		})
	}

	return docs
}

func buildSearchableText(doc *searcher.SearchFileDocument) string {
	parts := []string{
		doc.FileName,
		doc.FileExt,
		doc.PathText,
		doc.MetadataText,
		doc.Content,
	}

	if doc.LatestVersion != nil {
		parts = append(parts, doc.LatestVersion.Source, doc.LatestVersion.Bucket, doc.LatestVersion.MimeType)
	}

	for _, attachment := range doc.Attachments {
		parts = append(parts, attachment.Name, attachment.Path, attachment.Content, attachment.Bucket)
	}

	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			filtered = append(filtered, part)
		}
	}

	return strings.Join(filtered, "\n")
}
