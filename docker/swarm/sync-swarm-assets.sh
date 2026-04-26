#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT_DIR/docker/swarm/lib-env.sh"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
TARGETS="${TARGETS:-${SWARM_SYNC_TARGETS:-}}"
SSH_USER="${SSH_USER:-${SWARM_SYNC_SSH_USER:-}}"
REMOTE_ENV_FILE="${REMOTE_ENV_FILE:-}"
MODE="check"
SYNC_ENV="yes"
SYNC_PKI="auto"
SYNC_FONTS="auto"
USE_SUDO="${USE_SUDO:-${SWARM_SYNC_USE_SUDO:-no}}"
SSH_OPTIONS="${SSH_OPTIONS:-${SWARM_SYNC_SSH_OPTIONS:-}}"
HOST_NAME="$(hostname -s 2>/dev/null || hostname)"
ENV_BASE_FILE_PATH=""
REMOTE_ENV_BASE_FILE=""
declare -a SSH_CMD=(ssh)
RSYNC_SSH_CMD="ssh"

usage() {
  cat <<'EOF'
用法：
  docker/swarm/sync-swarm-assets.sh [--env-file 文件] [--targets "host1,host2"] [--ssh-user 用户]
                                    [--remote-env-file 文件] [--check] [--apply]
                                    [--no-sync-env] [--no-sync-pki] [--no-sync-fonts] [--sudo]

说明：
  这个脚本用于在多机 Swarm 环境里统一同步运行资产：
  1. `.env.swarm`
  2. `SWARM_PKI_MOUNT_SOURCE`（仅 bind 模式）
  3. `SHARED_CUSTOM_FONTS_MOUNT_SOURCE`（仅 bind 模式）

  注意：
  - Nginx / HAProxy 模板、启动脚本这类文本配置，已经通过 Swarm `configs` 统一下发，
    不需要用这个脚本额外同步。
  - 这个脚本依赖 `ssh + rsync`。
  - 远端账号必须对目标目录有写权限；生产里通常直接用 root 或具备 sudo 预处理能力的运维账号。

参数：
  --env-file FILE         环境变量文件，默认 .env.swarm
  --targets LIST          目标节点列表，逗号或空格分隔
  --ssh-user USER         如果 targets 里不带 user@，就自动补这个 SSH 用户
  --ssh-options OPTS      透传给 ssh/rsync 的额外参数，例如：'-o StrictHostKeyChecking=no'
  --remote-env-file FILE  远端 `.env.swarm` 落点；默认与本地 ENV_FILE 同路径
  --check                 只检查与展示同步计划
  --apply                 实际执行同步
  --no-sync-env           不同步 `.env.swarm`
  --no-sync-pki           不同步 PKI 目录
  --no-sync-fonts         不同步共享字体目录
  --sudo                  远端创建目录和写入文件时使用 sudo
  -h, --help              显示帮助

示例：
  docker/swarm/sync-swarm-assets.sh --targets "cr-prod-wkr-12,cr-prod-wkr-13,cr-prod-wkr-14" --check
  docker/swarm/sync-swarm-assets.sh --targets "cr-prod-wkr-12 cr-prod-wkr-13 cr-prod-wkr-14" --ssh-user root --apply
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      ENV_FILE="$2"
      shift 2
      ;;
    --targets)
      TARGETS="$2"
      shift 2
      ;;
    --ssh-user)
      SSH_USER="$2"
      shift 2
      ;;
    --ssh-options)
      SSH_OPTIONS="$2"
      shift 2
      ;;
    --remote-env-file)
      REMOTE_ENV_FILE="$2"
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
    --no-sync-env)
      SYNC_ENV="no"
      shift
      ;;
    --no-sync-pki)
      SYNC_PKI="no"
      shift
      ;;
    --no-sync-fonts)
      SYNC_FONTS="no"
      shift
      ;;
    --sudo)
      USE_SUDO="yes"
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

if [[ -n "$SSH_OPTIONS" ]]; then
  read -r -a _ssh_options_array <<<"$SSH_OPTIONS"
  SSH_CMD+=("${_ssh_options_array[@]}")
  RSYNC_SSH_CMD="ssh $SSH_OPTIONS"
fi

REMOTE_ENV_FILE="${REMOTE_ENV_FILE:-$ENV_FILE}"
if [[ -n "${ENV_BASE_FILE:-}" ]]; then
  ENV_BASE_FILE_PATH="$(resolve_swarm_env_path "$ENV_BASE_FILE" "$(dirname "$ENV_FILE")")"
  REMOTE_ENV_BASE_FILE="${REMOTE_ENV_BASE_FILE:-$(dirname "$REMOTE_ENV_FILE")/$(basename "$ENV_BASE_FILE_PATH")}"
