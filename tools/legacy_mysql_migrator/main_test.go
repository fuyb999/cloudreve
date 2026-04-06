package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigDefaultsMySQLBatchSize(t *testing.T) {
	configPath := writeTestConfig(t, `
source:
  driver: mysql
  dsn: user:pass@tcp(127.0.0.1:3306)/legacy?parseTime=true
  query: SELECT 1
target:
  driver: postgres
  dsn: host=127.0.0.1 port=5432 user=cloudreve password=cloudreve dbname=cloudreve sslmode=disable
  db_type: postgres
`)

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.Source.BatchSize != defaultMySQLSourceBatchSize {
		t.Fatalf("expected default batch size %d, got %d", defaultMySQLSourceBatchSize, cfg.Source.BatchSize)
	}
}

func TestLoadConfigPreservesExplicitBatchSize(t *testing.T) {
	configPath := writeTestConfig(t, `
source:
  driver: mysql
  dsn: user:pass@tcp(127.0.0.1:3306)/legacy?parseTime=true
  batch_size: 250
  query: SELECT 1
target:
  driver: postgres
  dsn: host=127.0.0.1 port=5432 user=cloudreve password=cloudreve dbname=cloudreve sslmode=disable
  db_type: postgres
`)

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.Source.BatchSize != 250 {
		t.Fatalf("expected batch size 250, got %d", cfg.Source.BatchSize)
	}
}

func TestLoadConfigRejectsNegativeBatchSize(t *testing.T) {
	configPath := writeTestConfig(t, `
source:
  driver: mysql
  dsn: user:pass@tcp(127.0.0.1:3306)/legacy?parseTime=true
  batch_size: -1
  query: SELECT 1
target:
  driver: postgres
  dsn: host=127.0.0.1 port=5432 user=cloudreve password=cloudreve dbname=cloudreve sslmode=disable
  db_type: postgres
`)

	_, err := loadConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "source.batch_size") {
		t.Fatalf("expected source.batch_size validation error, got %v", err)
	}
}

func TestLoadConfigRejectsExternalIdentityIssuerWithoutProvider(t *testing.T) {
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
  external_identity_issuer: legacy
`)

	_, err := loadConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "external_identity_provider") {
		t.Fatalf("expected external identity provider validation error, got %v", err)
	}
}

func TestLoadConfigParsesDisableNativeFTSEnqueue(t *testing.T) {
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
  disable_native_fts_enqueue: true
`)

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if !cfg.Migration.DisableNativeFTSEnqueue {
		t.Fatal("expected disable_native_fts_enqueue to be true")
	}
}

func TestBuildMySQLBatchQuery(t *testing.T) {
	query := "SELECT id, target_path FROM legacy_files ORDER BY id;"
	batchQuery := buildMySQLBatchQuery(query)
	expected := "SELECT id, target_path FROM legacy_files ORDER BY id LIMIT ? OFFSET ?"
	if batchQuery != expected {
		t.Fatalf("unexpected batch query: %q", batchQuery)
	}
}

func TestExternalIdentityLookupValue(t *testing.T) {
	m := &migrator{
		cfg: config{
			Migration: migrationConfig{
				ExternalIdentityProvider: "legacy-sync",
			},
		},
	}

	if got := m.externalIdentityLookupValue(&legacyRow{OwnerExternalUserID: "u-100"}); got != "u-100" {
		t.Fatalf("expected explicit external user id, got %q", got)
	}
	if got := m.externalIdentityLookupValue(&legacyRow{OwnerIDRaw: "9223372036854775807"}); got != "9223372036854775807" {
		t.Fatalf("expected raw owner id fallback, got %q", got)
	}
	if got := m.externalIdentityLookupValue(&legacyRow{}); got != "" {
		t.Fatalf("expected empty lookup value, got %q", got)
	}
}

func TestParseLegacyRowAcceptsBucketAndObjectPath(t *testing.T) {
	row, err := parseLegacyRow(map[string]any{
		"scope":       "personal",
		"kind":        "file",
		"target_path": "docs/report.pdf",
		"owner_id":    int64(922337203685477580),
		"bucket":      "legacy-private",
		"object_path": "archive/2024/report.pdf",
	}, 1)
	if err != nil {
		t.Fatalf("parseLegacyRow returned error: %v", err)
	}
	if row.OwnerIDRaw != "922337203685477580" {
		t.Fatalf("expected raw owner id to be preserved, got %q", row.OwnerIDRaw)
	}
	if row.Bucket != "legacy-private" {
		t.Fatalf("expected bucket legacy-private, got %q", row.Bucket)
	}
	if row.ObjectKey != "archive/2024/report.pdf" {
		t.Fatalf("expected object key from object_path, got %q", row.ObjectKey)
	}
}

func TestSelectConfiguredPolicyID(t *testing.T) {
	m := &migrator{
		cfg: config{
			Migration: migrationConfig{
				DefaultPolicyID: 9,
				PolicyRules: []policyRule{
					{Bucket: "legacy-private", PathPrefix: "archive/", PolicyID: 3},
					{Bucket: "legacy-public", PolicyID: 4},
				},
			},
		},
	}

	if got, ok := m.selectConfiguredPolicyID(&legacyRow{Bucket: "legacy-private", ObjectKey: "archive/a.txt"}); !ok || got != 3 {
		t.Fatalf("expected archive policy 3, got %d ok=%v", got, ok)
	}
	if got, ok := m.selectConfiguredPolicyID(&legacyRow{Bucket: "legacy-public", ObjectKey: "foo.txt"}); !ok || got != 4 {
		t.Fatalf("expected public policy 4, got %d ok=%v", got, ok)
	}
	if got, ok := m.selectConfiguredPolicyID(&legacyRow{Bucket: "unknown", ObjectKey: "foo.txt"}); !ok || got != 9 {
		t.Fatalf("expected default policy 9, got %d ok=%v", got, ok)
	}
}

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(configPath, []byte(strings.TrimSpace(content)), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	return configPath
}
