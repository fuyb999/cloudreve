#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
CLI_STACK_NAME="${STACK_NAME:-}"
RESOLVED_DIR="${RESOLVED_DIR:-$ROOT_DIR/.tmp}"
COMPOSE_FILE="$ROOT_DIR/docker-compose.swarm.registry.yml"
RENDER_ONLY=0

usage() {
  cat <<'EOF'
用法：
  docker/swarm/deploy-private-registry.sh [--stack-name 名称] [--env-file 文件] [--render-only]

说明：
  这个脚本用于单独部署 Cloudreve 自定义镜像私有仓库栈。
  推荐流程：

  1. 在所有 Swarm 节点执行 `docker/swarm/prepare-private-registry.sh`
  2. 在 registry 固定节点执行 `docker/swarm/prepare-bind-paths.sh --services registry`
  3. 执行本脚本部署 `registry:2`
  4. 执行 `docker/swarm/publish-private-images.sh`
  5. 再执行主业务栈 `docker/swarm/deploy-stack.sh`

参数：
  --stack-name NAME   仓库栈名称，默认读取 PRIVATE_REGISTRY_STACK_NAME 或 cloudreve-registry
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

if ! docker info --format '{{.Swarm.LocalNodeState}}' | grep -qx 'active'; then
  echo "当前节点还没有启用 Docker Swarm。" >&2
  exit 1
fi

mkdir -p "$RESOLVED_DIR"

if [[ -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
elif [[ -z "${PRIVATE_REGISTRY_ADDR:-}" ]]; then
  echo "找不到环境变量文件: $ENV_FILE，且当前环境也没有 PRIVATE_REGISTRY_ADDR。" >&2
  exit 1
fi

STACK_NAME="${CLI_STACK_NAME:-${PRIVATE_REGISTRY_STACK_NAME:-cloudreve-registry}}"

if [[ -z "${PRIVATE_REGISTRY_ADDR:-}" ]]; then
  echo "缺少 PRIVATE_REGISTRY_ADDR，无法部署私有仓库。" >&2
  exit 1
fi

resolved_file="$RESOLVED_DIR/${STACK_NAME}-resolved.yaml"

echo "正在渲染私有仓库配置到: $resolved_file"
docker stack config -c "$COMPOSE_FILE" >"$resolved_file"

if [[ "$RENDER_ONLY" -eq 1 ]]; then
  echo "渲染完成。"
  exit 0
fi

echo "正在部署私有仓库栈: $STACK_NAME"
docker stack deploy -c "$resolved_file" "$STACK_NAME"

echo
echo "当前服务状态："
docker stack services "$STACK_NAME"
