package main

import (
	"context"
	sqlstdlib "database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	ententity "github.com/cloudreve/Cloudreve/v4/ent/entity"
	entfile "github.com/cloudreve/Cloudreve/v4/ent/file"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	inventorytypes "github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/publicshare"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"gopkg.in/yaml.v3"
)

const (
	defaultMarkerKey = "sys:legacy_mysql_migrator"
)

type config struct {
	Source    sourceConfig    `yaml:"source"`
	Target    targetConfig    `yaml:"target"`
	Migration migrationConfig `yaml:"migration"`
}

type sourceConfig struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
	Query  string `yaml:"query"`
}

type targetConfig struct {
	Driver     string      `yaml:"driver"`
	DSN        string      `yaml:"dsn"`
	DBType     conf.DBType `yaml:"db_type"`
	HashIDSalt string      `yaml:"hashid_salt"`
}

type migrationConfig struct {
	DryRun                  bool        `yaml:"dry_run"`
	ContinueOnError         bool        `yaml:"continue_on_error"`
	SkipExisting            bool        `yaml:"skip_existing"`
	SameIDFallback          bool        `yaml:"same_id_fallback"`
	AllowEmailLookup        bool        `yaml:"allow_email_lookup"`
	AllowUsernameLookup     bool        `yaml:"allow_username_lookup"`
	DefaultPersonalPolicyID int         `yaml:"default_personal_policy_id"`
	DefaultPublicPolicyID   int         `yaml:"default_public_policy_id"`
	DefaultPublicOwnerID    int         `yaml:"default_public_owner_id"`
	MarkerKey               string      `yaml:"marker_key"`
	PrivateMetadataKeys     []string    `yaml:"private_metadata_keys"`
	UserIDMap               map[int]int `yaml:"user_id_map"`
}

type legacyRow struct {
	LegacyID        string
	Scope           string
	TargetPath      string
	OwnerID         int
	OwnerEmail      string
	OwnerUsername   string
	IsDir           bool
	ObjectKey       string
	Size            int64
	StoragePolicyID int
	Metadata        map[string]string
	CreatedAt       *time.Time
	UpdatedAt       *time.Time
}

type migrationMarker struct {
	LegacyID        string `json:"legacy_id,omitempty"`
	Scope           string `json:"scope,omitempty"`
	TargetPath      string `json:"target_path,omitempty"`
	LegacyOwnerID   int    `json:"legacy_owner_id,omitempty"`
	ResolvedOwnerID int    `json:"resolved_owner_id,omitempty"`
	OwnerEmail      string `json:"owner_email,omitempty"`
	OwnerUsername   string `json:"owner_username,omitempty"`
	ObjectKey       string `json:"object_key,omitempty"`
	StoragePolicyID int    `json:"storage_policy_id,omitempty"`
}

type migrationStats struct {
	RowsTotal        int
	FilesCreated     int
	FoldersCreated   int
	FoldersUpdated   int
	FilesSkipped     int
	FoldersSkipped   int
	DryRunPlanned    int
	Failed           int
	RootsAutoCreated int
}

type migrator struct {
	cfg             config
	l               logging.Logger
	sourceDB        *sqlstdlib.DB
	targetClient    *ent.Client
	targetDBType    conf.DBType
	fileClient      inventory.FileClient
	userClient      inventory.UserClient
	policyClient    inventory.StoragePolicyClient
	settingClient   inventory.SettingClient
	publicService   *publicshare.Service
	stats           migrationStats
	userByID        map[int]*ent.User
	userByEmail     map[string]*ent.User
	userByUsername  map[string]*ent.User
	userRootIDByID  map[int]int
	policyExists    map[int]bool
	folderIDByKey   map[string]int
	publicRootID    int
	privateMetaKeys map[string]bool
}

type ownerResolution struct {
	User   *ent.User
	Reason string
}

type rowOutcome struct {
	Description string
	FolderCache map[string]int
	StorageDiff inventory.StorageDiff
	CreatedRoot bool
}

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "", "Path to YAML config file")
	flag.Parse()
	if strings.TrimSpace(configPath) == "" {
		fmt.Fprintln(os.Stderr, "missing -config")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config failed: %v\n", err)
		os.Exit(1)
	}

	m, err := newMigrator(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init migrator failed: %v\n", err)
		os.Exit(1)
	}
	defer m.close()

	if err := m.run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}
}

