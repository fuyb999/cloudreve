package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ent "github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/metadata"
	"github.com/cloudreve/Cloudreve/v4/ent/schema"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	inventorytypes "github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	searchindexer "github.com/cloudreve/Cloudreve/v4/pkg/searcher/indexer"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	elasticsearch "github.com/elastic/go-elasticsearch/v8"
	"gopkg.in/yaml.v3"
)

const (
	defaultSearchSyncBatchSize   = 500
	defaultSearchScrollKeepAlive = "2m"
	defaultSearchSnapshotVersion = 2
	defaultSearchMarkerBatchSize = 1000
	elasticsearchDateTimeLayout  = "2006-01-02 15:04:05"
)

var authoritativeSearchFieldPaths = []string{
	"id",
	"file_id",
	"owner_id",
	"entity_id",
	"parent_id",
	"file_name",
	"file_ext",
	"file_type",
	"size",
	"created_at",
	"updated_at",
	"is_symbolic",
	"shared",
	"tree_path",
	"storage_policy_id",
	"storage_type",
	"storage_bucket",
	"snapshot_version",
	"synchronized_at",
	"latest_version.id",
	"latest_version.entity_id",
	"latest_version.entity_type",
	"latest_version.entity_type_value",
	"latest_version.source",
	"latest_version.size",
	"latest_version.created_at",
	"latest_version.updated_at",
	"latest_version.storage_policy_id",
	"latest_version.storage_type",
	"latest_version.bucket",
	"latest_version.reference_count",
	"latest_version.encrypted",
	"latest_version.props",
}

type searchSyncConfig struct {
	Enabled             bool                              `yaml:"enabled"`
	ContinueOnError     *bool                             `yaml:"continue_on_error"`
	EnsureTargetIndex   *bool                             `yaml:"ensure_target_index"`
	MarkIndexedMetadata *bool                             `yaml:"mark_indexed_metadata"`
	BatchSize           int                               `yaml:"batch_size"`
	ScrollKeepAlive     string                            `yaml:"scroll_keep_alive"`
	Source              searchSyncEndpointConfig          `yaml:"source"`
	Target              searchSyncEndpointConfig          `yaml:"target"`
	MatchRules          []searchSyncMatchRule             `yaml:"match_rules"`
	FieldMappings       map[string]searchSyncFieldMapping `yaml:"field_mappings"`
	Defaults            map[string]any                    `yaml:"defaults"`
}

type searchSyncEndpointConfig struct {
	Endpoint      string         `yaml:"endpoint"`
	CloudID       string         `yaml:"cloud_id"`
	APIKey        string         `yaml:"api_key"`
	Username      string         `yaml:"username"`
	Password      string         `yaml:"password"`
	Index         string         `yaml:"index"`
	SkipTLSVerify bool           `yaml:"skip_tls_verify"`
	Query         map[string]any `yaml:"query"`
}

type searchSyncMatchRule struct {
	Fields map[string]string `yaml:"fields"`
}

type searchSyncFieldMapping struct {
	From     string                            `yaml:"from"`
	Fallback []string                          `yaml:"fallback"`
	Value    any                               `yaml:"value"`
	Type     string                            `yaml:"type"`
	Fields   map[string]searchSyncFieldMapping `yaml:"fields"`
}

type sourceSearchHit struct {
	ID     string
	Index  string
	Source map[string]any
}

