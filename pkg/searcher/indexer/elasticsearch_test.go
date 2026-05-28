package indexer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	elasticsearch "github.com/elastic/go-elasticsearch/v8"
)

func TestSanitizeElasticsearchDocumentTruncatesContentWithoutMutatingSource(t *testing.T) {
	doc := &searcher.SearchFileDocument{
		ID:      "12",
		FileID:  12,
		Content: strings.Repeat("中", elasticsearchMaxContentBytes),
		Attachments: []searcher.SearchAttachmentDocument{
			{
				ID:      "12:embedded:a.txt",
				Content: strings.Repeat("a", elasticsearchMaxAttachmentContentBytes+1024),
			},
		},
	}

	sanitized := sanitizeElasticsearchDocument(doc)
	if sanitized == nil {
		t.Fatal("expected sanitized document")
	}
	if sanitized == doc {
		t.Fatal("expected sanitized document to be cloned")
	}
	if len(sanitized.Content) > elasticsearchMaxContentBytes {
		t.Fatalf("expected content to be truncated to %d bytes, got %d", elasticsearchMaxContentBytes, len(sanitized.Content))
	}
	if len(sanitized.Attachments) != 1 {
		t.Fatalf("unexpected attachment count: %d", len(sanitized.Attachments))
	}
	if len(sanitized.Attachments[0].Content) > elasticsearchMaxAttachmentContentBytes {
		t.Fatalf(
			"expected attachment content to be truncated to %d bytes, got %d",
			elasticsearchMaxAttachmentContentBytes,
			len(sanitized.Attachments[0].Content),
		)
	}
	if len(doc.Attachments[0].Content) != elasticsearchMaxAttachmentContentBytes+1024 {
		t.Fatal("expected source attachment content to stay unchanged")
	}
}

func TestSanitizeElasticsearchDocumentShrinksPayloadWhenDocumentIsTooLarge(t *testing.T) {
	attachments := make([]searcher.SearchAttachmentDocument, 0, 64)
	for i := 0; i < 64; i++ {
		attachments = append(attachments, searcher.SearchAttachmentDocument{
			ID:      strings.Repeat("attachment-", 32) + string(rune('a'+(i%26))),
			Content: strings.Repeat("b", elasticsearchMaxAttachmentContentBytes),
		})
	}

	doc := &searcher.SearchFileDocument{
		ID:          "13",
		FileID:      13,
		Content:     strings.Repeat("x", elasticsearchMaxContentBytes),
		Attachments: attachments,
	}

	sanitized := sanitizeElasticsearchDocument(doc)
	if sanitized == nil {
		t.Fatal("expected sanitized document")
	}
	if !elasticsearchDocumentSizeWithinLimit(sanitized, elasticsearchMaxDocumentPayloadBytes) {
		t.Fatal("expected sanitized document to fit the payload limit")
	}
	keptAttachmentContent := false
	for _, attachment := range sanitized.Attachments {
		if attachment.Content != "" {
			keptAttachmentContent = true
			break
		}
	}
	if !keptAttachmentContent {
		t.Fatal("expected proportional shrinking to retain at least part of attachment content")
	}
}

func TestRetryUpsertFileDocumentShrinksOversizedPayloadAndRetries(t *testing.T) {
	transport := &testElasticsearchTransport{
		statuses: []int{
			http.StatusRequestEntityTooLarge,
			http.StatusCreated,
		},
		bodies: []string{
			`{"error":{"type":"too_large","reason":"payload too large"}}`,
			`{"result":"created"}`,
		},
	}
	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{"http://example.com"},
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("failed to create elasticsearch client: %v", err)
	}

	indexer := &ElasticsearchIndexer{
		client: client,
		index:  elasticsearchDefaultIndexName,
	}
	doc := &searcher.SearchFileDocument{
		ID:      "oversized-doc",
		FileID:  66,
		Content: strings.Repeat("root-content-", 512),
		Attachments: []searcher.SearchAttachmentDocument{
			{ID: "att-1", Content: strings.Repeat("attachment-content-", 2048)},
			{ID: "att-2", Content: strings.Repeat("attachment-content-", 2048)},
		},
	}

	if err := indexer.UpsertFile(context.Background(), doc); err != nil {
		t.Fatalf("expected retrying upsert to succeed, got %v", err)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("expected two index requests, got %d", len(transport.requests))
	}
	if len(transport.requests[1]) >= len(transport.requests[0]) {
		t.Fatalf("expected retry payload to shrink, got first=%d second=%d", len(transport.requests[0]), len(transport.requests[1]))
	}
}

