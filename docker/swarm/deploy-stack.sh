#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT_DIR/docker/swarm/lib-env.sh"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
CLI_STACK_NAME="${STACK_NAME:-}"
RESOLVED_DIR="${RESOLVED_DIR:-$ROOT_DIR/.tmp}"
COMPOSE_NAME="${COMPOSE_NAME:-}"
COMPOSE_FILE="${COMPOSE_FILE:-}"
SINGLE_COMPOSE_FILE="$ROOT_DIR/docker-compose.swarm.single.yml"
CLOUDREVE_COMPOSE_FILE="$ROOT_DIR/docker-compose.swarm.cloudreve.yml"
FOUNDATION_COMPOSE_FILE="$ROOT_DIR/docker-compose.swarm.foundation.yml"
INFRA_COMPOSE_FILE="$ROOT_DIR/docker-compose.swarm.infra.yml"
EDGE_LB_COMPOSE_FILE="$ROOT_DIR/docker-compose.swarm.edge-lb.yml"
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
     - edge-lb   -> docker-compose.swarm.edge-lb.yml

  2. 不传 compose-name 时，默认发布 docker-compose.swarm.single.yml

参数：
  compose-name        直接传模板名，例如：single / cloudreve / foundation / infra / auth / registry / edge-lb
  --stack-name NAME   Swarm 栈名称
  --env-file FILE     要读取的环境变量文件，默认是 .env.swarm
  --compose-name NAME 显式指定模板名，等价于第一个位置参数
  --compose-file FILE 直接指定 Compose 文件路径
  --render-only       只渲染 stack 配置，不执行部署
  -h, --help          显示帮助

