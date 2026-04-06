#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
POSTGRES_INIT_IMAGE="${POSTGRES_INIT_IMAGE:-postgres:17.6-alpine3.21}"
AUTHVERSE_BACKEND_DIR="${AUTHVERSE_BACKEND_DIR:-$ROOT_DIR/../authverse-backend}"
SQL_BASE_FILE="$AUTHVERSE_BACKEND_DIR/sql/postgresql/authverse-20260329.sql"
SQL_REGISTRY_FILE="$AUTHVERSE_BACKEND_DIR/sql/postgresql/authverse-20260329-authz-integration-registry.sql"
HOST_NAME="$(hostname -s 2>/dev/null || hostname)"

usage() {
  cat <<'EOF'
用法：
  docker/swarm/init-authverse-db.sh [--env-file 文件]

说明：
  这个脚本会直连共享 PostgreSQL 主库，重建专用 `authverse` 数据库，
  然后执行统一认证初始化 SQL，并修正对象 owner / grant。

注意：
  1. 脚本默认连接 Cloudreve 主栈里的 `postgresql-1`
  2. SQL 是全量初始化，脚本会重建目标数据库
  3. 只应对专用的 `authverse` 数据库执行，不要对 Cloudreve 主库执行

参数：
  --env-file FILE     环境变量文件，默认是 .env.swarm
  -h, --help          显示帮助
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      ENV_FILE="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "[$HOST_NAME] 未知参数: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

if [[ ! -f "$ENV_FILE" ]]; then
  echo "[$HOST_NAME] 找不到环境变量文件: $ENV_FILE" >&2
  exit 1
fi
if [[ ! -f "$SQL_BASE_FILE" ]]; then
  echo "[$HOST_NAME] 找不到 SQL 文件: $SQL_BASE_FILE" >&2
  exit 1
fi
if [[ ! -f "$SQL_REGISTRY_FILE" ]]; then
  echo "[$HOST_NAME] 找不到 SQL 文件: $SQL_REGISTRY_FILE" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

CLOUDREVE_STACK_NAME="${CLOUDREVE_STACK_NAME:-cloudreve}"
AUTHVERSE_SHARED_NETWORK="${AUTHVERSE_SHARED_NETWORK:-${CLOUDREVE_STACK_NAME}_cloudreve_backend}"
AUTHVERSE_CLOUDREVE_SERVICE_PREFIX="${AUTHVERSE_CLOUDREVE_SERVICE_PREFIX:-${CLOUDREVE_STACK_NAME}_}"
AUTHVERSE_DB_INIT_HOST="${AUTHVERSE_DB_INIT_HOST:-${AUTHVERSE_CLOUDREVE_SERVICE_PREFIX}postgresql-1}"
AUTHVERSE_DB_INIT_PORT="${AUTHVERSE_DB_INIT_PORT:-5432}"
AUTHVERSE_DB_NAME="${AUTHVERSE_DB_NAME:-authverse}"
AUTHVERSE_DB_USERNAME="${AUTHVERSE_DB_USERNAME:-${POSTGRESQL_USERNAME:-cloudreve}}"
AUTHVERSE_DB_ADMIN_USER="${AUTHVERSE_DB_ADMIN_USER:-postgres}"
AUTHVERSE_DB_ADMIN_PASSWORD="${AUTHVERSE_DB_ADMIN_PASSWORD:-${POSTGRESQL_POSTGRES_PASSWORD:-}}"

if [[ -z "$AUTHVERSE_DB_ADMIN_PASSWORD" ]]; then
  echo "[$HOST_NAME] 缺少 AUTHVERSE_DB_ADMIN_PASSWORD 或 POSTGRESQL_POSTGRES_PASSWORD。" >&2
  exit 1
fi

if ! docker network inspect "$AUTHVERSE_SHARED_NETWORK" >/dev/null 2>&1; then
  echo "[$HOST_NAME] 共享 overlay 网络不存在：$AUTHVERSE_SHARED_NETWORK" >&2
  echo "[$HOST_NAME] 请先部署 Cloudreve 主栈，或者把 AUTHVERSE_SHARED_NETWORK 改成真实网络名。" >&2
  exit 1
fi

echo "[$HOST_NAME] 准备初始化统一认证数据库：$AUTHVERSE_DB_NAME"
docker run --rm \
  --network "$AUTHVERSE_SHARED_NETWORK" \
  -e PGHOST="$AUTHVERSE_DB_INIT_HOST" \
  -e PGPORT="$AUTHVERSE_DB_INIT_PORT" \
  -e PGADMINUSER="$AUTHVERSE_DB_ADMIN_USER" \
  -e PGPASSWORD_ADMIN="$AUTHVERSE_DB_ADMIN_PASSWORD" \
  -e APP_DB="$AUTHVERSE_DB_NAME" \
  -e APP_USER="$AUTHVERSE_DB_USERNAME" \
  -v "$SQL_BASE_FILE:/sql/authverse-base.sql:ro" \
  -v "$SQL_REGISTRY_FILE:/sql/authverse-registry.sql:ro" \
  "$POSTGRES_INIT_IMAGE" \
  sh -ec '
    export PGPASSWORD="$PGPASSWORD_ADMIN"

    until pg_isready -h "$PGHOST" -p "$PGPORT" -U "$PGADMINUSER" >/dev/null 2>&1; do
      echo "[authverse-db-init] 等待 PostgreSQL 就绪..."
      sleep 2
    done

    if ! psql -h "$PGHOST" -p "$PGPORT" -U "$PGADMINUSER" -d postgres -tAc \
      "SELECT 1 FROM pg_roles WHERE rolname = '\''$APP_USER'\''" | grep -q 1; then
      echo "[authverse-db-init] 目标应用角色不存在：$APP_USER" >&2
      echo "[authverse-db-init] 请先确认 AUTHVERSE_DB_USERNAME 或 Cloudreve 主库角色配置。" >&2
      exit 1
    fi

    if psql -h "$PGHOST" -p "$PGPORT" -U "$PGADMINUSER" -d postgres -tAc \
      "SELECT 1 FROM pg_database WHERE datname = '\''$APP_DB'\''" | grep -q 1; then
      echo "[authverse-db-init] 重建数据库：$APP_DB"
      psql -h "$PGHOST" -p "$PGPORT" -U "$PGADMINUSER" -d postgres -v ON_ERROR_STOP=1 -c \
        "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '\''$APP_DB'\'' AND pid <> pg_backend_pid();"
      dropdb -h "$PGHOST" -p "$PGPORT" -U "$PGADMINUSER" "$APP_DB"
    fi

    echo "[authverse-db-init] 创建数据库：$APP_DB"
    createdb -h "$PGHOST" -p "$PGPORT" -U "$PGADMINUSER" -O "$APP_USER" "$APP_DB"

    echo "[authverse-db-init] 导入基础 SQL"
    psql -h "$PGHOST" -p "$PGPORT" -U "$PGADMINUSER" -d "$APP_DB" -v ON_ERROR_STOP=1 -f /sql/authverse-base.sql

    echo "[authverse-db-init] 导入统一接入注册中心 SQL"
    psql -h "$PGHOST" -p "$PGPORT" -U "$PGADMINUSER" -d "$APP_DB" -v ON_ERROR_STOP=1 -f /sql/authverse-registry.sql

    echo "[authverse-db-init] 校正数据库对象所有权与授权"
    psql -h "$PGHOST" -p "$PGPORT" -U "$PGADMINUSER" -d "$APP_DB" -v ON_ERROR_STOP=1 <<SQL
ALTER DATABASE "$APP_DB" OWNER TO "$APP_USER";
ALTER SCHEMA public OWNER TO "$APP_USER";
DO \$authverse\$
DECLARE
  stmt text;
BEGIN
  FOR stmt IN
    SELECT CASE c.relkind
      WHEN '\''S'\'' THEN format('\''ALTER SEQUENCE %I.%I OWNER TO %I'\'', n.nspname, c.relname, '\''$APP_USER'\'')
      WHEN '\''v'\'' THEN format('\''ALTER VIEW %I.%I OWNER TO %I'\'', n.nspname, c.relname, '\''$APP_USER'\'')
      WHEN '\''m'\'' THEN format('\''ALTER MATERIALIZED VIEW %I.%I OWNER TO %I'\'', n.nspname, c.relname, '\''$APP_USER'\'')
      WHEN '\''f'\'' THEN format('\''ALTER FOREIGN TABLE %I.%I OWNER TO %I'\'', n.nspname, c.relname, '\''$APP_USER'\'')
      ELSE format('\''ALTER TABLE %I.%I OWNER TO %I'\'', n.nspname, c.relname, '\''$APP_USER'\'')
    END
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = '\''public'\''
      AND c.relkind IN ('\''r'\'','\''p'\'','\''S'\'','\''v'\'','\''m'\'','\''f'\'')
      AND pg_get_userbyid(c.relowner) = '\''$PGADMINUSER'\''
  LOOP
    EXECUTE stmt;
  END LOOP;

  FOR stmt IN
    SELECT format(
      '\''ALTER FUNCTION %I.%I(%s) OWNER TO %I'\'',
      n.nspname,
      p.proname,
      pg_get_function_identity_arguments(p.oid),
      '\''$APP_USER'\''
    )
    FROM pg_proc p
    JOIN pg_namespace n ON n.oid = p.pronamespace
    WHERE n.nspname = '\''public'\''
      AND pg_get_userbyid(p.proowner) = '\''$PGADMINUSER'\''
  LOOP
    EXECUTE stmt;
  END LOOP;
END
\$authverse\$;
GRANT ALL PRIVILEGES ON DATABASE "$APP_DB" TO "$APP_USER";
GRANT USAGE, CREATE ON SCHEMA public TO "$APP_USER";
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO "$APP_USER";
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO "$APP_USER";
GRANT ALL PRIVILEGES ON ALL FUNCTIONS IN SCHEMA public TO "$APP_USER";
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON TABLES TO "$APP_USER";
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON SEQUENCES TO "$APP_USER";
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON FUNCTIONS TO "$APP_USER";
SQL
  '

echo "[$HOST_NAME] 统一认证数据库初始化完成。"