func loadConfig(configPath string) (config, error) {
	content, err := os.ReadFile(configPath)
	if err != nil {
		return config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg config
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return config{}, fmt.Errorf("unmarshal config: %w", err)
	}

	cfg.Source.Driver = strings.TrimSpace(cfg.Source.Driver)
	cfg.Source.DSN = strings.TrimSpace(cfg.Source.DSN)
	cfg.Source.Query = strings.TrimSpace(cfg.Source.Query)
	cfg.Target.Driver = strings.TrimSpace(cfg.Target.Driver)
	cfg.Target.DSN = strings.TrimSpace(cfg.Target.DSN)
	cfg.Target.HashIDSalt = strings.TrimSpace(cfg.Target.HashIDSalt)
	cfg.Migration.MarkerKey = strings.TrimSpace(cfg.Migration.MarkerKey)

	if cfg.Source.Driver == "" || cfg.Source.DSN == "" || cfg.Source.Query == "" {
		return config{}, fmt.Errorf("source.driver/source.dsn/source.query are required")
	}
	if cfg.Target.Driver == "" || cfg.Target.DSN == "" {
		return config{}, fmt.Errorf("target.driver/target.dsn are required")
	}

	if cfg.Target.DBType == "" {
		cfg.Target.DBType = inferDBType(cfg.Target.Driver)
	}
	if cfg.Target.DBType == "" {
		return config{}, fmt.Errorf("target.db_type is required when target.driver cannot be inferred")
	}
	if cfg.Migration.MarkerKey == "" {
		cfg.Migration.MarkerKey = defaultMarkerKey
	}
	if cfg.Target.HashIDSalt == "" {
		cfg.Target.HashIDSalt = "legacy-mysql-migrator"
	}
	if cfg.Migration.UserIDMap == nil {
		cfg.Migration.UserIDMap = map[int]int{}
	}

	return cfg, nil
}

func inferDBType(driver string) conf.DBType {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "mysql", "mariadb":
		return conf.MySqlDB
	case "postgres", "postgresql":
		return conf.PostgresDB
	case "sqlite", "sqlite3":
		return conf.SQLiteDB
	default:
		return ""
	}
}

func newMigrator(cfg config) (*migrator, error) {
	logger := logging.NewConsoleLogger(logging.LevelInformational)

	sourceDB, err := sqlstdlib.Open(cfg.Source.Driver, cfg.Source.DSN)
	if err != nil {
		return nil, fmt.Errorf("open source db: %w", err)
	}
	if err := sourceDB.Ping(); err != nil {
		_ = sourceDB.Close()
		return nil, fmt.Errorf("ping source db: %w", err)
	}

	targetClient, err := ent.Open(cfg.Target.Driver, cfg.Target.DSN)
	if err != nil {
		_ = sourceDB.Close()
		return nil, fmt.Errorf("open target db: %w", err)
	}
	if _, err := targetClient.User.Query().Limit(1).All(context.Background()); err != nil {
		_ = targetClient.Close()
		_ = sourceDB.Close()
		return nil, fmt.Errorf("probe target db: %w", err)
	}

	hasher, err := hashid.New(cfg.Target.HashIDSalt)
	if err != nil {
		_ = targetClient.Close()
		_ = sourceDB.Close()
		return nil, fmt.Errorf("create hashid encoder: %w", err)
	}

	m := &migrator{
		cfg:             cfg,
		l:               logger,
		sourceDB:        sourceDB,
		targetClient:    targetClient,
		targetDBType:    cfg.Target.DBType,
		fileClient:      inventory.NewFileClient(targetClient, cfg.Target.DBType, nil),
		userClient:      inventory.NewUserClient(targetClient),
		policyClient:    inventory.NewStoragePolicyClient(targetClient, nil),
		settingClient:   inventory.NewSettingClient(targetClient, nil),
		userByID:        map[int]*ent.User{},
		userByEmail:     map[string]*ent.User{},
		userByUsername:  map[string]*ent.User{},
		userRootIDByID:  map[int]int{},
		policyExists:    map[int]bool{},
		folderIDByKey:   map[string]int{},
		privateMetaKeys: map[string]bool{},
	}
	m.publicService = publicshare.NewService(logger, m.fileClient, m.settingClient, hasher)
	for _, key := range cfg.Migration.PrivateMetadataKeys {
		trimmed := strings.TrimSpace(key)
		if trimmed != "" {
			m.privateMetaKeys[trimmed] = true
		}
	}
	m.privateMetaKeys[cfg.Migration.MarkerKey] = true

	return m, nil
}

func (m *migrator) close() {
	if m.targetClient != nil {
		_ = m.targetClient.Close()
	}
	if m.sourceDB != nil {
		_ = m.sourceDB.Close()
	}
}