示例：
  docker/swarm/deploy-stack.sh
  docker/swarm/deploy-stack.sh single
  docker/swarm/deploy-stack.sh foundation --stack-name cloudreve-prod-foundation
  docker/swarm/deploy-stack.sh infra --stack-name cloudreve-prod-infra
  docker/swarm/deploy-stack.sh cloudreve --stack-name cloudreve-prod-app
  docker/swarm/deploy-stack.sh auth --stack-name authverse-prod
  docker/swarm/deploy-stack.sh registry --stack-name cloudreve-registry
  docker/swarm/deploy-stack.sh edge-lb --stack-name cloudreve-prod-edge-lb

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
    edge-lb|edge_lb|edgelb)
      printf '%s\n' "$EDGE_LB_COMPOSE_FILE"
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
    docker-compose.swarm.edge-lb.yml)
      printf '%s\n' "edge-lb"
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
    SWARM_EDGE_LB
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
    SWARM_EDGE_LB
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
  AUTHVERSE_BACKEND_PORT="${AUTHVERSE_BACKEND_PORT:-48080}"
  AUTHVERSE_DB_USERNAME="${AUTHVERSE_DB_USERNAME:-${POSTGRESQL_USERNAME:-cloudreve}}"
  AUTHVERSE_DB_PASSWORD="${AUTHVERSE_DB_PASSWORD:-${POSTGRESQL_PASSWORD:-}}"
  AUTHVERSE_DB_SLAVE_USERNAME="${AUTHVERSE_DB_SLAVE_USERNAME:-$AUTHVERSE_DB_USERNAME}"
  AUTHVERSE_DB_SLAVE_PASSWORD="${AUTHVERSE_DB_SLAVE_PASSWORD:-$AUTHVERSE_DB_PASSWORD}"
  AUTHVERSE_REDIS_PASSWORD="${AUTHVERSE_REDIS_PASSWORD:-${REDIS_PASSWORD:-}}"
  AUTHVERSE_REDIS_DATABASE="${AUTHVERSE_REDIS_DATABASE:-0}"
  AUTHVERSE_REDIS_SSL_ENABLED="${AUTHVERSE_REDIS_SSL_ENABLED:-false}"
  AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL="${AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL:-${CLOUDREVE_SITE_URL:-}}"
  AUTHVERSE_MAX_FILE_SIZE="${AUTHVERSE_MAX_FILE_SIZE:-64MB}"
  AUTHVERSE_MAX_REQUEST_SIZE="${AUTHVERSE_MAX_REQUEST_SIZE:-128MB}"
  AUTHVERSE_TENANT_ENABLED="${AUTHVERSE_TENANT_ENABLED:-false}"
  AUTHVERSE_ELASTICSEARCH_ENABLED="${AUTHVERSE_ELASTICSEARCH_ENABLED:-true}"
  AUTHVERSE_ELASTICSEARCH_CONNECTION_TIMEOUT="${AUTHVERSE_ELASTICSEARCH_CONNECTION_TIMEOUT:-3s}"
  AUTHVERSE_ELASTICSEARCH_SOCKET_TIMEOUT="${AUTHVERSE_ELASTICSEARCH_SOCKET_TIMEOUT:-10s}"
  AUTHVERSE_ELASTICSEARCH_CONNECTION_REQUEST_TIMEOUT="${AUTHVERSE_ELASTICSEARCH_CONNECTION_REQUEST_TIMEOUT:-3s}"
  AUTHVERSE_OIDC_ENABLED="${AUTHVERSE_OIDC_ENABLED:-true}"
  AUTHVERSE_OIDC_RSA_AUTO_GENERATE="${AUTHVERSE_OIDC_RSA_AUTO_GENERATE:-false}"
  AUTHVERSE_OIDC_PRIVATE_KEY_PATH="${AUTHVERSE_OIDC_PRIVATE_KEY_PATH:-classpath:oidc/private.pem}"
  AUTHVERSE_OIDC_PUBLIC_KEY_PATH="${AUTHVERSE_OIDC_PUBLIC_KEY_PATH:-classpath:oidc/public.pem}"
  AUTHVERSE_OIDC_KEY_ID="${AUTHVERSE_OIDC_KEY_ID:-oidc-rsa-key-prod}"
  AUTHVERSE_TOKEN_CALLBACK_ENABLED="${AUTHVERSE_TOKEN_CALLBACK_ENABLED:-true}"
  AUTHVERSE_TOKEN_CALLBACK_TIMEOUT_MS="${AUTHVERSE_TOKEN_CALLBACK_TIMEOUT_MS:-5000}"
  AUTHVERSE_TOKEN_CALLBACK_RETRY_TIMES="${AUTHVERSE_TOKEN_CALLBACK_RETRY_TIMES:-3}"
  AUTHVERSE_TOKEN_CALLBACK_RETRY_INTERVAL_MS="${AUTHVERSE_TOKEN_CALLBACK_RETRY_INTERVAL_MS:-1000}"
  AUTHVERSE_WEBSOCKET_SENDER_TYPE="${AUTHVERSE_WEBSOCKET_SENDER_TYPE:-redis}"
  AUTHVERSE_DB_POOL_INITIAL_SIZE="${AUTHVERSE_DB_POOL_INITIAL_SIZE:-1}"
  AUTHVERSE_DB_POOL_MIN_IDLE="${AUTHVERSE_DB_POOL_MIN_IDLE:-1}"
  AUTHVERSE_DB_POOL_MAX_ACTIVE="${AUTHVERSE_DB_POOL_MAX_ACTIVE:-8}"
  AUTHVERSE_DB_POOL_MAX_WAIT="${AUTHVERSE_DB_POOL_MAX_WAIT:-30000}"
  AUTHVERSE_DB_POOL_TIME_BETWEEN_EVICTION_RUNS_MILLIS="${AUTHVERSE_DB_POOL_TIME_BETWEEN_EVICTION_RUNS_MILLIS:-15000}"
  AUTHVERSE_DB_POOL_MIN_EVICTABLE_IDLE_TIME_MILLIS="${AUTHVERSE_DB_POOL_MIN_EVICTABLE_IDLE_TIME_MILLIS:-600000}"
  AUTHVERSE_DB_POOL_MAX_EVICTABLE_IDLE_TIME_MILLIS="${AUTHVERSE_DB_POOL_MAX_EVICTABLE_IDLE_TIME_MILLIS:-1800000}"
  AUTHVERSE_DB_POOL_VALIDATION_QUERY_TIMEOUT="${AUTHVERSE_DB_POOL_VALIDATION_QUERY_TIMEOUT:-3}"
  AUTHVERSE_DB_POOL_TEST_ON_BORROW="${AUTHVERSE_DB_POOL_TEST_ON_BORROW:-true}"
  AUTHVERSE_DB_POOL_KEEP_ALIVE="${AUTHVERSE_DB_POOL_KEEP_ALIVE:-true}"
  AUTHVERSE_DB_POOL_CONNECTION_ERROR_RETRY_ATTEMPTS="${AUTHVERSE_DB_POOL_CONNECTION_ERROR_RETRY_ATTEMPTS:-3}"
  AUTHVERSE_REDIS_TIMEOUT="${AUTHVERSE_REDIS_TIMEOUT:-3s}"
  AUTHVERSE_REDIS_CONNECT_TIMEOUT="${AUTHVERSE_REDIS_CONNECT_TIMEOUT:-3s}"
  AUTHVERSE_ACTUATOR_WATCHDOG_INCLUDE="${AUTHVERSE_ACTUATOR_WATCHDOG_INCLUDE:-*}"
  AUTHVERSE_BACKEND_WATCHDOG_ENABLED="${AUTHVERSE_BACKEND_WATCHDOG_ENABLED:-true}"
  AUTHVERSE_BACKEND_WATCHDOG_URL="${AUTHVERSE_BACKEND_WATCHDOG_URL:-http://127.0.0.1:48080/actuator/health/watchdog}"
  AUTHVERSE_BACKEND_WATCHDOG_INTERVAL_SECONDS="${AUTHVERSE_BACKEND_WATCHDOG_INTERVAL_SECONDS:-15}"
  AUTHVERSE_BACKEND_WATCHDOG_FAILURE_THRESHOLD="${AUTHVERSE_BACKEND_WATCHDOG_FAILURE_THRESHOLD:-8}"
  AUTHVERSE_BACKEND_WATCHDOG_START_PERIOD_SECONDS="${AUTHVERSE_BACKEND_WATCHDOG_START_PERIOD_SECONDS:-180}"
  AUTHVERSE_BACKEND_WATCHDOG_STOP_TIMEOUT_SECONDS="${AUTHVERSE_BACKEND_WATCHDOG_STOP_TIMEOUT_SECONDS:-30}"
  AUTHVERSE_BACKEND_WATCHDOG_REQUEST_TIMEOUT_SECONDS="${AUTHVERSE_BACKEND_WATCHDOG_REQUEST_TIMEOUT_SECONDS:-5}"

  export AUTHVERSE_BACKEND_PORT
  export AUTHVERSE_DB_NAME
  export AUTHVERSE_DB_USERNAME
  export AUTHVERSE_DB_PASSWORD
  export AUTHVERSE_DB_SLAVE_USERNAME
  export AUTHVERSE_DB_SLAVE_PASSWORD
  export AUTHVERSE_REDIS_PASSWORD
  export AUTHVERSE_REDIS_DATABASE
  export AUTHVERSE_REDIS_SSL_ENABLED
  export AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL
  export AUTHVERSE_MAX_FILE_SIZE
  export AUTHVERSE_MAX_REQUEST_SIZE
  export AUTHVERSE_TENANT_ENABLED
  export AUTHVERSE_ELASTICSEARCH_ENABLED
  export AUTHVERSE_ELASTICSEARCH_CONNECTION_TIMEOUT
  export AUTHVERSE_ELASTICSEARCH_SOCKET_TIMEOUT
  export AUTHVERSE_ELASTICSEARCH_CONNECTION_REQUEST_TIMEOUT
  export AUTHVERSE_OIDC_ENABLED
  export AUTHVERSE_OIDC_RSA_AUTO_GENERATE
  export AUTHVERSE_OIDC_PRIVATE_KEY_PATH
  export AUTHVERSE_OIDC_PUBLIC_KEY_PATH
  export AUTHVERSE_OIDC_KEY_ID
  export AUTHVERSE_TOKEN_CALLBACK_ENABLED
  export AUTHVERSE_TOKEN_CALLBACK_TIMEOUT_MS
  export AUTHVERSE_TOKEN_CALLBACK_RETRY_TIMES
  export AUTHVERSE_TOKEN_CALLBACK_RETRY_INTERVAL_MS
  export AUTHVERSE_WEBSOCKET_SENDER_TYPE
  export AUTHVERSE_DB_POOL_INITIAL_SIZE
  export AUTHVERSE_DB_POOL_MIN_IDLE
  export AUTHVERSE_DB_POOL_MAX_ACTIVE
  export AUTHVERSE_DB_POOL_MAX_WAIT
  export AUTHVERSE_DB_POOL_TIME_BETWEEN_EVICTION_RUNS_MILLIS
  export AUTHVERSE_DB_POOL_MIN_EVICTABLE_IDLE_TIME_MILLIS
  export AUTHVERSE_DB_POOL_MAX_EVICTABLE_IDLE_TIME_MILLIS
  export AUTHVERSE_DB_POOL_VALIDATION_QUERY_TIMEOUT
  export AUTHVERSE_DB_POOL_TEST_ON_BORROW
  export AUTHVERSE_DB_POOL_KEEP_ALIVE
  export AUTHVERSE_DB_POOL_CONNECTION_ERROR_RETRY_ATTEMPTS
  export AUTHVERSE_REDIS_TIMEOUT
  export AUTHVERSE_REDIS_CONNECT_TIMEOUT
  export AUTHVERSE_ACTUATOR_WATCHDOG_INCLUDE
  export AUTHVERSE_BACKEND_WATCHDOG_ENABLED
  export AUTHVERSE_BACKEND_WATCHDOG_URL
  export AUTHVERSE_BACKEND_WATCHDOG_INTERVAL_SECONDS
  export AUTHVERSE_BACKEND_WATCHDOG_FAILURE_THRESHOLD
  export AUTHVERSE_BACKEND_WATCHDOG_START_PERIOD_SECONDS
  export AUTHVERSE_BACKEND_WATCHDOG_STOP_TIMEOUT_SECONDS
  export AUTHVERSE_BACKEND_WATCHDOG_REQUEST_TIMEOUT_SECONDS
}