func TestTruncateUTF8ByBytesKeepsValidUTF8(t *testing.T) {
	value := "中文ABC"
	got := truncateUTF8ByBytes(value, 5)
	if got != "中" {
		t.Fatalf("unexpected truncated value: got %q want %q", got, "中")
	}
}

func TestElasticsearchDocumentJSONUsesCustomTimeFormat(t *testing.T) {
	baseTime := time.Date(2026, 4, 1, 20, 15, 16, 987654321, time.Local)
	doc := &searcher.SearchFileDocument{
		ID:        "42",
		FileID:    42,
		FileName:  "report.pdf",
		FileType:  0,
		Size:      128,
		CreatedAt: util.NewDateTimeSecond(baseTime),
		UpdatedAt: util.NewDateTimeSecond(baseTime.Add(2 * time.Minute)),
		LatestVersion: &searcher.SearchFileVersionDocument{
			ID:        "entity-1",
			EntityID:  99,
			CreatedAt: util.NewDateTimeSecond(baseTime.Add(4 * time.Minute)),
			UpdatedAt: util.NewDateTimeSecond(baseTime.Add(5 * time.Minute)),
		},
		Attachments: []searcher.SearchAttachmentDocument{
			{
				ID:        "att-1",
				CreatedAt: util.NewDateTimeSecond(baseTime.Add(6 * time.Minute)),
				UpdatedAt: util.NewDateTimeSecond(baseTime.Add(7 * time.Minute)),
			},
		},
		SynchronizedAt: util.NewDateTimeSecond(baseTime.Add(8 * time.Minute)),
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("failed to marshal elasticsearch document: %v", err)
	}

	payload := string(raw)
	expected := []string{
		`"created_at":"2026-04-01 20:15:16"`,
		`"updated_at":"2026-04-01 20:17:16"`,
		`"synchronized_at":"2026-04-01 20:23:16"`,
		`"created_at":"2026-04-01 20:19:16"`,
		`"updated_at":"2026-04-01 20:20:16"`,
		`"created_at":"2026-04-01 20:21:16"`,
		`"updated_at":"2026-04-01 20:22:16"`,
	}

	for _, want := range expected {
		if !strings.Contains(payload, want) {
			t.Fatalf("expected payload to contain %q, got %s", want, payload)
		}
	}
	if strings.Contains(payload, "T20:15:16") {
		t.Fatalf("expected payload to avoid RFC3339 timestamps, got %s", payload)
	}
}

func TestElasticsearchDocumentRoundTripsCustomTimeFormat(t *testing.T) {
	raw := []byte(`{
		"id":"42",
		"file_id":42,
		"owner_id":7,
		"file_name":"report.pdf",
		"file_type":0,
		"size":128,
		"created_at":"2026-04-01 20:15:16",
		"updated_at":"2026-04-01 20:17:16",
		"synchronized_at":"2026-04-01 20:23:16",
		"latest_version":{
			"id":"entity-1",
			"entity_id":99,
			"entity_type":"version",
			"entity_type_value":1,
			"created_at":"2026-04-01 20:19:16",
			"updated_at":"2026-04-01 20:20:16"
		},
		"attachments":[
			{
				"id":"att-1",
				"created_at":"2026-04-01 20:21:16",
				"updated_at":"2026-04-01 20:22:16"
			}
		]
	}`)

	var doc searcher.SearchFileDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("failed to unmarshal elasticsearch document: %v", err)
	}

	checkTime := func(label string, got, want time.Time) {
		if !got.Equal(want) {
			t.Fatalf("unexpected %s: got %s want %s", label, got, want)
		}
	}

	checkTime("created_at", doc.CreatedAt.Time(), time.Date(2026, 4, 1, 20, 15, 16, 0, time.Local))
	checkTime("updated_at", doc.UpdatedAt.Time(), time.Date(2026, 4, 1, 20, 17, 16, 0, time.Local))
	checkTime("synchronized_at", doc.SynchronizedAt.Time(), time.Date(2026, 4, 1, 20, 23, 16, 0, time.Local))
	checkTime("latest_version.created_at", doc.LatestVersion.CreatedAt.Time(), time.Date(2026, 4, 1, 20, 19, 16, 0, time.Local))
	checkTime("latest_version.updated_at", doc.LatestVersion.UpdatedAt.Time(), time.Date(2026, 4, 1, 20, 20, 16, 0, time.Local))
	checkTime("attachment.created_at", doc.Attachments[0].CreatedAt.Time(), time.Date(2026, 4, 1, 20, 21, 16, 0, time.Local))
	checkTime("attachment.updated_at", doc.Attachments[0].UpdatedAt.Time(), time.Date(2026, 4, 1, 20, 22, 16, 0, time.Local))
}

