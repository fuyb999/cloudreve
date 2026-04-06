#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
STACK_NAME="${STACK_NAME:-cloudreve}"
RESOLVED_DIR="${RESOLVED_DIR:-$ROOT_DIR/.tmp}"
WITH_CLUSTER="${WITH_CLUSTER:-}"
COMPOSE_FILE="$ROOT_DIR/docker-compose.swarm.yml"
CLUSTER_COMPOSE_FILE="$ROOT_DIR/docker-compose.swarm.cluster.yml"

usage() {
  cat <<'EOF'
用法：
  docker/swarm/deploy-stack.sh [--stack-name 名称] [--env-file 文件] [--with-cluster|--without-cluster] [--render-only]

参数：
  --stack-name NAME   Swarm 栈名称，默认是 cloudreve
  --env-file FILE     要读取的环境变量文件，默认是 .env.swarm
  --with-cluster      启用 MinIO / Elasticsearch / Kafka 集群覆盖文件
  --without-cluster   强制关闭 MinIO / Elasticsearch / Kafka 集群覆盖文件
  --render-only       只渲染 stack 配置，不执行部署
  -h, --help          显示帮助

可通过环境变量覆盖：
  STACK_NAME, ENV_FILE, RESOLVED_DIR, WITH_CLUSTER, SWARM_WITH_CLUSTER
EOF
}

RENDER_ONLY=0

strip_single_node_services() {
  local src="$1"
  local dst="$2"

  sed \
    -e '/^[[:space:]]*# swarm-cluster-strip:minio:begin$/,/^[[:space:]]*# swarm-cluster-strip:minio:end$/d' \
    -e '/^[[:space:]]*# swarm-cluster-strip:elasticsearch:begin$/,/^[[:space:]]*# swarm-cluster-strip:elasticsearch:end$/d' \
    "$src" >"$dst"
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

while [[ $# -gt 0 ]]; do
  case "$1" in
    --stack-name)
      STACK_NAME="$2"
      shift 2
      ;;
    --env-file)
      ENV_FILE="$2"
      shift 2
      ;;
    --with-cluster)
      WITH_CLUSTER="yes"
      shift
      ;;
    --without-cluster)
      WITH_CLUSTER="no"
      shift
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

if [[ ! -f "$ENV_FILE" ]]; then
  echo "找不到环境变量文件: $ENV_FILE" >&2
  exit 1
fi

if [[ ! -f "$COMPOSE_FILE" ]]; then
  echo "找不到 Compose 文件: $COMPOSE_FILE" >&2
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

WITH_CLUSTER="${WITH_CLUSTER:-${SWARM_WITH_CLUSTER:-no}}"

# 兼容旧变量：如果仍在使用旧写法，但还没有显式声明新的挂载模式变量，
# 就自动映射到新的写法。
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

compose_args=(-c "$COMPOSE_FILE")
render_base_compose_file="$COMPOSE_FILE"

case "$WITH_CLUSTER" in
  yes)
    if [[ ! -f "$CLUSTER_COMPOSE_FILE" ]]; then
      echo "找不到 cluster compose 文件: $CLUSTER_COMPOSE_FILE" >&2
      exit 1
    fi
    render_base_compose_file="$RESOLVED_DIR/${STACK_NAME}-cluster-base.yaml"
    strip_single_node_services "$COMPOSE_FILE" "$render_base_compose_file"
    compose_args=(-c "$render_base_compose_file")
    compose_args+=(-c "$CLUSTER_COMPOSE_FILE")
    ;;
  no)
    :
    ;;
  *)
    echo "WITH_CLUSTER 的值无效: $WITH_CLUSTER" >&2
    exit 1
    ;;
esac

resolved_file="$RESOLVED_DIR/${STACK_NAME}-resolved.yaml"
resolved_raw_file="$RESOLVED_DIR/${STACK_NAME}-resolved.raw.yaml"

echo "正在渲染 stack 配置到: $resolved_file"
if [[ "$WITH_CLUSTER" == "yes" ]]; then
  echo "已启用 MinIO / Elasticsearch / Kafka 集群覆盖文件。"
fi
docker stack config "${compose_args[@]}" >"$resolved_raw_file"
rewrite_relative_config_paths "$resolved_raw_file" "$resolved_file"
rm -f "$resolved_raw_file"

if [[ "$RENDER_ONLY" -eq 1 ]]; then
  echo "渲染完成。"
  exit 0
fi

echo "正在部署 stack: $STACK_NAME"
docker stack deploy -c "$resolved_file" "$STACK_NAME"

echo
echo "当前服务状态："
docker stack services "$STACK_NAME"