prepare_java_truststore_env() {
  local truststore_path="${SWARM_TRUSTSTORE_PATH:-${SWARM_PKI_MOUNT_TARGET:-/run/swarm-pki}/ca/truststore.p12}"
  local truststore_type="${SWARM_TRUSTSTORE_TYPE:-PKCS12}"
  local truststore_password="${SWARM_TRUSTSTORE_PASSWORD:-changeit}"

  AUTHVERSE_JAVA_TOOL_OPTIONS="${AUTHVERSE_JAVA_TOOL_OPTIONS:-}"
  if [[ -z "$AUTHVERSE_JAVA_TOOL_OPTIONS" ]]; then
    AUTHVERSE_JAVA_TOOL_OPTIONS="-Djavax.net.ssl.trustStore=${truststore_path} -Djavax.net.ssl.trustStoreType=${truststore_type} -Djavax.net.ssl.trustStorePassword=${truststore_password}"
  fi

  KAFKA_UI_TRUSTSTORE_LOCATION="${KAFKA_UI_TRUSTSTORE_LOCATION:-$truststore_path}"
  KAFKA_UI_TRUSTSTORE_TYPE="${KAFKA_UI_TRUSTSTORE_TYPE:-$truststore_type}"
  KAFKA_UI_TRUSTSTORE_PASSWORD="${KAFKA_UI_TRUSTSTORE_PASSWORD:-$truststore_password}"
  KAFKA_UI_JAVA_TOOL_OPTIONS="${KAFKA_UI_JAVA_TOOL_OPTIONS:-}"
  if [[ -z "$KAFKA_UI_JAVA_TOOL_OPTIONS" ]]; then
    KAFKA_UI_JAVA_TOOL_OPTIONS="-Djavax.net.ssl.trustStore=${KAFKA_UI_TRUSTSTORE_LOCATION} -Djavax.net.ssl.trustStoreType=${KAFKA_UI_TRUSTSTORE_TYPE} -Djavax.net.ssl.trustStorePassword=${KAFKA_UI_TRUSTSTORE_PASSWORD}"
  fi

  export AUTHVERSE_JAVA_TOOL_OPTIONS
  export KAFKA_UI_TRUSTSTORE_LOCATION
  export KAFKA_UI_TRUSTSTORE_TYPE
  export KAFKA_UI_TRUSTSTORE_PASSWORD
  export KAFKA_UI_JAVA_TOOL_OPTIONS
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

cloudreve_default_s3_init_enabled() {
  case "$(printf '%s' "${CR_INIT_DEFAULT_STORAGE:-}" | tr '[:upper:]' '[:lower:]' | xargs)" in
    s3)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

prepare_cloudreve_secret_mappings() {
  local base_mappings="CR_CONF_System.SessionSecret=cloudreve_session_secret,CR_CONF_Database.Password=postgresql_password,CR_CONF_Redis.Password=redis_password"

  if cloudreve_default_s3_init_enabled; then
    CLOUDREVE_MASTER_SECRET_ENV_MAPPINGS="${CLOUDREVE_MASTER_SECRET_ENV_MAPPINGS:-${base_mappings},CR_INIT_S3_SECRET_KEY=cloudreve_s3_secret_key}"
  else
    CLOUDREVE_MASTER_SECRET_ENV_MAPPINGS="${CLOUDREVE_MASTER_SECRET_ENV_MAPPINGS:-${base_mappings}}"
    unset SWARM_SECRET_CLOUDREVE_S3_SECRET_KEY_NAME
  fi

  export CLOUDREVE_MASTER_SECRET_ENV_MAPPINGS
}

prepare_cloudreve_kafka_env() {
  local default_brokers="$1"
  local mode

  mode="$(printf '%s' "${CLOUDREVE_GLOBAL_KAFKA_TLS_MODE:-internal-plaintext}" | tr '[:upper:]' '[:lower:]' | xargs)"

  case "$mode" in
    ""|internal-plaintext)
      CLOUDREVE_GLOBAL_KAFKA_TLS_MODE="internal-plaintext"
      CLOUDREVE_GLOBAL_KAFKA_BROKERS="$default_brokers"
      CLOUDREVE_GLOBAL_KAFKA_SECURITY_PROTOCOL="PLAINTEXT"
      ;;
    external-plaintext)
      CLOUDREVE_GLOBAL_KAFKA_TLS_MODE="external-plaintext"
      CLOUDREVE_GLOBAL_KAFKA_BROKERS="${CLOUDREVE_GLOBAL_KAFKA_BROKERS:-$default_brokers}"
      CLOUDREVE_GLOBAL_KAFKA_SECURITY_PROTOCOL="PLAINTEXT"
      ;;
    external-tls)
      CLOUDREVE_GLOBAL_KAFKA_TLS_MODE="external-tls"
      CLOUDREVE_GLOBAL_KAFKA_BROKERS="${CLOUDREVE_GLOBAL_KAFKA_BROKERS:-$default_brokers}"
      CLOUDREVE_GLOBAL_KAFKA_SECURITY_PROTOCOL="SSL"
      ;;
    external-sasl-plaintext)
      CLOUDREVE_GLOBAL_KAFKA_TLS_MODE="external-sasl-plaintext"
      CLOUDREVE_GLOBAL_KAFKA_BROKERS="${CLOUDREVE_GLOBAL_KAFKA_BROKERS:-$default_brokers}"
      CLOUDREVE_GLOBAL_KAFKA_SECURITY_PROTOCOL="SASL_PLAINTEXT"
      ;;
    external-sasl-ssl)
      CLOUDREVE_GLOBAL_KAFKA_TLS_MODE="external-sasl-ssl"
      CLOUDREVE_GLOBAL_KAFKA_BROKERS="${CLOUDREVE_GLOBAL_KAFKA_BROKERS:-$default_brokers}"
      CLOUDREVE_GLOBAL_KAFKA_SECURITY_PROTOCOL="SASL_SSL"
      ;;
    custom)
      CLOUDREVE_GLOBAL_KAFKA_TLS_MODE="custom"
      CLOUDREVE_GLOBAL_KAFKA_BROKERS="${CLOUDREVE_GLOBAL_KAFKA_BROKERS:-$default_brokers}"
      ;;
    *)
      echo "不支持的 CLOUDREVE_GLOBAL_KAFKA_TLS_MODE: $mode" >&2
      echo "允许值: internal-plaintext / external-plaintext / external-tls / external-sasl-plaintext / external-sasl-ssl / custom" >&2
      exit 1
      ;;
  esac

  CLOUDREVE_GLOBAL_KAFKA_TLS_SKIP_VERIFY="${CLOUDREVE_GLOBAL_KAFKA_TLS_SKIP_VERIFY:-false}"
  CLOUDREVE_GLOBAL_KAFKA_TLS_SERVER_NAME="${CLOUDREVE_GLOBAL_KAFKA_TLS_SERVER_NAME:-kafka}"
  CLOUDREVE_GLOBAL_KAFKA_TLS_CA_PATH="${CLOUDREVE_GLOBAL_KAFKA_TLS_CA_PATH:-/run/swarm-pki/ca/ca.crt}"
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

