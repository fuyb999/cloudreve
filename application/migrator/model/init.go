package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/jinzhu/gorm"

	"github.com/cloudreve/Cloudreve/v4/application/migrator/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	_ "github.com/jinzhu/gorm/dialects/mssql"
	_ "github.com/jinzhu/gorm/dialects/mysql"
	_ "github.com/jinzhu/gorm/dialects/postgres"
)

// DB 数据库链接单例
var DB *gorm.DB

// Init 初始化 MySQL 链接
func Init() error {
	var (
		db         *gorm.DB
		err        error
		confDBType string = conf.DatabaseConfig.Type
	)

	// 兼容已有配置中的 "sqlite3" 配置项
	if confDBType == "sqlite3" {
		confDBType = "sqlite"
	}

	// 兼容 "mariadb" 数据库
	if confDBType == "mariadb" {
		confDBType = "mysql"
	}

	switch confDBType {
	case "UNSET", "sqlite":
		// 未指定数据库或者明确指定为 sqlite 时，使用 SQLite 数据库
		db, err = gorm.Open("sqlite3", util.RelativePath(conf.DatabaseConfig.DBFile))
	case "postgres":
		db, err = gorm.Open(confDBType, buildPostgresConnString())
	case "mysql", "mssql":
		var host string
		if conf.DatabaseConfig.UnixSocket {
			host = fmt.Sprintf("unix(%s)",
				conf.DatabaseConfig.Host)
		} else {
			host = fmt.Sprintf("(%s:%d)",
				conf.DatabaseConfig.Host,
				conf.DatabaseConfig.Port)
		}

		db, err = gorm.Open(confDBType, fmt.Sprintf("%s:%s@%s/%s?charset=%s&parseTime=True&loc=Local",
			conf.DatabaseConfig.User,
			conf.DatabaseConfig.Password,
			host,
			conf.DatabaseConfig.Name,
			conf.DatabaseConfig.Charset))
	default:
		return fmt.Errorf("unsupported database type %q", confDBType)
	}

	//db.SetLogger(util.Log())
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	// 处理表前缀
	gorm.DefaultTableNameHandler = func(db *gorm.DB, defaultTableName string) string {
		return conf.DatabaseConfig.TablePrefix + defaultTableName
	}

	// Debug模式下，输出所有 SQL 日志
	db.LogMode(true)

	applyDBPoolConfig(db, confDBType)

	DB = db

	return nil
}

func buildPostgresConnString() string {
	sslConfig := strings.TrimSpace(conf.DatabaseConfig.SSLMode)
	if sslConfig == "" {
		sslConfig = "disable"
	}
	if !strings.Contains(sslConfig, "=") {
		sslConfig = "sslmode=" + sslConfig
	}

	return fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%d %s",
		conf.DatabaseConfig.Host,
		conf.DatabaseConfig.User,
		conf.DatabaseConfig.Password,
		conf.DatabaseConfig.Name,
		conf.DatabaseConfig.Port,
		sslConfig,
	)
}

func applyDBPoolConfig(db *gorm.DB, dbType string) {
	if dbType == "sqlite" || dbType == "UNSET" {
		db.DB().SetMaxOpenConns(1)
		db.DB().SetMaxIdleConns(1)
		db.DB().SetConnMaxLifetime(0)
		db.DB().SetConnMaxIdleTime(0)
		return
	}

	maxOpen := conf.DatabaseConfig.MaxOpenConns
	maxIdle := conf.DatabaseConfig.MaxIdleConns
	if maxOpen > 0 && maxIdle > maxOpen {
		maxIdle = maxOpen
	}

	connMaxLifetime := time.Duration(conf.DatabaseConfig.ConnMaxLifetime) * time.Second
	connMaxIdleTime := time.Duration(conf.DatabaseConfig.ConnMaxIdleTime) * time.Second
	if connMaxLifetime > 0 && connMaxIdleTime > connMaxLifetime {
		connMaxIdleTime = connMaxLifetime
	}

	db.DB().SetMaxOpenConns(maxOpen)
	db.DB().SetMaxIdleConns(maxIdle)
	db.DB().SetConnMaxLifetime(connMaxLifetime)
	db.DB().SetConnMaxIdleTime(connMaxIdleTime)
}
