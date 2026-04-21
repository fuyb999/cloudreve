#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
MODE="apply"
SERVICES="all"
HOST_NAME="$(hostname -s 2>/dev/null || hostname)"

usage() {
  cat <<'EOF'
用法：
  sudo docker/swarm/prepare-bind-paths.sh [--env-file 文件] [--check] [--apply] [--services 列表]

说明：
  这个脚本用于在宿主机上预创建 Swarm bind 路径，并修正常见权限。
  它不会替你执行 `docker stack deploy`，只负责准备宿主机目录。
  `minio` / `elasticsearch` / `kafka` 会自动优先处理各自的集群 bind 路径；
  如果没有配置集群 bind，再回退到单节点 bind 路径。

参数：
  --env-file FILE       读取的环境变量文件，默认是 .env.swarm
  --check               只检查，不改动
  --apply               执行创建和修正，默认就是 apply
  --services LIST       只处理指定服务，逗号分隔
                        可选：all,pg,redis,cloudreve,minio,elasticsearch,kafka,tika,onlyoffice,registry,authverse
  -h, --help            显示帮助

示例：
  sudo docker/swarm/prepare-bind-paths.sh --check
  sudo docker/swarm/prepare-bind-paths.sh --services pg,redis
  sudo docker/swarm/prepare-bind-paths.sh --services minio,elasticsearch,kafka
  sudo docker/swarm/prepare-bind-paths.sh --services tika,onlyoffice,registry,authverse
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
    --services)
      SERVICES="$2"
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

if [[ "$MODE" == "apply" && "${EUID:-$(id -u)}" -ne 0 ]]; then
  echo "[$HOST_NAME] 请使用 root 或 sudo 运行 apply 模式。" >&2
  exit 1
fi

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
  fi
}

# 兼容旧变量。
map_legacy_mount_var "PG_1_DATA_PATH" "PG_1_DATA_MOUNT_TYPE" "PG_1_DATA_MOUNT_SOURCE"
map_legacy_mount_var "PG_2_DATA_PATH" "PG_2_DATA_MOUNT_TYPE" "PG_2_DATA_MOUNT_SOURCE"
map_legacy_mount_var "PG_3_DATA_PATH" "PG_3_DATA_MOUNT_TYPE" "PG_3_DATA_MOUNT_SOURCE"
map_legacy_mount_var "REDIS_1_DATA_PATH" "REDIS_1_DATA_MOUNT_TYPE" "REDIS_1_DATA_MOUNT_SOURCE"
map_legacy_mount_var "REDIS_2_DATA_PATH" "REDIS_2_DATA_MOUNT_TYPE" "REDIS_2_DATA_MOUNT_SOURCE"
map_legacy_mount_var "REDIS_3_DATA_PATH" "REDIS_3_DATA_MOUNT_TYPE" "REDIS_3_DATA_MOUNT_SOURCE"
if [[ -n "${TIKA_CUSTOM_FONTS_HOST_PATH:-}" && -z "${TIKA_CUSTOM_FONTS_MOUNT_SOURCE:-}" ]]; then
  TIKA_CUSTOM_FONTS_MOUNT_TYPE="${TIKA_CUSTOM_FONTS_MOUNT_TYPE:-bind}"
  TIKA_CUSTOM_FONTS_MOUNT_SOURCE="$TIKA_CUSTOM_FONTS_HOST_PATH"
fi
if [[ -n "${SHARED_CUSTOM_FONTS_HOST_PATH:-}" && -z "${SHARED_CUSTOM_FONTS_MOUNT_SOURCE:-}" ]]; then
  SHARED_CUSTOM_FONTS_MOUNT_TYPE="${SHARED_CUSTOM_FONTS_MOUNT_TYPE:-bind}"
  SHARED_CUSTOM_FONTS_MOUNT_SOURCE="$SHARED_CUSTOM_FONTS_HOST_PATH"