render_env_template_to_stdout() {
  local template_file="$1"

  if [[ ! -f "$template_file" ]]; then
    echo "找不到模板文件: $template_file" >&2
    exit 1
  fi

  if command -v envsubst >/dev/null 2>&1; then
    envsubst <"$template_file"
    return 0
  fi

  LC_ALL=C LANG=C perl - "$template_file" <<'PERL'
use strict;
use warnings;

my $template_file = shift @ARGV;
open my $fh, '<', $template_file or die "cannot open template $template_file: $!";
local $/;
my $content = <$fh>;
close $fh;

$content =~ s/\$\{([A-Za-z_][A-Za-z0-9_]*)\}/
  exists $ENV{$1} ? $ENV{$1} : die "missing env variable: $1\n"
/ge;

print $content;
PERL
}

register_stack_secret_from_template() {
  local export_var="$1"
  local secret_key="$2"
  local template_file="$3"
  local required="${4:-1}"
  local rendered_value

  if [[ ! -f "$template_file" ]]; then
    if [[ "$required" == "1" ]]; then
      echo "缺少模板文件，无法生成 secret：$template_file" >&2
      exit 1
    fi
    return 0
  fi

  rendered_value="$(render_env_template_to_stdout "$template_file")"
  register_stack_secret "$export_var" "$secret_key" "$rendered_value" "$required"
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
      register_stack_secret "SWARM_SECRET_CLOUDREVE_S3_SECRET_KEY_NAME" "cloudreve_s3_secret_key" "${CR_INIT_S3_SECRET_KEY:-}" "$([ cloudreve_default_s3_init_enabled ] && printf '1' || printf '0')"
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
      register_stack_secret_from_template \
        "SWARM_SECRET_AUTHVERSE_BACKEND_APPLICATION_NAME" \
        "authverse_backend_application" \
        "$ROOT_DIR/docker/swarm/templates/authverse-backend/application-swarm.yml.template" \
        1
      ;;
    single)
      register_stack_secret "SWARM_SECRET_CLOUDREVE_SESSION_SECRET_NAME" "cloudreve_session_secret" "${CLOUDREVE_SESSION_SECRET:-}" 1
      register_stack_secret "SWARM_SECRET_POSTGRESQL_POSTGRES_PASSWORD_NAME" "postgresql_postgres_password" "${POSTGRESQL_POSTGRES_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_POSTGRESQL_PASSWORD_NAME" "postgresql_password" "${POSTGRESQL_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_REPMGR_PASSWORD_NAME" "repmgr_password" "${REPMGR_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_PGPOOL_ADMIN_PASSWORD_NAME" "pgpool_admin_password" "${PGPOOL_ADMIN_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_REDIS_PASSWORD_NAME" "redis_password" "${REDIS_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_MINIO_ROOT_PASSWORD_NAME" "minio_root_password" "${MINIO_ROOT_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_CLOUDREVE_S3_SECRET_KEY_NAME" "cloudreve_s3_secret_key" "${CR_INIT_S3_SECRET_KEY:-}" "$([ cloudreve_default_s3_init_enabled ] && printf '1' || printf '0')"
      register_stack_secret "SWARM_SECRET_ONLYOFFICE_DB_PASSWORD_NAME" "onlyoffice_db_password" "${ONLYOFFICE_DB_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_ONLYOFFICE_REDIS_PASSWORD_NAME" "onlyoffice_redis_password" "${ONLYOFFICE_REDIS_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_ONLYOFFICE_RABBITMQ_PASSWORD_NAME" "onlyoffice_rabbitmq_password" "${ONLYOFFICE_RABBITMQ_PASSWORD:-}" 1
      register_stack_secret "SWARM_SECRET_ONLYOFFICE_JWT_SECRET_NAME" "onlyoffice_jwt_secret" "${ONLYOFFICE_JWT_SECRET:-}" 1
      register_stack_secret "SWARM_SECRET_ONLYOFFICE_AMQP_URI_NAME" "onlyoffice_amqp_uri" "$(build_onlyoffice_amqp_uri)" 1
      register_stack_secret_from_template \
        "SWARM_SECRET_AUTHVERSE_BACKEND_APPLICATION_NAME" \
        "authverse_backend_application" \
        "$ROOT_DIR/docker/swarm/templates/authverse-backend/application-swarm.yml.template" \
        1
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
  if is_truthy "${SWARM_AUTO_SERVICE_PREFIXES:-true}"; then
    CLOUDREVE_REDIS_ENDPOINT="redis-1:6379"
    CLOUDREVE_REDIS_USE_TLS="false"
    CLOUDREVE_FTS_ELASTICSEARCH_ENDPOINT="http://elasticsearch-internal:9200"
    CLOUDREVE_FTS_TIKA_ENDPOINT="http://tika:9998"
    CR_INIT_S3_ENDPOINT="http://minio-internal:9000"
    MINIO_INIT_ENDPOINT="http://minio-internal:9000"
    KAFKA_UI_BOOTSTRAP_SERVERS="kafka:9092"
    ONLYOFFICE_DB_HOST="pgpool-internal"
    ONLYOFFICE_REDIS_HOST="redis-proxy-internal"

    export CLOUDREVE_REDIS_ENDPOINT
    export CLOUDREVE_REDIS_USE_TLS
    export CLOUDREVE_FTS_ELASTICSEARCH_ENDPOINT
    export CLOUDREVE_FTS_TIKA_ENDPOINT
    export CR_INIT_S3_ENDPOINT
    export MINIO_INIT_ENDPOINT
    export KAFKA_UI_BOOTSTRAP_SERVERS
    export ONLYOFFICE_DB_HOST
    export ONLYOFFICE_REDIS_HOST
  fi

  prepare_cloudreve_kafka_env "${CLOUDREVE_GLOBAL_KAFKA_BROKERS:-kafka:9092}"
  export CLOUDREVE_GLOBAL_KAFKA_TLS_MODE
  export CLOUDREVE_GLOBAL_KAFKA_BROKERS
  export CLOUDREVE_GLOBAL_KAFKA_SECURITY_PROTOCOL
  export CLOUDREVE_GLOBAL_KAFKA_TLS_SKIP_VERIFY
  export CLOUDREVE_GLOBAL_KAFKA_TLS_SERVER_NAME
  export CLOUDREVE_GLOBAL_KAFKA_TLS_CA_PATH

  AUTHVERSE_SINGLE_CLOUDREVE_INTERNAL_UPSTREAM="${AUTHVERSE_SINGLE_CLOUDREVE_INTERNAL_UPSTREAM:-cloudreve-master:5212}"
  AUTHVERSE_SINGLE_POSTGRES_HOST="${AUTHVERSE_SINGLE_POSTGRES_HOST:-pgpool-internal}"
  AUTHVERSE_SINGLE_POSTGRES_PORT="${AUTHVERSE_SINGLE_POSTGRES_PORT:-5432}"
  AUTHVERSE_SINGLE_REDIS_HOST="${AUTHVERSE_SINGLE_REDIS_HOST:-redis-1}"
  AUTHVERSE_SINGLE_REDIS_PORT="${AUTHVERSE_SINGLE_REDIS_PORT:-6379}"
  AUTHVERSE_SINGLE_ELASTICSEARCH_URI="${AUTHVERSE_SINGLE_ELASTICSEARCH_URI:-http://elasticsearch:9200}"
  AUTHVERSE_SINGLE_DB_MASTER_URL="${AUTHVERSE_SINGLE_DB_MASTER_URL:-jdbc:postgresql://${AUTHVERSE_SINGLE_POSTGRES_HOST}:${AUTHVERSE_SINGLE_POSTGRES_PORT}/${AUTHVERSE_DB_NAME}?sslmode=disable&tcpKeepAlive=true}"
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