fi
SWARM_PKI_MOUNT_TYPE="${SWARM_PKI_MOUNT_TYPE:-bind}"
SWARM_PKI_MOUNT_SOURCE="${SWARM_PKI_MOUNT_SOURCE:-/srv/cloudreve/pki}"
SWARM_PKI_LOCAL_SOURCE="${SWARM_PKI_LOCAL_SOURCE:-$SWARM_PKI_MOUNT_SOURCE}"
SHARED_CUSTOM_FONTS_MOUNT_TYPE="${SHARED_CUSTOM_FONTS_MOUNT_TYPE:-volume}"
SHARED_CUSTOM_FONTS_MOUNT_SOURCE="${SHARED_CUSTOM_FONTS_MOUNT_SOURCE:-shared_custom_fonts}"
SHARED_CUSTOM_FONTS_LOCAL_SOURCE="${SHARED_CUSTOM_FONTS_LOCAL_SOURCE:-$SHARED_CUSTOM_FONTS_MOUNT_SOURCE}"

normalize_targets() {
  local raw="$1"
  local normalized=""
  local item
  local old_ifs="$IFS"

  raw="${raw//,/ }"
  IFS=' '
  set -- $raw
  IFS="$old_ifs"

  for item in "$@"; do
    [[ -n "$item" ]] || continue
    if [[ -n "$normalized" ]]; then
      normalized="$normalized"$'\n'"$item"
    else
      normalized="$item"
    fi
  done

  printf '%s\n' "$normalized"
}

resolve_target() {
  local target="$1"

  if [[ -n "$SSH_USER" && "$target" != *@* ]]; then
    printf '%s@%s\n' "$SSH_USER" "$target"
  else
    printf '%s\n' "$target"
  fi
}

use_remote_sudo() {
  case "$USE_SUDO" in
    1|true|TRUE|yes|YES|on|ON)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

remote_mkdir_p() {
  local target="$1"
  shift || true
  local mkdir_cmd="mkdir -p"
  local remote_dir

  for remote_dir in "$@"; do
    mkdir_cmd+=" $(printf '%q' "$remote_dir")"
  done

  if use_remote_sudo; then
    "${SSH_CMD[@]}" "$target" "sudo $mkdir_cmd"
  else
    "${SSH_CMD[@]}" "$target" "$mkdir_cmd"
  fi
}

sync_file_to_remote() {
  local src="$1"
  local target="$2"
  local dst="$3"

  if use_remote_sudo; then
    rsync -az -e "$RSYNC_SSH_CMD" --rsync-path="sudo rsync" "$src" "$target:$dst"
  else
    rsync -az -e "$RSYNC_SSH_CMD" "$src" "$target:$dst"
  fi
}

sync_dir_to_remote() {
  local src="$1"
  local target="$2"
  local dst="$3"

  if use_remote_sudo; then
    rsync -az -e "$RSYNC_SSH_CMD" --delete --omit-dir-times --no-perms --rsync-path="sudo rsync" "$src/" "$target:$dst/"
  else
    rsync -az -e "$RSYNC_SSH_CMD" "$src/" "$target:$dst/"
  fi
}

should_sync_pki() {
  case "$SYNC_PKI" in
    yes)
      return 0
      ;;
    no)
      return 1
      ;;
    auto)
      [[ "$SWARM_PKI_MOUNT_TYPE" == "bind" ]]
      return
      ;;
  esac
  return 1
}

should_sync_fonts() {
  case "$SYNC_FONTS" in
    yes)
      return 0
      ;;
    no)
      return 1
      ;;
    auto)
      [[ "$SHARED_CUSTOM_FONTS_MOUNT_TYPE" == "bind" ]]
      return
      ;;
  esac
  return 1
}

require_local_path() {
  local path="$1"
  local label="$2"

  if [[ ! -e "$path" ]]; then
    echo "[$HOST_NAME] 缺少本地 $label：$path" >&2
    exit 1
  fi
}

if [[ -z "${TARGETS//[[:space:]]/}" ]]; then
  echo "[$HOST_NAME] 缺少目标节点列表，请传 --targets 或设置 SWARM_SYNC_TARGETS。" >&2
  exit 1
fi

