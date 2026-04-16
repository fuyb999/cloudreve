#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
IMAGE_KEYS="${IMAGE_KEYS:-AUTHVERSE_WEB,AUTHVERSE_BACKEND}"
PULL_BASE="no"
HOST_NAME="$(hostname -s 2>/dev/null || hostname)"

usage() {
  cat <<'EOF'
用法：
  docker/swarm/build-auth-images.sh [--env-file 文件] [--image-keys 列表] [--pull] [--no-pull]

说明：
  这个脚本用于在 manager 节点本地构建统一认证前后端镜像。
  构建完成后，再用 `docker/swarm/publish-private-images.sh` 推送到 registry:2。

参数：
  --env-file FILE       读取的环境变量文件，默认是 .env.swarm
  --image-keys LIST     要构建的镜像列表，默认是 AUTHVERSE_WEB,AUTHVERSE_BACKEND
  --pull                构建前主动拉取基础镜像
  --no-pull             不主动拉取基础镜像
  -h, --help            显示帮助

示例：
  docker/swarm/build-auth-images.sh --env-file .env.swarm
  docker/swarm/build-auth-images.sh --image-keys AUTHVERSE_WEB
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      ENV_FILE="$2"
      shift 2
      ;;
    --image-keys)
      IMAGE_KEYS="$2"
      shift 2
      ;;
    --pull)
      PULL_BASE="yes"
      shift
      ;;
    --no-pull)
      PULL_BASE="no"
      shift
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

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

AUTHVERSE_FRONTEND_DIR="${AUTHVERSE_FRONTEND_DIR:-$ROOT_DIR/../authverse}"
AUTHVERSE_BACKEND_DIR="${AUTHVERSE_BACKEND_DIR:-$ROOT_DIR/../authverse-backend}"
AUTHVERSE_FRONTEND_DOCKERFILE="${AUTHVERSE_FRONTEND_DOCKERFILE:-$AUTHVERSE_FRONTEND_DIR/Dockerfile.swarm}"
AUTHVERSE_BACKEND_DOCKERFILE="${AUTHVERSE_BACKEND_DOCKERFILE:-$AUTHVERSE_BACKEND_DIR/Dockerfile.swarm}"
SWARM_IMAGE_SOURCE="${SWARM_IMAGE_SOURCE:-remote}"
build_args=()

if [[ "$PULL_BASE" == "yes" ]]; then
  build_args+=(--pull)
fi

docker_build() {
  if [[ ${#build_args[@]} -gt 0 ]]; then
    docker build "${build_args[@]}" "$@"
  else
    docker build "$@"
  fi
}

resolve_image_by_source() {
  local image_key="$1"
  local default_image="$2"
  local image_var="${image_key}_IMAGE"
  local local_var="${image_key}_LOCAL_IMAGE"
  local remote_var="${image_key}_REMOTE_IMAGE"

  case "$SWARM_IMAGE_SOURCE" in
    remote)
      printf '%s' "${!image_var:-${!remote_var:-${!local_var:-$default_image}}}"
      ;;
    local|"")
      printf '%s' "${!image_var:-${!local_var:-${!remote_var:-$default_image}}}"
      ;;
    *)
      echo "[$HOST_NAME] 不支持的 SWARM_IMAGE_SOURCE=$SWARM_IMAGE_SOURCE" >&2
      exit 1
      ;;
  esac
}

require_private_remote_image() {
  local image_key="$1"
  local image_value="$2"

  if [[ "$SWARM_IMAGE_SOURCE" != "remote" ]]; then
    return 0
  fi
  if [[ -z "${PRIVATE_REGISTRY_ADDR:-}" ]]; then
    echo "[$HOST_NAME] SWARM_IMAGE_SOURCE=remote 时缺少 PRIVATE_REGISTRY_ADDR。" >&2
    exit 1
  fi
  if [[ "$image_value" != "${PRIVATE_REGISTRY_ADDR}/"* ]]; then
    echo "[$HOST_NAME] $image_key 必须走私有仓库，当前值为：$image_value" >&2
    exit 1
  fi
}