fi
if [[ -n "${SHARED_CUSTOM_FONTS_MOUNT_SOURCE:-}" ]]; then
  TIKA_CUSTOM_FONTS_MOUNT_TYPE="${TIKA_CUSTOM_FONTS_MOUNT_TYPE:-${SHARED_CUSTOM_FONTS_MOUNT_TYPE:-bind}}"
  TIKA_CUSTOM_FONTS_MOUNT_SOURCE="${TIKA_CUSTOM_FONTS_MOUNT_SOURCE:-$SHARED_CUSTOM_FONTS_MOUNT_SOURCE}"
  ONLYOFFICE_CUSTOM_FONTS_MOUNT_TYPE="${ONLYOFFICE_CUSTOM_FONTS_MOUNT_TYPE:-${SHARED_CUSTOM_FONTS_MOUNT_TYPE:-bind}}"
  ONLYOFFICE_CUSTOM_FONTS_MOUNT_SOURCE="${ONLYOFFICE_CUSTOM_FONTS_MOUNT_SOURCE:-$SHARED_CUSTOM_FONTS_MOUNT_SOURCE}"
fi
SWARM_PKI_MOUNT_TYPE="${SWARM_PKI_MOUNT_TYPE:-bind}"
SWARM_PKI_MOUNT_SOURCE="${SWARM_PKI_MOUNT_SOURCE:-/srv/cloudreve/pki}"
SWARM_PKI_LOCAL_SOURCE="${SWARM_PKI_LOCAL_SOURCE:-$SWARM_PKI_MOUNT_SOURCE}"
SHARED_CUSTOM_FONTS_LOCAL_SOURCE="${SHARED_CUSTOM_FONTS_LOCAL_SOURCE:-${SHARED_CUSTOM_FONTS_MOUNT_SOURCE:-}}"

service_list_contains() {
  local name="$1"
  local item
  IFS=',' read -r -a _selected_services <<<"$SERVICES"
  for item in "${_selected_services[@]}"; do
    item="${item//[[:space:]]/}"
    if [[ "$item" == "$name" || "$item" == "all" ]]; then
      return 0
    fi
  done
  return 1
}

validate_services() {
  local item
  IFS=',' read -r -a _selected_services <<<"$SERVICES"
  for item in "${_selected_services[@]}"; do
    item="${item//[[:space:]]/}"
    case "$item" in
      all|pg|redis|cloudreve|minio|elasticsearch|kafka|tika|onlyoffice|registry|authverse)
        ;;
      *)
        echo "[$HOST_NAME] 不支持的服务名: $item" >&2
        usage >&2
        exit 1
        ;;
    esac
  done
}

cluster_bind_enabled() {
  local prefix="$1"
  local last_index="$2"
  local index type_var

  for ((index = 1; index <= last_index; index++)); do
    type_var="${prefix}_${index}_DATA_MOUNT_TYPE"
    if [[ "${!type_var:-volume}" == "bind" ]]; then
      return 0
    fi
  done

  return 1
}

service_enabled() {
  local name="$1"
  if [[ "$SERVICES" == "all" ]]; then
    return 0
  fi
  if service_list_contains "$name"; then
    return 0
  fi
  return 1
}

shared_assets_enabled() {
  service_enabled pg && return 0
  service_enabled redis && return 0
  service_enabled cloudreve && return 0
  service_enabled minio && return 0
  service_enabled elasticsearch && return 0
  service_enabled kafka && return 0
  service_enabled tika && return 0
  service_enabled onlyoffice && return 0
  service_enabled registry && return 0
  service_enabled authverse && return 0
  return 1
}