if [[ "$SYNC_ENV" == "yes" ]]; then
  require_local_path "$ENV_FILE" ".env 文件"
  if [[ -n "$ENV_BASE_FILE_PATH" ]]; then
    require_local_path "$ENV_BASE_FILE_PATH" "基础 .env 文件"
  fi
fi
if should_sync_pki; then
  require_local_path "$SWARM_PKI_LOCAL_SOURCE" "PKI 目录"
fi
if should_sync_fonts; then
  require_local_path "$SHARED_CUSTOM_FONTS_LOCAL_SOURCE" "共享字体目录"
fi

if [[ "$MODE" == "apply" ]]; then
  if ! command -v ssh >/dev/null 2>&1; then
    echo "[$HOST_NAME] 缺少 ssh，无法执行远程同步。" >&2
    exit 1
  fi
  if ! command -v rsync >/dev/null 2>&1; then
    echo "[$HOST_NAME] 缺少 rsync，无法执行远程同步。" >&2
    exit 1
  fi
fi

echo "[$HOST_NAME] 统一下发策略："
echo "[$HOST_NAME] - 文本配置 / 启动脚本 / 代理模板：Swarm configs"
echo "[$HOST_NAME] - PKI / 共享字体 / .env：sync-swarm-assets.sh"
echo

while IFS= read -r raw_target <&3; do
  [[ -n "$raw_target" ]] || continue
  target="$(resolve_target "$raw_target")"

  echo "[$HOST_NAME] 目标节点：$target"
  if [[ "$SYNC_ENV" == "yes" ]]; then
    echo "[$HOST_NAME]   ENV   $ENV_FILE -> $REMOTE_ENV_FILE"
    if [[ -n "$ENV_BASE_FILE_PATH" ]]; then
      echo "[$HOST_NAME]   BASE  $ENV_BASE_FILE_PATH -> $REMOTE_ENV_BASE_FILE"
    fi
  else
    echo "[$HOST_NAME]   ENV   已跳过"
  fi

  if should_sync_pki; then
    echo "[$HOST_NAME]   PKI   $SWARM_PKI_LOCAL_SOURCE -> $SWARM_PKI_MOUNT_SOURCE"
  else
    echo "[$HOST_NAME]   PKI   已跳过（当前不是 bind，或显式关闭）"
  fi

  if should_sync_fonts; then
    echo "[$HOST_NAME]   FONTS $SHARED_CUSTOM_FONTS_LOCAL_SOURCE -> $SHARED_CUSTOM_FONTS_MOUNT_SOURCE"
  else
    echo "[$HOST_NAME]   FONTS 已跳过（当前不是 bind，或显式关闭）"
  fi

  if [[ "$MODE" == "apply" ]]; then
    remote_dirs=()
    if [[ "$SYNC_ENV" == "yes" ]]; then
      remote_dirs+=("$(dirname "$REMOTE_ENV_FILE")")
      if [[ -n "$ENV_BASE_FILE_PATH" ]]; then
        remote_dirs+=("$(dirname "$REMOTE_ENV_BASE_FILE")")
      fi
    fi
    if should_sync_pki; then
      remote_dirs+=("$SWARM_PKI_MOUNT_SOURCE")
    fi
    if should_sync_fonts; then
      remote_dirs+=("$SHARED_CUSTOM_FONTS_MOUNT_SOURCE")
    fi

    if [[ ${#remote_dirs[@]} -gt 0 ]]; then
      remote_mkdir_p "$target" "${remote_dirs[@]}"
    fi

    if [[ "$SYNC_ENV" == "yes" ]]; then
      if [[ -n "$ENV_BASE_FILE_PATH" ]]; then
        sync_file_to_remote "$ENV_BASE_FILE_PATH" "$target" "$REMOTE_ENV_BASE_FILE"
      fi
      sync_file_to_remote "$ENV_FILE" "$target" "$REMOTE_ENV_FILE"
    fi
    if should_sync_pki; then
      sync_dir_to_remote "$SWARM_PKI_LOCAL_SOURCE" "$target" "$SWARM_PKI_MOUNT_SOURCE"
    fi
    if should_sync_fonts; then
      sync_dir_to_remote "$SHARED_CUSTOM_FONTS_LOCAL_SOURCE" "$target" "$SHARED_CUSTOM_FONTS_MOUNT_SOURCE"
    fi

    echo "[$HOST_NAME]   [DONE] $target 同步完成"
  fi

  echo
done 3< <(normalize_targets "$TARGETS")

if [[ "$MODE" == "check" ]]; then
  echo "[$HOST_NAME] [NEXT] 确认无误后执行 --apply。"
fi
