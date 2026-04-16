#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
POSTGRES_INIT_IMAGE="${POSTGRES_INIT_IMAGE:-bitnamilegacy/postgresql-repmgr:17.6.0-debian-12-r2}"
AUTHVERSE_BACKEND_DIR="${AUTHVERSE_BACKEND_DIR:-$ROOT_DIR/../authverse-backend}"
SQL_BASE_FILE="$AUTHVERSE_BACKEND_DIR/sql/postgresql/authverse-20260329.sql"
SQL_REGISTRY_FILE="$AUTHVERSE_BACKEND_DIR/sql/postgresql/authverse-20260329-authz-integration-registry.sql"
HOST_NAME="$(hostname -s 2>/dev/null || hostname)"

usage() {
  cat <<'EOF'
用法：
  docker/swarm/init-authverse-db.sh [--env-file 文件]

说明：
  这个脚本会通过 Pgpool 的对外 TLS 入口连接 Cloudreve 主库，
  重建专用 `authverse` 数据库，
  然后执行统一认证初始化 SQL，并修正对象 owner / grant。

注意：
  1. 脚本默认连接本机 `pgpool` 发布端口，不再依赖 attachable overlay 网络
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

SWARM_PKI_MOUNT_TYPE="${SWARM_PKI_MOUNT_TYPE:-bind}"
SWARM_PKI_MOUNT_SOURCE="${SWARM_PKI_MOUNT_SOURCE:-/srv/cloudreve/pki}"
AUTHVERSE_DB_INIT_MODE="${AUTHVERSE_DB_INIT_MODE:-public-port}"
AUTHVERSE_DB_INIT_HOST="${AUTHVERSE_DB_INIT_HOST:-127.0.0.1}"
AUTHVERSE_DB_INIT_PORT="${AUTHVERSE_DB_INIT_PORT:-${PGPOOL_PUBLIC_PORT:-15432}}"
AUTHVERSE_DB_INIT_SSLMODE="${AUTHVERSE_DB_INIT_SSLMODE:-verify-ca}"
AUTHVERSE_DB_INIT_SSLROOTCERT="${AUTHVERSE_DB_INIT_SSLROOTCERT:-/pki/ca/ca.crt}"
AUTHVERSE_DB_PRIMARY_CONTAINER_NAME="${AUTHVERSE_DB_PRIMARY_CONTAINER_NAME:-${CLOUDREVE_STACK_NAME:-cloudreve}_postgresql-1}"
AUTHVERSE_DB_NAME="${AUTHVERSE_DB_NAME:-authverse}"
AUTHVERSE_DB_USERNAME="${AUTHVERSE_DB_USERNAME:-${POSTGRESQL_USERNAME:-cloudreve}}"
AUTHVERSE_DB_ADMIN_USER="${AUTHVERSE_DB_ADMIN_USER:-postgres}"
AUTHVERSE_DB_ADMIN_PASSWORD="${AUTHVERSE_DB_ADMIN_PASSWORD:-${POSTGRESQL_POSTGRES_PASSWORD:-}}"

if [[ -z "$AUTHVERSE_DB_ADMIN_PASSWORD" ]]; then
  echo "[$HOST_NAME] 缺少 AUTHVERSE_DB_ADMIN_PASSWORD 或 POSTGRESQL_POSTGRES_PASSWORD。" >&2
  exit 1
fi

if [[ "${SWARM_IMAGE_SOURCE:-}" == "remote" && "$AUTHVERSE_DB_INIT_MODE" == "public-port" ]]; then
  if [[ -z "${PRIVATE_REGISTRY_ADDR:-}" ]]; then
    echo "[$HOST_NAME] SWARM_IMAGE_SOURCE=remote 时缺少 PRIVATE_REGISTRY_ADDR。" >&2
    exit 1
  fi
  if [[ "$POSTGRES_INIT_IMAGE" != "${PRIVATE_REGISTRY_ADDR}/"* ]]; then
    echo "[$HOST_NAME] AUTHVERSE_DB_INIT_MODE=public-port 时，POSTGRES_INIT_IMAGE 必须走私有仓库，当前为：$POSTGRES_INIT_IMAGE" >&2
    exit 1
  fi
fi

ensure_pki_mount_source() {
  case "$SWARM_PKI_MOUNT_TYPE" in
    bind)
      if [[ ! -e "$SWARM_PKI_MOUNT_SOURCE" ]]; then
        echo "[$HOST_NAME] 找不到 SWARM_PKI_MOUNT_SOURCE：$SWARM_PKI_MOUNT_SOURCE" >&2
        exit 1
      fi
      ;;
    volume)
      if ! docker volume inspect "$SWARM_PKI_MOUNT_SOURCE" >/dev/null 2>&1; then
        echo "[$HOST_NAME] 找不到 PKI volume：$SWARM_PKI_MOUNT_SOURCE" >&2
        exit 1
      fi
      ;;
    *)
      echo "[$HOST_NAME] 不支持的 SWARM_PKI_MOUNT_TYPE：$SWARM_PKI_MOUNT_TYPE" >&2
      exit 1
      ;;
  esac
}

