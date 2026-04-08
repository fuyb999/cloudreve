package inventory

import (
	"context"
	rawsql "database/sql"
	"database/sql/driver"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/cloudreve/Cloudreve/v4/ent"
	_ "github.com/cloudreve/Cloudreve/v4/ent/runtime"
	"github.com/cloudreve/Cloudreve/v4/inventory/debug"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"modernc.org/sqlite"
)

const (
	DBVersionPrefix           = "db_version_"
	EnvDefaultOverwritePrefix = "CR_SETTING_DEFAULT_"
	EnvEnableAria2            = "CR_ENABLE_ARIA2"
)

// InitializeDBClient runs migration and returns a new ent.Client with additional configurations
// for hooks and interceptors.
func InitializeDBClient(l logging.Logger,
	client *ent.Client, kv cache.Driver, requiredDbVersion string, dbType conf.DBType) (*ent.Client, error) {
	ctx := context.WithValue(context.Background(), logging.LoggerCtx{}, l)
	if err := ensurePostgresLtree(ctx, client, dbType); err != nil {
		return nil, err
	}
	if needMigration(client, ctx, requiredDbVersion) {
		// Run the auto migration tool.
		if err := migrate(l, client, ctx, kv, requiredDbVersion); err != nil {
			return nil, fmt.Errorf("failed to migrate database: %w", err)
		}
	} else {
		l.Info("Database schema is up to date.")
		if err := client.Schema.Create(ctx); err != nil {
			return nil, fmt.Errorf("failed to reconcile database schema: %w", err)
		}
		if err := migrateOAuthClient(l, client, ctx); err != nil {
			return nil, fmt.Errorf("failed to ensure default OAuth clients: %w", err)
		}
	}
	if err := ensureFileTreePathSupport(ctx, l, client, dbType); err != nil {
		return nil, fmt.Errorf("failed to ensure file tree path support: %w", err)
	}
	if err := ensureFileExtSupport(ctx, l, client); err != nil {
		return nil, fmt.Errorf("failed to ensure file_ext support: %w", err)
	}
	if err := ensureUsernamesSupport(ctx, l, client); err != nil {
		return nil, fmt.Errorf("failed to ensure user username support: %w", err)
	}
	if err := ensureDefaultSettings(ctx, l, client,
		"audit_log_enabled_types",
		"show_desktop_app_promotion",
		"syncthing_upgrade_version",
		"syncthing_download_linux_url",
		"syncthing_download_windows_url",
		"oidc_enabled",
		"oidc_display_name",
		"oidc_auto_redirect",
		"oidc_config_mode",
		"oidc_binding_code",
		"oidc_sso_url",
		"oidc_wellknown_url",
		"oidc_client_id",
		"oidc_client_secret",
		"oidc_scope",
		"queue_content_processing_worker_num",
		"queue_content_processing_max_execution",
		"queue_content_processing_backoff_factor",
		"queue_content_processing_backoff_max_duration",
		"queue_content_processing_max_retry",
		"queue_content_processing_retry_delay",
		"fts_external_enabled",
		"fts_external_mode",
		"fts_external_use_global_kafka",
		"fts_external_kafka_brokers",
		"fts_external_kafka_security_protocol",
		"fts_external_kafka_sasl_mechanism",
		"fts_external_kafka_username",
		"fts_external_kafka_password",
		"fts_external_kafka_tls_skip_verify",
		"fts_external_kafka_process_topic",
		"fts_external_kafka_result_topic",
		"fts_external_kafka_error_topic",
		"fts_external_kafka_consumer_group",
		"fts_external_timeout_seconds",
		"fts_external_retry_max",
		"fts_external_quality_enabled",
		"fts_external_quality_min_text_length",
		"fts_external_quality_max_replacement_ratio",
		"fts_external_quality_max_control_char_ratio",
		"fts_external_quality_min_printable_ratio",
		"fts_external_quality_font_box_min_count",
		"fts_external_quality_font_box_min_run",
		"fts_external_quality_font_box_min_ratio",
		"fts_external_recursive_attachments",
		"fts_external_ocr_enabled",
		"fts_external_skip_encrypted_files",
	); err != nil {
		return nil, fmt.Errorf("failed to ensure audit log settings: %w", err)
	}
	if err := removeDeprecatedSettings(ctx, l, client,
		"fts_external_quality_font_issue_keywords",
	); err != nil {
		return nil, fmt.Errorf("failed to remove deprecated settings: %w", err)
	}
	if err := ensureSystemPublicRootSupport(ctx, l, client, dbType); err != nil {
		return nil, fmt.Errorf("failed to ensure hidden public root: %w", err)
	}
	if err := ensureAuditLogSystemUserSupport(ctx, l, client); err != nil {
		return nil, fmt.Errorf("failed to ensure audit log system user support: %w", err)
	}

	//createMockData(client, ctx)
	return client, nil
}