type sourceSearchResponse struct {
	ScrollID string `json:"_scroll_id"`
	Hits     struct {
		Hits []struct {
			ID     string         `json:"_id"`
			Index  string         `json:"_index"`
			Source map[string]any `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
}

type bulkIndexResponse struct {
	Errors bool `json:"errors"`
	Items  []struct {
		Index struct {
			ID     string `json:"_id"`
			Status int    `json:"status"`
			Error  struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			} `json:"error"`
		} `json:"index"`
	} `json:"items"`
}

type searchMarkerRecord struct {
	FileID int
	Fields map[string]string
}

type searchMatchLookup struct {
	rules     []searchSyncMatchRule
	unique    []map[string]searchMarkerRecord
	ambiguous []map[string]struct{}
}

type bulkTargetDoc struct {
	DocID    string
	FileID   int
	EntityID int
	Body     map[string]any
}

func (m *searchSyncFieldMapping) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	if value.Kind == yaml.ScalarNode {
		var from string
		if err := value.Decode(&from); err != nil {
			return err
		}
		m.From = strings.TrimSpace(from)
		return nil
	}

	type raw searchSyncFieldMapping
	var decoded raw
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*m = searchSyncFieldMapping(decoded)
	m.normalize()
	return nil
}

func (c *searchSyncConfig) normalize() {
	c.Source.Endpoint = strings.TrimSpace(c.Source.Endpoint)
	c.Source.CloudID = strings.TrimSpace(c.Source.CloudID)
	c.Source.APIKey = strings.TrimSpace(c.Source.APIKey)
	c.Source.Username = strings.TrimSpace(c.Source.Username)
	c.Source.Password = strings.TrimSpace(c.Source.Password)
	c.Source.Index = strings.TrimSpace(c.Source.Index)
	c.Target.Endpoint = strings.TrimSpace(c.Target.Endpoint)
	c.Target.CloudID = strings.TrimSpace(c.Target.CloudID)
	c.Target.APIKey = strings.TrimSpace(c.Target.APIKey)
	c.Target.Username = strings.TrimSpace(c.Target.Username)
	c.Target.Password = strings.TrimSpace(c.Target.Password)
	c.Target.Index = strings.TrimSpace(c.Target.Index)
	c.ScrollKeepAlive = strings.TrimSpace(c.ScrollKeepAlive)

	if c.BatchSize <= 0 {
		c.BatchSize = defaultSearchSyncBatchSize
	}
	if c.ScrollKeepAlive == "" {
		c.ScrollKeepAlive = defaultSearchScrollKeepAlive
	}
	if len(c.MatchRules) == 0 {
		c.MatchRules = []searchSyncMatchRule{
			{Fields: map[string]string{"legacy_id": "file_id"}},
			{Fields: map[string]string{"legacy_id": "_id"}},
		}
	}
	if c.FieldMappings == nil {
		c.FieldMappings = map[string]searchSyncFieldMapping{}
	}
	for targetField, mapping := range defaultSearchSyncFieldMappings() {
		if _, ok := c.FieldMappings[targetField]; !ok {
			c.FieldMappings[targetField] = mapping
		}
	}
	for targetField, mapping := range c.FieldMappings {
		normalizedField := strings.TrimSpace(targetField)
		mapping.normalize()
		delete(c.FieldMappings, targetField)
		if normalizedField != "" {
			c.FieldMappings[normalizedField] = mapping
		}
	}
	for i := range c.MatchRules {
		normalized := make(map[string]string, len(c.MatchRules[i].Fields))
		for markerField, sourceField := range c.MatchRules[i].Fields {
			markerField = strings.TrimSpace(markerField)
			sourceField = strings.TrimSpace(sourceField)
			if markerField == "" || sourceField == "" {
				continue
			}
			normalized[markerField] = sourceField
		}
		c.MatchRules[i].Fields = normalized
	}
}

func (c searchSyncConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Source.Endpoint == "" && c.Source.CloudID == "" {
		return fmt.Errorf("search_sync.source.endpoint or search_sync.source.cloud_id is required when search_sync.enabled=true")
	}
	if c.Source.Index == "" {
		return fmt.Errorf("search_sync.source.index is required when search_sync.enabled=true")
	}
	if c.Target.Endpoint == "" && c.Target.CloudID == "" {
		return fmt.Errorf("search_sync.target.endpoint or search_sync.target.cloud_id is required when search_sync.enabled=true")
	}
	if c.Target.Index == "" {
		return fmt.Errorf("search_sync.target.index is required when search_sync.enabled=true")
	}
	if c.BatchSize <= 0 {
		return fmt.Errorf("search_sync.batch_size must be > 0")
	}
	for i, rule := range c.MatchRules {
		if len(rule.Fields) == 0 {
			return fmt.Errorf("search_sync.match_rules[%d].fields must not be empty", i)
		}
	}
	return nil
}

func (c searchSyncConfig) continueOnErrorEnabled(fallback bool) bool {
	if c.ContinueOnError != nil {
		return *c.ContinueOnError
	}
	return fallback
}

func (c searchSyncConfig) ensureTargetIndexEnabled() bool {
	if c.EnsureTargetIndex != nil {
		return *c.EnsureTargetIndex
	}
	return true
}

func (c searchSyncConfig) markIndexedMetadataEnabled() bool {
	if c.MarkIndexedMetadata != nil {
		return *c.MarkIndexedMetadata
	}
	return true
}

func (m *searchSyncFieldMapping) normalize() {
	m.From = strings.TrimSpace(m.From)
	m.Type = strings.TrimSpace(strings.ToLower(m.Type))
	for i := range m.Fallback {
		m.Fallback[i] = strings.TrimSpace(m.Fallback[i])
	}
	for field, nested := range m.Fields {
		trimmed := strings.TrimSpace(field)
		nested.normalize()
		delete(m.Fields, field)
		if trimmed != "" {
			m.Fields[trimmed] = nested
		}
	}
}

func defaultSearchSyncFieldMappings() map[string]searchSyncFieldMapping {
	return map[string]searchSyncFieldMapping{
		"content":                  {From: "content"},
		"metadata_text":            {From: "metadata_text"},
		"path_text":                {From: "path_text"},
		"attachments":              {From: "attachments"},
		"latest_version.mime_type": {From: "latest_version.mime_type"},
	}
}

func (m *migrator) runSearchSync(ctx context.Context) error {
	cfg := m.cfg.SearchSync
	m.l.Info("Starting Elasticsearch sync. dry_run=%v batch_size=%d continue_on_error=%v", m.cfg.Migration.DryRun, cfg.BatchSize, cfg.continueOnErrorEnabled(m.cfg.Migration.ContinueOnError))

	lookup, err := m.loadSearchMatchLookup(ctx)
	if err != nil {
		return err
	}

	sourceClient, err := newSearchSyncElasticsearchClient(cfg.Source)
	if err != nil {
		return fmt.Errorf("create search_sync source client: %w", err)
	}
	targetClient, err := newSearchSyncElasticsearchClient(cfg.Target)
	if err != nil {
		return fmt.Errorf("create search_sync target client: %w", err)
	}

	if !m.cfg.Migration.DryRun && cfg.ensureTargetIndexEnabled() {
		if err := m.ensureSearchSyncTargetIndex(ctx, cfg.Target); err != nil {
			return err
		}
	}

	hits, scrollID, err := openSearchSyncScroll(ctx, sourceClient, cfg.Source, cfg.BatchSize, cfg.ScrollKeepAlive)
	if err != nil {
		return err
	}
	defer func() {
		closeSearchSyncScroll(context.Background(), sourceClient, scrollID)
	}()

	for {
		if len(hits) == 0 {
			return nil
		}

		batchDocs := make([]bulkTargetDoc, 0, len(hits))
		for _, hit := range hits {
			m.stats.ESSourceDocs++

			record, found, ambiguous := lookup.resolve(hit)
			if ambiguous {
				err := fmt.Errorf("source ES doc _id=%q matched multiple migrated files", hit.ID)
				if hErr := m.handleSearchSyncError(err); hErr != nil {
					return hErr
				}
				continue
			}
			if !found {
				m.stats.ESSkippedDocs++
				m.l.Warning("Skip source ES doc _id=%s: no migrated file match", hit.ID)
				continue
			}

			body, fileID, entityID, err := m.buildSearchSyncDocument(ctx, record, hit)
			if err != nil {
				if hErr := m.handleSearchSyncError(fmt.Errorf("build target search doc for source _id=%q: %w", hit.ID, err)); hErr != nil {
					return hErr
				}
				continue
			}

			m.stats.ESMatchedDocs++
			batchDocs = append(batchDocs, bulkTargetDoc{
				DocID:    fmt.Sprintf("%d", fileID),
				FileID:   fileID,
				EntityID: entityID,
				Body:     body,
			})
		}

		if m.cfg.Migration.DryRun {
			m.l.Info("DRY RUN search_sync batch source_docs=%d matched=%d", len(hits), len(batchDocs))
		} else if len(batchDocs) > 0 {
			successes, err := m.bulkIndexSearchSyncDocuments(ctx, targetClient, cfg.Target.Index, batchDocs)
			if err != nil {
				if hErr := m.handleSearchSyncError(err); hErr != nil {
					return hErr
				}
			}
			m.stats.ESWrittenDocs += len(successes)
			if cfg.markIndexedMetadataEnabled() {
				m.markSearchSyncDocumentsIndexed(ctx, successes)
			}
		}

		hits, scrollID, err = continueSearchSyncScroll(ctx, sourceClient, scrollID, cfg.ScrollKeepAlive)
		if err != nil {
			return err
		}
	}
}

func (m *migrator) handleSearchSyncError(err error) error {
	m.stats.ESFailedDocs++
	m.l.Error("search_sync failed: %v", err)
	if !m.cfg.SearchSync.continueOnErrorEnabled(m.cfg.Migration.ContinueOnError) {
		return err
	}
	return nil
}

func (m *migrator) ensureSearchSyncTargetIndex(ctx context.Context, cfg searchSyncEndpointConfig) error {
	indexer, err := searchindexer.NewElasticsearchIndexer(&setting.FTSIndexElasticsearchSetting{
		Endpoint:      cfg.Endpoint,
		CloudID:       cfg.CloudID,
		APIKey:        cfg.APIKey,
		Username:      cfg.Username,
		Password:      cfg.Password,
		Index:         cfg.Index,
		SkipTLSVerify: cfg.SkipTLSVerify,
	}, m.l)
	if err != nil {
		return fmt.Errorf("create target elasticsearch indexer: %w", err)
	}
	if err := indexer.EnsureIndex(ctx); err != nil {
		return fmt.Errorf("ensure target search index: %w", err)
	}
	return nil
}

func (m *migrator) loadSearchMatchLookup(ctx context.Context) (*searchMatchLookup, error) {
	lookup := &searchMatchLookup{
		rules:     m.cfg.SearchSync.MatchRules,
		unique:    make([]map[string]searchMarkerRecord, len(m.cfg.SearchSync.MatchRules)),
		ambiguous: make([]map[string]struct{}, len(m.cfg.SearchSync.MatchRules)),
	}
	for i := range lookup.unique {
		lookup.unique[i] = map[string]searchMarkerRecord{}
		lookup.ambiguous[i] = map[string]struct{}{}
	}

	lastID := 0
	loaded := 0
	for {
		rows, err := m.targetClient.Metadata.Query().
			Where(
				metadata.NameEQ(m.cfg.Migration.MarkerKey),
				metadata.IDGT(lastID),
			).
			Order(ent.Asc(metadata.FieldID)).
			Limit(defaultSearchMarkerBatchSize).
			All(schema.SkipSoftDelete(ctx))
		if err != nil {
			return nil, fmt.Errorf("load migration markers: %w", err)
		}
		if len(rows) == 0 {
			break
		}

		for _, row := range rows {
			fields, err := parseSearchMarkerFields(row.Value)
			if err != nil {
				return nil, fmt.Errorf("parse migration marker file_id=%d: %w", row.FileID, err)
			}
			record := searchMarkerRecord{
				FileID: row.FileID,
				Fields: fields,
			}
			lookup.add(record)
			lastID = row.ID
			loaded++
		}
	}

	m.l.Info("Loaded migration markers for search sync. files=%d rules=%d", loaded, len(lookup.rules))
	return lookup, nil
}

func (l *searchMatchLookup) add(record searchMarkerRecord) {
	for i, rule := range l.rules {
		key, ok := buildSearchMarkerMatchKey(rule, record.Fields)
		if !ok {
			continue
		}
		if existing, exists := l.unique[i][key]; exists && existing.FileID != record.FileID {
			delete(l.unique[i], key)
			l.ambiguous[i][key] = struct{}{}
			continue
		}
		if _, ambiguous := l.ambiguous[i][key]; ambiguous {
			continue
		}
		l.unique[i][key] = record
	}
}

func (l *searchMatchLookup) resolve(hit sourceSearchHit) (searchMarkerRecord, bool, bool) {
	for i, rule := range l.rules {
		key, ok := buildSourceSearchMatchKey(rule, hit)
		if !ok {
			continue
		}
		if _, ambiguous := l.ambiguous[i][key]; ambiguous {
			return searchMarkerRecord{}, false, true
		}
		if record, found := l.unique[i][key]; found {
			return record, true, false
		}
	}
	return searchMarkerRecord{}, false, false
}

func buildSearchMarkerMatchKey(rule searchSyncMatchRule, fields map[string]string) (string, bool) {
	if len(rule.Fields) == 0 {
		return "", false
	}

	markerFields := sortedKeys(rule.Fields)
	parts := make([]string, 0, len(markerFields))
	for _, markerField := range markerFields {
		value := strings.TrimSpace(fields[markerField])
		if value == "" {
			return "", false
		}
		parts = append(parts, markerField+"="+value)
	}

	return strings.Join(parts, "\n"), true
}

func buildSourceSearchMatchKey(rule searchSyncMatchRule, hit sourceSearchHit) (string, bool) {
	if len(rule.Fields) == 0 {
		return "", false
	}

	markerFields := sortedKeys(rule.Fields)
	parts := make([]string, 0, len(markerFields))
	for _, markerField := range markerFields {
		value, ok := resolveSearchSourcePath(hit.Source, hit, rule.Fields[markerField])
		if !ok {
			return "", false
		}
		normalized, ok := normalizeSearchMatchValue(markerField, value)
		if !ok {
			return "", false
		}
		parts = append(parts, markerField+"="+normalized)
	}

	return strings.Join(parts, "\n"), true
}

func parseSearchMarkerFields(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}, nil
	}

	var decoded map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}

	fields := map[string]string{}
	for _, name := range []string{
		"legacy_id",
		"scope",
		"target_path",
		"legacy_owner_id",
		"owner_external_user_id",
		"resolved_owner_id",
		"owner_email",
		"owner_username",
		"bucket",
		"object_key",
		"storage_policy_id",
	} {
		value, ok := decoded[name]
		if !ok {
			continue
		}
		normalized, ok := normalizeSearchMatchValue(name, value)
		if ok {
			fields[name] = normalized
		}
	}

	return fields, nil
}

func normalizeSearchMatchValue(name string, value any) (string, bool) {
	raw := strings.TrimSpace(stringifySearchValue(value))
	if raw == "" {
		return "", false
	}

	switch strings.ToLower(strings.TrimSpace(name)) {
	case "scope":
		if normalized, err := normalizeScope(raw); err == nil {
			return normalized, true
		}
		return strings.ToLower(raw), true
	case "target_path":
		if normalized, err := normalizeTargetPath(raw); err == nil {
			return normalized, true
		}
		return raw, true
	case "object_key":
		return normalizeObjectPathPrefix(raw), true
	case "bucket", "legacy_id", "legacy_owner_id", "owner_external_user_id", "owner_email", "owner_username":
		return raw, true
	case "resolved_owner_id", "storage_policy_id":
		return raw, true
	default:
		return raw, true
	}
}

func stringifySearchValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		if typed == float64(int64(typed)) {
			return fmt.Sprintf("%d", int64(typed))
		}
		return fmt.Sprintf("%v", typed)
	case float32:
		if typed == float32(int64(typed)) {
			return fmt.Sprintf("%d", int64(typed))
		}
		return fmt.Sprintf("%v", typed)
	default:
		return fmt.Sprint(typed)
	}
}

func newSearchSyncElasticsearchClient(cfg searchSyncEndpointConfig) (*elasticsearch.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.SkipTLSVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	esCfg := elasticsearch.Config{
		CloudID:   cfg.CloudID,
		APIKey:    cfg.APIKey,
		Username:  cfg.Username,
		Password:  cfg.Password,
		Transport: transport,
	}
	if cfg.Endpoint != "" {
		esCfg.Addresses = []string{cfg.Endpoint}
	}

	client, err := elasticsearch.NewClient(esCfg)
	if err != nil {
		return nil, err
	}

	return client, nil
}

func openSearchSyncScroll(
	ctx context.Context,
	client *elasticsearch.Client,
	cfg searchSyncEndpointConfig,
	batchSize int,
	scrollKeepAlive string,
) ([]sourceSearchHit, string, error) {
	scrollDuration, err := time.ParseDuration(scrollKeepAlive)
	if err != nil {
		return nil, "", fmt.Errorf("invalid search_sync.scroll_keep_alive %q: %w", scrollKeepAlive, err)
	}

	body := map[string]any{
		"size": batchSize,
		"sort": []any{"_doc"},
		"query": map[string]any{
			"match_all": map[string]any{},
		},
	}
	if len(cfg.Query) > 0 {
		body["query"] = cfg.Query
	}

	rawBody, err := json.Marshal(body)
	if err != nil {
		return nil, "", fmt.Errorf("marshal source ES query: %w", err)
	}

	res, err := client.Search(
		client.Search.WithContext(ctx),
		client.Search.WithIndex(cfg.Index),
		client.Search.WithBody(bytes.NewReader(rawBody)),
		client.Search.WithScroll(scrollDuration),
	)
	if err != nil {
		return nil, "", fmt.Errorf("open source ES scroll: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return nil, "", readSearchSyncElasticsearchError("open source ES scroll", res.StatusCode, res.Body)
	}

	parsedHits, parsedScrollID, err := decodeSourceSearchResponse(res.Body)
	if err != nil {
		return nil, "", err
	}

	return parsedHits, parsedScrollID, nil
}

func continueSearchSyncScroll(
	ctx context.Context,
	client *elasticsearch.Client,
	scrollID string,
	scrollKeepAlive string,
) ([]sourceSearchHit, string, error) {
	if strings.TrimSpace(scrollID) == "" {
		return nil, "", nil
	}
	scrollDuration, err := time.ParseDuration(scrollKeepAlive)
	if err != nil {
		return nil, "", fmt.Errorf("invalid search_sync.scroll_keep_alive %q: %w", scrollKeepAlive, err)
	}

	res, err := client.Scroll(
		client.Scroll.WithContext(ctx),
		client.Scroll.WithScrollID(scrollID),
		client.Scroll.WithScroll(scrollDuration),
	)
	if err != nil {
		return nil, "", fmt.Errorf("continue source ES scroll: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return nil, "", readSearchSyncElasticsearchError("continue source ES scroll", res.StatusCode, res.Body)
	}

	parsedHits, parsedScrollID, err := decodeSourceSearchResponse(res.Body)
	if err != nil {
		return nil, "", err
	}

	return parsedHits, parsedScrollID, nil
}

func closeSearchSyncScroll(ctx context.Context, client *elasticsearch.Client, scrollID string) {
	if client == nil || strings.TrimSpace(scrollID) == "" {
		return
	}

	res, err := client.ClearScroll(
		client.ClearScroll.WithContext(ctx),
		client.ClearScroll.WithScrollID(scrollID),
	)
	if err != nil {
		return
	}
	defer res.Body.Close()
}

func decodeSourceSearchResponse(body io.Reader) ([]sourceSearchHit, string, error) {
	var decoded sourceSearchResponse
	decoder := json.NewDecoder(body)
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, "", fmt.Errorf("decode source ES response: %w", err)
	}

	hits := make([]sourceSearchHit, 0, len(decoded.Hits.Hits))
	for _, hit := range decoded.Hits.Hits {
		hits = append(hits, sourceSearchHit{
			ID:     hit.ID,
			Index:  hit.Index,
			Source: hit.Source,
		})
	}

	return hits, decoded.ScrollID, nil
}

func readSearchSyncElasticsearchError(prefix string, statusCode int, body io.Reader) error {
	payload, _ := io.ReadAll(body)
	if len(payload) == 0 {
		return fmt.Errorf("%s: status=%d", prefix, statusCode)
	}
	return fmt.Errorf("%s: status=%d body=%s", prefix, statusCode, strings.TrimSpace(string(payload)))
}

func (m *migrator) buildSearchSyncDocument(ctx context.Context, record searchMarkerRecord, hit sourceSearchHit) (map[string]any, int, int, error) {
	authoritative, fileID, entityID, err := m.buildAuthoritativeSearchDocument(ctx, record.FileID, record.Fields)
	if err != nil {
		return nil, 0, 0, err
	}

	doc := map[string]any{}
	if defaults, ok := deepCopyMap(m.cfg.SearchSync.Defaults); ok {
		doc = defaults
	}
	deepMergeMap(doc, authoritative)

	for targetField, mapping := range m.cfg.SearchSync.FieldMappings {
		value, ok, err := resolveSearchFieldMapping(mapping, hit.Source, hit)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("resolve field mapping %q: %w", targetField, err)
		}
		if !ok {
			continue
		}
		setNestedValue(doc, targetField, value)
	}
	applyAuthoritativeSearchFields(doc, authoritative)

	return doc, fileID, entityID, nil
}

func (m *migrator) buildAuthoritativeSearchDocument(ctx context.Context, fileID int, markerFields map[string]string) (map[string]any, int, int, error) {
	loadCtx := context.WithValue(ctx, inventory.LoadFileEntity{}, true)
	loadCtx = context.WithValue(loadCtx, inventory.LoadFileMetadata{}, true)
	loadCtx = context.WithValue(loadCtx, inventory.LoadFileShare{}, true)
	loadCtx = context.WithValue(loadCtx, inventory.LoadEntityStoragePolicy{}, true)

	fileModel, err := m.fileClient.GetByID(loadCtx, fileID)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("load file %d: %w", fileID, err)
	}
	if fileModel.Type != int(inventorytypes.FileTypeFile) {
		return nil, 0, 0, fmt.Errorf("file %d is not indexable type=%d", fileID, fileModel.Type)
	}

	metadataMap := map[string]string{}
	for _, item := range fileModel.Edges.Metadata {
		if item == nil {
			continue
		}
		metadataMap[item.Name] = item.Value
	}

	filePolicy, err := m.searchPolicyByID(ctx, fileModel.StoragePolicyFiles)
	if err != nil {
		return nil, 0, 0, err
	}

	primaryEntity := selectPrimaryEntity(fileModel)
	entityPolicy := filePolicy
	if primaryEntity != nil && primaryEntity.Edges.StoragePolicy != nil {
		entityPolicy = primaryEntity.Edges.StoragePolicy
	}

	doc := map[string]any{
		"id":                fmt.Sprintf("%d", fileModel.ID),
		"file_id":           fileModel.ID,
		"owner_id":          fileModel.OwnerID,
		"entity_id":         fileModel.PrimaryEntity,
		"parent_id":         fileModel.FileChildren,
		"file_name":         fileModel.Name,
		"file_ext":          firstNonEmptyString(fileModel.FileExt, strings.TrimPrefix(filepath.Ext(fileModel.Name), ".")),
		"file_type":         fileModel.Type,
		"size":              fileModel.Size,
		"is_symbolic":       fileModel.IsSymbolic,
		"shared":            len(fileModel.Edges.Shares) > 0,
		"tree_path":         fileModel.TreePath,
		"storage_policy_id": fileModel.StoragePolicyFiles,
		"metadata":          metadataMap,
		"metadata_text":     joinSearchMetadata(metadataMap),
		"props":             searchMapFromAny(fileModel.Props),
		"snapshot_version":  defaultSearchSnapshotVersion,
		"synchronized_at":   formatSearchDate(time.Now()),
	}
	if createdAt := formatSearchDate(fileModel.CreatedAt); createdAt != "" {
		doc["created_at"] = createdAt
	}
	if updatedAt := formatSearchDate(fileModel.UpdatedAt); updatedAt != "" {
		doc["updated_at"] = updatedAt
	}
	if pathText := strings.TrimSpace(markerFields["target_path"]); pathText != "" {
		doc["path_text"] = pathText
	}
	if filePolicy != nil {
		doc["storage_type"] = filePolicy.Type
		doc["storage_bucket"] = filePolicy.BucketName
		if fileModel.StoragePolicyFiles == 0 {
			doc["storage_policy_id"] = filePolicy.ID
		}
	}

	if latestVersion := buildAuthoritativeSearchVersion(primaryEntity, fileModel.Name, entityPolicy); len(latestVersion) > 0 {
		doc["latest_version"] = latestVersion
		if policyID, ok := latestVersion["storage_policy_id"]; ok {
			doc["storage_policy_id"] = policyID
		}
		if storageType, ok := latestVersion["storage_type"]; ok {
			doc["storage_type"] = storageType
		}
		if bucket, ok := latestVersion["bucket"]; ok {
			doc["storage_bucket"] = bucket
		}
	}

	return doc, fileModel.ID, fileModel.PrimaryEntity, nil
}

func (m *migrator) searchPolicyByID(ctx context.Context, id int) (*ent.StoragePolicy, error) {
	if id <= 0 {
		return nil, nil
	}
	if cached, ok := m.policyByID[id]; ok {
		return cached, nil
	}
	policy, err := m.policyClient.GetPolicyByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load storage policy %d: %w", id, err)
	}
	m.policyByID[id] = policy
	return policy, nil
}

func buildAuthoritativeSearchVersion(entity *ent.Entity, fileName string, policy *ent.StoragePolicy) map[string]any {
	if entity == nil {
		return nil
	}

	doc := map[string]any{
		"id":                fmt.Sprintf("%d", entity.ID),
		"entity_id":         entity.ID,
		"entity_type":       searchEntityTypeString(inventorytypes.EntityType(entity.Type)),
		"entity_type_value": entity.Type,
		"source":            entity.Source,
		"size":              entity.Size,
		"storage_policy_id": entity.StoragePolicyEntities,
		"reference_count":   entity.ReferenceCount,
		"encrypted":         entity.Props != nil && entity.Props.EncryptMetadata != nil,
		"props":             searchMapFromAny(entity.Props),
		"mime_type":         mime.TypeByExtension(filepath.Ext(fileName)),
	}
	if createdAt := formatSearchDate(entity.CreatedAt); createdAt != "" {
		doc["created_at"] = createdAt
	}
	if updatedAt := formatSearchDate(entity.UpdatedAt); updatedAt != "" {
		doc["updated_at"] = updatedAt
	}
	if policy != nil {
		doc["storage_type"] = policy.Type
		doc["bucket"] = policy.BucketName
		if entity.StoragePolicyEntities == 0 {
			doc["storage_policy_id"] = policy.ID
		}
	}

	return doc
}

func resolveSearchFieldMapping(mapping searchSyncFieldMapping, current any, hit sourceSearchHit) (any, bool, error) {
	if len(mapping.Fields) > 0 {
		base := current
		if sourceValue, ok, err := resolveSearchFieldMappingRaw(mapping, current, hit); err != nil {
			return nil, false, err
		} else if ok {
			base = sourceValue
		}
		if base == nil {
			return nil, false, nil
		}

		switch typed := base.(type) {
		case map[string]any:
			obj, ok, err := resolveSearchFieldMappingObject(mapping.Fields, typed, hit)
			return obj, ok, err
		case []any:
			out := make([]any, 0, len(typed))
			for _, item := range typed {
				obj, ok, err := resolveSearchFieldMappingObject(mapping.Fields, item, hit)
				if err != nil {
					return nil, false, err
				}
				if ok {
					out = append(out, obj)
				}
			}
			if len(out) == 0 {
				return nil, false, nil
			}
			return out, true, nil
		default:
			return nil, false, fmt.Errorf("expected object/array mapping input, got %T", base)
		}
	}

	raw, ok, err := resolveSearchFieldMappingRaw(mapping, current, hit)
	if err != nil || !ok {
		return nil, ok, err
	}
	coerced, err := coerceSearchFieldValue(raw, mapping.Type)
	if err != nil {
		return nil, false, err
	}
	return coerced, true, nil
}

func resolveSearchFieldMappingObject(
	fields map[string]searchSyncFieldMapping,
	current any,
	hit sourceSearchHit,
) (map[string]any, bool, error) {
	obj := map[string]any{}
	applied := false
	for targetField, mapping := range fields {
		value, ok, err := resolveSearchFieldMapping(mapping, current, hit)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			continue
		}
		applied = true
		setNestedValue(obj, targetField, value)
	}
	return obj, applied, nil
}

func resolveSearchFieldMappingRaw(mapping searchSyncFieldMapping, current any, hit sourceSearchHit) (any, bool, error) {
	if mapping.Value != nil {
		return deepCopyValue(mapping.Value), true, nil
	}
	if mapping.From != "" {
		if value, ok := resolveSearchSourcePath(current, hit, mapping.From); ok {
			return value, true, nil
		}
	}
	for _, fallback := range mapping.Fallback {
		if fallback == "" {
			continue
		}
		if value, ok := resolveSearchSourcePath(current, hit, fallback); ok {
			return value, true, nil
		}
	}
	return nil, false, nil
}

func resolveSearchSourcePath(current any, hit sourceSearchHit, path string) (any, bool) {
	path = strings.TrimSpace(path)
	switch path {
	case "":
		if current == nil {
			return nil, false
		}
		return current, true
	case "_id":
		return hit.ID, strings.TrimSpace(hit.ID) != ""
	case "_index":
		return hit.Index, strings.TrimSpace(hit.Index) != ""
	case "$":
		return hit.Source, hit.Source != nil
	}

	if strings.HasPrefix(path, "$.") {
		return resolveSearchSourcePath(hit.Source, hit, strings.TrimPrefix(path, "$."))
	}

	value := current
	for _, segment := range strings.Split(path, ".") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return nil, false
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := object[segment]
		if !ok {
			return nil, false
		}
		value = next
	}

	if value == nil {
		return nil, false
	}
	return value, true
}

func coerceSearchFieldValue(value any, valueType string) (any, error) {
	switch valueType {
	case "", "raw":
		return deepCopyValue(value), nil
	case "string":
		return strings.TrimSpace(stringifySearchValue(value)), nil
	case "int":
		parsed, ok := firstInt(map[string]any{"value": value}, "value")
		if !ok {
			return nil, fmt.Errorf("cannot convert %T to int", value)
		}
		return parsed, nil
	case "int64":
		parsed, ok := firstInt64(map[string]any{"value": value}, "value")
		if !ok {
			return nil, fmt.Errorf("cannot convert %T to int64", value)
		}
		return parsed, nil
	case "bool":
		parsed, ok := firstBool(map[string]any{"value": value}, "value")
		if !ok {
			return nil, fmt.Errorf("cannot convert %T to bool", value)
		}
		return parsed, nil
	case "datetime":
		if formatted := formatSearchAnyDate(value); formatted != "" {
			return formatted, nil
		}
		return nil, fmt.Errorf("cannot convert %T to datetime", value)
	case "map_string_string":
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cannot convert %T to map[string]string", value)
		}
		result := make(map[string]string, len(object))
		for key, item := range object {
			result[key] = stringifySearchValue(item)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported search_sync field mapping type %q", valueType)
	}
}

func applyAuthoritativeSearchFields(target map[string]any, authoritative map[string]any) {
	for _, fieldPath := range authoritativeSearchFieldPaths {
		value, ok := getNestedValue(authoritative, fieldPath)
		if !ok {
			continue
		}
		setNestedValue(target, fieldPath, deepCopyValue(value))
	}
}

func deepMergeMap(dst, src map[string]any) {
	for key, value := range src {
		srcMap, srcIsMap := value.(map[string]any)
		if !srcIsMap {
			dst[key] = deepCopyValue(value)
			continue
		}

		current, ok := dst[key].(map[string]any)
		if !ok {
			dst[key] = deepCopyValue(srcMap)
			continue
		}
		deepMergeMap(current, srcMap)
	}
}

func setNestedValue(target map[string]any, path string, value any) {
	segments := strings.Split(path, ".")
	current := target
	for i, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return
		}
		if i == len(segments)-1 {
			current[segment] = deepCopyValue(value)
			return
		}
		next, ok := current[segment].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[segment] = next
		}
		current = next
	}
}

func getNestedValue(target map[string]any, path string) (any, bool) {
	value := any(target)
	for _, segment := range strings.Split(path, ".") {
		segment = strings.TrimSpace(segment)
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := object[segment]
		if !ok {
			return nil, false
		}
		value = next
	}
	return value, true
}

func deepCopyMap(input map[string]any) (map[string]any, bool) {
	if input == nil {
		return nil, false
	}
	copied, ok := deepCopyValue(input).(map[string]any)
	if !ok {
		return nil, false
	}
	return copied, true
}

func deepCopyValue(value any) any {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var copied any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&copied); err != nil {
		return value
	}
	return copied
}

func selectPrimaryEntity(fileModel *ent.File) *ent.Entity {
	if fileModel == nil {
		return nil
	}
	for _, entity := range fileModel.Edges.Entities {
		if entity != nil && entity.ID == fileModel.PrimaryEntity {
			return entity
		}
	}
	return nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func joinSearchMetadata(metadata map[string]string) string {
	if len(metadata) == 0 {
		return ""
	}

	keys := sortedKeys(metadata)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+": "+metadata[key])
	}
	return strings.Join(parts, "\n")
}

func searchMapFromAny(value any) map[string]any {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	result := make(map[string]any)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil
	}
	return result
}

func formatSearchDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(elasticsearchDateTimeLayout)
}

func formatSearchAnyDate(value any) string {
	switch typed := value.(type) {
	case time.Time:
		return formatSearchDate(typed)
	case string:
		if parsed, ok := firstTime(map[string]any{"value": typed}, "value"); ok && parsed != nil {
			return formatSearchDate(*parsed)
		}
	case float64, float32, int, int64, uint64:
		if parsed, ok := firstTime(map[string]any{"value": stringifySearchValue(typed)}, "value"); ok && parsed != nil {
			return formatSearchDate(*parsed)
		}
	}
	return ""
}

func searchEntityTypeString(entityType inventorytypes.EntityType) string {
	switch entityType {
	case inventorytypes.EntityTypeThumbnail:
		return "thumbnail"
	case inventorytypes.EntityTypeLivePhoto:
		return "live_photo"
	default:
		return "version"
	}
}

func sortedKeys[T any](input map[string]T) []string {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (m *migrator) bulkIndexSearchSyncDocuments(
	ctx context.Context,
	client *elasticsearch.Client,
	index string,
	docs []bulkTargetDoc,
) ([]bulkTargetDoc, error) {
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	for _, doc := range docs {
		if err := encoder.Encode(map[string]any{
			"index": map[string]any{
				"_index": index,
				"_id":    doc.DocID,
			},
		}); err != nil {
			return nil, fmt.Errorf("encode bulk action: %w", err)
		}
		if err := encoder.Encode(doc.Body); err != nil {
			return nil, fmt.Errorf("encode bulk document: %w", err)
		}
	}

	res, err := client.Bulk(bytes.NewReader(body.Bytes()), client.Bulk.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("bulk index search_sync documents: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return nil, readSearchSyncElasticsearchError("bulk index search_sync documents", res.StatusCode, res.Body)
	}

	var decoded bulkIndexResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode bulk response: %w", err)
	}
	if !decoded.Errors {
		return docs, nil
	}

	successes := make([]bulkTargetDoc, 0, len(docs))
	failures := make([]string, 0)
	for i, item := range decoded.Items {
		if i >= len(docs) {
			break
		}
		if item.Index.Status >= 200 && item.Index.Status < 300 {
			successes = append(successes, docs[i])
			continue
		}
		m.stats.ESFailedDocs++
		failures = append(failures, fmt.Sprintf("_id=%s status=%d type=%s reason=%s",
			item.Index.ID,
			item.Index.Status,
			item.Index.Error.Type,
			item.Index.Error.Reason,
		))
	}

	if len(failures) > 0 {
		m.l.Warning("search_sync bulk partial failure: %s", strings.Join(failures, "; "))
		return successes, fmt.Errorf("bulk index search_sync documents partially failed: %s", strings.Join(failures, "; "))
	}

	return successes, nil
}

func (m *migrator) markSearchSyncDocumentsIndexed(ctx context.Context, docs []bulkTargetDoc) {
	for _, doc := range docs {
		if err := m.fileClient.UpsertMetadata(ctx, &ent.File{ID: doc.FileID}, map[string]string{
			dbfs.FullTextIndexKey: dbfs.BuildFullTextIndexMetadataValue(m.hasher, doc.FileID, doc.EntityID),
		}, nil); err != nil {
			m.l.Warning("Failed to mark file %d as indexed after search_sync: %s", doc.FileID, err)
		}
	}
}
