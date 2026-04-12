#!/usr/bin/env bash

set -euo pipefail

MC_BIN="${MC_BIN:-/opt/bitnami/minio-client/bin/mc}"
MC_CONFIG_DIR="${MC_CONFIG_DIR:-/tmp/.mc}"
MINIO_ENDPOINT="${MINIO_ENDPOINT:-http://minio:9000}"
MINIO_HEALTH_ENDPOINT="${MINIO_HEALTH_ENDPOINT:-${MINIO_ENDPOINT%/}/minio/health/live}"
MINIO_CA_FILE="${MINIO_CA_FILE:-}"
MINIO_ROOT_USER="${MINIO_ROOT_USER:?set MINIO_ROOT_USER}"
MINIO_ROOT_PASSWORD="${MINIO_ROOT_PASSWORD:?set MINIO_ROOT_PASSWORD}"
MINIO_DEFAULT_BUCKETS="${MINIO_DEFAULT_BUCKETS:-}"
MINIO_ALIAS="${MINIO_ALIAS:-local}"
MINIO_INIT_INTERVAL_SECONDS="${MINIO_INIT_INTERVAL_SECONDS:-30}"

mkdir -p "${MC_CONFIG_DIR}"

CURL_ARGS=(-fsS)
if [[ -n "${MINIO_CA_FILE}" && -f "${MINIO_CA_FILE}" ]]; then
  mkdir -p "${MC_CONFIG_DIR}/certs/CAs"
  cp -f "${MINIO_CA_FILE}" "${MC_CONFIG_DIR}/certs/CAs/$(basename "${MINIO_CA_FILE}")"
  CURL_ARGS+=(--cacert "${MINIO_CA_FILE}")
fi

mc_cmd() {
  "${MC_BIN}" --config-dir "${MC_CONFIG_DIR}" "$@"
}

ensure_buckets() {
  while IFS= read -r bucket; do
    bucket="$(echo "${bucket}" | xargs)"
    [[ -n "${bucket}" ]] || continue
    mc_cmd mb --ignore-existing "${MINIO_ALIAS}/${bucket}" >/dev/null
  done < <(printf '%s' "${MINIO_DEFAULT_BUCKETS}" | tr ',;' '\n')
}

while true; do
  if curl "${CURL_ARGS[@]}" "${MINIO_HEALTH_ENDPOINT}" >/dev/null 2>&1; then
    if mc_cmd alias set "${MINIO_ALIAS}" "${MINIO_ENDPOINT}" "${MINIO_ROOT_USER}" "${MINIO_ROOT_PASSWORD}" >/dev/null 2>&1; then
      ensure_buckets
    fi
  fi

  sleep "${MINIO_INIT_INTERVAL_SECONDS}"
done