is_absolute_path() {
  [[ "$1" == /* ]]
}

stat_owner() {
  local path="$1"
  if stat -c '%u:%g' "$path" >/dev/null 2>&1; then
    stat -c '%u:%g' "$path"
  else
    stat -f '%u:%g' "$path"
  fi
}

entries=()

validate_services

add_entry() {
  local label="$1"
  local path="$2"
  local owner="$3"
  local dir_mode="$4"
  local action="$5"

  [[ -n "$path" ]] || return 0
  entries+=("${label}|${path}|${owner}|${dir_mode}|${action}")
}

collect_entries() {
  if shared_assets_enabled; then
    if [[ "${SWARM_PKI_MOUNT_TYPE:-bind}" == "bind" ]]; then
      add_entry "SWARM_PKI_MOUNT_SOURCE" "${SWARM_PKI_MOUNT_SOURCE:-}" "" "0755" "mkdir_only"
      add_entry "SWARM_PKI_CA_MOUNT_SOURCE" "${SWARM_PKI_MOUNT_SOURCE:-}/ca" "" "0755" "mkdir_only"
      add_entry "SWARM_PKI_SERVICES_MOUNT_SOURCE" "${SWARM_PKI_MOUNT_SOURCE:-}/services" "" "0755" "mkdir_only"
    fi

    if [[ "${SHARED_CUSTOM_FONTS_MOUNT_TYPE:-volume}" == "bind" ]]; then
      add_entry "SHARED_CUSTOM_FONTS_MOUNT_SOURCE" "${SHARED_CUSTOM_FONTS_MOUNT_SOURCE:-}" "" "0755" "readable_recursive"
    fi
  fi

  if service_enabled pg; then
    if [[ "${PG_1_DATA_MOUNT_TYPE:-bind}" == "bind" ]]; then
      add_entry "PG_1_DATA_MOUNT_SOURCE" "${PG_1_DATA_MOUNT_SOURCE:-}" "1001:1001" "0755" "chown_recursive"
    fi
    if [[ "${PG_2_DATA_MOUNT_TYPE:-bind}" == "bind" ]]; then
      add_entry "PG_2_DATA_MOUNT_SOURCE" "${PG_2_DATA_MOUNT_SOURCE:-}" "1001:1001" "0755" "chown_recursive"
    fi
    if [[ "${PG_3_DATA_MOUNT_TYPE:-bind}" == "bind" ]]; then
      add_entry "PG_3_DATA_MOUNT_SOURCE" "${PG_3_DATA_MOUNT_SOURCE:-}" "1001:1001" "0755" "chown_recursive"
    fi
  fi

  if service_enabled redis; then
    if [[ "${REDIS_1_DATA_MOUNT_TYPE:-bind}" == "bind" ]]; then
      add_entry "REDIS_1_DATA_MOUNT_SOURCE" "${REDIS_1_DATA_MOUNT_SOURCE:-}" "1001:1001" "0755" "chown_recursive"
    fi
    if [[ "${REDIS_2_DATA_MOUNT_TYPE:-bind}" == "bind" ]]; then
      add_entry "REDIS_2_DATA_MOUNT_SOURCE" "${REDIS_2_DATA_MOUNT_SOURCE:-}" "1001:1001" "0755" "chown_recursive"
    fi
    if [[ "${REDIS_3_DATA_MOUNT_TYPE:-bind}" == "bind" ]]; then
      add_entry "REDIS_3_DATA_MOUNT_SOURCE" "${REDIS_3_DATA_MOUNT_SOURCE:-}" "1001:1001" "0755" "chown_recursive"
    fi
  fi

  if service_enabled cloudreve; then
    if [[ "${CLOUDREVE_MASTER_RUNTIME_MOUNT_TYPE:-volume}" == "bind" ]]; then
      add_entry "CLOUDREVE_MASTER_RUNTIME_MOUNT_SOURCE" "${CLOUDREVE_MASTER_RUNTIME_MOUNT_SOURCE:-}" "" "0755" "mkdir_only"
    fi
    if [[ "${CLOUDREVE_SLAVE_RUNTIME_MOUNT_TYPE:-volume}" == "bind" ]]; then
      add_entry "CLOUDREVE_SLAVE_RUNTIME_MOUNT_SOURCE" "${CLOUDREVE_SLAVE_RUNTIME_MOUNT_SOURCE:-}" "" "0755" "mkdir_only"
    fi
  fi

  if service_enabled minio; then
    if cluster_bind_enabled "MINIO" 4; then
      if [[ "${MINIO_1_DATA_MOUNT_TYPE:-volume}" == "bind" ]]; then
        add_entry "MINIO_1_DATA_MOUNT_SOURCE" "${MINIO_1_DATA_MOUNT_SOURCE:-}" "1001:1001" "0755" "chown_recursive"
      fi
      if [[ "${MINIO_2_DATA_MOUNT_TYPE:-volume}" == "bind" ]]; then
        add_entry "MINIO_2_DATA_MOUNT_SOURCE" "${MINIO_2_DATA_MOUNT_SOURCE:-}" "1001:1001" "0755" "chown_recursive"
      fi
      if [[ "${MINIO_3_DATA_MOUNT_TYPE:-volume}" == "bind" ]]; then
        add_entry "MINIO_3_DATA_MOUNT_SOURCE" "${MINIO_3_DATA_MOUNT_SOURCE:-}" "1001:1001" "0755" "chown_recursive"
      fi
      if [[ "${MINIO_4_DATA_MOUNT_TYPE:-volume}" == "bind" ]]; then
        add_entry "MINIO_4_DATA_MOUNT_SOURCE" "${MINIO_4_DATA_MOUNT_SOURCE:-}" "1001:1001" "0755" "chown_recursive"
      fi
    elif [[ "${MINIO_DATA_MOUNT_TYPE:-volume}" == "bind" ]]; then
      add_entry "MINIO_DATA_MOUNT_SOURCE" "${MINIO_DATA_MOUNT_SOURCE:-}" "1001:1001" "0755" "chown_recursive"
    fi
  fi

  if service_enabled elasticsearch; then
    if cluster_bind_enabled "ELASTICSEARCH" 3; then
      if [[ "${ELASTICSEARCH_1_DATA_MOUNT_TYPE:-volume}" == "bind" ]]; then
        add_entry "ELASTICSEARCH_1_DATA_MOUNT_SOURCE" "${ELASTICSEARCH_1_DATA_MOUNT_SOURCE:-}" "1000:0" "0775" "chown_recursive"
      fi
      if [[ "${ELASTICSEARCH_2_DATA_MOUNT_TYPE:-volume}" == "bind" ]]; then
        add_entry "ELASTICSEARCH_2_DATA_MOUNT_SOURCE" "${ELASTICSEARCH_2_DATA_MOUNT_SOURCE:-}" "1000:0" "0775" "chown_recursive"
      fi
      if [[ "${ELASTICSEARCH_3_DATA_MOUNT_TYPE:-volume}" == "bind" ]]; then
        add_entry "ELASTICSEARCH_3_DATA_MOUNT_SOURCE" "${ELASTICSEARCH_3_DATA_MOUNT_SOURCE:-}" "1000:0" "0775" "chown_recursive"
      fi
    elif [[ "${ELASTICSEARCH_DATA_MOUNT_TYPE:-volume}" == "bind" ]]; then
      add_entry "ELASTICSEARCH_DATA_MOUNT_SOURCE" "${ELASTICSEARCH_DATA_MOUNT_SOURCE:-}" "1000:0" "0775" "chown_recursive"
    fi
  fi

  if service_enabled kafka; then
    if cluster_bind_enabled "KAFKA" 3; then
      if [[ "${KAFKA_1_DATA_MOUNT_TYPE:-volume}" == "bind" ]]; then
        add_entry "KAFKA_1_DATA_MOUNT_SOURCE" "${KAFKA_1_DATA_MOUNT_SOURCE:-}" "1000:1000" "0775" "chown_recursive"
      fi
      if [[ "${KAFKA_2_DATA_MOUNT_TYPE:-volume}" == "bind" ]]; then
        add_entry "KAFKA_2_DATA_MOUNT_SOURCE" "${KAFKA_2_DATA_MOUNT_SOURCE:-}" "1000:1000" "0775" "chown_recursive"
      fi
      if [[ "${KAFKA_3_DATA_MOUNT_TYPE:-volume}" == "bind" ]]; then
        add_entry "KAFKA_3_DATA_MOUNT_SOURCE" "${KAFKA_3_DATA_MOUNT_SOURCE:-}" "1000:1000" "0775" "chown_recursive"
      fi
    elif [[ "${KAFKA_DATA_MOUNT_TYPE:-volume}" == "bind" ]]; then
      add_entry "KAFKA_DATA_MOUNT_SOURCE" "${KAFKA_DATA_MOUNT_SOURCE:-}" "1000:1000" "0775" "chown_recursive"
    fi
  fi

  if service_enabled tika && [[ "${TIKA_CUSTOM_FONTS_MOUNT_TYPE:-volume}" == "bind" ]]; then
    add_entry "TIKA_CUSTOM_FONTS_MOUNT_SOURCE" "${TIKA_CUSTOM_FONTS_MOUNT_SOURCE:-}" "" "0755" "readable_recursive"
  fi

  if service_enabled onlyoffice; then
    if [[ "${ONLYOFFICE_DATA_MOUNT_TYPE:-volume}" == "bind" ]]; then
      add_entry "ONLYOFFICE_DATA_MOUNT_SOURCE" "${ONLYOFFICE_DATA_MOUNT_SOURCE:-}" "" "0755" "mkdir_only"
    fi
    if [[ "${ONLYOFFICE_LIB_MOUNT_TYPE:-volume}" == "bind" ]]; then
      add_entry "ONLYOFFICE_LIB_MOUNT_SOURCE" "${ONLYOFFICE_LIB_MOUNT_SOURCE:-}" "" "0755" "mkdir_only"
    fi
    if [[ "${ONLYOFFICE_LOG_MOUNT_TYPE:-volume}" == "bind" ]]; then
      add_entry "ONLYOFFICE_LOG_MOUNT_SOURCE" "${ONLYOFFICE_LOG_MOUNT_SOURCE:-}" "" "0755" "mkdir_only"
    fi
    if [[ "${ONLYOFFICE_CUSTOM_FONTS_MOUNT_TYPE:-volume}" == "bind" ]]; then
      add_entry "ONLYOFFICE_CUSTOM_FONTS_MOUNT_SOURCE" "${ONLYOFFICE_CUSTOM_FONTS_MOUNT_SOURCE:-}" "" "0755" "readable_recursive"
    fi
    if [[ "${ONLYOFFICE_RABBITMQ_MOUNT_TYPE:-volume}" == "bind" ]]; then
      add_entry "ONLYOFFICE_RABBITMQ_MOUNT_SOURCE" "${ONLYOFFICE_RABBITMQ_MOUNT_SOURCE:-}" "999:999" "0775" "chown_recursive"
    fi
  fi

  if service_enabled registry && [[ "${PRIVATE_REGISTRY_DATA_MOUNT_TYPE:-volume}" == "bind" ]]; then
    add_entry "PRIVATE_REGISTRY_DATA_MOUNT_SOURCE" "${PRIVATE_REGISTRY_DATA_MOUNT_SOURCE:-}" "" "0755" "mkdir_only"
  fi

  if service_enabled authverse && [[ "${AUTHVERSE_OIDC_KEYS_MOUNT_TYPE:-volume}" == "bind" ]]; then
    add_entry "AUTHVERSE_OIDC_KEYS_MOUNT_SOURCE" "${AUTHVERSE_OIDC_KEYS_MOUNT_SOURCE:-}" "" "0755" "readable_recursive"
  fi
}

process_entry() {
  local entry="$1"
  local label path owner dir_mode action
  IFS='|' read -r label path owner dir_mode action <<<"$entry"

  if ! is_absolute_path "$path"; then
    echo "[$HOST_NAME] [ERROR] $label 不是绝对路径: $path" >&2
    return 1
  fi

  if [[ "$MODE" == "check" ]]; then
    if [[ -d "$path" ]]; then
      echo "[$HOST_NAME] [OK] $label -> $path (owner=$(stat_owner "$path"))"
    else
      echo "[$HOST_NAME] [MISSING] $label -> $path"
    fi
    return 0
  fi

  mkdir -p "$path"
  chmod "$dir_mode" "$path"

  case "$action" in
    chown_recursive)
      chown -R "$owner" "$path"
      ;;
    readable_recursive)
      chmod -R a+rX "$path"
      ;;
    mkdir_only)
      ;;
    *)
      echo "[$HOST_NAME] [ERROR] 未知 action: $action" >&2
      return 1
      ;;
  esac

  echo "[$HOST_NAME] [DONE] $label -> $path"
}

collect_entries

if [[ "${#entries[@]}" -eq 0 ]]; then
  echo "[$HOST_NAME] 当前选择的服务没有需要处理的 bind 路径。"
  exit 0
fi

echo "[$HOST_NAME] 开始处理 bind 路径，模式=$MODE，服务=$SERVICES"

for entry in "${entries[@]}"; do
  process_entry "$entry"
done
