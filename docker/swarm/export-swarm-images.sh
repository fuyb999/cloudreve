#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT_DIR/docker/swarm/lib-env.sh"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
OUTPUT_PARENT="${OUTPUT_PARENT:-$ROOT_DIR}"
STAMP="${STAMP:-$(date +%Y%m%d-%H%M%S)}"
IMAGE_KEYS="${IMAGE_KEYS:-${PRIVATE_REGISTRY_IMAGE_KEYS:-TIKA,AUTHVERSE_WEB,AUTHVERSE_BACKEND}}"
EXTRA_IMAGES="${EXTRA_IMAGES:-}"
INCLUDE_REMOTE_TAGS="${INCLUDE_REMOTE_TAGS:-yes}"
XZ_OPTIONS="${XZ_OPTIONS:--T0 -3}"
HOST_NAME="$(hostname -s 2>/dev/null || hostname)"

usage() {
  cat <<'EOF'
用法：
  docker/swarm/export-swarm-images.sh [--env-file 文件] [--output-dir 目录] [--archive 文件] [--image-keys 列表] [--extra-images 列表] [--without-remote-tags]

说明：
  这个脚本用于把当前 Swarm 生产方案相关镜像导出到一个目录，并再打成 `.tar.xz`。
  默认会导出：
  - Cloudreve 主栈基础镜像
  - MinIO / Elasticsearch / Kafka 集群镜像
  - `registry:2`
  - `TIKA`、`AUTHVERSE_WEB`、`AUTHVERSE_BACKEND` 这些自定义镜像的本地 tag
  - 如果本地还没有对应 remote tag，会自动按 `.env.swarm` 里的 `<KEY>_REMOTE_IMAGE` 补 tag

参数：
  --env-file FILE         读取的环境变量文件，默认是 .env.swarm
  --output-dir DIR        导出目录的父目录，默认是仓库根目录
  --archive FILE          自定义最终归档文件名，默认是 <output-dir>/swarm-images-时间戳.tar.xz
  --image-keys LIST       自定义镜像键，默认读取 PRIVATE_REGISTRY_IMAGE_KEYS
  --extra-images LIST     额外补充导出的镜像，逗号分隔
  --without-remote-tags   只导出本地 tag，不自动补 remote tag
  --xz-options OPTS       传给 xz 的参数，默认是 '-T0 -3'
  -h, --help              显示帮助

示例：
  docker/swarm/export-swarm-images.sh --env-file .env.swarm
  docker/swarm/export-swarm-images.sh --env-file .env.swarm.prod-4x256g.example --output-dir .
EOF
}

ARCHIVE_FILE=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      ENV_FILE="$2"
      shift 2
      ;;
    --output-dir)
      OUTPUT_PARENT="$2"
      shift 2
      ;;
    --archive)
      ARCHIVE_FILE="$2"
      shift 2
      ;;
    --image-keys)
      IMAGE_KEYS="$2"
      shift 2
      ;;
    --extra-images)
      EXTRA_IMAGES="$2"
      shift 2
      ;;
    --without-remote-tags)
      INCLUDE_REMOTE_TAGS="no"
      shift
      ;;
    --xz-options)
      XZ_OPTIONS="$2"
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

mkdir -p "$OUTPUT_PARENT"

load_swarm_env "$ENV_FILE"

BUNDLE_DIR="$OUTPUT_PARENT/swarm-images-$STAMP"
ARCHIVE_FILE="${ARCHIVE_FILE:-$OUTPUT_PARENT/swarm-images-$STAMP.tar.xz}"
IMAGE_TAR_FILE="$BUNDLE_DIR/docker-images.tar"
MANIFEST_FILE="$BUNDLE_DIR/images.txt"
README_FILE="$BUNDLE_DIR/README.txt"
resolved_images=()
missing_images=()
xz_args=()

read -r -a xz_args <<<"$XZ_OPTIONS"

contains_image() {
  local needle="$1"
  local item
  shift || true
  for item in "$@"; do
    if [[ "$item" == "$needle" ]]; then
      return 0
    fi
  done
  return 1
}