// NewRawEntClient returns a new ent.Client without additional configurations.
func NewRawEntClient(l logging.Logger, config conf.ConfigProvider) (*ent.Client, error) {
	l.Info("Initializing database connection...")
	dbConfig := config.Database()
	confDBType := dbConfig.Type
	if confDBType == conf.SQLite3DB || confDBType == "" {
		confDBType = conf.SQLiteDB
	}
	if confDBType == conf.MariaDB {
		confDBType = conf.MySqlDB
	}

	var (
		err    error
		client *sql.Driver
	)

	// Check if the database type is supported.
	if confDBType != conf.SQLiteDB && confDBType != conf.MySqlDB && confDBType != conf.PostgresDB {
		return nil, fmt.Errorf("unsupported database type: %s", confDBType)
	}
	// If Database connection string provided, use it directly.
	if dbConfig.DatabaseURL != "" {
		l.Info("Connect to database with connection string")
		client, err = sql.Open(string(confDBType), dbConfig.DatabaseURL)
	} else {

		switch confDBType {
		case conf.SQLiteDB:
			dbFile := util.RelativePath(dbConfig.DBFile)
			l.Info("Connect to SQLite database %q.", dbFile)
			client, err = sql.Open("sqlite3", util.RelativePath(dbConfig.DBFile))
		case conf.PostgresDB:
			l.Info("Connect to Postgres database %q.", dbConfig.Host)
			client, err = sql.Open("postgres", fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%d sslmode=disable",
				dbConfig.Host,
				dbConfig.User,
				dbConfig.Password,
				dbConfig.Name,
				dbConfig.Port))
		case conf.MySqlDB, conf.MsSqlDB:
			l.Info("Connect to MySQL/SQLServer database %q.", dbConfig.Host)
			var host string
			if dbConfig.UnixSocket {
				host = fmt.Sprintf("unix(%s)",
					dbConfig.Host)
			} else {
				host = fmt.Sprintf("(%s:%d)",
					dbConfig.Host,
					dbConfig.Port)
			}

			client, err = sql.Open(string(confDBType), fmt.Sprintf("%s:%s@%s/%s?charset=%s&parseTime=True&loc=Local",
				dbConfig.User,
				dbConfig.Password,
				host,
				dbConfig.Name,
				dbConfig.Charset))
		default:
			return nil, fmt.Errorf("unsupported database type %q", confDBType)
		}

		if err != nil {
			return nil, fmt.Errorf("failed to open database: %w", err)
		}

	}
	// Set connection pool
	db := client.DB()
	db.SetMaxIdleConns(50)
	if confDBType == "sqlite" || confDBType == "UNSET" {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(100)
	}

	// Set timeout
	db.SetConnMaxLifetime(time.Second * 30)

	driverOpt := ent.Driver(client)

	// Enable verbose logging for debug mode.
	if config.System().Debug {
		l.Debug("Debug mode is enabled for DB client.")
		driverOpt = ent.Driver(debug.DebugWithContext(client, func(ctx context.Context, i ...any) {
			logging.FromContext(ctx).Debug(i[0].(string), i[1:]...)
		}))
	}

	return ent.NewClient(driverOpt), nil
}

type sqlite3Driver struct {
	*sqlite.Driver
}

type sqlite3DriverConn interface {
	Exec(string, []driver.Value) (driver.Result, error)
}

func (d sqlite3Driver) Open(name string) (conn driver.Conn, err error) {
	conn, err = d.Driver.Open(name)
	if err != nil {
		return
	}
	_, err = conn.(sqlite3DriverConn).Exec("PRAGMA foreign_keys = ON;", nil)
	if err != nil {
		_ = conn.Close()
	}
	return
}

func init() {
	rawsql.Register("sqlite3", sqlite3Driver{Driver: &sqlite.Driver{}})
}
