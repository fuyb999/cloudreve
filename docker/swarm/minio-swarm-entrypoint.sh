#!/usr/bin/env bash

set -euo pipefail

# 复用 Bitnami 镜像里的环境默认值，但不再执行它的 setup.sh，
# 避免分布式模式下额外注入的 MINIO_* 环境变量互相校验失败。
. /opt/bitnami/scripts/minio-env.sh

MINIO_EXEC="${MINIO_EXEC:-$(command -v minio)}"
MINIO_SCHEME="${MINIO_SCHEME:-http}"
MINIO_DATA_DIR="${MINIO_DATA_DIR:-/bitnami/minio/data}"
MINIO_CERTS_DIR="${MINIO_CERTS_DIR:-/certs}"
MINIO_API_PORT_NUMBER="${MINIO_API_PORT_NUMBER:-9000}"
MINIO_CONSOLE_PORT_NUMBER="${MINIO_CONSOLE_PORT_NUMBER:-9001}"
MINIO_DISTRIBUTED_MODE_ENABLED="${MINIO_DISTRIBUTED_MODE_ENABLED:-no}"
MINIO_DISTRIBUTED_NODES="${MINIO_DISTRIBUTED_NODES:-}"

mkdir -p "${MINIO_DATA_DIR}"

args=(
  server
  --certs-dir "${MINIO_CERTS_DIR}"
  --console-address ":${MINIO_CONSOLE_PORT_NUMBER}"
  --address ":${MINIO_API_PORT_NUMBER}"
)

if [[ "${MINIO_DISTRIBUTED_MODE_ENABLED}" == "yes" ]]; then
  if [[ -z "${MINIO_DISTRIBUTED_NODES}" ]]; then
    echo "[minio-swarm-entrypoint] MINIO_DISTRIBUTED_MODE_ENABLED=yes 但 MINIO_DISTRIBUTED_NODES 为空" >&2
    exit 1
  fi

  read -r -a nodes <<< "$(tr ',;' ' ' <<< "${MINIO_DISTRIBUTED_NODES}")"
  for node in "${nodes[@]}"; do
    [[ -n "${node}" ]] || continue

    case "${node}" in
      http://*|https://*)
        args+=("${node}")
        ;;
      */*)
        args+=("${MINIO_SCHEME}://${node}")
        ;;
      *)
        if [[ "${MINIO_DATA_DIR}" == /* ]]; then
          args+=("${MINIO_SCHEME}://${node}:${MINIO_API_PORT_NUMBER}${MINIO_DATA_DIR}")
        else
          args+=("${MINIO_SCHEME}://${node}:${MINIO_API_PORT_NUMBER}/${MINIO_DATA_DIR}")
        fi
        ;;
    esac
  done
else
  args+=("${MINIO_DATA_DIR}")
fi

exec "${MINIO_EXEC}" "${args[@]}" "$@"
