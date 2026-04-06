package main

import (
	"encoding/json"
	"testing"
)

func TestLoadConfigAllowsSearchSyncOnly(t *testing.T) {
	configPath := writeTestConfig(t, `
target:
  driver: postgres
  dsn: host=127.0.0.1 port=5432 user=cloudreve password=cloudreve dbname=cloudreve sslmode=disable
  db_type: postgres
migration:
  skip_metadata_import: true
search_sync:
  enabled: true
  source:
    endpoint: http://127.0.0.1:9200
    index: legacy_files
  target:
    endpoint: http://127.0.0.1:9201
    index: cloudreve_files
`)

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if !cfg.Migration.SkipMetadataImport {
		t.Fatal("expected skip_metadata_import to be true")
	}
	if !cfg.SearchSync.Enabled {
		t.Fatal("expected search_sync.enabled to be true")
	}
}

func TestLoadConfigParsesLegacyUserIDMapKeysAsRawStrings(t *testing.T) {
	configPath := writeTestConfig(t, `
source:
  driver: mysql
  dsn: user:pass@tcp(127.0.0.1:3306)/legacy?parseTime=true
  query: SELECT 1
target:
  driver: postgres
  dsn: host=127.0.0.1 port=5432 user=cloudreve password=cloudreve dbname=cloudreve sslmode=disable
  db_type: postgres
migration:
  user_id_map:
    922337203685477580: 12
`)

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if got := cfg.Migration.UserIDMap["922337203685477580"]; got != 12 {
		t.Fatalf("expected raw owner id key to map to 12, got %d", got)
	}
}

func TestParseSearchMarkerFieldsSupportsNumericOwnerID(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"legacy_id":       "file-1",
		"legacy_owner_id": 922337203685477580,
		"target_path":     "/docs/report.pdf",
	})
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}

	fields, err := parseSearchMarkerFields(string(raw))
	if err != nil {
		t.Fatalf("parseSearchMarkerFields returned error: %v", err)
	}
	if got := fields["legacy_owner_id"]; got != "922337203685477580" {
		t.Fatalf("expected legacy_owner_id to stay numeric string, got %q", got)
	}
	if got := fields["target_path"]; got != "docs/report.pdf" {
		t.Fatalf("expected normalized target_path, got %q", got)
	}
}

func TestResolveSearchFieldMappingTransformsAttachmentArray(t *testing.T) {
	mapping := searchSyncFieldMapping{
		From: "attachments",
		Fields: map[string]searchSyncFieldMapping{
			"name":    {From: "filename"},
			"content": {From: "body"},
			"path":    {From: "path"},
		},
	}
	hit := sourceSearchHit{
		ID: "legacy-1",
		Source: map[string]any{
			"attachments": []any{
				map[string]any{
					"filename": "a.txt",
					"body":     "hello",
					"path":     "/docs/a.txt",
				},
			},
		},
	}

	value, ok, err := resolveSearchFieldMapping(mapping, hit.Source, hit)
	if err != nil {
		t.Fatalf("resolveSearchFieldMapping returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected mapping to resolve")
	}

	items, ok := value.([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("expected one transformed attachment, got %#v", value)
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("expected transformed attachment to be an object, got %#v", items[0])
	}
	if got := item["name"]; got != "a.txt" {
		t.Fatalf("expected attachment name a.txt, got %#v", got)
	}
	if got := item["content"]; got != "hello" {
		t.Fatalf("expected attachment content hello, got %#v", got)
	}
}