func (m *migrator) run(ctx context.Context) error {
	m.l.Info("Starting legacy MySQL metadata migration. dry_run=%v skip_existing=%v continue_on_error=%v", m.cfg.Migration.DryRun, m.cfg.Migration.SkipExisting, m.cfg.Migration.ContinueOnError)

	if err := m.validateConfiguredDefaults(ctx); err != nil {
		return err
	}

	rows, err := m.sourceDB.QueryContext(ctx, m.cfg.Source.Query)
	if err != nil {
		return fmt.Errorf("query source rows: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("read source columns: %w", err)
	}

	rowNum := 0
	for rows.Next() {
		rowNum++
		m.stats.RowsTotal++

		raw, err := scanRow(rows, columns)
		if err != nil {
			if hErr := m.handleRowError(rowNum, fmt.Errorf("scan row: %w", err)); hErr != nil {
				return hErr
			}
			continue
		}

		legacy, err := parseLegacyRow(raw, rowNum)
		if err != nil {
			if hErr := m.handleRowError(rowNum, err); hErr != nil {
				return hErr
			}
			continue
		}

		if err := m.processRow(ctx, legacy); err != nil {
			if hErr := m.handleRowError(rowNum, fmt.Errorf("legacy_id=%s path=%s: %w", legacy.LegacyID, legacy.TargetPath, err)); hErr != nil {
				return hErr
			}
			continue
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate source rows: %w", err)
	}

	m.l.Info("Migration completed. rows=%d files_created=%d folders_created=%d folders_updated=%d files_skipped=%d folders_skipped=%d dry_run_planned=%d failed=%d auto_roots=%d",
		m.stats.RowsTotal,
		m.stats.FilesCreated,
		m.stats.FoldersCreated,
		m.stats.FoldersUpdated,
		m.stats.FilesSkipped,
		m.stats.FoldersSkipped,
		m.stats.DryRunPlanned,
		m.stats.Failed,
		m.stats.RootsAutoCreated,
	)
	return nil
}

func (m *migrator) validateConfiguredDefaults(ctx context.Context) error {
	if m.cfg.Migration.DefaultPersonalPolicyID > 0 {
		if err := m.ensurePolicyExists(ctx, m.cfg.Migration.DefaultPersonalPolicyID); err != nil {
			return fmt.Errorf("default_personal_policy_id invalid: %w", err)
		}
	}
	if m.cfg.Migration.DefaultPublicPolicyID > 0 {
		if err := m.ensurePolicyExists(ctx, m.cfg.Migration.DefaultPublicPolicyID); err != nil {
			return fmt.Errorf("default_public_policy_id invalid: %w", err)
		}
	}
	if m.cfg.Migration.DefaultPublicOwnerID > 0 {
		if _, err := m.getUserByID(ctx, m.cfg.Migration.DefaultPublicOwnerID); err != nil {
			return fmt.Errorf("default_public_owner_id invalid: %w", err)
		}
	}
	return nil
}

func (m *migrator) handleRowError(rowNum int, err error) error {
	m.stats.Failed++
	m.l.Error("Row %d failed: %v", rowNum, err)
	if !m.cfg.Migration.ContinueOnError {
		return err
	}
	return nil
}

func scanRow(rows *sqlstdlib.Rows, columns []string) (map[string]any, error) {
	values := make([]any, len(columns))
	dest := make([]any, len(columns))
	for i := range values {
		dest[i] = &values[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, err
	}

	result := make(map[string]any, len(columns))
	for i, col := range columns {
		result[strings.ToLower(col)] = normalizeDBValue(values[i])
	}
	return result, nil
}

func normalizeDBValue(v any) any {
	switch typed := v.(type) {
	case []byte:
		return string(typed)
	default:
		return typed
	}
}

func parseLegacyRow(raw map[string]any, rowNum int) (*legacyRow, error) {
	scope, ok := firstString(raw, "scope")
	if !ok {
		return nil, fmt.Errorf("row %d missing required column scope", rowNum)
	}
	normalizedScope, err := normalizeScope(scope)
	if err != nil {
		return nil, fmt.Errorf("row %d invalid scope %q: %w", rowNum, scope, err)
	}

	rawPath, ok := firstString(raw, "target_path", "path")
	if !ok {
		return nil, fmt.Errorf("row %d missing required column target_path", rowNum)
	}
	normalizedPath, err := normalizeTargetPath(rawPath)
	if err != nil {
		return nil, fmt.Errorf("row %d invalid target_path %q: %w", rowNum, rawPath, err)
	}

	isDir, err := inferIsDir(raw)
	if err != nil {
		return nil, fmt.Errorf("row %d invalid file kind: %w", rowNum, err)
	}
	if !isDir && normalizedPath == "" {
		return nil, fmt.Errorf("row %d file target_path cannot point to root", rowNum)
	}

	legacyID, ok := firstString(raw, "legacy_id", "id")
	if !ok {
		legacyID = fmt.Sprintf("row-%d", rowNum)
	}

	ownerID, _ := firstInt(raw, "owner_id", "legacy_owner_id")
	ownerEmail, _ := firstString(raw, "owner_email")
	ownerUsername, _ := firstString(raw, "owner_username")
	objectKey, _ := firstString(raw, "object_key", "source")
	if !isDir && strings.TrimSpace(objectKey) == "" {
		return nil, fmt.Errorf("row %d file row missing object_key/source", rowNum)
	}

	size, ok := firstInt64(raw, "size")
	if !ok || size < 0 {
		size = 0
	}
	policyID, _ := firstInt(raw, "storage_policy_id", "policy_id")
	createdAt, _ := firstTime(raw, "created_at")
	updatedAt, _ := firstTime(raw, "updated_at", "modified_at")
	metadata, err := firstMetadata(raw, "metadata_json")
	if err != nil {
		return nil, fmt.Errorf("row %d invalid metadata_json: %w", rowNum, err)
	}

	return &legacyRow{
		LegacyID:        legacyID,
		Scope:           normalizedScope,
		TargetPath:      normalizedPath,
		OwnerID:         ownerID,
		OwnerEmail:      ownerEmail,
		OwnerUsername:   ownerUsername,
		IsDir:           isDir,
		ObjectKey:       strings.TrimSpace(objectKey),
		Size:            size,
		StoragePolicyID: policyID,
		Metadata:        metadata,
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
	}, nil
}

func normalizeScope(scope string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "personal", "private", "my":
		return "personal", nil
	case "public":
		return "public", nil
	default:
		return "", fmt.Errorf("supported values are personal/public")
	}
}

func normalizeTargetPath(raw string) (string, error) {
	replaced := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if replaced == "" || replaced == "/" {
		return "", nil
	}
	parts := strings.Split(replaced, "/")
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", fmt.Errorf("parent path traversal is not allowed")
		}
		normalized = append(normalized, part)
	}
	return strings.Join(normalized, "/"), nil
}

