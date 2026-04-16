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
STACK_SECRET_NAMES=()

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

validate_remote_image_source_env() {
  local image_source="${SWARM_IMAGE_SOURCE:-}"
  local image_keys=(
    CLOUDREVE
    CLOUDREVE_SLAVE
    NGINX
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
  local key target_var image_value

  if [[ "$image_source" != "remote" ]]; then
    return 0
  fi
  if [[ -z "${PRIVATE_REGISTRY_ADDR:-}" ]]; then
    echo "SWARM_IMAGE_SOURCE=remote 时缺少 PRIVATE_REGISTRY_ADDR。" >&2
    exit 1
  fi

  for key in "${image_keys[@]}"; do
    target_var="${key}_IMAGE"
    image_value="${!target_var:-}"
    [[ -n "$image_value" ]] || continue
    if [[ "$image_value" != "${PRIVATE_REGISTRY_ADDR}/"* ]]; then
      echo "SWARM_IMAGE_SOURCE=remote 时，${target_var} 必须以 ${PRIVATE_REGISTRY_ADDR}/ 开头，当前为：$image_value" >&2
      exit 1
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
  AUTHVERSE_JAVA_OPTS_BASE="${AUTHVERSE_JAVA_OPTS:--Xms1g -Xmx2g -Djava.security.egd=file:/dev/./urandom}"

  export AUTHVERSE_DB_NAME
  export AUTHVERSE_DB_USERNAME
  export AUTHVERSE_DB_PASSWORD
  export AUTHVERSE_DB_SLAVE_USERNAME
  export AUTHVERSE_DB_SLAVE_PASSWORD
  export AUTHVERSE_REDIS_PASSWORD
  export AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL
  export AUTHVERSE_JAVA_OPTS_BASE
}

prepare_secret_value_env() {
  CR_INIT_S3_SECRET_KEY="${CR_INIT_S3_SECRET_KEY:-${MINIO_ROOT_PASSWORD:-}}"
  ONLYOFFICE_DB_PASSWORD="${ONLYOFFICE_DB_PASSWORD:-${POSTGRESQL_PASSWORD:-}}"
  ONLYOFFICE_REDIS_PASSWORD="${ONLYOFFICE_REDIS_PASSWORD:-${REDIS_PASSWORD:-}}"
  AUTHVERSE_DB_ADMIN_PASSWORD="${AUTHVERSE_DB_ADMIN_PASSWORD:-${POSTGRESQL_POSTGRES_PASSWORD:-}}"

  export CR_INIT_S3_SECRET_KEY
  export ONLYOFFICE_DB_PASSWORD
  export ONLYOFFICE_REDIS_PASSWORD
  export AUTHVERSE_DB_ADMIN_PASSWORD
}

sha256_string() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | awk '{print $1}'
  else
    shasum -a 256 | awk '{print $1}'
  fi
}

build_onlyoffice_amqp_uri() {
  local user="${ONLYOFFICE_RABBITMQ_USER:-onlyoffice}"
  local password="${ONLYOFFICE_RABBITMQ_PASSWORD:-}"
  local host="${ONLYOFFICE_RABBITMQ_HOST:-onlyoffice-rabbitmq}"
  local port="${ONLYOFFICE_RABBITMQ_PORT:-5672}"
  local vhost="${ONLYOFFICE_RABBITMQ_VHOST:-onlyoffice}"

  if [[ -z "$password" ]]; then
    echo "缺少 ONLYOFFICE_RABBITMQ_PASSWORD，无法生成 OnlyOffice AMQP secret。" >&2
    exit 1
  fi

  printf 'amqp://%s:%s@%s:%s/%s' "$user" "$password" "$host" "$port" "$vhost"
}