build_authverse_web() {
  local image="${AUTHVERSE_WEB_LOCAL_IMAGE:-${AUTHVERSE_WEB_IMAGE:-}}"
  local builder_base_image
  local runtime_base_image

  if [[ -z "$image" ]]; then
    echo "[$HOST_NAME] 缺少 AUTHVERSE_WEB_LOCAL_IMAGE 或 AUTHVERSE_WEB_IMAGE。" >&2
    exit 1
  fi
  if [[ ! -f "$AUTHVERSE_FRONTEND_DOCKERFILE" ]]; then
    echo "[$HOST_NAME] 找不到前端 Dockerfile: $AUTHVERSE_FRONTEND_DOCKERFILE" >&2
    exit 1
  fi

  builder_base_image="$(resolve_image_by_source "AUTHVERSE_WEB_BUILDER_BASE" "node:22.14.0-alpine3.21")"
  runtime_base_image="$(resolve_image_by_source "AUTHVERSE_WEB_RUNTIME_BASE" "nginx:1.27.5-alpine")"
  require_private_remote_image "AUTHVERSE_WEB_BUILDER_BASE_IMAGE" "$builder_base_image"
  require_private_remote_image "AUTHVERSE_WEB_RUNTIME_BASE_IMAGE" "$runtime_base_image"

  echo "[$HOST_NAME] [BUILD] AUTHVERSE_WEB -> $image"
  docker_build \
    -f "$AUTHVERSE_FRONTEND_DOCKERFILE" \
    -t "$image" \
    --build-arg "BUILDER_BASE_IMAGE=${builder_base_image}" \
    --build-arg "RUNTIME_BASE_IMAGE=${runtime_base_image}" \
    --build-arg "NODE_OPTIONS=${AUTHVERSE_WEB_BUILD_NODE_OPTIONS:---max-old-space-size=4096}" \
    --build-arg "VITE_YUDAO_AUTH_ENABLED=true" \
    --build-arg "VITE_YUDAO_API_BASE=/admin-api" \
    --build-arg "VITE_YUDAO_APP_API_BASE=/app-api" \
    --build-arg "VITE_YUDAO_TENANT_ENABLED=${AUTHVERSE_TENANT_ENABLED:-false}" \
    --build-arg "VITE_YUDAO_DEFAULT_TENANT_NAME=${AUTHVERSE_DEFAULT_TENANT_NAME:-统一认证}" \
    --build-arg "VITE_YUDAO_SITE_TITLE=${AUTHVERSE_SITE_TITLE:-统一授权中心}" \
    --build-arg "VITE_YUDAO_LOGIN_CAPTCHA=${AUTHVERSE_LOGIN_CAPTCHA_ENABLED:-false}" \
    --build-arg "VITE_YUDAO_REFRESH_TOKEN_TTL_SECONDS=${AUTHVERSE_REFRESH_TOKEN_TTL_SECONDS:-2592000}" \
    --build-arg "VITE_YUDAO_ENABLE_MOCK_FALLBACK=false" \
    --build-arg "VITE_YUDAO_MOCK_TENANT_ID=1" \
    --build-arg "VITE_YUDAO_MOCK_USERNAME=admin" \
    --build-arg "VITE_YUDAO_MOCK_PASSWORD=admin123" \
    --build-arg "VITE_YUDAO_MOCK_NICKNAME=管理员" \
    "$AUTHVERSE_FRONTEND_DIR"
}

build_authverse_backend() {
  local image="${AUTHVERSE_BACKEND_LOCAL_IMAGE:-${AUTHVERSE_BACKEND_IMAGE:-}}"
  local builder_base_image
  local runtime_base_image

  if [[ -z "$image" ]]; then
    echo "[$HOST_NAME] 缺少 AUTHVERSE_BACKEND_LOCAL_IMAGE 或 AUTHVERSE_BACKEND_IMAGE。" >&2
    exit 1
  fi
  if [[ ! -f "$AUTHVERSE_BACKEND_DOCKERFILE" ]]; then
    echo "[$HOST_NAME] 找不到后端 Dockerfile: $AUTHVERSE_BACKEND_DOCKERFILE" >&2
    exit 1
  fi

  builder_base_image="$(resolve_image_by_source "AUTHVERSE_BACKEND_BUILDER_BASE" "maven:3.9.9-eclipse-temurin-21")"
  runtime_base_image="$(resolve_image_by_source "AUTHVERSE_BACKEND_RUNTIME_BASE" "eclipse-temurin:21-jre-jammy")"
  require_private_remote_image "AUTHVERSE_BACKEND_BUILDER_BASE_IMAGE" "$builder_base_image"
  require_private_remote_image "AUTHVERSE_BACKEND_RUNTIME_BASE_IMAGE" "$runtime_base_image"

  echo "[$HOST_NAME] [BUILD] AUTHVERSE_BACKEND -> $image"
  docker_build \
    -f "$AUTHVERSE_BACKEND_DOCKERFILE" \
    -t "$image" \
    --build-arg "BUILDER_BASE_IMAGE=${builder_base_image}" \
    --build-arg "RUNTIME_BASE_IMAGE=${runtime_base_image}" \
    "$AUTHVERSE_BACKEND_DIR"
}

IFS=',' read -r -a image_keys <<<"$IMAGE_KEYS"
for image_key in "${image_keys[@]}"; do
  image_key="$(echo "$image_key" | xargs)"
  [[ -n "$image_key" ]] || continue

  case "$image_key" in
    AUTHVERSE_WEB)
      build_authverse_web
      ;;
    AUTHVERSE_BACKEND)
      build_authverse_backend
      ;;
    *)
      echo "[$HOST_NAME] 不支持的镜像键：$image_key" >&2
      exit 1
      ;;
  esac
done

echo "[$HOST_NAME] 统一认证镜像构建完成。"