func inferIsDir(raw map[string]any) (bool, error) {
	if kind, ok := firstString(raw, "kind"); ok {
		switch strings.ToLower(strings.TrimSpace(kind)) {
		case "dir", "folder", "directory":
			return true, nil
		case "file", "document":
			return false, nil
		default:
			return false, fmt.Errorf("kind must be file/folder")
		}
	}
	if isDir, ok := firstBool(raw, "is_dir", "is_folder"); ok {
		return isDir, nil
	}
	return false, fmt.Errorf("require either kind or is_dir column")
}

func firstString(raw map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		value, ok := raw[strings.ToLower(key)]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			trimmed := strings.TrimSpace(typed)
			if trimmed == "" {
				continue
			}
			return trimmed, true
		case fmt.Stringer:
			trimmed := strings.TrimSpace(typed.String())
			if trimmed == "" {
				continue
			}
			return trimmed, true
		case time.Time:
			return typed.Format(time.RFC3339), true
		default:
			trimmed := strings.TrimSpace(fmt.Sprint(typed))
			if trimmed == "" || trimmed == "<nil>" {
				continue
			}
			return trimmed, true
		}
	}
	return "", false
}

func firstInt(raw map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		value, ok := raw[strings.ToLower(key)]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case int:
			return typed, true
		case int8:
			return int(typed), true
		case int16:
			return int(typed), true
		case int32:
			return int(typed), true
		case int64:
			return int(typed), true
		case uint:
			return int(typed), true
		case uint8:
			return int(typed), true
		case uint16:
			return int(typed), true
		case uint32:
			return int(typed), true
		case uint64:
			return int(typed), true
		case float32:
			return int(typed), true
		case float64:
			return int(typed), true
		case string:
			parsed, err := strconv.Atoi(strings.TrimSpace(typed))
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

func firstInt64(raw map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		value, ok := raw[strings.ToLower(key)]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case int:
			return int64(typed), true
		case int8:
			return int64(typed), true
		case int16:
			return int64(typed), true
		case int32:
			return int64(typed), true
		case int64:
			return typed, true
		case uint:
			return int64(typed), true
		case uint8:
			return int64(typed), true
		case uint16:
			return int64(typed), true
		case uint32:
			return int64(typed), true
		case uint64:
			return int64(typed), true
		case float32:
			return int64(typed), true
		case float64:
			return int64(typed), true
		case string:
			parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

func firstBool(raw map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		value, ok := raw[strings.ToLower(key)]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return typed, true
		case int:
			return typed != 0, true
		case int64:
			return typed != 0, true
		case uint64:
			return typed != 0, true
		case float64:
			return typed != 0, true
		case string:
			trimmed := strings.ToLower(strings.TrimSpace(typed))
			switch trimmed {
			case "1", "true", "yes", "y":
				return true, true
			case "0", "false", "no", "n":
				return false, true
			}
		}
	}
	return false, false
}

func firstTime(raw map[string]any, keys ...string) (*time.Time, bool) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, key := range keys {
		value, ok := raw[strings.ToLower(key)]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case time.Time:
			copy := typed
			return &copy, true
		case string:
			trimmed := strings.TrimSpace(typed)
			if trimmed == "" {
				continue
			}
			for _, layout := range layouts {
				if parsed, err := time.ParseInLocation(layout, trimmed, time.Local); err == nil {
					return &parsed, true
				}
			}
		}
	}
	return nil, false
}

func firstMetadata(raw map[string]any, keys ...string) (map[string]string, error) {
	for _, key := range keys {
		value, ok := raw[strings.ToLower(key)]
		if !ok || value == nil {
			continue
		}
		rawJSON := strings.TrimSpace(fmt.Sprint(value))
		if rawJSON == "" || rawJSON == "null" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(rawJSON), &decoded); err != nil {
			return nil, err
		}
		result := make(map[string]string, len(decoded))
		for k, v := range decoded {
			switch typed := v.(type) {
			case string:
				result[k] = typed
			default:
				encoded, err := json.Marshal(typed)
				if err != nil {
					return nil, fmt.Errorf("marshal metadata %q: %w", k, err)
				}
				result[k] = string(encoded)
			}
		}
		return result, nil
	}
	return map[string]string{}, nil
}