inspect_swarm_network_field() {
  local network_name="$1"
  local template="$2"
  docker network inspect "$network_name" --format "$template" 2>/dev/null || true
}

list_swarm_networks_by_subnet() {
  local subnet="$1"
  local network_id network_name network_subnet

  while IFS= read -r network_id; do
    [[ -n "$network_id" ]] || continue
    network_name="$(docker network inspect "$network_id" --format '{{.Name}}' 2>/dev/null || true)"
    network_subnet="$(docker network inspect "$network_id" --format '{{range .IPAM.Config}}{{.Subnet}}{{end}}' 2>/dev/null || true)"
    if [[ -n "$network_name" && "$network_subnet" == "$subnet" ]]; then
      printf '%s\n' "$network_name"
    fi
  done < <(docker network ls -q 2>/dev/null || true)
}

ensure_shared_overlay_network() {
  local network_name="$1"
  local subnet="${CLOUDREVE_BACKEND_SUBNET:-10.20.0.0/24}"
  local cmd=(docker network create --driver overlay --subnet "$subnet")
  local driver scope actual_subnet attachable encrypted has_containers
  local create_output overlaps

  if docker network inspect "$network_name" >/dev/null 2>&1; then
    driver="$(inspect_swarm_network_field "$network_name" '{{.Driver}}')"
    scope="$(inspect_swarm_network_field "$network_name" '{{.Scope}}')"
    actual_subnet="$(inspect_swarm_network_field "$network_name" '{{range .IPAM.Config}}{{.Subnet}}{{end}}')"
    attachable="$(inspect_swarm_network_field "$network_name" '{{.Attachable}}')"
    encrypted="$(inspect_swarm_network_field "$network_name" '{{if .Options}}{{if index .Options "encrypted"}}true{{else}}false{{end}}{{else}}false{{end}}')"
    has_containers="$(inspect_swarm_network_field "$network_name" '{{if .Containers}}true{{else}}false{{end}}')"

    if [[ "$driver" == "overlay" && "$scope" == "swarm" && "$actual_subnet" == "$subnet" ]]; then
      return 0
    fi

    if [[ "$has_containers" == "true" ]]; then
      echo "共享 overlay 网络 $network_name 已存在，但状态异常，无法自动重建。" >&2
      echo "当前 driver=$driver scope=$scope subnet=${actual_subnet:-<empty>} attachable=${attachable:-<empty>} encrypted=${encrypted:-<empty>}" >&2
      echo "请先清理占用该网络的旧 stack 或改用新的 CLOUDREVE_BACKEND_SUBNET 后重试。" >&2
      exit 1
    fi

    echo "检测到损坏或不匹配的共享 overlay 网络：$network_name，开始删除并重建。"
    echo "当前 driver=$driver scope=$scope subnet=${actual_subnet:-<empty>} attachable=${attachable:-<empty>} encrypted=${encrypted:-<empty>}"
    docker network rm "$network_name" >/dev/null
  fi

  if is_truthy "${SWARM_OVERLAY_ATTACHABLE:-false}"; then
    cmd+=(--attachable)
  fi
  if is_truthy "${SWARM_OVERLAY_ENCRYPT:-true}"; then
    cmd+=(--opt encrypted)
  fi
  cmd+=("$network_name")

  echo "共享 overlay 网络不存在，开始创建：$network_name"
  if ! create_output="$("${cmd[@]}" 2>&1)"; then
    echo "创建共享 overlay 网络失败：$network_name" >&2
    echo "$create_output" >&2
    overlaps="$(list_swarm_networks_by_subnet "$subnet" | paste -sd ',' -)"
    if [[ -n "$overlaps" ]]; then
      echo "检测到相同子网 $subnet 已被这些网络占用：$overlaps" >&2
    fi
    exit 1
  fi
}