build_authverse_backend_args() {
  local db_master_url
  local db_slave_url
  local redis_host
  local redis_port
  local elasticsearch_uri

  if [[ "$COMPOSE_KIND" == "single" ]]; then
    db_master_url="${AUTHVERSE_SINGLE_DB_MASTER_URL:?set AUTHVERSE_SINGLE_DB_MASTER_URL}"
    db_slave_url="${AUTHVERSE_SINGLE_DB_SLAVE_URL:?set AUTHVERSE_SINGLE_DB_SLAVE_URL}"
    redis_host="${AUTHVERSE_SINGLE_REDIS_HOST:?set AUTHVERSE_SINGLE_REDIS_HOST}"
    redis_port="${AUTHVERSE_SINGLE_REDIS_PORT:-6379}"
    elasticsearch_uri="${AUTHVERSE_SINGLE_ELASTICSEARCH_URI:?set AUTHVERSE_SINGLE_ELASTICSEARCH_URI}"
  else
    db_master_url="${AUTHVERSE_DB_MASTER_URL:?set AUTHVERSE_DB_MASTER_URL}"
    db_slave_url="${AUTHVERSE_DB_SLAVE_URL:?set AUTHVERSE_DB_SLAVE_URL}"
    redis_host="${AUTHVERSE_REDIS_HOST:?set AUTHVERSE_REDIS_HOST}"
    redis_port="${AUTHVERSE_REDIS_PORT:-6379}"
    elasticsearch_uri="${AUTHVERSE_ELASTICSEARCH_URI:?set AUTHVERSE_ELASTICSEARCH_URI}"
  fi

  printf '%s' "\
--server.port=48080 \
--server.forward-headers-strategy=framework \
--spring.datasource.dynamic.datasource.master.url=${db_master_url} \
--spring.datasource.dynamic.datasource.master.username=${AUTHVERSE_DB_USERNAME:?set AUTHVERSE_DB_USERNAME} \
--spring.datasource.dynamic.datasource.master.password=${AUTHVERSE_DB_PASSWORD:?set AUTHVERSE_DB_PASSWORD} \
--spring.datasource.dynamic.datasource.slave.url=${db_slave_url} \
--spring.datasource.dynamic.datasource.slave.username=${AUTHVERSE_DB_SLAVE_USERNAME:?set AUTHVERSE_DB_SLAVE_USERNAME} \
--spring.datasource.dynamic.datasource.slave.password=${AUTHVERSE_DB_SLAVE_PASSWORD:?set AUTHVERSE_DB_SLAVE_PASSWORD} \
--spring.data.redis.host=${redis_host} \
--spring.data.redis.port=${redis_port} \
--spring.data.redis.password=${AUTHVERSE_REDIS_PASSWORD:?set AUTHVERSE_REDIS_PASSWORD} \
--spring.data.redis.database=${AUTHVERSE_REDIS_DATABASE:-0} \
--spring.data.redis.ssl.enabled=${AUTHVERSE_REDIS_SSL_ENABLED:-true} \
--spring.servlet.multipart.max-file-size=${AUTHVERSE_MAX_FILE_SIZE:-64MB} \
--spring.servlet.multipart.max-request-size=${AUTHVERSE_MAX_REQUEST_SIZE:-128MB} \
"
}

