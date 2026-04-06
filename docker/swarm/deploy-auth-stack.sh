#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
CLI_STACK_NAME="${STACK_NAME:-}"
RESOLVED_DIR="${RESOLVED_DIR:-$ROOT_DIR/.tmp}"
COMPOSE_FILE="$ROOT_DIR/docker-compose.swarm.auth.yml"
RENDER_ONLY=0

usage() {
  cat <<'EOF'
用法：
  docker/swarm/deploy-auth-stack.sh [--stack-name 名称] [--env-file 文件] [--render-only]

说明：
  这个脚本用于部署统一认证 Swarm 栈。
  它默认复用 Cloudreve 主栈已经存在的 overlay 网络和中间件入口：
  - PostgreSQL：pgpool
  - Redis：redis-proxy
  - Elasticsearch：elasticsearch

部署顺序建议：
  1. 先确保 Cloudreve 主栈已经部署完成
  2. 执行 `docker/swarm/init-authverse-db.sh`
  3. 执行 `docker/swarm/build-auth-images.sh`
  4. 执行 `docker/swarm/publish-private-images.sh --image-keys AUTHVERSE_WEB,AUTHVERSE_BACKEND`
  5. 执行本脚本部署统一认证栈

参数：
  --stack-name NAME   栈名称，默认读取 AUTHVERSE_STACK_NAME 或 authverse
  --env-file FILE     环境变量文件，默认是 .env.swarm
  --render-only       只渲染，不部署
  -h, --help          显示帮助
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --stack-name)
      CLI_STACK_NAME="$2"
      shift 2
      ;;
    --env-file)
      ENV_FILE="$2"
      shift 2
      ;;
    --render-only)
      RENDER_ONLY=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "未知参数: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

if [[ ! -f "$COMPOSE_FILE" ]]; then
  echo "找不到 Compose 文件: $COMPOSE_FILE" >&2
  exit 1
fi

if [[ ! -f "$ENV_FILE" ]]; then
  echo "找不到环境变量文件: $ENV_FILE" >&2
  exit 1
fi

if ! docker info --format '{{.Swarm.LocalNodeState}}' | grep -qx 'active'; then
  echo "当前节点还没有启用 Docker Swarm。" >&2
  exit 1
fi

mkdir -p "$RESOLVED_DIR"

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

CLOUDREVE_STACK_NAME="${CLOUDREVE_STACK_NAME:-cloudreve}"
AUTHVERSE_STACK_NAME="${CLI_STACK_NAME:-${AUTHVERSE_STACK_NAME:-authverse}}"
AUTHVERSE_CLOUDREVE_SERVICE_PREFIX="${AUTHVERSE_CLOUDREVE_SERVICE_PREFIX:-${CLOUDREVE_STACK_NAME}_}"
AUTHVERSE_SHARED_NETWORK="${AUTHVERSE_SHARED_NETWORK:-${CLOUDREVE_STACK_NAME}_cloudreve_backend}"
AUTHVERSE_CLOUDREVE_INTERNAL_UPSTREAM="${AUTHVERSE_CLOUDREVE_INTERNAL_UPSTREAM:-${AUTHVERSE_CLOUDREVE_SERVICE_PREFIX}cloudreve-master:5212}"
AUTHVERSE_POSTGRES_HOST="${AUTHVERSE_POSTGRES_HOST:-${AUTHVERSE_CLOUDREVE_SERVICE_PREFIX}pgpool}"
AUTHVERSE_POSTGRES_PORT="${AUTHVERSE_POSTGRES_PORT:-5432}"
AUTHVERSE_REDIS_HOST="${AUTHVERSE_REDIS_HOST:-${AUTHVERSE_CLOUDREVE_SERVICE_PREFIX}redis-proxy}"
AUTHVERSE_REDIS_PORT="${AUTHVERSE_REDIS_PORT:-6379}"
AUTHVERSE_ELASTICSEARCH_URI="${AUTHVERSE_ELASTICSEARCH_URI:-http://${AUTHVERSE_CLOUDREVE_SERVICE_PREFIX}elasticsearch:9200}"
AUTHVERSE_DB_NAME="${AUTHVERSE_DB_NAME:-authverse}"
AUTHVERSE_DB_USERNAME="${AUTHVERSE_DB_USERNAME:-${POSTGRESQL_USERNAME:-cloudreve}}"
AUTHVERSE_DB_PASSWORD="${AUTHVERSE_DB_PASSWORD:-${POSTGRESQL_PASSWORD:-}}"
AUTHVERSE_DB_MASTER_URL="${AUTHVERSE_DB_MASTER_URL:-jdbc:postgresql://${AUTHVERSE_POSTGRES_HOST}:${AUTHVERSE_POSTGRES_PORT}/${AUTHVERSE_DB_NAME}}"
AUTHVERSE_DB_SLAVE_URL="${AUTHVERSE_DB_SLAVE_URL:-$AUTHVERSE_DB_MASTER_URL}"
AUTHVERSE_DB_SLAVE_USERNAME="${AUTHVERSE_DB_SLAVE_USERNAME:-$AUTHVERSE_DB_USERNAME}"
AUTHVERSE_DB_SLAVE_PASSWORD="${AUTHVERSE_DB_SLAVE_PASSWORD:-$AUTHVERSE_DB_PASSWORD}"
AUTHVERSE_REDIS_PASSWORD="${AUTHVERSE_REDIS_PASSWORD:-${REDIS_PASSWORD:-}}"
AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL="${AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL:-${CLOUDREVE_SITE_URL:-}}"

export CLOUDREVE_STACK_NAME
export AUTHVERSE_STACK_NAME
export AUTHVERSE_CLOUDREVE_SERVICE_PREFIX
export AUTHVERSE_SHARED_NETWORK
export AUTHVERSE_CLOUDREVE_INTERNAL_UPSTREAM
export AUTHVERSE_POSTGRES_HOST
export AUTHVERSE_POSTGRES_PORT
export AUTHVERSE_REDIS_HOST
export AUTHVERSE_REDIS_PORT
export AUTHVERSE_ELASTICSEARCH_URI
export AUTHVERSE_DB_NAME
export AUTHVERSE_DB_USERNAME
export AUTHVERSE_DB_PASSWORD
export AUTHVERSE_DB_MASTER_URL
export AUTHVERSE_DB_SLAVE_URL
export AUTHVERSE_DB_SLAVE_USERNAME
export AUTHVERSE_DB_SLAVE_PASSWORD
export AUTHVERSE_REDIS_PASSWORD
export AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL

if ! docker network inspect "$AUTHVERSE_SHARED_NETWORK" >/dev/null 2>&1; then
  echo "共享 overlay 网络不存在：$AUTHVERSE_SHARED_NETWORK" >&2
  echo "请先部署 Cloudreve 主栈，或者把 AUTHVERSE_SHARED_NETWORK 改成真实网络名。" >&2
  exit 1
fi

resolved_file="$RESOLVED_DIR/${AUTHVERSE_STACK_NAME}-resolved.yaml"

echo "正在渲染统一认证配置到: $resolved_file"
docker stack config -c "$COMPOSE_FILE" >"$resolved_file"

if [[ "$RENDER_ONLY" -eq 1 ]]; then
  echo "渲染完成。"
  exit 0
fi

echo "正在部署统一认证栈: $AUTHVERSE_STACK_NAME"
docker stack deploy -c "$resolved_file" "$AUTHVERSE_STACK_NAME"

echo
echo "当前服务状态："
docker stack services "$AUTHVERSE_STACK_NAME"