func (m *migrator) processRow(ctx context.Context, row *legacyRow) error {
	owner, err := m.resolveOwner(ctx, row)
	if err != nil {
		return err
	}

	if m.cfg.Migration.DryRun {
		return m.processRowDryRun(ctx, row, owner)
	}

	outcome, err := m.processRowLive(ctx, row, owner)
	if err != nil {
		return err
	}

	for key, id := range outcome.FolderCache {
		m.folderIDByKey[key] = id
	}
	if outcome.CreatedRoot {
		m.stats.RootsAutoCreated++
	}
	if len(outcome.StorageDiff) > 0 {
		if err := m.userClient.ApplyStorageDiff(ctx, outcome.StorageDiff); err != nil {
			return fmt.Errorf("apply storage diff: %w", err)
		}
	}
	m.l.Info(outcome.Description)
	return nil
}

func (m *migrator) resolveOwner(ctx context.Context, row *legacyRow) (*ownerResolution, error) {
	if row.OwnerID > 0 {
		if mapped, ok := m.cfg.Migration.UserIDMap[row.OwnerID]; ok {
			user, err := m.getUserByID(ctx, mapped)
			if err != nil {
				return nil, fmt.Errorf("mapped owner_id %d -> %d not found: %w", row.OwnerID, mapped, err)
			}
			return &ownerResolution{User: user, Reason: fmt.Sprintf("user_id_map:%d->%d", row.OwnerID, mapped)}, nil
		}
		if m.cfg.Migration.SameIDFallback {
			user, err := m.getUserByID(ctx, row.OwnerID)
			if err == nil {
				return &ownerResolution{User: user, Reason: fmt.Sprintf("same_id:%d", row.OwnerID)}, nil
			}
		}
	}
	if m.cfg.Migration.AllowEmailLookup && strings.TrimSpace(row.OwnerEmail) != "" {
		user, err := m.getUserByEmail(ctx, row.OwnerEmail)
		if err == nil {
			return &ownerResolution{User: user, Reason: fmt.Sprintf("email:%s", row.OwnerEmail)}, nil
		}
	}
	if m.cfg.Migration.AllowUsernameLookup && strings.TrimSpace(row.OwnerUsername) != "" {
		user, err := m.getUserByUsername(ctx, row.OwnerUsername)
		if err == nil {
			return &ownerResolution{User: user, Reason: fmt.Sprintf("username:%s", row.OwnerUsername)}, nil
		}
	}
	if row.Scope == "public" && m.cfg.Migration.DefaultPublicOwnerID > 0 {
		user, err := m.getUserByID(ctx, m.cfg.Migration.DefaultPublicOwnerID)
		if err != nil {
			return nil, fmt.Errorf("default_public_owner_id %d not found: %w", m.cfg.Migration.DefaultPublicOwnerID, err)
		}
		return &ownerResolution{User: user, Reason: fmt.Sprintf("default_public_owner_id:%d", user.ID)}, nil
	}
	if row.Scope == "personal" {
		return nil, fmt.Errorf("cannot resolve personal owner: owner_id=%d owner_email=%q owner_username=%q", row.OwnerID, row.OwnerEmail, row.OwnerUsername)
	}
	return nil, fmt.Errorf("cannot resolve public owner: owner_id=%d owner_email=%q owner_username=%q", row.OwnerID, row.OwnerEmail, row.OwnerUsername)
}

func (m *migrator) processRowDryRun(ctx context.Context, row *legacyRow, owner *ownerResolution) error {
	var ownerID int
	if owner != nil && owner.User != nil {
		ownerID = owner.User.ID
	}
	if !row.IsDir {
		policyID, err := m.resolvePolicyID(ctx, row)
		if err != nil {
			return err
		}
		m.stats.DryRunPlanned++
		m.l.Info("DRY RUN file legacy_id=%s scope=%s owner=%d(%s) policy=%d path=%s source=%s size=%d", row.LegacyID, row.Scope, ownerID, owner.Reason, policyID, path.Join("/", row.TargetPath), row.ObjectKey, row.Size)
		return nil
	}
	m.stats.DryRunPlanned++
	m.l.Info("DRY RUN folder legacy_id=%s scope=%s owner=%d(%s) path=%s", row.LegacyID, row.Scope, ownerID, owner.Reason, path.Join("/", row.TargetPath))
	return nil
}

