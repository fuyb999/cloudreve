#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
IMAGE_KEYS="${IMAGE_KEYS:-${PRIVATE_REGISTRY_IMAGE_KEYS:-TIKA}}"
HOST_NAME="$(hostname -s 2>/dev/null || hostname)"

usage() {
  cat <<'EOF'
用法：
  docker/swarm/publish-private-images.sh [--env-file 文件] [--image-keys 列表]

说明：
  这个脚本用于把本地已有的自定义镜像重新打 tag 并 push 到 registry:2 私有仓库。
  默认读取：
  - PRIVATE_REGISTRY_ADDR
  - PRIVATE_REGISTRY_SCHEME
  - PRIVATE_REGISTRY_IMAGE_KEYS
  - <KEY>_LOCAL_IMAGE
  - <KEY>_REMOTE_IMAGE
  - <KEY>_IMAGE

默认 image key 是 `TIKA`，也就是：
  - TIKA_LOCAL_IMAGE
  - TIKA_REMOTE_IMAGE
  - TIKA_IMAGE

示例：
  docker/swarm/publish-private-images.sh --env-file .env.swarm
  docker/swarm/publish-private-images.sh --env-file .env.swarm --image-keys TIKA
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

if [[ -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
elif [[ -z "${PRIVATE_REGISTRY_ADDR:-}" ]]; then
  echo "[$HOST_NAME] 找不到环境变量文件: $ENV_FILE，且当前环境也没有 PRIVATE_REGISTRY_ADDR。" >&2
  exit 1
fi

PRIVATE_REGISTRY_SCHEME="${PRIVATE_REGISTRY_SCHEME:-http}"
PRIVATE_REGISTRY_ADDR="${PRIVATE_REGISTRY_ADDR:-}"

if [[ -z "$PRIVATE_REGISTRY_ADDR" ]]; then
  echo "[$HOST_NAME] 缺少 PRIVATE_REGISTRY_ADDR，无法发布私有镜像。" >&2
  exit 1
fi

registry_url="${PRIVATE_REGISTRY_SCHEME}://${PRIVATE_REGISTRY_ADDR}"

echo "[$HOST_NAME] 检查私有仓库：$registry_url"
curl -fsS "${registry_url}/v2/" >/dev/null

IFS=',' read -r -a image_keys <<<"$IMAGE_KEYS"
for image_key in "${image_keys[@]}"; do
  image_key="$(echo "$image_key" | xargs)"
  [[ -n "$image_key" ]] || continue

  local_var="${image_key}_LOCAL_IMAGE"
  remote_var="${image_key}_REMOTE_IMAGE"
  image_var="${image_key}_IMAGE"
  local_image="${!local_var:-}"
  remote_image="${!remote_var:-${!image_var:-}}"

  if [[ -z "$local_image" ]]; then
    echo "[$HOST_NAME] 缺少 $local_var，跳过 $image_key。" >&2
    continue
  fi
  if [[ -z "$remote_image" ]]; then
    echo "[$HOST_NAME] 缺少 $remote_var 或 $image_var，跳过 $image_key。" >&2
    continue
  fi
  if [[ "$remote_image" != "${PRIVATE_REGISTRY_ADDR}/"* ]]; then
    echo "[$HOST_NAME] $remote_var 不是以 ${PRIVATE_REGISTRY_ADDR}/ 开头：$remote_image" >&2
    exit 1
  fi
  if ! docker image inspect "$local_image" >/dev/null 2>&1; then
    echo "[$HOST_NAME] 本地不存在镜像：$local_image" >&2
    exit 1
  fi

  echo "[$HOST_NAME] [TAG]  $local_image -> $remote_image"
  docker tag "$local_image" "$remote_image"

  echo "[$HOST_NAME] [PUSH] $remote_image"
  docker push "$remote_image"

  echo "[$HOST_NAME] [READY] $image_key 已发布到私有仓库。"
done
