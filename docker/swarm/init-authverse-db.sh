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
  这个脚本会在共享 PostgreSQL 集群里创建 `authverse` 数据库（如果还不存在），
  然后执行统一认证初始化 SQL。

注意：
  1. 脚本默认连接 Cloudreve 主栈里的 `pgpool`
  2. SQL 是全量建库脚本，会先 drop 再 create 目标数据库内的表
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
AUTHVERSE_POSTGRES_HOST="${AUTHVERSE_POSTGRES_HOST:-${AUTHVERSE_CLOUDREVE_SERVICE_PREFIX}pgpool}"
AUTHVERSE_POSTGRES_PORT="${AUTHVERSE_POSTGRES_PORT:-5432}"
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
  -e PGHOST="$AUTHVERSE_POSTGRES_HOST" \
  -e PGPORT="$AUTHVERSE_POSTGRES_PORT" \
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

    if ! psql -h "$PGHOST" -p "$PGPORT" -U "$PGADMINUSER" -d postgres -tAc \
      "SELECT 1 FROM pg_database WHERE datname = '\''$APP_DB'\''" | grep -q 1; then
      echo "[authverse-db-init] 创建数据库：$APP_DB"
      createdb -h "$PGHOST" -p "$PGPORT" -U "$PGADMINUSER" -O "$APP_USER" "$APP_DB"
    fi

    echo "[authverse-db-init] 导入基础 SQL"
    psql -h "$PGHOST" -p "$PGPORT" -U "$PGADMINUSER" -d "$APP_DB" -v ON_ERROR_STOP=1 -f /sql/authverse-base.sql

    echo "[authverse-db-init] 导入统一接入注册中心 SQL"
    psql -h "$PGHOST" -p "$PGPORT" -U "$PGADMINUSER" -d "$APP_DB" -v ON_ERROR_STOP=1 -f /sql/authverse-registry.sql
  '

echo "[$HOST_NAME] 统一认证数据库初始化完成。"
