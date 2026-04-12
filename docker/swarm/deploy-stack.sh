#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
CLI_STACK_NAME="${STACK_NAME:-}"
RESOLVED_DIR="${RESOLVED_DIR:-$ROOT_DIR/.tmp}"
COMPOSE_NAME="${COMPOSE_NAME:-}"
COMPOSE_FILE="${COMPOSE_FILE:-}"
SINGLE_COMPOSE_FILE="$ROOT_DIR/docker-compose.swarm.single.yml"
CLOUDREVE_COMPOSE_FILE="$ROOT_DIR/docker-compose.swarm.cloudreve.yml"
FOUNDATION_COMPOSE_FILE="$ROOT_DIR/docker-compose.swarm.foundation.yml"
INFRA_COMPOSE_FILE="$ROOT_DIR/docker-compose.swarm.infra.yml"
RENDER_ONLY=0

usage() {
  cat <<'EOF'
用法：
  docker/swarm/deploy-stack.sh [compose-name] [--stack-name 名称] [--env-file 文件] [--compose-name 名称] [--compose-file 文件] [--render-only]

说明：
  这是统一的 Swarm 发布入口。

  1. 传入 compose-name 时，会自动解析并发布对应文件：
     - single    -> docker-compose.swarm.single.yml
     - cloudreve -> docker-compose.swarm.cloudreve.yml
     - auth      -> docker-compose.swarm.auth.yml
     - registry  -> docker-compose.swarm.registry.yml
     - foundation -> docker-compose.swarm.foundation.yml
     - infra     -> docker-compose.swarm.infra.yml

  2. 不传 compose-name 时，默认发布 docker-compose.swarm.single.yml

参数：
  compose-name        直接传模板名，例如：single / cloudreve / foundation / infra / auth / registry
  --stack-name NAME   Swarm 栈名称
  --env-file FILE     要读取的环境变量文件，默认是 .env.swarm
  --compose-name NAME 显式指定模板名，等价于第一个位置参数
  --compose-file FILE 直接指定 Compose 文件路径
  --render-only       只渲染 stack 配置，不执行部署
  -h, --help          显示帮助

示例：
  docker/swarm/deploy-stack.sh
  docker/swarm/deploy-stack.sh single
  docker/swarm/deploy-stack.sh cloudreve --stack-name cloudreve-prod
  docker/swarm/deploy-stack.sh foundation --stack-name cloudreve-prod
  docker/swarm/deploy-stack.sh infra --stack-name cloudreve-prod
  docker/swarm/deploy-stack.sh auth --stack-name authverse-prod
  docker/swarm/deploy-stack.sh registry --stack-name cloudreve-registry

可通过环境变量覆盖：
  STACK_NAME, ENV_FILE, RESOLVED_DIR, COMPOSE_NAME, COMPOSE_FILE
EOF
}