run_authverse_sql_on_primary_exec() {
  local primary_container
  local psql_bin="/opt/bitnami/postgresql/bin/psql"
  local createdb_bin="/opt/bitnami/postgresql/bin/createdb"
  local dropdb_bin="/opt/bitnami/postgresql/bin/dropdb"
  local pg_isready_bin="/opt/bitnami/postgresql/bin/pg_isready"

  primary_container="$(docker ps --filter "name=${AUTHVERSE_DB_PRIMARY_CONTAINER_NAME}" --format '{{.ID}}' | head -n1)"
  if [[ -z "$primary_container" ]]; then
    echo "[$HOST_NAME] 找不到主库容器：${AUTHVERSE_DB_PRIMARY_CONTAINER_NAME}" >&2
    exit 1
  fi

  echo "[$HOST_NAME] 使用主库容器直连模式初始化：$primary_container"

  until docker exec -e PGPASSWORD="$AUTHVERSE_DB_ADMIN_PASSWORD" "$primary_container" \
    "$pg_isready_bin" -h 127.0.0.1 -p 5432 -U "$AUTHVERSE_DB_ADMIN_USER" >/dev/null 2>&1; do
    echo "[authverse-db-init] 等待 PostgreSQL 主库容器就绪..."
    sleep 2
  done

  if ! docker exec -e PGPASSWORD="$AUTHVERSE_DB_ADMIN_PASSWORD" "$primary_container" \
    "$psql_bin" -U "$AUTHVERSE_DB_ADMIN_USER" -d postgres -tAc \
    "SELECT 1 FROM pg_roles WHERE rolname = '$AUTHVERSE_DB_USERNAME'" | grep -q 1; then
    echo "[authverse-db-init] 目标应用角色不存在：$AUTHVERSE_DB_USERNAME" >&2
    echo "[authverse-db-init] 请先确认 AUTHVERSE_DB_USERNAME 或 Cloudreve 主库角色配置。" >&2
    exit 1
  fi

  if docker exec -e PGPASSWORD="$AUTHVERSE_DB_ADMIN_PASSWORD" "$primary_container" \
    "$psql_bin" -U "$AUTHVERSE_DB_ADMIN_USER" -d postgres -tAc \
    "SELECT 1 FROM pg_database WHERE datname = '$AUTHVERSE_DB_NAME'" | grep -q 1; then
    echo "[authverse-db-init] 重建数据库：$AUTHVERSE_DB_NAME"
    docker exec -e PGPASSWORD="$AUTHVERSE_DB_ADMIN_PASSWORD" "$primary_container" \
      "$psql_bin" -U "$AUTHVERSE_DB_ADMIN_USER" -d postgres -v ON_ERROR_STOP=1 -c \
      "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$AUTHVERSE_DB_NAME' AND pid <> pg_backend_pid();" >/dev/null
    docker exec -e PGPASSWORD="$AUTHVERSE_DB_ADMIN_PASSWORD" "$primary_container" \
      "$dropdb_bin" -U "$AUTHVERSE_DB_ADMIN_USER" "$AUTHVERSE_DB_NAME"
  fi

  echo "[authverse-db-init] 创建数据库：$AUTHVERSE_DB_NAME"
  docker exec -e PGPASSWORD="$AUTHVERSE_DB_ADMIN_PASSWORD" "$primary_container" \
    "$createdb_bin" -U "$AUTHVERSE_DB_ADMIN_USER" -O "$AUTHVERSE_DB_USERNAME" "$AUTHVERSE_DB_NAME"

  echo "[authverse-db-init] 导入基础 SQL"
  cat "$SQL_BASE_FILE" | docker exec -i -e PGPASSWORD="$AUTHVERSE_DB_ADMIN_PASSWORD" "$primary_container" \
    "$psql_bin" -U "$AUTHVERSE_DB_ADMIN_USER" -d "$AUTHVERSE_DB_NAME" -v ON_ERROR_STOP=1 -f -

  echo "[authverse-db-init] 导入统一接入注册中心 SQL"
  cat "$SQL_REGISTRY_FILE" | docker exec -i -e PGPASSWORD="$AUTHVERSE_DB_ADMIN_PASSWORD" "$primary_container" \
    "$psql_bin" -U "$AUTHVERSE_DB_ADMIN_USER" -d "$AUTHVERSE_DB_NAME" -v ON_ERROR_STOP=1 -f -

  echo "[authverse-db-init] 校正数据库对象所有权与授权"
  docker exec -i -e PGPASSWORD="$AUTHVERSE_DB_ADMIN_PASSWORD" "$primary_container" \
    "$psql_bin" -U "$AUTHVERSE_DB_ADMIN_USER" -d "$AUTHVERSE_DB_NAME" -v ON_ERROR_STOP=1 -f - <<SQL
ALTER DATABASE "$AUTHVERSE_DB_NAME" OWNER TO "$AUTHVERSE_DB_USERNAME";
ALTER SCHEMA public OWNER TO "$AUTHVERSE_DB_USERNAME";
DO \$authverse\$
DECLARE
  stmt text;
BEGIN
  FOR stmt IN
    SELECT CASE c.relkind
      WHEN 'S' THEN format('ALTER SEQUENCE %I.%I OWNER TO %I', n.nspname, c.relname, '$AUTHVERSE_DB_USERNAME')
      WHEN 'v' THEN format('ALTER VIEW %I.%I OWNER TO %I', n.nspname, c.relname, '$AUTHVERSE_DB_USERNAME')
      WHEN 'm' THEN format('ALTER MATERIALIZED VIEW %I.%I OWNER TO %I', n.nspname, c.relname, '$AUTHVERSE_DB_USERNAME')
      WHEN 'f' THEN format('ALTER FOREIGN TABLE %I.%I OWNER TO %I', n.nspname, c.relname, '$AUTHVERSE_DB_USERNAME')
      ELSE format('ALTER TABLE %I.%I OWNER TO %I', n.nspname, c.relname, '$AUTHVERSE_DB_USERNAME')
    END
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public'
      AND c.relkind IN ('r','p','S','v','m','f')
      AND pg_get_userbyid(c.relowner) = '$AUTHVERSE_DB_ADMIN_USER'
  LOOP
    EXECUTE stmt;
  END LOOP;

  FOR stmt IN
    SELECT format(
      'ALTER FUNCTION %I.%I(%s) OWNER TO %I',
      n.nspname,
      p.proname,
      pg_get_function_identity_arguments(p.oid),
      '$AUTHVERSE_DB_USERNAME'
    )
    FROM pg_proc p
    JOIN pg_namespace n ON n.oid = p.pronamespace
    WHERE n.nspname = 'public'
      AND pg_get_userbyid(p.proowner) = '$AUTHVERSE_DB_ADMIN_USER'
  LOOP
    EXECUTE stmt;
  END LOOP;
END
\$authverse\$;
GRANT ALL PRIVILEGES ON DATABASE "$AUTHVERSE_DB_NAME" TO "$AUTHVERSE_DB_USERNAME";
GRANT USAGE, CREATE ON SCHEMA public TO "$AUTHVERSE_DB_USERNAME";
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO "$AUTHVERSE_DB_USERNAME";
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO "$AUTHVERSE_DB_USERNAME";
GRANT ALL PRIVILEGES ON ALL FUNCTIONS IN SCHEMA public TO "$AUTHVERSE_DB_USERNAME";
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON TABLES TO "$AUTHVERSE_DB_USERNAME";
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON SEQUENCES TO "$AUTHVERSE_DB_USERNAME";
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON FUNCTIONS TO "$AUTHVERSE_DB_USERNAME";
SQL
}