build_authverse_backend_java_opts() {
  local elasticsearch_uri

  if [[ "$COMPOSE_KIND" == "single" ]]; then
    elasticsearch_uri="${AUTHVERSE_SINGLE_ELASTICSEARCH_URI:?set AUTHVERSE_SINGLE_ELASTICSEARCH_URI}"
  else
    elasticsearch_uri="${AUTHVERSE_ELASTICSEARCH_URI:?set AUTHVERSE_ELASTICSEARCH_URI}"
  fi

  printf '%s' "\
${AUTHVERSE_JAVA_OPTS_BASE} \
-Dauthverse.tenant.enable=${AUTHVERSE_TENANT_ENABLED:-false} \
-Dyudao.tenant.enable=${AUTHVERSE_TENANT_ENABLED:-false} \
-Dauthverse.web.admin-ui.url=${AUTHVERSE_PUBLIC_BASE_URL:?set AUTHVERSE_PUBLIC_BASE_URL} \
-Dyudao.web.admin-ui.url=${AUTHVERSE_PUBLIC_BASE_URL:?set AUTHVERSE_PUBLIC_BASE_URL} \
-Dauthverse.cloudreve.base-uri=${AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL:?set AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL} \
-Dyudao.cloudreve.base-uri=${AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL:?set AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL} \
-Dauthverse.search.elasticsearch.enabled=${AUTHVERSE_ELASTICSEARCH_ENABLED:-true} \
-Dyudao.search.elasticsearch.enabled=${AUTHVERSE_ELASTICSEARCH_ENABLED:-true} \
-Dauthverse.search.elasticsearch.uris[0]=${elasticsearch_uri} \
-Dyudao.search.elasticsearch.uris[0]=${elasticsearch_uri} \
-Dauthverse.search.elasticsearch.connection-timeout=${AUTHVERSE_ELASTICSEARCH_CONNECTION_TIMEOUT:-3s} \
-Dyudao.search.elasticsearch.connection-timeout=${AUTHVERSE_ELASTICSEARCH_CONNECTION_TIMEOUT:-3s} \
-Dauthverse.search.elasticsearch.socket-timeout=${AUTHVERSE_ELASTICSEARCH_SOCKET_TIMEOUT:-10s} \
-Dyudao.search.elasticsearch.socket-timeout=${AUTHVERSE_ELASTICSEARCH_SOCKET_TIMEOUT:-10s} \
-Dauthverse.search.elasticsearch.connection-request-timeout=${AUTHVERSE_ELASTICSEARCH_CONNECTION_REQUEST_TIMEOUT:-3s} \
-Dyudao.search.elasticsearch.connection-request-timeout=${AUTHVERSE_ELASTICSEARCH_CONNECTION_REQUEST_TIMEOUT:-3s} \
-Dauthverse.oidc.enabled=${AUTHVERSE_OIDC_ENABLED:-true} \
-Dyudao.oidc.enabled=${AUTHVERSE_OIDC_ENABLED:-true} \
-Dauthverse.oidc.issuer=${AUTHVERSE_PUBLIC_BASE_URL:?set AUTHVERSE_PUBLIC_BASE_URL} \
-Dyudao.oidc.issuer=${AUTHVERSE_PUBLIC_BASE_URL:?set AUTHVERSE_PUBLIC_BASE_URL} \
-Dauthverse.oidc.rsa.auto-generate=${AUTHVERSE_OIDC_RSA_AUTO_GENERATE:-false} \
-Dyudao.oidc.rsa.auto-generate=${AUTHVERSE_OIDC_RSA_AUTO_GENERATE:-false} \
-Dauthverse.oidc.rsa.private-key-path=${AUTHVERSE_OIDC_PRIVATE_KEY_PATH:-classpath:oidc/private.pem} \
-Dyudao.oidc.rsa.private-key-path=${AUTHVERSE_OIDC_PRIVATE_KEY_PATH:-classpath:oidc/private.pem} \
-Dauthverse.oidc.rsa.public-key-path=${AUTHVERSE_OIDC_PUBLIC_KEY_PATH:-classpath:oidc/public.pem} \
-Dyudao.oidc.rsa.public-key-path=${AUTHVERSE_OIDC_PUBLIC_KEY_PATH:-classpath:oidc/public.pem} \
-Dauthverse.oidc.rsa.key-id=${AUTHVERSE_OIDC_KEY_ID:-oidc-rsa-key-prod} \
-Dyudao.oidc.rsa.key-id=${AUTHVERSE_OIDC_KEY_ID:-oidc-rsa-key-prod} \
-Dauthverse.oidc.callback.enabled=${AUTHVERSE_TOKEN_CALLBACK_ENABLED:-true} \
-Dyudao.oidc.callback.enabled=${AUTHVERSE_TOKEN_CALLBACK_ENABLED:-true} \
-Dauthverse.oidc.callback.timeout=${AUTHVERSE_TOKEN_CALLBACK_TIMEOUT_MS:-5000} \
-Dyudao.oidc.callback.timeout=${AUTHVERSE_TOKEN_CALLBACK_TIMEOUT_MS:-5000} \
-Dauthverse.oidc.callback.retry-times=${AUTHVERSE_TOKEN_CALLBACK_RETRY_TIMES:-3} \
-Dyudao.oidc.callback.retry-times=${AUTHVERSE_TOKEN_CALLBACK_RETRY_TIMES:-3} \
-Dauthverse.oidc.callback.retry-interval=${AUTHVERSE_TOKEN_CALLBACK_RETRY_INTERVAL_MS:-1000} \
-Dyudao.oidc.callback.retry-interval=${AUTHVERSE_TOKEN_CALLBACK_RETRY_INTERVAL_MS:-1000} \
-Dauthverse.websocket.sender-type=${AUTHVERSE_WEBSOCKET_SENDER_TYPE:-redis} \
-Dyudao.websocket.sender-type=${AUTHVERSE_WEBSOCKET_SENDER_TYPE:-redis}"
}

