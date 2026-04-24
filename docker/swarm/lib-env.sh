#!/usr/bin/env bash

resolve_swarm_env_path() {
  local path="$1"
  local base_dir="${2:-$PWD}"

  if [[ "$path" == /* ]]; then
    printf '%s\n' "$path"
  else
    printf '%s/%s\n' "$base_dir" "$path"
  fi
}

extract_env_base_file() {
  local env_file="$1"
  local line value

  while IFS= read -r line || [[ -n "$line" ]]; do
    case "$line" in
      ENV_BASE_FILE=*|[[:space:]]ENV_BASE_FILE=*)
        value="${line#*=}"
        value="${value%%#*}"
        value="${value#"${value%%[![:space:]]*}"}"
        value="${value%"${value##*[![:space:]]}"}"
        value="${value%\"}"
        value="${value#\"}"
        value="${value%\'}"
        value="${value#\'}"
        printf '%s\n' "$value"
        ;;
    esac
  done <"$env_file" | tail -n1
}

load_swarm_env() {
  local env_file="$1"
  local env_dir
  local env_base_file="${ENV_BASE_FILE:-}"
  local had_allexport=0

  if [[ ! -f "$env_file" ]]; then
    echo "找不到环境变量文件: $env_file" >&2
    return 1
  fi

  env_file="$(cd "$(dirname "$env_file")" && pwd)/$(basename "$env_file")"
  env_dir="$(dirname "$env_file")"

  if [[ -z "$env_base_file" ]]; then
    env_base_file="$(extract_env_base_file "$env_file")"
  fi
  if [[ -n "$env_base_file" ]]; then
    env_base_file="$(resolve_swarm_env_path "$env_base_file" "$env_dir")"
    if [[ ! -f "$env_base_file" ]]; then
      echo "找不到 ENV_BASE_FILE 指向的基础环境变量文件: $env_base_file" >&2
      return 1
    fi
  fi

  case "$-" in
    *a*) had_allexport=1 ;;
  esac

  set -a
  if [[ -n "$env_base_file" ]]; then
    # shellcheck disable=SC1090
    source "$env_base_file"
  fi
  # shellcheck disable=SC1090
  source "$env_file"

  if [[ "$had_allexport" -eq 0 ]]; then
    set +a
  fi
}
