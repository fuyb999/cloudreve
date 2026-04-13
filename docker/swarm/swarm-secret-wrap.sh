#!/bin/sh

set -eu

SECRETS_DIR="${SWARM_SECRETS_DIR:-/run/secrets}"
SECRET_MAPPINGS="${SWARM_SECRET_ENV_MAPPINGS:-}"

if [ "$#" -eq 0 ]; then
  echo "[swarm-secret-wrap] 缺少要执行的原始启动命令。" >&2
  exit 1
fi

if [ -n "$SECRET_MAPPINGS" ]; then
  OLD_IFS="$IFS"
  IFS=','
  for mapping in $SECRET_MAPPINGS; do
    [ -n "$mapping" ] || continue

    env_name=${mapping%%=*}
    secret_target=${mapping#*=}

    if [ "$env_name" = "$mapping" ] || [ -z "$env_name" ] || [ -z "$secret_target" ]; then
      echo "[swarm-secret-wrap] 非法映射: $mapping，格式必须是 ENV_NAME=secret_target" >&2
      exit 1
    fi

    current_value="$(printenv "$env_name" 2>/dev/null || true)"
    if [ -n "$current_value" ]; then
      continue
    fi

    secret_file="$SECRETS_DIR/$secret_target"
    if [ ! -f "$secret_file" ]; then
      echo "[swarm-secret-wrap] 找不到 secret 文件: $secret_file" >&2
      exit 1
    fi

    secret_value="$(cat "$secret_file")"
    set -- "${env_name}=${secret_value}" "$@"
  done
  IFS="$OLD_IFS"
fi

exec env "$@"