build_stack_secret_name() {
  local stack_name="$1"
  local secret_key="$2"
  local secret_hash="$3"
  local secret_name
  local stack_token
  local secret_key_limit

  secret_name="${stack_name}_secret_${secret_key}_${secret_hash}"
  if [[ "${#secret_name}" -le 64 ]]; then
    printf '%s' "$secret_name"
    return 0
  fi

  stack_token="$(printf '%s' "$stack_name" | sha256_string | cut -c1-8)"
  secret_key_limit=$((64 - ${#stack_token} - ${#secret_hash} - 4))
  if [[ "$secret_key_limit" -lt 8 ]]; then
    secret_key_limit=8
  fi

  printf '%s' "s_${stack_token}_${secret_key:0:$secret_key_limit}_${secret_hash}"
}

register_stack_secret() {
  local export_var="$1"
  local secret_key="$2"
  local secret_value="$3"
  local required="${4:-1}"
  local secret_hash secret_name

  if [[ -z "$secret_value" ]]; then
    if [[ "$required" == "1" ]]; then
      echo "缺少 secret 值：$secret_key" >&2
      exit 1
    fi
    return 0
  fi

  secret_hash="$(printf '%s' "$secret_value" | sha256_string | cut -c1-12)"
  secret_name="$(build_stack_secret_name "$STACK_NAME" "$secret_key" "$secret_hash")"

  printf -v "$export_var" '%s' "$secret_name"
  export "$export_var"
  STACK_SECRET_NAMES+=("$secret_name")

  if [[ "$RENDER_ONLY" -eq 0 ]] && ! docker secret inspect "$secret_name" >/dev/null 2>&1; then
    printf '%s' "$secret_value" | docker secret create "$secret_name" -
  fi
}

prepare_stack_secrets() {
  STACK_SECRET_NAMES=()

  case "$COMPOSE_KIND" in
    cloudreve)
      register_stack_secret "SWARM_SECRET_CLOUDREVE_SESSION_SECRET_NAME" "cloudreve_session_secret" "${CLOUDREVE_SESSION_SECRET:-}" 1
      register_stack_secret "SWARM_SECRET_POSTGRESQL_PASSWORD_NAME" "postgresql_password" "${POSTGRESQL_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_REDIS_PASSWORD_NAME" "redis_password" "${REDIS_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_CLOUDREVE_S3_SECRET_KEY_NAME" "cloudreve_s3_secret_key" "${CR_INIT_S3_SECRET_KEY:-}" 1
      register_stack_secret "SWARM_SECRET_CLOUDREVE_SLAVE_SECRET_NAME" "cloudreve_slave_secret" "${CLOUDREVE_SLAVE_SECRET:-}" 1
      ;;
    foundation)
      register_stack_secret "SWARM_SECRET_POSTGRESQL_POSTGRES_PASSWORD_NAME" "postgresql_postgres_password" "${POSTGRESQL_POSTGRES_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_POSTGRESQL_PASSWORD_NAME" "postgresql_password" "${POSTGRESQL_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_REPMGR_PASSWORD_NAME" "repmgr_password" "${REPMGR_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_PGPOOL_ADMIN_PASSWORD_NAME" "pgpool_admin_password" "${PGPOOL_ADMIN_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_REDIS_PASSWORD_NAME" "redis_password" "${REDIS_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_ONLYOFFICE_DB_PASSWORD_NAME" "onlyoffice_db_password" "${ONLYOFFICE_DB_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_ONLYOFFICE_REDIS_PASSWORD_NAME" "onlyoffice_redis_password" "${ONLYOFFICE_REDIS_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_ONLYOFFICE_RABBITMQ_PASSWORD_NAME" "onlyoffice_rabbitmq_password" "${ONLYOFFICE_RABBITMQ_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_ONLYOFFICE_JWT_SECRET_NAME" "onlyoffice_jwt_secret" "${ONLYOFFICE_JWT_SECRET:-}" 1
      register_stack_secret "SWARM_SECRET_ONLYOFFICE_AMQP_URI_NAME" "onlyoffice_amqp_uri" "$(build_onlyoffice_amqp_uri)" 1
      ;;
    infra)
      register_stack_secret "SWARM_SECRET_MINIO_ROOT_PASSWORD_NAME" "minio_root_password" "${MINIO_ROOT_PASSWORD:-}" 1
      ;;
    auth)
      register_stack_secret "SWARM_SECRET_AUTHVERSE_BACKEND_ARGS_NAME" "authverse_backend_args" "$(build_authverse_backend_args)" 1
      ;;
    single)
      register_stack_secret "SWARM_SECRET_CLOUDREVE_SESSION_SECRET_NAME" "cloudreve_session_secret" "${CLOUDREVE_SESSION_SECRET:-}" 1
      register_stack_secret "SWARM_SECRET_POSTGRESQL_POSTGRES_PASSWORD_NAME" "postgresql_postgres_password" "${POSTGRESQL_POSTGRES_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_POSTGRESQL_PASSWORD_NAME" "postgresql_password" "${POSTGRESQL_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_REPMGR_PASSWORD_NAME" "repmgr_password" "${REPMGR_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_PGPOOL_ADMIN_PASSWORD_NAME" "pgpool_admin_password" "${PGPOOL_ADMIN_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_REDIS_PASSWORD_NAME" "redis_password" "${REDIS_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_MINIO_ROOT_PASSWORD_NAME" "minio_root_password" "${MINIO_ROOT_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_CLOUDREVE_S3_SECRET_KEY_NAME" "cloudreve_s3_secret_key" "${CR_INIT_S3_SECRET_KEY:-}" 1
      register_stack_secret "SWARM_SECRET_ONLYOFFICE_DB_PASSWORD_NAME" "onlyoffice_db_password" "${ONLYOFFICE_DB_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_ONLYOFFICE_REDIS_PASSWORD_NAME" "onlyoffice_redis_password" "${ONLYOFFICE_REDIS_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_ONLYOFFICE_RABBITMQ_PASSWORD_NAME" "onlyoffice_rabbitmq_password" "${ONLYOFFICE_RABBITMQ_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_ONLYOFFICE_JWT_SECRET_NAME" "onlyoffice_jwt_secret" "${ONLYOFFICE_JWT_SECRET:-}" 1
      register_stack_secret "SWARM_SECRET_ONLYOFFICE_AMQP_URI_NAME" "onlyoffice_amqp_uri" "$(build_onlyoffice_amqp_uri)" 1
      register_stack_secret "SWARM_SECRET_AUTHVERSE_BACKEND_ARGS_NAME" "authverse_backend_args" "$(build_authverse_backend_args)" 1
      ;;
    *)
      ;;
  esac
}

