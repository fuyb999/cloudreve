package indexer

import (
	"strings"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
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