func (m *migrator) processRowLive(ctx context.Context, row *legacyRow, owner *ownerResolution) (*rowOutcome, error) {
	rootID, createdRoot, err := m.ensureScopeRootID(ctx, row.Scope, owner.User)
	if err != nil {
		return nil, err
	}
	actorCtx := context.WithValue(ctx, inventory.UserCtx{}, owner.User)
	actorCtx = context.WithValue(actorCtx, inventory.UserIDCtx{}, owner.User.ID)

	tx, err := m.targetClient.Tx(actorCtx)
	if err != nil {
		return nil, fmt.Errorf("open target tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	txClient := tx.Client()
	txFileClient := inventory.NewFileClient(txClient, m.targetDBType, nil)

	root, err := txFileClient.GetByID(actorCtx, rootID)
	if err != nil {
		return nil, fmt.Errorf("reload scope root %d: %w", rootID, err)
	}

	folderOwnerID := owner.User.ID
	folderCache := map[string]int{}
	metadata, privateMask, err := m.buildMetadata(row, owner.User.ID)
	if err != nil {
		return nil, err
	}

	if row.IsDir {
		if row.TargetPath == "" {
			committed = true
			if err := tx.Rollback(); err != nil && !errors.Is(err, sqlstdlib.ErrTxDone) {
				return nil, err
			}
			m.stats.FoldersSkipped++
			return &rowOutcome{Description: fmt.Sprintf("Skip root folder row legacy_id=%s scope=%s", row.LegacyID, row.Scope), FolderCache: folderCache, CreatedRoot: createdRoot}, nil
		}
		folder, resolvedCache, err := m.ensureFolderPathTx(actorCtx, txFileClient, root, folderOwnerID, strings.Split(row.TargetPath, "/"))
		if err != nil {
			return nil, err
		}
		for key, id := range resolvedCache {
			folderCache[key] = id
		}
		if folder.OwnerID != folderOwnerID {
			updated, err := txClient.File.UpdateOneID(folder.ID).SetOwnerID(folderOwnerID).Save(actorCtx)
			if err != nil {
				return nil, fmt.Errorf("update folder owner %d -> %d: %w", folder.ID, folderOwnerID, err)
			}
			folder = updated
		}
		if len(metadata) > 0 {
			if err := txFileClient.UpsertMetadata(actorCtx, folder, metadata, privateMask); err != nil {
				return nil, fmt.Errorf("upsert folder metadata: %w", err)
			}
		}
		if err := m.updateTimestamps(actorCtx, txClient, entfile.Table, folder.ID, row.CreatedAt, row.UpdatedAt); err != nil {
			return nil, fmt.Errorf("update folder timestamps: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit folder tx: %w", err)
		}
		committed = true
		m.stats.FoldersUpdated++
		return &rowOutcome{
			Description: fmt.Sprintf("Imported folder legacy_id=%s scope=%s owner=%d(%s) path=%s", row.LegacyID, row.Scope, owner.User.ID, owner.Reason, path.Join("/", row.TargetPath)),
			FolderCache: folderCache,
			CreatedRoot: createdRoot,
		}, nil
	}

	policyID, err := m.resolvePolicyID(ctx, row)
	if err != nil {
		return nil, err
	}

	parentSegments, fileName := splitParentAndName(row.TargetPath)
	parent, resolvedCache, err := m.ensureFolderPathTx(actorCtx, txFileClient, root, folderOwnerID, parentSegments)
	if err != nil {
		return nil, err
	}
	for key, id := range resolvedCache {
		folderCache[key] = id
	}

	existing, err := txFileClient.GetChildFile(actorCtx, parent, 0, fileName, true)
	if err == nil {
		if existing.Type != int(inventorytypes.FileTypeFile) {
			return nil, fmt.Errorf("target path %q already exists as non-file id=%d", row.TargetPath, existing.ID)
		}
		if m.cfg.Migration.SkipExisting {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sqlstdlib.ErrTxDone) {
				return nil, err
			}
			committed = true
			m.stats.FilesSkipped++
			return &rowOutcome{
				Description: fmt.Sprintf("Skip existing file legacy_id=%s scope=%s path=%s existing_id=%d", row.LegacyID, row.Scope, path.Join("/", row.TargetPath), existing.ID),
				FolderCache: folderCache,
				CreatedRoot: createdRoot,
			}, nil
		}
		return nil, fmt.Errorf("target file %q already exists as id=%d; set migration.skip_existing=true to ignore", row.TargetPath, existing.ID)
	}
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("query existing target file: %w", err)
	}

	createdFile, createdEntity, storageDiff, err := txFileClient.CreateFile(actorCtx, parent, &inventory.CreateFileParameters{
		FileType:            inventorytypes.FileTypeFile,
		Name:                fileName,
		StoragePolicyID:     policyID,
		Metadata:            metadata,
		MetadataPrivateMask: privateMask,
		EntityParameters: &inventory.EntityParameters{
			EntityType:      inventorytypes.EntityTypeVersion,
			StoragePolicyID: policyID,
			Source:          row.ObjectKey,
			Size:            row.Size,
			Importing:       true,
			ModifiedAt:      row.UpdatedAt,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create file: %w", err)
	}

	if createdFile.OwnerID != owner.User.ID {
		updated, err := txClient.File.UpdateOneID(createdFile.ID).SetOwnerID(owner.User.ID).Save(actorCtx)
		if err != nil {
			return nil, fmt.Errorf("update file owner %d -> %d: %w", createdFile.ID, owner.User.ID, err)
		}
		createdFile = updated
		shiftStorageDiff(storageDiff, row.Size, parent.OwnerID, owner.User.ID)
	}

	if err := m.updateTimestamps(actorCtx, txClient, entfile.Table, createdFile.ID, row.CreatedAt, row.UpdatedAt); err != nil {
		return nil, fmt.Errorf("update file timestamps: %w", err)
	}
	if createdEntity != nil {
		if err := m.updateTimestamps(actorCtx, txClient, ententity.Table, createdEntity.ID, row.CreatedAt, row.UpdatedAt); err != nil {
			return nil, fmt.Errorf("update entity timestamps: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit file tx: %w", err)
	}
	committed = true
	m.stats.FilesCreated++
	return &rowOutcome{
		Description: fmt.Sprintf("Imported file legacy_id=%s scope=%s owner=%d(%s) policy=%d path=%s source=%s size=%d", row.LegacyID, row.Scope, owner.User.ID, owner.Reason, policyID, path.Join("/", row.TargetPath), row.ObjectKey, row.Size),
		FolderCache: folderCache,
		StorageDiff: storageDiff,
		CreatedRoot: createdRoot,
	}, nil
}

func shiftStorageDiff(diff inventory.StorageDiff, size int64, fromID, toID int) {
	if diff == nil || fromID == toID || size == 0 {
		return
	}
	diff[fromID] -= size
	if diff[fromID] == 0 {
		delete(diff, fromID)
	}
	diff[toID] += size
}

func splitParentAndName(targetPath string) ([]string, string) {
	parts := strings.Split(targetPath, "/")
	if len(parts) == 1 {
		return nil, parts[0]
	}
	return parts[:len(parts)-1], parts[len(parts)-1]
}

func (m *migrator) ensureScopeRootID(ctx context.Context, scope string, owner *ent.User) (int, bool, error) {
	switch scope {
	case "personal":
		return m.ensureUserRootID(ctx, owner)
	case "public":
		return m.ensurePublicRootID(ctx)
	default:
		return 0, false, fmt.Errorf("unsupported scope %q", scope)
	}
}

func (m *migrator) ensureUserRootID(ctx context.Context, owner *ent.User) (int, bool, error) {
	if owner == nil {
		return 0, false, fmt.Errorf("personal scope owner is nil")
	}
	if cached, ok := m.userRootIDByID[owner.ID]; ok {
		return cached, false, nil
	}
	root, err := m.fileClient.Root(ctx, owner)
	if err == nil {
		m.userRootIDByID[owner.ID] = root.ID
		return root.ID, false, nil
	}
	if !ent.IsNotFound(err) {
		return 0, false, fmt.Errorf("query user %d root: %w", owner.ID, err)
	}
	created, err := m.fileClient.CreateFolder(ctx, nil, &inventory.CreateFolderParameters{Owner: owner.ID, Name: inventory.RootFolderName})
	if err != nil {
		return 0, false, fmt.Errorf("create user %d root: %w", owner.ID, err)
	}
	m.userRootIDByID[owner.ID] = created.ID
	return created.ID, true, nil
}

func (m *migrator) ensurePublicRootID(ctx context.Context) (int, bool, error) {
	if m.publicRootID > 0 {
		return m.publicRootID, false, nil
	}
	root, err := m.publicService.Root(ctx)
	if err == nil {
		m.publicRootID = root.ID
		return root.ID, false, nil
	}
	owner, ownerErr := inventory.EnsurePublicSystemOwner(ctx, m.targetClient)
	if ownerErr != nil {
		return 0, false, fmt.Errorf("ensure public system owner: %w", ownerErr)
	}
	root, err = m.publicService.EnsureRoot(ctx, owner)
	if err != nil {
		return 0, false, fmt.Errorf("ensure public root: %w", err)
	}
	m.publicRootID = root.ID
	return root.ID, true, nil
}

func (m *migrator) ensureFolderPathTx(ctx context.Context, txFileClient inventory.FileClient, root *ent.File, ownerID int, segments []string) (*ent.File, map[string]int, error) {
	current := root
	resolved := map[string]int{}
	if len(segments) == 0 {
		return current, resolved, nil
	}

	currentPath := ""
	for _, segment := range segments {
		if strings.TrimSpace(segment) == "" {
			continue
		}
		currentPath = joinPath(currentPath, segment)
		cacheKey := folderCacheKey(root.ID, currentPath)
		if cachedID, ok := m.folderIDByKey[cacheKey]; ok {
			loaded, err := txFileClient.GetByID(ctx, cachedID)
			if err == nil {
				current = loaded
				resolved[cacheKey] = loaded.ID
				continue
			}
		}
		child, err := txFileClient.GetChildFile(ctx, current, 0, segment, true)
		if err == nil {
			if child.Type != int(inventorytypes.FileTypeFolder) {
				return nil, nil, fmt.Errorf("path segment %q under %q already exists as non-folder id=%d", segment, currentPath, child.ID)
			}
			current = child
			resolved[cacheKey] = child.ID
			continue
		}
		if !ent.IsNotFound(err) {
			return nil, nil, fmt.Errorf("query folder %q: %w", currentPath, err)
		}
		created, err := txFileClient.CreateFolder(ctx, current, &inventory.CreateFolderParameters{Owner: ownerID, Name: segment})
		if err != nil {
			return nil, nil, fmt.Errorf("create folder %q: %w", currentPath, err)
		}
		current = created
		resolved[cacheKey] = created.ID
		m.stats.FoldersCreated++
	}
	return current, resolved, nil
}

func folderCacheKey(rootID int, relativePath string) string {
	return fmt.Sprintf("%d:%s", rootID, relativePath)
}

func joinPath(base, name string) string {
	if base == "" {
		return name
	}
	return base + "/" + name
}

func (m *migrator) resolvePolicyID(ctx context.Context, row *legacyRow) (int, error) {
	policyID := row.StoragePolicyID
	if policyID == 0 {
		if row.Scope == "public" {
			policyID = m.cfg.Migration.DefaultPublicPolicyID
		} else {
			policyID = m.cfg.Migration.DefaultPersonalPolicyID
		}
	}
	if policyID <= 0 {
		return 0, fmt.Errorf("no storage policy resolved for legacy_id=%s path=%s", row.LegacyID, row.TargetPath)
	}
	if err := m.ensurePolicyExists(ctx, policyID); err != nil {
		return 0, err
	}
	return policyID, nil
}

func (m *migrator) ensurePolicyExists(ctx context.Context, policyID int) error {
	if policyID <= 0 {
		return fmt.Errorf("policy_id must be > 0")
	}
	if m.policyExists[policyID] {
		return nil
	}
	if _, err := m.policyClient.GetPolicyByID(ctx, policyID); err != nil {
		return fmt.Errorf("policy %d not found: %w", policyID, err)
	}
	m.policyExists[policyID] = true
	return nil
}

func (m *migrator) getUserByID(ctx context.Context, id int) (*ent.User, error) {
	if user, ok := m.userByID[id]; ok {
		return user, nil
	}
	user, err := m.userClient.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	m.cacheUser(user)
	return user, nil
}

func (m *migrator) getUserByEmail(ctx context.Context, email string) (*ent.User, error) {
	key := strings.ToLower(strings.TrimSpace(email))
	if user, ok := m.userByEmail[key]; ok {
		return user, nil
	}
	user, err := m.userClient.GetByEmail(ctx, key)
	if err != nil {
		return nil, err
	}
	m.cacheUser(user)
	return user, nil
}

func (m *migrator) getUserByUsername(ctx context.Context, username string) (*ent.User, error) {
	key := strings.ToLower(strings.TrimSpace(username))
	if user, ok := m.userByUsername[key]; ok {
		return user, nil
	}
	user, err := m.userClient.GetByUsername(ctx, key)
	if err != nil {
		return nil, err
	}
	m.cacheUser(user)
	return user, nil
}

func (m *migrator) cacheUser(user *ent.User) {
	if user == nil {
		return
	}
	m.userByID[user.ID] = user
	m.userByEmail[strings.ToLower(strings.TrimSpace(user.Email))] = user
	if user.Username != nil {
		m.userByUsername[strings.ToLower(strings.TrimSpace(*user.Username))] = user
	}
}

func (m *migrator) buildMetadata(row *legacyRow, resolvedOwnerID int) (map[string]string, map[string]bool, error) {
	metadata := make(map[string]string, len(row.Metadata)+1)
	privateMask := make(map[string]bool, len(row.Metadata)+1)
	for key, value := range row.Metadata {
		metadata[key] = value
		if m.privateMetaKeys[key] {
			privateMask[key] = true
		}
	}

	markerBytes, err := json.Marshal(migrationMarker{
		LegacyID:        row.LegacyID,
		Scope:           row.Scope,
		TargetPath:      row.TargetPath,
		LegacyOwnerID:   row.OwnerID,
		ResolvedOwnerID: resolvedOwnerID,
		OwnerEmail:      row.OwnerEmail,
		OwnerUsername:   row.OwnerUsername,
		ObjectKey:       row.ObjectKey,
		StoragePolicyID: row.StoragePolicyID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal migration marker: %w", err)
	}
	metadata[m.cfg.Migration.MarkerKey] = string(markerBytes)
	privateMask[m.cfg.Migration.MarkerKey] = true
	return metadata, privateMask, nil
}

func (m *migrator) updateTimestamps(ctx context.Context, client *ent.Client, table string, id int, createdAt, updatedAt *time.Time) error {
	if createdAt == nil && updatedAt == nil {
		return nil
	}
	assignments := make([]string, 0, 2)
	args := make([]any, 0, 3)
	placeholder := func(position int) string {
		if m.targetDBType == conf.PostgresDB {
			return fmt.Sprintf("$%d", position)
		}
		return "?"
	}
	index := 1
	if createdAt != nil {
		assignments = append(assignments, fmt.Sprintf("created_at = %s", placeholder(index)))
		args = append(args, *createdAt)
		index++
	}
	if updatedAt != nil {
		assignments = append(assignments, fmt.Sprintf("updated_at = %s", placeholder(index)))
		args = append(args, *updatedAt)
		index++
	}
	args = append(args, id)
	query := fmt.Sprintf("UPDATE %s SET %s WHERE id = %s", table, strings.Join(assignments, ", "), placeholder(index))
	if _, err := client.File.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	return nil
}