queue_image() {
  local image="$1"
  [[ -n "$image" ]] || return 0
  if [[ ${#resolved_images[@]} -eq 0 ]]; then
    resolved_images+=("$image")
    return 0
  fi
  if ! contains_image "$image" "${resolved_images[@]}"; then
    resolved_images+=("$image")
  fi
}

has_local_image() {
  docker image inspect "$1" >/dev/null 2>&1
}

ensure_remote_tag() {
  local local_image="$1"
  local remote_image="$2"

  [[ -n "$local_image" && -n "$remote_image" ]] || return 0
  if has_local_image "$remote_image"; then
    return 0
  fi
  if ! has_local_image "$local_image"; then
    return 1
  fi

  echo "[$HOST_NAME] [TAG] $local_image -> $remote_image"
  docker tag "$local_image" "$remote_image"
}

require_image() {
  local image="$1"
  [[ -n "$image" ]] || return 0
  if has_local_image "$image"; then
    queue_image "$image"
  else
    missing_images+=("$image")
  fi
}

require_default_images() {
  require_image "${CLOUDREVE_IMAGE:-cloudreve/cloudreve:4.15.0}"
  require_image "${CLOUDREVE_SLAVE_IMAGE:-${CLOUDREVE_IMAGE:-cloudreve/cloudreve:4.15.0}}"
  require_image "${NGINX_IMAGE:-nginx:1.27-alpine}"
  require_image "${POSTGRESQL_REPMGR_IMAGE:-bitnamilegacy/postgresql-repmgr:17.6.0-debian-12-r2}"
  require_image "${PGPOOL_IMAGE:-bitnamilegacy/pgpool:4.6.3-debian-12-r0}"
  require_image "${REDIS_IMAGE:-bitnamilegacy/redis:8.2.1-debian-12-r0}"
  require_image "${REDIS_SENTINEL_IMAGE:-bitnamilegacy/redis-sentinel:8.2.1-debian-12-r0}"
  require_image "${REDIS_PROXY_IMAGE:-haproxy:3.0-alpine}"
  require_image "${MINIO_IMAGE:-bitnamilegacy/minio:2024.10.2-debian-12-r0}"
  require_image "${MINIO_PROXY_IMAGE:-haproxy:3.0-alpine}"
  require_image "${ELASTICSEARCH_IMAGE:-elasticsearch:8.12.2}"
  require_image "${ELASTICSEARCH_PROXY_IMAGE:-haproxy:3.0-alpine}"
  require_image "${KAFKA_IMAGE:-apache/kafka:4.2.0}"
  require_image "${KAFKA_PROXY_IMAGE:-haproxy:3.0-alpine}"
  require_image "${KAFKA_UI_IMAGE:-provectuslabs/kafka-ui:v0.7.2}"
  require_image "${ONLYOFFICE_IMAGE:-onlyoffice/documentserver:8.2.2}"
  require_image "${ONLYOFFICE_RABBITMQ_IMAGE:-rabbitmq:3.13.7-alpine}"
  require_image "${PRIVATE_REGISTRY_IMAGE:-registry:2.8.3}"
}

require_custom_images() {
  local image_key local_var remote_var image_var local_image remote_image

  IFS=',' read -r -a _image_keys <<<"$IMAGE_KEYS"
  for image_key in "${_image_keys[@]}"; do
    image_key="$(echo "$image_key" | xargs)"
    [[ -n "$image_key" ]] || continue

    local_var="${image_key}_LOCAL_IMAGE"
    remote_var="${image_key}_REMOTE_IMAGE"
    image_var="${image_key}_IMAGE"
    local_image="${!local_var:-}"
    remote_image="${!remote_var:-${!image_var:-}}"

    if [[ -n "$local_image" ]]; then
      require_image "$local_image"
    elif [[ -n "$remote_image" ]]; then
      require_image "$remote_image"
    fi

    if [[ "$INCLUDE_REMOTE_TAGS" == "yes" && -n "$remote_image" && -n "$local_image" ]]; then
      if ensure_remote_tag "$local_image" "$remote_image"; then
        require_image "$remote_image"
      fi
    fi
  done
}

require_extra_images() {
  local image
  if [[ -z "$EXTRA_IMAGES" ]]; then
    return 0
  fi
  IFS=',' read -r -a _extra_images <<<"$EXTRA_IMAGES"
  for image in "${_extra_images[@]}"; do
    image="$(echo "$image" | xargs)"
    [[ -n "$image" ]] || continue
    require_image "$image"
  done
}

require_default_images
require_custom_images
require_extra_images

if [[ "${#missing_images[@]}" -gt 0 ]]; then
  printf '[%s] 本地缺少以下镜像，无法导出：\n' "$HOST_NAME" >&2
  printf '  - %s\n' "${missing_images[@]}" >&2
  exit 1
fi

mkdir -p "$BUNDLE_DIR"
rm -f "$IMAGE_TAR_FILE" "$ARCHIVE_FILE"

{
  echo "导出时间：$(date '+%Y-%m-%d %H:%M:%S %z')"
  echo "环境文件：$ENV_FILE"
  echo "镜像数量：${#resolved_images[@]}"
  echo
  printf '%s\n' "${resolved_images[@]}"
} >"$MANIFEST_FILE"

cat >"$README_FILE" <<EOF
解包后加载镜像：
  docker load -i docker-images.tar

如果你已经把私有仓库部署在 manager 上，再按环境文件里的 remote tag 推送：
  docker/swarm/publish-private-images.sh --env-file .env.swarm --image-keys ${IMAGE_KEYS}
EOF

echo "[$HOST_NAME] 正在导出镜像到: $IMAGE_TAR_FILE"
docker save -o "$IMAGE_TAR_FILE" "${resolved_images[@]}"

echo "[$HOST_NAME] 正在归档为: $ARCHIVE_FILE"
LC_ALL=C tar -cf - -C "$OUTPUT_PARENT" "$(basename "$BUNDLE_DIR")" | xz "${xz_args[@]}" >"$ARCHIVE_FILE"

echo "[$HOST_NAME] 导出完成。"
echo "[$HOST_NAME] 目录: $BUNDLE_DIR"
echo "[$HOST_NAME] 归档: $ARCHIVE_FILE"