prepare_stack_name_defaults() {
  SINGLE_STACK_NAME="${SINGLE_STACK_NAME:-cloudreve-single}"
  FOUNDATION_STACK_NAME="${FOUNDATION_STACK_NAME:-cloudreve-foundation}"
  INFRA_STACK_NAME="${INFRA_STACK_NAME:-cloudreve-infra}"
  CLOUDREVE_STACK_NAME="${CLOUDREVE_STACK_NAME:-cloudreve-app}"
  AUTHVERSE_STACK_NAME="${AUTHVERSE_STACK_NAME:-authverse}"
  PRIVATE_REGISTRY_STACK_NAME="${PRIVATE_REGISTRY_STACK_NAME:-cloudreve-registry}"
  SWARM_EDGE_LB_STACK_NAME="${SWARM_EDGE_LB_STACK_NAME:-cloudreve-edge-lb}"
  SWARM_SHARED_NETWORK="${SWARM_SHARED_NETWORK:-cloudreve_backend}"

  export SINGLE_STACK_NAME
  export FOUNDATION_STACK_NAME
  export INFRA_STACK_NAME
  export CLOUDREVE_STACK_NAME
  export AUTHVERSE_STACK_NAME
  export PRIVATE_REGISTRY_STACK_NAME
  export SWARM_EDGE_LB_STACK_NAME
  export SWARM_SHARED_NETWORK
}

