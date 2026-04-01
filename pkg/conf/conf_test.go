package conf

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/go-ini/ini"
	"github.com/stretchr/testify/require"
)

func TestNewIniConfigProviderCreateDefaultConfig(t *testing.T) {
	confPath := filepath.Join(t.TempDir(), "missing", "conf.ini")

	provider, err := NewIniConfigProvider(confPath, logging.NewConsoleLogger(logging.LevelError))
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.True(t, util.Exists(confPath))
}

func TestNewIniConfigProviderInvalidConfig(t *testing.T) {
	confPath := filepath.Join(t.TempDir(), "conf.ini")
	require.NoError(t, os.WriteFile(confPath, []byte(`[Database]
Type = mysql
User = root
Password233root
Host = 127.0.0.1:3306
Name = v3
TablePrefix = v3_
`), 0o644))

	_, err := NewIniConfigProvider(confPath, logging.NewConsoleLogger(logging.LevelError))
	require.Error(t, err)
}

func TestNewIniConfigProviderValidConfig(t *testing.T) {
	confPath := filepath.Join(t.TempDir(), "conf.ini")
	require.NoError(t, os.WriteFile(confPath, []byte(`
[System]
Listen = :3000
ForceColor = true
CallerMode = on
StacktraceMode = error
SessionSecret = test-secret

[Database]
Type = mysql
User = root
Password = root
Host = 127.0.0.1
Port = 3306
Name = v3
TablePrefix = v3_
`), 0o644))

	provider, err := NewIniConfigProvider(confPath, logging.NewConsoleLogger(logging.LevelError))
	require.NoError(t, err)
	require.Equal(t, ":3000", provider.System().Listen)
	require.True(t, provider.System().ForceColor)
	require.Equal(t, "on", provider.System().CallerMode)
	require.Equal(t, "error", provider.System().StacktraceMode)
	require.Equal(t, MySqlDB, provider.Database().Type)
}

func TestMapSection(t *testing.T) {
	cfg, err := ini.Load([]byte(`
[Database]
Type = mysql
User = root
Password = root
Host = 127.0.0.1
Port = 3306
Name = v3
TablePrefix = v3_
`))
	require.NoError(t, err)

	db := *DatabaseConfig
	err = mapSection(cfg, "Database", &db)
	require.NoError(t, err)
	require.Equal(t, MySqlDB, db.Type)
	require.Equal(t, "root", db.User)
}