cleanup_old_stack_secrets() {
  local prefix="${STACK_NAME}_secret_"
  local used_secrets_file all_secrets_file secret_name

  [[ "$RENDER_ONLY" -eq 0 ]] || return 0

  used_secrets_file="$(mktemp)"
  all_secrets_file="$(mktemp)"
  trap 'rm -f "$used_secrets_file" "$all_secrets_file"' RETURN

  docker stack services "$STACK_NAME" -q \
    | while read -r service_id; do
        [[ -n "$service_id" ]] || continue
        docker service inspect "$service_id" \
          --format '{{range .Spec.TaskTemplate.ContainerSpec.Secrets}}{{println .SecretName}}{{end}}'
      done \
    | sed '/^$/d' \
    | sort -u >"$used_secrets_file"

  docker secret ls --format '{{.Name}}' | sed -n "/^${prefix}/p" >"$all_secrets_file"

  while read -r secret_name; do
    [[ -n "$secret_name" ]] || continue
    if ! grep -qx "$secret_name" "$used_secrets_file"; then
      docker secret rm "$secret_name" >/dev/null 2>&1 || true
    fi
  done <"$all_secrets_file"
}

prepare_single_auth_env() {
  AUTHVERSE_SINGLE_CLOUDREVE_INTERNAL_UPSTREAM="${AUTHVERSE_SINGLE_CLOUDREVE_INTERNAL_UPSTREAM:-cloudreve-master:5212}"
  AUTHVERSE_SINGLE_POSTGRES_HOST="${AUTHVERSE_SINGLE_POSTGRES_HOST:-pgpool-internal}"
  AUTHVERSE_SINGLE_POSTGRES_PORT="${AUTHVERSE_SINGLE_POSTGRES_PORT:-5432}"
  AUTHVERSE_SINGLE_REDIS_HOST="${AUTHVERSE_SINGLE_REDIS_HOST:-redis-1}"
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

  AUTHVERSE_JAVA_OPTS_EFFECTIVE="$(build_authverse_backend_java_opts)"
  export AUTHVERSE_JAVA_OPTS_EFFECTIVE
}