prepare_shared_stack_env() {
  local auto_prefixes=0
  local default_cloudreve_kafka_brokers
  if is_truthy "${SWARM_AUTO_SERVICE_PREFIXES:-true}"; then
    auto_prefixes=1
    CLOUDREVE_FOUNDATION_SERVICE_PREFIX="${FOUNDATION_STACK_NAME}_"
    CLOUDREVE_INFRA_SERVICE_PREFIX="${INFRA_STACK_NAME}_"
    CLOUDREVE_APP_SERVICE_PREFIX="${CLOUDREVE_STACK_NAME}_"
  else
    CLOUDREVE_FOUNDATION_SERVICE_PREFIX="${CLOUDREVE_FOUNDATION_SERVICE_PREFIX:-${FOUNDATION_STACK_NAME}_}"
    CLOUDREVE_INFRA_SERVICE_PREFIX="${CLOUDREVE_INFRA_SERVICE_PREFIX:-${INFRA_STACK_NAME}_}"
    CLOUDREVE_APP_SERVICE_PREFIX="${CLOUDREVE_APP_SERVICE_PREFIX:-${CLOUDREVE_STACK_NAME}_}"
  fi
  AUTHVERSE_SHARED_NETWORK="${AUTHVERSE_SHARED_NETWORK:-$SWARM_SHARED_NETWORK}"
  if [[ "$auto_prefixes" -eq 1 ]]; then
    AUTHVERSE_CLOUDREVE_SERVICE_PREFIX="$CLOUDREVE_APP_SERVICE_PREFIX"
  else
    AUTHVERSE_CLOUDREVE_SERVICE_PREFIX="${AUTHVERSE_CLOUDREVE_SERVICE_PREFIX:-$CLOUDREVE_APP_SERVICE_PREFIX}"
  fi

  export CLOUDREVE_FOUNDATION_SERVICE_PREFIX
  export CLOUDREVE_INFRA_SERVICE_PREFIX
  export CLOUDREVE_APP_SERVICE_PREFIX
  export AUTHVERSE_SHARED_NETWORK
  export AUTHVERSE_CLOUDREVE_SERVICE_PREFIX

  if [[ "$COMPOSE_KIND" == "single" ]]; then
    return 0
  fi

  default_cloudreve_kafka_brokers="${CLOUDREVE_INFRA_SERVICE_PREFIX}kafka:9092"

  if [[ "$auto_prefixes" -eq 1 ]]; then
    CLOUDREVE_POSTGRES_HOST="${CLOUDREVE_FOUNDATION_SERVICE_PREFIX}pgpool-internal"
    CLOUDREVE_REDIS_ENDPOINT="${CLOUDREVE_FOUNDATION_SERVICE_PREFIX}redis-proxy-internal:6379"
    CLOUDREVE_REDIS_USE_TLS="false"
    CLOUDREVE_FTS_ELASTICSEARCH_ENDPOINT="http://${CLOUDREVE_INFRA_SERVICE_PREFIX}elasticsearch-internal:9200"
    CLOUDREVE_FTS_TIKA_ENDPOINT="http://${CLOUDREVE_FOUNDATION_SERVICE_PREFIX}tika:9998"
    CR_INIT_S3_ENDPOINT="http://minio-internal:9000"
    MINIO_INIT_ENDPOINT="http://minio-internal:9000"
    KAFKA_UI_BOOTSTRAP_SERVERS="${CLOUDREVE_INFRA_SERVICE_PREFIX}kafka:9092"
    KAFKA_1_ADVERTISED_LISTENER="${CLOUDREVE_INFRA_SERVICE_PREFIX}kafka-1:9092"
    KAFKA_2_ADVERTISED_LISTENER="${CLOUDREVE_INFRA_SERVICE_PREFIX}kafka-2:9092"
    KAFKA_3_ADVERTISED_LISTENER="${CLOUDREVE_INFRA_SERVICE_PREFIX}kafka-3:9092"
    ONLYOFFICE_DB_HOST="${CLOUDREVE_FOUNDATION_SERVICE_PREFIX}pgpool-internal"
    ONLYOFFICE_REDIS_HOST="${CLOUDREVE_FOUNDATION_SERVICE_PREFIX}redis-proxy-internal"
    AUTHVERSE_CLOUDREVE_INTERNAL_UPSTREAM="${CLOUDREVE_APP_SERVICE_PREFIX}cloudreve-master:5212"
    AUTHVERSE_POSTGRES_HOST="${CLOUDREVE_FOUNDATION_SERVICE_PREFIX}pgpool-internal"
    AUTHVERSE_REDIS_HOST="${CLOUDREVE_FOUNDATION_SERVICE_PREFIX}redis-proxy-internal"
    AUTHVERSE_ELASTICSEARCH_URI="http://${CLOUDREVE_INFRA_SERVICE_PREFIX}elasticsearch-internal:9200"
    AUTHVERSE_DB_MASTER_URL="jdbc:postgresql://${AUTHVERSE_POSTGRES_HOST}:${AUTHVERSE_POSTGRES_PORT:-5432}/${AUTHVERSE_DB_NAME:-authverse}?sslmode=disable&tcpKeepAlive=true"
    AUTHVERSE_DB_SLAVE_URL="$AUTHVERSE_DB_MASTER_URL"
  else
    CLOUDREVE_POSTGRES_HOST="${CLOUDREVE_POSTGRES_HOST:-${CLOUDREVE_FOUNDATION_SERVICE_PREFIX}pgpool-internal}"
    CLOUDREVE_REDIS_ENDPOINT="${CLOUDREVE_REDIS_ENDPOINT:-${CLOUDREVE_FOUNDATION_SERVICE_PREFIX}redis-proxy-internal:6379}"
    CLOUDREVE_REDIS_USE_TLS="${CLOUDREVE_REDIS_USE_TLS:-false}"
    CLOUDREVE_FTS_ELASTICSEARCH_ENDPOINT="${CLOUDREVE_FTS_ELASTICSEARCH_ENDPOINT:-http://${CLOUDREVE_INFRA_SERVICE_PREFIX}elasticsearch-internal:9200}"
    CLOUDREVE_FTS_TIKA_ENDPOINT="${CLOUDREVE_FTS_TIKA_ENDPOINT:-http://${CLOUDREVE_FOUNDATION_SERVICE_PREFIX}tika:9998}"
    CR_INIT_S3_ENDPOINT="${CR_INIT_S3_ENDPOINT:-http://minio-internal:9000}"
    MINIO_INIT_ENDPOINT="${MINIO_INIT_ENDPOINT:-http://minio-internal:9000}"
    KAFKA_UI_BOOTSTRAP_SERVERS="${KAFKA_UI_BOOTSTRAP_SERVERS:-${CLOUDREVE_INFRA_SERVICE_PREFIX}kafka:9092}"
    KAFKA_1_ADVERTISED_LISTENER="${KAFKA_1_ADVERTISED_LISTENER:-${CLOUDREVE_INFRA_SERVICE_PREFIX}kafka-1:9092}"
    KAFKA_2_ADVERTISED_LISTENER="${KAFKA_2_ADVERTISED_LISTENER:-${CLOUDREVE_INFRA_SERVICE_PREFIX}kafka-2:9092}"
    KAFKA_3_ADVERTISED_LISTENER="${KAFKA_3_ADVERTISED_LISTENER:-${CLOUDREVE_INFRA_SERVICE_PREFIX}kafka-3:9092}"
    ONLYOFFICE_DB_HOST="${ONLYOFFICE_DB_HOST:-${CLOUDREVE_FOUNDATION_SERVICE_PREFIX}pgpool-internal}"
    ONLYOFFICE_REDIS_HOST="${ONLYOFFICE_REDIS_HOST:-${CLOUDREVE_FOUNDATION_SERVICE_PREFIX}redis-proxy-internal}"
    AUTHVERSE_CLOUDREVE_INTERNAL_UPSTREAM="${AUTHVERSE_CLOUDREVE_INTERNAL_UPSTREAM:-${CLOUDREVE_APP_SERVICE_PREFIX}cloudreve-master:5212}"
    AUTHVERSE_POSTGRES_HOST="${AUTHVERSE_POSTGRES_HOST:-${CLOUDREVE_FOUNDATION_SERVICE_PREFIX}pgpool-internal}"
    AUTHVERSE_REDIS_HOST="${AUTHVERSE_REDIS_HOST:-${CLOUDREVE_FOUNDATION_SERVICE_PREFIX}redis-proxy-internal}"
    AUTHVERSE_ELASTICSEARCH_URI="${AUTHVERSE_ELASTICSEARCH_URI:-http://${CLOUDREVE_INFRA_SERVICE_PREFIX}elasticsearch-internal:9200}"
    AUTHVERSE_DB_MASTER_URL="${AUTHVERSE_DB_MASTER_URL:-jdbc:postgresql://${AUTHVERSE_POSTGRES_HOST}:${AUTHVERSE_POSTGRES_PORT:-5432}/${AUTHVERSE_DB_NAME:-authverse}?sslmode=disable&tcpKeepAlive=true}"
    AUTHVERSE_DB_SLAVE_URL="${AUTHVERSE_DB_SLAVE_URL:-$AUTHVERSE_DB_MASTER_URL}"
  fi

  prepare_cloudreve_kafka_env "$default_cloudreve_kafka_brokers"

  export CLOUDREVE_POSTGRES_HOST
  export CLOUDREVE_REDIS_ENDPOINT
  export CLOUDREVE_REDIS_USE_TLS
  export CLOUDREVE_FTS_ELASTICSEARCH_ENDPOINT
  export CLOUDREVE_FTS_TIKA_ENDPOINT
  export CR_INIT_S3_ENDPOINT
  export MINIO_INIT_ENDPOINT
  export CLOUDREVE_GLOBAL_KAFKA_TLS_MODE
  export CLOUDREVE_GLOBAL_KAFKA_BROKERS
  export CLOUDREVE_GLOBAL_KAFKA_SECURITY_PROTOCOL
  export CLOUDREVE_GLOBAL_KAFKA_TLS_SKIP_VERIFY
  export CLOUDREVE_GLOBAL_KAFKA_TLS_SERVER_NAME
  export CLOUDREVE_GLOBAL_KAFKA_TLS_CA_PATH
  export KAFKA_UI_BOOTSTRAP_SERVERS
  export KAFKA_1_ADVERTISED_LISTENER
  export KAFKA_2_ADVERTISED_LISTENER
  export KAFKA_3_ADVERTISED_LISTENER
  export ONLYOFFICE_DB_HOST
  export ONLYOFFICE_REDIS_HOST
  export AUTHVERSE_CLOUDREVE_INTERNAL_UPSTREAM
  export AUTHVERSE_POSTGRES_HOST
  export AUTHVERSE_REDIS_HOST
  export AUTHVERSE_ELASTICSEARCH_URI
  export AUTHVERSE_DB_MASTER_URL
  export AUTHVERSE_DB_SLAVE_URL
}