run_authverse_sql_via_public_port() {
  ensure_pki_mount_source

  docker run --rm \
    --network host \
    -e PGHOST="$AUTHVERSE_DB_INIT_HOST" \
    -e PGPORT="$AUTHVERSE_DB_INIT_PORT" \
    -e PGSSLMODE="$AUTHVERSE_DB_INIT_SSLMODE" \
    -e PGSSLROOTCERT="$AUTHVERSE_DB_INIT_SSLROOTCERT" \
    -e PGADMINUSER="$AUTHVERSE_DB_ADMIN_USER" \
    -e PGPASSWORD_ADMIN="$AUTHVERSE_DB_ADMIN_PASSWORD" \
    -e APP_DB="$AUTHVERSE_DB_NAME" \
    -e APP_USER="$AUTHVERSE_DB_USERNAME" \
    -v "$SWARM_PKI_MOUNT_SOURCE:/pki:ro" \
    -v "$SQL_BASE_FILE:/sql/authverse-base.sql:ro" \
    -v "$SQL_REGISTRY_FILE:/sql/authverse-registry.sql:ro" \
    "$POSTGRES_INIT_IMAGE" \
    bash -ec '
      export PATH="/opt/bitnami/postgresql/bin:$PATH"
      export PGPASSWORD="$PGPASSWORD_ADMIN"

      if [[ "$PGSSLMODE" != "disable" && ! -f "$PGSSLROOTCERT" ]]; then
        echo "[authverse-db-init] 缺少 CA 文件：$PGSSLROOTCERT" >&2
        exit 1
      fi

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
}

echo "[$HOST_NAME] 准备初始化统一认证数据库：$AUTHVERSE_DB_NAME"
case "$AUTHVERSE_DB_INIT_MODE" in
  primary-exec)
    run_authverse_sql_on_primary_exec
    ;;
  public-port)
    run_authverse_sql_via_public_port
    ;;
  *)
    echo "[$HOST_NAME] 不支持的 AUTHVERSE_DB_INIT_MODE：$AUTHVERSE_DB_INIT_MODE" >&2
    exit 1
    ;;
esac

echo "[$HOST_NAME] 统一认证数据库初始化完成。"
