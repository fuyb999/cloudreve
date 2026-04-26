#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT_DIR/docker/swarm/lib-env.sh"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
PUSH_REMOTE="no"
HOST_NAME="$(hostname -s 2>/dev/null || hostname)"

usage() {
  cat <<'EOF'
用法：
  docker/swarm/build-edge-lb-image.sh [--env-file 文件] [--push-remote]

说明：
  构建容器化 HAProxy + Keepalived 边缘 LB 镜像。
  默认只构建本地 tag；加 --push-remote 时会自动推送到私有仓库。

参数：
  --env-file FILE   读取的环境变量文件，默认 .env.swarm
  --push-remote     构建后自动执行 tag + push
  -h, --help        显示帮助
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      ENV_FILE="$2"
      shift 2
      ;;
    --push-remote)
      PUSH_REMOTE="yes"
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

load_swarm_env "$ENV_FILE"

EDGE_LB_LOCAL_IMAGE="${SWARM_EDGE_LB_LOCAL_IMAGE:-${SWARM_EDGE_LB_IMAGE:-}}"
EDGE_LB_REMOTE_IMAGE="${SWARM_EDGE_LB_REMOTE_IMAGE:-}"

if [[ -z "$EDGE_LB_LOCAL_IMAGE" ]]; then
  echo "[$HOST_NAME] 缺少 SWARM_EDGE_LB_LOCAL_IMAGE 或 SWARM_EDGE_LB_IMAGE。" >&2
  exit 1
fi

echo "[$HOST_NAME] [BUILD] SWARM_EDGE_LB -> $EDGE_LB_LOCAL_IMAGE"
docker build \
  -f "$ROOT_DIR/docker/swarm/Dockerfile.edge-lb" \
  -t "$EDGE_LB_LOCAL_IMAGE" \
  "$ROOT_DIR"

if [[ "$PUSH_REMOTE" != "yes" ]]; then
  echo "[$HOST_NAME] edge-lb 镜像构建完成。"
  exit 0
fi

if [[ -z "$EDGE_LB_REMOTE_IMAGE" ]]; then
  echo "[$HOST_NAME] 缺少 SWARM_EDGE_LB_REMOTE_IMAGE，无法推送到私有仓库。" >&2
  exit 1
fi

echo "[$HOST_NAME] [TAG]  $EDGE_LB_LOCAL_IMAGE -> $EDGE_LB_REMOTE_IMAGE"
docker tag "$EDGE_LB_LOCAL_IMAGE" "$EDGE_LB_REMOTE_IMAGE"

echo "[$HOST_NAME] [PUSH] $EDGE_LB_REMOTE_IMAGE"
docker push "$EDGE_LB_REMOTE_IMAGE"

echo "[$HOST_NAME] edge-lb 镜像已推送到私有仓库。"