func TestElasticsearchIndexDefinitionUsesCustomDateFormat(t *testing.T) {
	definition := elasticsearchIndexDefinition()
	mappings := definition["mappings"].(map[string]any)
	properties := mappings["properties"].(map[string]any)
	createdAt := properties["created_at"].(map[string]any)
	updatedAt := properties["updated_at"].(map[string]any)
	synchronizedAt := properties["synchronized_at"].(map[string]any)

	for field, mapping := range map[string]map[string]any{
		"created_at":      createdAt,
		"updated_at":      updatedAt,
		"synchronized_at": synchronizedAt,
	} {
		if got := mapping["format"]; got != util.DateTimeSecondFormat {
			t.Fatalf("unexpected %s format: got %v want %s", field, got, util.DateTimeSecondFormat)
		}
	}
}

func TestElasticsearchIndexDefinitionCoversAdvancedSearchFields(t *testing.T) {
	definition := elasticsearchIndexDefinition()
	mappings := definition["mappings"].(map[string]any)
	properties := mappings["properties"].(map[string]any)

	for _, field := range []string{
		"file_name",
		"file_ext",
		"file_type",
		"size",
		"created_at",
		"updated_at",
		"metadata",
		"metadata_keys",
		"tags",
		"custom_props",
		"owner_uri",
		"public_uri",
		"search_uris",
		"search_paths",
		"tree_path",
	} {
		if _, ok := properties[field]; !ok {
			t.Fatalf("expected mapping to include %s", field)
		}
	}
}

func TestElasticsearchSearchFiltersBySearchBaseURI(t *testing.T) {
	transport := &testElasticsearchTransport{
		statuses: []int{http.StatusOK},
		bodies:   []string{`{"hits":{"total":{"value":0},"hits":[]}}`},
	}
	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{"http://example.com"},
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("failed to create elasticsearch client: %v", err)
	}

	indexer := &ElasticsearchIndexer{
		client:   client,
		index:    elasticsearchDefaultIndexName,
		pageSize: 10,
	}
	ownerID := 7
	_, _, err = indexer.Search(context.Background(), &searcher.SearchRequest{
		Query:         "report",
		OwnerID:       &ownerID,
		SearchBaseURI: "cloudreve://u7@my/docs",
	})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("expected one request, got %d", len(transport.requests))
	}

	payload := string(transport.requests[0])
	for _, want := range []string{
		`"owner_id":7`,
		`"search_paths":"cloudreve://u7@my/docs"`,
	} {
		if !strings.Contains(payload, want) {
			t.Fatalf("expected search payload to contain %q, got %s", want, payload)
		}
	}
}

func TestElasticsearchSearchFiltersByCompatibleSearchBaseURIs(t *testing.T) {
	transport := &testElasticsearchTransport{
		statuses: []int{http.StatusOK},
		bodies:   []string{`{"hits":{"total":{"value":0},"hits":[]}}`},
	}
	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{"http://example.com"},
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("failed to create elasticsearch client: %v", err)
	}

	indexer := &ElasticsearchIndexer{
		client:   client,
		index:    elasticsearchDefaultIndexName,
		pageSize: 10,
	}
	ownerID := 7
	_, _, err = indexer.Search(context.Background(), &searcher.SearchRequest{
		Query:   "report",
		OwnerID: &ownerID,
		SearchBaseURIs: []string{
			"cloudreve://u7@my/docs",
			"cloudreve://my/docs",
		},
	})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("expected one request, got %d", len(transport.requests))
	}

	payload := string(transport.requests[0])
	for _, want := range []string{
		`"owner_id":7`,
		`"terms":{"search_paths":["cloudreve://u7@my/docs","cloudreve://my/docs"]}`,
	} {
		if !strings.Contains(payload, want) {
			t.Fatalf("expected search payload to contain %q, got %s", want, payload)
		}
	}
}

type testElasticsearchTransport struct {
	statuses []int
	bodies   []string
	requests [][]byte
}

func (t *testElasticsearchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(req.Body)
	t.requests = append(t.requests, raw)

	index := len(t.requests) - 1
	if index >= len(t.statuses) {
		index = len(t.statuses) - 1
	}
	status := http.StatusOK
	body := `{}`
	if index >= 0 && len(t.statuses) > 0 {
		status = t.statuses[index]
	}
	if index >= 0 && index < len(t.bodies) {
		body = t.bodies[index]
	}

	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header: http.Header{
			"X-Elastic-Product": []string{"Elasticsearch"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}, nil
}
