package inventory

import (
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/stretchr/testify/require"
)

func TestNormalizeDBPoolConfigForSQLite(t *testing.T) {
	t.Parallel()

	maxOpen, maxIdle, maxLifetime, maxIdleTime := normalizeDBPoolConfig(conf.SQLiteDB, &conf.Database{
		MaxOpenConns:    99,
		MaxIdleConns:    88,
		ConnMaxLifetime: 777,
		ConnMaxIdleTime: 666,
	})

	require.Equal(t, 1, maxOpen)
	require.Equal(t, 1, maxIdle)
	require.Zero(t, maxLifetime)
	require.Zero(t, maxIdleTime)
}

func TestNormalizeDBPoolConfigClampsInvalidCombinations(t *testing.T) {
	t.Parallel()

	maxOpen, maxIdle, maxLifetime, maxIdleTime := normalizeDBPoolConfig(conf.PostgresDB, &conf.Database{
		MaxOpenConns:    12,
		MaxIdleConns:    18,
		ConnMaxLifetime: 120,
		ConnMaxIdleTime: 300,
	})

	require.Equal(t, 12, maxOpen)
	require.Equal(t, 12, maxIdle)
	require.Equal(t, 120*time.Second, maxLifetime)
	require.Equal(t, 120*time.Second, maxIdleTime)
}

func TestBuildPostgresConnString(t *testing.T) {
	t.Parallel()

	base := &conf.Database{
		Host:     "pgpool-internal",
		User:     "cloudreve",
		Password: "secret",
		Name:     "cloudreve",
		Port:     5432,
	}

	require.Equal(t,
		"host=pgpool-internal user=cloudreve password=secret dbname=cloudreve port=5432 sslmode=disable",
		buildPostgresConnString(base),
	)

	withMode := *base
	withMode.SSLMode = "require"
	require.Equal(t,
		"host=pgpool-internal user=cloudreve password=secret dbname=cloudreve port=5432 sslmode=require",
		buildPostgresConnString(&withMode),
	)

	withOptions := *base
	withOptions.SSLMode = "sslmode=verify-full sslrootcert=/run/swarm-pki/ca/ca.crt"
	require.Equal(t,
		"host=pgpool-internal user=cloudreve password=secret dbname=cloudreve port=5432 sslmode=verify-full sslrootcert=/run/swarm-pki/ca/ca.crt",
		buildPostgresConnString(&withOptions),
	)
}