rewrite_relative_config_paths() {
  local src="$1"
  local dst="$2"
  local line prefix suffix

  : >"$dst"
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" == *"file: ./"* ]]; then
      prefix="${line%%./*}"
      suffix="${line#"$prefix./"}"
      printf '%s%s/%s\n' "$prefix" "$ROOT_DIR" "$suffix" >>"$dst"
    elif [[ "$line" == *"file: $RESOLVED_DIR/"* ]]; then
      prefix="${line%%$RESOLVED_DIR/*}"
      suffix="${line#"$prefix$RESOLVED_DIR/"}"
      if [[ "$suffix" == docker/* ]]; then
        printf '%s%s/%s\n' "$prefix" "$ROOT_DIR" "$suffix" >>"$dst"
      else
        printf '%s\n' "$line" >>"$dst"
      fi
    else
      printf '%s\n' "$line" >>"$dst"
    fi
  done <"$src"
}

resolve_named_compose_file() {
  local name="$1"

  case "$name" in
    app|cloudreve|main)
      printf '%s\n' "$CLOUDREVE_COMPOSE_FILE"
      ;;
    cluster|infra)
      printf '%s\n' "$INFRA_COMPOSE_FILE"
      ;;
    single|foundation|auth|registry)
      printf '%s\n' "$ROOT_DIR/docker-compose.swarm.${name}.yml"
      ;;
    *)
      local candidate="$ROOT_DIR/docker-compose.swarm.${name}.yml"
      if [[ -f "$candidate" ]]; then
        printf '%s\n' "$candidate"
        return 0
      fi
      return 1
      ;;
  esac
}

detect_compose_kind() {
  case "$(basename "$1")" in
    docker-compose.swarm.yml)
      printf '%s\n' "cloudreve"
      ;;
    docker-compose.swarm.cloudreve.yml)
      printf '%s\n' "cloudreve"
      ;;
    docker-compose.swarm.single.yml)
      printf '%s\n' "single"
      ;;
    docker-compose.swarm.foundation.yml)
      printf '%s\n' "foundation"
      ;;
    docker-compose.swarm.cluster.yml)
      printf '%s\n' "infra"
      ;;
    docker-compose.swarm.infra.yml)
      printf '%s\n' "infra"
      ;;
    docker-compose.swarm.auth.yml)
      printf '%s\n' "auth"
      ;;
    docker-compose.swarm.registry.yml)
      printf '%s\n' "registry"
      ;;
    *)
      printf '%s\n' "custom"
      ;;
  esac
}

map_legacy_mount_var() {
  local legacy_var="$1"
  local type_var="$2"
  local source_var="$3"
  local legacy_value="${!legacy_var:-}"
  local source_value="${!source_var:-}"

  if [[ -n "$legacy_value" && -z "$source_value" ]]; then
    printf -v "$type_var" '%s' "${!type_var:-bind}"
    printf -v "$source_var" '%s' "$legacy_value"
    export "$type_var" "$source_var"
  fi
}

prepare_common_mount_env() {
  map_legacy_mount_var "PG_1_DATA_PATH" "PG_1_DATA_MOUNT_TYPE" "PG_1_DATA_MOUNT_SOURCE"
  map_legacy_mount_var "PG_2_DATA_PATH" "PG_2_DATA_MOUNT_TYPE" "PG_2_DATA_MOUNT_SOURCE"
  map_legacy_mount_var "PG_3_DATA_PATH" "PG_3_DATA_MOUNT_TYPE" "PG_3_DATA_MOUNT_SOURCE"
  map_legacy_mount_var "REDIS_1_DATA_PATH" "REDIS_1_DATA_MOUNT_TYPE" "REDIS_1_DATA_MOUNT_SOURCE"
  map_legacy_mount_var "REDIS_2_DATA_PATH" "REDIS_2_DATA_MOUNT_TYPE" "REDIS_2_DATA_MOUNT_SOURCE"
  map_legacy_mount_var "REDIS_3_DATA_PATH" "REDIS_3_DATA_MOUNT_TYPE" "REDIS_3_DATA_MOUNT_SOURCE"

  if [[ -n "${TIKA_CUSTOM_FONTS_HOST_PATH:-}" && -z "${TIKA_CUSTOM_FONTS_MOUNT_SOURCE:-}" ]]; then
    export TIKA_CUSTOM_FONTS_MOUNT_TYPE="${TIKA_CUSTOM_FONTS_MOUNT_TYPE:-bind}"
    export TIKA_CUSTOM_FONTS_MOUNT_SOURCE="$TIKA_CUSTOM_FONTS_HOST_PATH"
  fi

  if [[ -n "${SHARED_CUSTOM_FONTS_HOST_PATH:-}" && -z "${SHARED_CUSTOM_FONTS_MOUNT_SOURCE:-}" ]]; then
    export SHARED_CUSTOM_FONTS_MOUNT_TYPE="${SHARED_CUSTOM_FONTS_MOUNT_TYPE:-bind}"
    export SHARED_CUSTOM_FONTS_MOUNT_SOURCE="$SHARED_CUSTOM_FONTS_HOST_PATH"
  fi

  if [[ -n "${SHARED_CUSTOM_FONTS_MOUNT_SOURCE:-}" ]]; then
    export TIKA_CUSTOM_FONTS_MOUNT_TYPE="${TIKA_CUSTOM_FONTS_MOUNT_TYPE:-${SHARED_CUSTOM_FONTS_MOUNT_TYPE:-bind}}"
    export TIKA_CUSTOM_FONTS_MOUNT_SOURCE="${TIKA_CUSTOM_FONTS_MOUNT_SOURCE:-$SHARED_CUSTOM_FONTS_MOUNT_SOURCE}"
    export ONLYOFFICE_CUSTOM_FONTS_MOUNT_TYPE="${ONLYOFFICE_CUSTOM_FONTS_MOUNT_TYPE:-${SHARED_CUSTOM_FONTS_MOUNT_TYPE:-bind}}"
    export ONLYOFFICE_CUSTOM_FONTS_MOUNT_SOURCE="${ONLYOFFICE_CUSTOM_FONTS_MOUNT_SOURCE:-$SHARED_CUSTOM_FONTS_MOUNT_SOURCE}"
  fi

  export SWARM_PKI_MOUNT_TYPE="${SWARM_PKI_MOUNT_TYPE:-bind}"
  export SWARM_PKI_MOUNT_SOURCE="${SWARM_PKI_MOUNT_SOURCE:-/srv/cloudreve/pki}"
  export SWARM_PKI_MOUNT_TARGET="${SWARM_PKI_MOUNT_TARGET:-/run/swarm-pki}"
}

prepare_image_source_env() {
  local image_source="${SWARM_IMAGE_SOURCE:-}"
  local image_keys=(
    CLOUDREVE
    CLOUDREVE_SLAVE
    NGINX
    PRIVATE_REGISTRY
    TIKA
    AUTHVERSE_WEB
    AUTHVERSE_BACKEND
    POSTGRESQL_REPMGR
    PGPOOL
    REDIS
    REDIS_SENTINEL
    REDIS_PROXY
    MINIO
    MINIO_PROXY
    ELASTICSEARCH
    ELASTICSEARCH_PROXY
    KAFKA
    KAFKA_PROXY
    KAFKA_UI
    ONLYOFFICE
    ONLYOFFICE_RABBITMQ
  )
  local key target_var local_var remote_var resolved_value

  case "$image_source" in
    ""|local|remote)
      ;;
    *)
      echo "不支持的 SWARM_IMAGE_SOURCE: $image_source，允许值: local / remote" >&2
      exit 1
      ;;
  esac

  for key in "${image_keys[@]}"; do
    target_var="${key}_IMAGE"
    local_var="${key}_LOCAL_IMAGE"
    remote_var="${key}_REMOTE_IMAGE"

    case "$image_source" in
      remote)
        resolved_value="${!remote_var:-${!target_var:-${!local_var:-}}}"
        ;;
      *)
        resolved_value="${!local_var:-${!target_var:-${!remote_var:-}}}"
        ;;
    esac

    if [[ -n "$resolved_value" ]]; then
      printf -v "$target_var" '%s' "$resolved_value"
      export "$target_var"
    fi
  done
}

prepare_auth_common_env() {
  AUTHVERSE_DB_NAME="${AUTHVERSE_DB_NAME:-authverse}"
  AUTHVERSE_DB_USERNAME="${AUTHVERSE_DB_USERNAME:-${POSTGRESQL_USERNAME:-cloudreve}}"
  AUTHVERSE_DB_PASSWORD="${AUTHVERSE_DB_PASSWORD:-${POSTGRESQL_PASSWORD:-}}"
  AUTHVERSE_DB_SLAVE_USERNAME="${AUTHVERSE_DB_SLAVE_USERNAME:-$AUTHVERSE_DB_USERNAME}"
  AUTHVERSE_DB_SLAVE_PASSWORD="${AUTHVERSE_DB_SLAVE_PASSWORD:-$AUTHVERSE_DB_PASSWORD}"
  AUTHVERSE_REDIS_PASSWORD="${AUTHVERSE_REDIS_PASSWORD:-${REDIS_PASSWORD:-}}"
  AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL="${AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL:-${CLOUDREVE_SITE_URL:-}}"

  export AUTHVERSE_DB_NAME
  export AUTHVERSE_DB_USERNAME
  export AUTHVERSE_DB_PASSWORD
  export AUTHVERSE_DB_SLAVE_USERNAME
  export AUTHVERSE_DB_SLAVE_PASSWORD
  export AUTHVERSE_REDIS_PASSWORD
  export AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL
}

prepare_single_auth_env() {
  AUTHVERSE_SINGLE_CLOUDREVE_INTERNAL_UPSTREAM="${AUTHVERSE_SINGLE_CLOUDREVE_INTERNAL_UPSTREAM:-cloudreve-master:5212}"
  AUTHVERSE_SINGLE_POSTGRES_HOST="${AUTHVERSE_SINGLE_POSTGRES_HOST:-pgpool-internal}"
  AUTHVERSE_SINGLE_POSTGRES_PORT="${AUTHVERSE_SINGLE_POSTGRES_PORT:-5432}"
  AUTHVERSE_SINGLE_REDIS_HOST="${AUTHVERSE_SINGLE_REDIS_HOST:-redis-proxy}"
  AUTHVERSE_SINGLE_REDIS_PORT="${AUTHVERSE_SINGLE_REDIS_PORT:-6379}"
  AUTHVERSE_SINGLE_ELASTICSEARCH_URI="${AUTHVERSE_SINGLE_ELASTICSEARCH_URI:-https://elasticsearch:9200}"
  AUTHVERSE_SINGLE_DB_MASTER_URL="${AUTHVERSE_SINGLE_DB_MASTER_URL:-jdbc:postgresql://${AUTHVERSE_SINGLE_POSTGRES_HOST}:${AUTHVERSE_SINGLE_POSTGRES_PORT}/${AUTHVERSE_DB_NAME}}"
  AUTHVERSE_SINGLE_DB_SLAVE_URL="${AUTHVERSE_SINGLE_DB_SLAVE_URL:-$AUTHVERSE_SINGLE_DB_MASTER_URL}"

  export AUTHVERSE_SINGLE_CLOUDREVE_INTERNAL_UPSTREAM
  export AUTHVERSE_SINGLE_POSTGRES_HOST
  export AUTHVERSE_SINGLE_POSTGRES_PORT
  export AUTHVERSE_SINGLE_REDIS_HOST
  export AUTHVERSE_SINGLE_REDIS_PORT
  export AUTHVERSE_SINGLE_ELASTICSEARCH_URI
  export AUTHVERSE_SINGLE_DB_MASTER_URL
  export AUTHVERSE_SINGLE_DB_SLAVE_URL
}

prepare_external_auth_env() {
  CLOUDREVE_STACK_NAME="${CLOUDREVE_STACK_NAME:-cloudreve}"
  AUTHVERSE_STACK_NAME="$STACK_NAME"
  AUTHVERSE_CLOUDREVE_SERVICE_PREFIX="${AUTHVERSE_CLOUDREVE_SERVICE_PREFIX:-${CLOUDREVE_STACK_NAME}_}"
  AUTHVERSE_SHARED_NETWORK="${AUTHVERSE_SHARED_NETWORK:-${CLOUDREVE_STACK_NAME}_cloudreve_backend}"
  AUTHVERSE_CLOUDREVE_INTERNAL_UPSTREAM="${AUTHVERSE_CLOUDREVE_INTERNAL_UPSTREAM:-${AUTHVERSE_CLOUDREVE_SERVICE_PREFIX}cloudreve-master:5212}"
  AUTHVERSE_POSTGRES_HOST="${AUTHVERSE_POSTGRES_HOST:-${AUTHVERSE_CLOUDREVE_SERVICE_PREFIX}pgpool-internal}"
  AUTHVERSE_POSTGRES_PORT="${AUTHVERSE_POSTGRES_PORT:-5432}"
  AUTHVERSE_REDIS_HOST="${AUTHVERSE_REDIS_HOST:-${AUTHVERSE_CLOUDREVE_SERVICE_PREFIX}redis-proxy}"
  AUTHVERSE_REDIS_PORT="${AUTHVERSE_REDIS_PORT:-6379}"
  AUTHVERSE_ELASTICSEARCH_URI="${AUTHVERSE_ELASTICSEARCH_URI:-https://${AUTHVERSE_CLOUDREVE_SERVICE_PREFIX}elasticsearch:9200}"
  AUTHVERSE_DB_MASTER_URL="${AUTHVERSE_DB_MASTER_URL:-jdbc:postgresql://${AUTHVERSE_POSTGRES_HOST}:${AUTHVERSE_POSTGRES_PORT}/${AUTHVERSE_DB_NAME}}"
  AUTHVERSE_DB_SLAVE_URL="${AUTHVERSE_DB_SLAVE_URL:-$AUTHVERSE_DB_MASTER_URL}"

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
  export AUTHVERSE_DB_MASTER_URL
  export AUTHVERSE_DB_SLAVE_URL

  if [[ "$RENDER_ONLY" -eq 0 ]] && ! docker network inspect "$AUTHVERSE_SHARED_NETWORK" >/dev/null 2>&1; then
    echo "共享 overlay 网络不存在：$AUTHVERSE_SHARED_NETWORK" >&2
    echo "请先部署 Cloudreve 主栈，或者把 AUTHVERSE_SHARED_NETWORK 改成真实网络名。" >&2
    exit 1
  fi
}

maybe_init_authverse_single_db() {
  local web_replicas="${AUTHVERSE_SINGLE_WEB_REPLICAS:-1}"
  local backend_replicas="${AUTHVERSE_SINGLE_BACKEND_REPLICAS:-1}"
  local auto_init="${AUTHVERSE_SINGLE_AUTO_INIT_DB:-yes}"

  if [[ "$COMPOSE_KIND" != "single" ]]; then
    return 0
  fi
  if [[ "$web_replicas" == "0" || "$backend_replicas" == "0" ]]; then
    return 0
  fi

  case "$auto_init" in
    1|true|TRUE|yes|YES|on|ON)
      ;;
    *)
      return 0
      ;;
  esac

  echo "单节点栈已包含 authverse，开始初始化统一认证数据库。"
  "$ROOT_DIR/docker/swarm/init-authverse-db.sh" --env-file "$ENV_FILE"
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
    --compose-name)
      COMPOSE_NAME="$2"
      shift 2
      ;;
    --compose-file)
      COMPOSE_FILE="$2"
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
      if [[ "$1" != -* && -z "$COMPOSE_NAME" ]]; then
        COMPOSE_NAME="$1"
        shift
      else
        echo "未知参数: $1" >&2
        usage >&2
        exit 1
      fi
      ;;
  esac
done

if [[ -n "$COMPOSE_NAME" && -n "$COMPOSE_FILE" ]]; then
  echo "--compose-name 和 --compose-file 不能同时使用。" >&2
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

prepare_common_mount_env
prepare_image_source_env
prepare_auth_common_env

COMPOSE_KIND=""
COMPOSE_LABEL=""
compose_args=()

if [[ -n "$COMPOSE_FILE" ]]; then
  if [[ "$COMPOSE_FILE" != /* ]]; then
    COMPOSE_FILE="$(cd "$(dirname "$COMPOSE_FILE")" && pwd)/$(basename "$COMPOSE_FILE")"
  fi
  if [[ ! -f "$COMPOSE_FILE" ]]; then
    echo "找不到 Compose 文件: $COMPOSE_FILE" >&2
    exit 1
  fi
  COMPOSE_KIND="$(detect_compose_kind "$COMPOSE_FILE")"
  COMPOSE_LABEL="$(basename "$COMPOSE_FILE")"
  compose_args=(-c "$COMPOSE_FILE")
else
  if [[ -n "$COMPOSE_NAME" ]]; then
    resolved_named_file="$(resolve_named_compose_file "$COMPOSE_NAME")" || {
      echo "找不到名为 $COMPOSE_NAME 的 Compose 模板。" >&2
      exit 1
    }
    if [[ ! -f "$resolved_named_file" ]]; then
      echo "找不到 Compose 文件: $resolved_named_file" >&2
      exit 1
    fi
    COMPOSE_KIND="$(detect_compose_kind "$resolved_named_file")"
    COMPOSE_LABEL="$(basename "$resolved_named_file")"
    compose_args=(-c "$resolved_named_file")
  else
    if [[ ! -f "$SINGLE_COMPOSE_FILE" ]]; then
      echo "找不到单节点 Compose 文件: $SINGLE_COMPOSE_FILE" >&2
      exit 1
    fi
    COMPOSE_KIND="single"
    COMPOSE_LABEL="$(basename "$SINGLE_COMPOSE_FILE")"
    compose_args=(-c "$SINGLE_COMPOSE_FILE")
  fi
fi

case "$COMPOSE_KIND" in
  auth)
    STACK_NAME="${CLI_STACK_NAME:-${AUTHVERSE_STACK_NAME:-authverse}}"
    prepare_external_auth_env
    ;;
  registry)
    STACK_NAME="${CLI_STACK_NAME:-${PRIVATE_REGISTRY_STACK_NAME:-cloudreve-registry}}"
    if [[ -z "${PRIVATE_REGISTRY_ADDR:-}" ]]; then
      echo "缺少 PRIVATE_REGISTRY_ADDR，无法部署私有仓库。" >&2
      exit 1
    fi
    ;;
  *)
    STACK_NAME="${CLI_STACK_NAME:-${CLOUDREVE_STACK_NAME:-cloudreve}}"
    export CLOUDREVE_STACK_NAME="$STACK_NAME"
    prepare_single_auth_env
    ;;
esac

resolved_file="$RESOLVED_DIR/${STACK_NAME}-resolved.yaml"
resolved_raw_file="$RESOLVED_DIR/${STACK_NAME}-resolved.raw.yaml"

echo "正在渲染 stack 配置到: $resolved_file"
echo "已使用 Compose 文件: $COMPOSE_LABEL"

docker stack config "${compose_args[@]}" >"$resolved_raw_file"
rewrite_relative_config_paths "$resolved_raw_file" "$resolved_file"
rm -f "$resolved_raw_file"

if [[ "$RENDER_ONLY" -eq 1 ]]; then
  echo "渲染完成。"
  exit 0
fi

echo "正在部署 stack: $STACK_NAME"
docker stack deploy --resolve-image "${SWARM_RESOLVE_IMAGE_MODE:-never}" -c "$resolved_file" "$STACK_NAME"

maybe_init_authverse_single_db

echo
echo "当前服务状态："
docker stack services "$STACK_NAME"