prepare_external_auth_env() {
  AUTHVERSE_STACK_NAME="$STACK_NAME"
  AUTHVERSE_CLOUDREVE_SERVICE_PREFIX="${AUTHVERSE_CLOUDREVE_SERVICE_PREFIX:-${CLOUDREVE_APP_SERVICE_PREFIX}}"
  AUTHVERSE_SHARED_NETWORK="${AUTHVERSE_SHARED_NETWORK:-$SWARM_SHARED_NETWORK}"
  AUTHVERSE_CLOUDREVE_INTERNAL_UPSTREAM="${AUTHVERSE_CLOUDREVE_INTERNAL_UPSTREAM:-${CLOUDREVE_APP_SERVICE_PREFIX}cloudreve-master:5212}"
  AUTHVERSE_POSTGRES_HOST="${AUTHVERSE_POSTGRES_HOST:-${CLOUDREVE_FOUNDATION_SERVICE_PREFIX}pgpool-internal}"
  AUTHVERSE_POSTGRES_PORT="${AUTHVERSE_POSTGRES_PORT:-5432}"
  AUTHVERSE_REDIS_HOST="${AUTHVERSE_REDIS_HOST:-${CLOUDREVE_FOUNDATION_SERVICE_PREFIX}redis-proxy-internal}"
  AUTHVERSE_REDIS_PORT="${AUTHVERSE_REDIS_PORT:-6379}"
  AUTHVERSE_ELASTICSEARCH_URI="${AUTHVERSE_ELASTICSEARCH_URI:-http://${CLOUDREVE_INFRA_SERVICE_PREFIX}elasticsearch-internal:9200}"
  if is_truthy "${SWARM_AUTO_SERVICE_PREFIXES:-true}"; then
    AUTHVERSE_DB_MASTER_URL="jdbc:postgresql://${AUTHVERSE_POSTGRES_HOST}:${AUTHVERSE_POSTGRES_PORT}/${AUTHVERSE_DB_NAME}?sslmode=disable&tcpKeepAlive=true"
    AUTHVERSE_DB_SLAVE_URL="$AUTHVERSE_DB_MASTER_URL"
  else
    AUTHVERSE_DB_MASTER_URL="${AUTHVERSE_DB_MASTER_URL:-jdbc:postgresql://${AUTHVERSE_POSTGRES_HOST}:${AUTHVERSE_POSTGRES_PORT}/${AUTHVERSE_DB_NAME}?sslmode=disable&tcpKeepAlive=true}"
    AUTHVERSE_DB_SLAVE_URL="${AUTHVERSE_DB_SLAVE_URL:-$AUTHVERSE_DB_MASTER_URL}"
  fi

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
  export AUTHVERSE_REDIS_SSL_ENABLED

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

if [[ "$RENDER_ONLY" -eq 0 ]] && ! docker info --format '{{.Swarm.LocalNodeState}}' | grep -qx 'active'; then
  echo "当前节点还没有启用 Docker Swarm。" >&2
  exit 1
fi

mkdir -p "$RESOLVED_DIR"

load_swarm_env "$ENV_FILE"

prepare_common_mount_env
prepare_image_source_env
validate_remote_image_source_env
prepare_auth_common_env
prepare_java_truststore_env
prepare_secret_value_env
prepare_cloudreve_secret_mappings

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

prepare_stack_name_defaults

case "$COMPOSE_KIND" in
  auth)
    STACK_NAME="${CLI_STACK_NAME:-${AUTHVERSE_STACK_NAME:-authverse}}"
    AUTHVERSE_STACK_NAME="$STACK_NAME"
    export AUTHVERSE_STACK_NAME
    prepare_shared_stack_env
    prepare_external_auth_env
    ;;
  edge-lb)
    STACK_NAME="${CLI_STACK_NAME:-${SWARM_EDGE_LB_STACK_NAME:-cloudreve-edge-lb}}"
    SWARM_EDGE_LB_STACK_NAME="$STACK_NAME"
    export SWARM_EDGE_LB_STACK_NAME
    ;;
  registry)
    STACK_NAME="${CLI_STACK_NAME:-${PRIVATE_REGISTRY_STACK_NAME:-cloudreve-registry}}"
    PRIVATE_REGISTRY_STACK_NAME="$STACK_NAME"
    export PRIVATE_REGISTRY_STACK_NAME
    prepare_shared_stack_env
    if [[ -z "${PRIVATE_REGISTRY_ADDR:-}" ]]; then
      echo "缺少 PRIVATE_REGISTRY_ADDR，无法部署私有仓库。" >&2
      exit 1
    fi
    ;;
  foundation)
    STACK_NAME="${CLI_STACK_NAME:-${FOUNDATION_STACK_NAME:-cloudreve-foundation}}"
    FOUNDATION_STACK_NAME="$STACK_NAME"
    export FOUNDATION_STACK_NAME
    prepare_shared_stack_env
    ;;
  infra)
    STACK_NAME="${CLI_STACK_NAME:-${INFRA_STACK_NAME:-cloudreve-infra}}"
    INFRA_STACK_NAME="$STACK_NAME"
    export INFRA_STACK_NAME
    prepare_shared_stack_env
    ;;
  cloudreve)
    STACK_NAME="${CLI_STACK_NAME:-${CLOUDREVE_STACK_NAME:-cloudreve-app}}"
    CLOUDREVE_STACK_NAME="$STACK_NAME"
    export CLOUDREVE_STACK_NAME
    prepare_shared_stack_env
    ;;
  single)
    STACK_NAME="${CLI_STACK_NAME:-${SINGLE_STACK_NAME:-cloudreve-single}}"
    export CLOUDREVE_STACK_NAME="$STACK_NAME"
    prepare_single_auth_env
    ;;
  *)
    STACK_NAME="${CLI_STACK_NAME:-${CLOUDREVE_STACK_NAME:-cloudreve-app}}"
    export CLOUDREVE_STACK_NAME="$STACK_NAME"
    prepare_shared_stack_env
    ;;
esac

case "$COMPOSE_KIND" in
  cloudreve|foundation|infra|auth|registry)
    if [[ "$RENDER_ONLY" -eq 0 ]]; then
      ensure_shared_overlay_network "$SWARM_SHARED_NETWORK"
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
