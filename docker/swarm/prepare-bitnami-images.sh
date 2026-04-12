#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
MODE="apply"
PULL_SOURCE="yes"
HOST_NAME="$(hostname -s 2>/dev/null || hostname)"

usage() {
  cat <<'EOF'
用法：
  docker/swarm/prepare-bitnami-images.sh [--env-file 文件] [--check] [--apply] [--pull] [--no-pull]

说明：
  这个脚本用于准备当前模板里 PostgreSQL / Redis / MinIO 的本地源镜像。
  它会优先读取 `*_LOCAL_IMAGE`，只有没配置时才回退到 `*_IMAGE`。

  截至 2026-04-06，这些服务在 Docker Hub 上可稳定使用的版本化 tag
  仍然主要是 `bitnamilegacy/*`。当前模板按这个事实保持一致。

  这个脚本不会做 retag，只负责：

  1. 检查本机是否已存在这些镜像
  2. 缺失时执行 docker pull

  典型用法有两种：

  1. `SWARM_IMAGE_SOURCE=local`
     在每台会调度相关服务的节点执行一次，提前把本地镜像拉齐。
  2. `SWARM_IMAGE_SOURCE=remote`
     只在固定部署 manager 执行一次，准备好 local 源镜像后再执行
     `publish-private-images.sh` 推到私有仓库。

参数：
  --env-file FILE       读取的环境变量文件，默认是 .env.swarm
  --check               只检查，不改动
  --apply               执行拉取，默认就是 apply
  --pull                允许缺失时自动拉取，默认开启
  --no-pull             缺失时不自动拉取，只报错
  -h, --help            显示帮助

示例：
  docker/swarm/prepare-bitnami-images.sh --check
  docker/swarm/prepare-bitnami-images.sh --env-file .env.swarm.prod-4x128g.example
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      ENV_FILE="$2"
      shift 2
      ;;
    --check)
      MODE="check"
      shift
      ;;
    --apply)
      MODE="apply"
      shift
      ;;
    --pull)
      PULL_SOURCE="yes"
      shift
      ;;
    --no-pull)
      PULL_SOURCE="no"
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

failures=0

image_exists() {
  docker image inspect "$1" >/dev/null 2>&1
}

pull_image() {
  local image="$1"
  echo "[$HOST_NAME] [PULL] $image"
  docker pull "$image"
}

resolve_source_image() {
  local image_key="$1"
  local default_image="$2"
  local local_var="${image_key}_LOCAL_IMAGE"
  local image_var="${image_key}_IMAGE"
  local image="${!local_var:-${!image_var:-$default_image}}"

  printf '%s' "$image"
}

ensure_image() {
  local image_key="$1"
  local default_image="$2"
  local image_var="${image_key}_LOCAL_IMAGE"
  local image

  image="$(resolve_source_image "$image_key" "$default_image")"

  if image_exists "$image"; then
    echo "[$HOST_NAME] [OK] $image_var -> $image"
    return 0
  fi

  if [[ "$MODE" == "check" ]]; then
    echo "[$HOST_NAME] [MISSING] $image_var -> $image"
    failures=$((failures + 1))
    return 0
  fi

  if [[ "$PULL_SOURCE" != "yes" ]]; then
    echo "[$HOST_NAME] [ERROR] $image_var 缺失，且已禁用自动拉取: $image" >&2
    failures=$((failures + 1))
    return 0
  fi

  pull_image "$image"
  echo "[$HOST_NAME] [READY] $image_var -> $image"
}

echo "[$HOST_NAME] 开始处理有状态基础镜像，模式=$MODE，自动拉取=$PULL_SOURCE"

ensure_image \
  "POSTGRESQL_REPMGR" \
  "bitnamilegacy/postgresql-repmgr:17.6.0-debian-12-r2"

ensure_image \
  "PGPOOL" \
  "bitnamilegacy/pgpool:4.6.3-debian-12-r0"

ensure_image \
  "REDIS" \
  "bitnamilegacy/redis:8.2.1-debian-12-r0"

ensure_image \
  "REDIS_SENTINEL" \
  "bitnamilegacy/redis-sentinel:8.2.1-debian-12-r0"

ensure_image \
  "MINIO" \
  "bitnamilegacy/minio:2024.10.2-debian-12-r0"

if [[ "$failures" -gt 0 ]]; then
  echo "[$HOST_NAME] 处理完成，但有 $failures 项仍未满足。" >&2
  exit 1
fi

echo "[$HOST_NAME] 有状态基础镜像已就绪。"
