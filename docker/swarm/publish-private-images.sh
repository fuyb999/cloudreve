#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT_DIR/docker/swarm/lib-env.sh"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
IMAGE_KEYS="${IMAGE_KEYS:-${PRIVATE_REGISTRY_IMAGE_KEYS:-TIKA}}"
HOST_NAME="$(hostname -s 2>/dev/null || hostname)"

usage() {
  cat <<'EOF'
用法：
  docker/swarm/publish-private-images.sh [--env-file 文件] [--image-keys 列表]

说明：
 这个脚本用于把本地已有镜像重新打 tag 并 push 到 registry:2 私有仓库。
 既支持自定义镜像，也支持把公共镜像 mirror 到私有仓库。
  默认读取：
  - PRIVATE_REGISTRY_ADDR
  - PRIVATE_REGISTRY_SCHEME
  - PRIVATE_REGISTRY_CA_FILE
 - PRIVATE_REGISTRY_IMAGE_KEYS
  - <KEY>_LOCAL_IMAGE
  - <KEY>_REMOTE_IMAGE
  - <KEY>_IMAGE

这个脚本不会自动从外部仓库拉取缺失镜像。
如果本地缺少 `<KEY>_LOCAL_IMAGE`，请先通过 `docker load`、你自己的离线分发流程，
或明确允许的独立准备步骤把镜像放到本机，再执行发布。

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
  load_swarm_env "$ENV_FILE"
elif [[ -z "${PRIVATE_REGISTRY_ADDR:-}" ]]; then
  echo "[$HOST_NAME] 找不到环境变量文件: $ENV_FILE，且当前环境也没有 PRIVATE_REGISTRY_ADDR。" >&2
  exit 1
fi

PRIVATE_REGISTRY_SCHEME="${PRIVATE_REGISTRY_SCHEME:-http}"
PRIVATE_REGISTRY_ADDR="${PRIVATE_REGISTRY_ADDR:-}"
SWARM_PKI_MOUNT_SOURCE="${SWARM_PKI_MOUNT_SOURCE:-/srv/cloudreve/pki}"
PRIVATE_REGISTRY_CA_FILE="${PRIVATE_REGISTRY_CA_FILE:-$SWARM_PKI_MOUNT_SOURCE/ca/ca.crt}"

if [[ -z "$PRIVATE_REGISTRY_ADDR" ]]; then
  echo "[$HOST_NAME] 缺少 PRIVATE_REGISTRY_ADDR，无法发布私有镜像。" >&2
  exit 1
fi

case "$PRIVATE_REGISTRY_SCHEME" in
  http|https)
    ;;
  *)
    echo "[$HOST_NAME] 不支持的 PRIVATE_REGISTRY_SCHEME=$PRIVATE_REGISTRY_SCHEME，只允许 http / https。" >&2
    exit 1
    ;;
esac

registry_url="${PRIVATE_REGISTRY_SCHEME}://${PRIVATE_REGISTRY_ADDR}"
curl_args=(-fsS --connect-timeout 5 --retry 3 --retry-delay 1)

if [[ "$PRIVATE_REGISTRY_SCHEME" == "https" ]]; then
  if [[ ! -f "$PRIVATE_REGISTRY_CA_FILE" ]]; then
    echo "[$HOST_NAME] 找不到私有仓库 CA 文件：$PRIVATE_REGISTRY_CA_FILE" >&2
    echo "[$HOST_NAME] 先执行 docker/swarm/generate-swarm-pki.sh，或把你自己的 CA 放到这个路径后再重试。" >&2
    exit 1
  fi
  curl_args+=(--cacert "$PRIVATE_REGISTRY_CA_FILE")
fi

echo "[$HOST_NAME] 检查私有仓库：$registry_url"
curl "${curl_args[@]}" "${registry_url}/v2/" >/dev/null

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
    echo "[$HOST_NAME] 本地不存在镜像，且当前流程禁止自动外部拉取：$local_image" >&2
    echo "[$HOST_NAME] 请先准备本地镜像后再执行发布。" >&2
    exit 1
  fi

  echo "[$HOST_NAME] [TAG]  $local_image -> $remote_image"
  docker tag "$local_image" "$remote_image"

  echo "[$HOST_NAME] [PUSH] $remote_image"
  docker push "$remote_image"

  echo "[$HOST_NAME] [READY] $image_key 已发布到私有仓库。"
done