is_truthy() {
  case "${1:-}" in
    1|true|TRUE|yes|YES|on|ON)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

ensure_shared_overlay_network() {
  local network_name="$1"
  local subnet="${CLOUDREVE_BACKEND_SUBNET:-10.20.0.0/24}"
  local cmd=(docker network create --driver overlay --subnet "$subnet")

  if docker network inspect "$network_name" >/dev/null 2>&1; then
    return 0
  fi

  if is_truthy "${SWARM_OVERLAY_ATTACHABLE:-false}"; then
    cmd+=(--attachable)
  fi
  if is_truthy "${SWARM_OVERLAY_ENCRYPT:-true}"; then
    cmd+=(--opt encrypted)
  fi
  cmd+=("$network_name")

  echo "共享 overlay 网络不存在，开始创建：$network_name"
  "${cmd[@]}" >/dev/null
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

  AUTHVERSE_REDIS_SSL_ENABLED="${AUTHVERSE_REDIS_SSL_ENABLED:-false}"
  AUTHVERSE_JAVA_OPTS_EFFECTIVE="$(build_authverse_backend_java_opts)"
  export AUTHVERSE_REDIS_SSL_ENABLED
  export AUTHVERSE_JAVA_OPTS_EFFECTIVE

  if [[ "$RENDER_ONLY" -eq 0 ]]; then
    ensure_shared_overlay_network "$AUTHVERSE_SHARED_NETWORK"
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
validate_remote_image_source_env
prepare_auth_common_env
prepare_secret_value_env

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

case "$COMPOSE_KIND" in
  cloudreve|foundation|infra)
    if [[ "$RENDER_ONLY" -eq 0 ]]; then
      ensure_shared_overlay_network "${CLOUDREVE_STACK_NAME}_cloudreve_backend"
    fi
    ;;
esac

prepare_stack_secrets

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
cleanup_old_stack_secrets

echo
echo "当前服务状态："
docker stack services "$STACK_NAME"
