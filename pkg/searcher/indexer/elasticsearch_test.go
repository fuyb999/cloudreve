package indexer

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
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

func TestSanitizeElasticsearchDocumentDropsAttachmentContentWhenPayloadTooLarge(t *testing.T) {
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
	for _, attachment := range sanitized.Attachments {
		if attachment.Content != "" {
			t.Fatal("expected attachment contents to be dropped when payload is too large")
		}
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
		CreatedAt: baseTime,
		UpdatedAt: baseTime.Add(2 * time.Minute),
		LatestVersion: &searcher.SearchFileVersionDocument{
			ID:        "entity-1",
			EntityID:  99,
			CreatedAt: baseTime.Add(4 * time.Minute),
			UpdatedAt: baseTime.Add(5 * time.Minute),
		},
		Attachments: []searcher.SearchAttachmentDocument{
			{
				ID:        "att-1",
				CreatedAt: baseTime.Add(6 * time.Minute),
				UpdatedAt: baseTime.Add(7 * time.Minute),
			},
		},
		SynchronizedAt: baseTime.Add(8 * time.Minute),
	}

	raw, err := json.Marshal(newElasticsearchDocument(doc))
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

	var doc elasticsearchFileDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("failed to unmarshal elasticsearch document: %v", err)
	}

	roundTripped := doc.toSearchFileDocument()
	if roundTripped == nil {
		t.Fatal("expected round-tripped document")
	}

	checkTime := func(label string, got, want time.Time) {
		if !got.Equal(want) {
			t.Fatalf("unexpected %s: got %s want %s", label, got, want)
		}
	}

	checkTime("created_at", roundTripped.CreatedAt, time.Date(2026, 4, 1, 20, 15, 16, 0, time.Local))
	checkTime("updated_at", roundTripped.UpdatedAt, time.Date(2026, 4, 1, 20, 17, 16, 0, time.Local))
	checkTime("synchronized_at", roundTripped.SynchronizedAt, time.Date(2026, 4, 1, 20, 23, 16, 0, time.Local))
	checkTime("latest_version.created_at", roundTripped.LatestVersion.CreatedAt, time.Date(2026, 4, 1, 20, 19, 16, 0, time.Local))
	checkTime("latest_version.updated_at", roundTripped.LatestVersion.UpdatedAt, time.Date(2026, 4, 1, 20, 20, 16, 0, time.Local))
	checkTime("attachment.created_at", roundTripped.Attachments[0].CreatedAt, time.Date(2026, 4, 1, 20, 21, 16, 0, time.Local))
	checkTime("attachment.updated_at", roundTripped.Attachments[0].UpdatedAt, time.Date(2026, 4, 1, 20, 22, 16, 0, time.Local))
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
