#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
FORCE_CA=0
FORCE_CERTS=0
REQUESTED_SERVICES="all"

usage() {
  cat <<'EOF'
用法：
  docker/swarm/generate-swarm-pki.sh [--env-file 文件] [--services 列表] [--force-ca] [--force-certs]

说明：
  这个脚本会在宿主机生成一套默认 Swarm PKI：
  1. 根 CA：后续你可以直接替换
  2. Java truststore：供 authverse / kafka-ui 这类 JVM 客户端直接信任根 CA
  3. 各对外入口服务证书：统一由这套 CA 签发

参数：
  --env-file FILE     环境变量文件，默认 .env.swarm
  --services LIST     要签发的服务，逗号分隔；默认 all
                      可选：
                      all,cloudreve-master,cloudreve-slave,authverse,registry,
                      pgpool,redis,minio,elasticsearch,tika,onlyoffice,kafka,kafka-ui
  --force-ca          强制重建根 CA
  --force-certs       强制重签所有目标服务证书
  -h, --help          显示帮助

生成目录结构：
  ca/ca.crt
  ca/ca.key
  ca/truststore.p12
  services/<service>/tls.crt
  services/<service>/tls.key
  services/<service>/server.key
  services/<service>/fullchain.crt
  services/<service>/haproxy.pem
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      ENV_FILE="$2"
      shift 2
      ;;
    --services)
      REQUESTED_SERVICES="$2"
      shift 2
      ;;
    --force-ca)
      FORCE_CA=1
      shift
      ;;
    --force-certs)
      FORCE_CERTS=1
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

if [[ ! -f "$ENV_FILE" ]]; then
  echo "找不到环境变量文件: $ENV_FILE" >&2
  exit 1
fi

if ! command -v openssl >/dev/null 2>&1; then
  echo "当前系统缺少 openssl，无法生成证书。" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

SWARM_PKI_MOUNT_TYPE="${SWARM_PKI_MOUNT_TYPE:-bind}"
SWARM_PKI_MOUNT_SOURCE="${SWARM_PKI_MOUNT_SOURCE:-/srv/cloudreve/pki}"
SWARM_CA_VALID_DAYS="${SWARM_CA_VALID_DAYS:-3650}"
SWARM_CERT_VALID_DAYS="${SWARM_CERT_VALID_DAYS:-825}"
SWARM_CA_COMMON_NAME="${SWARM_CA_COMMON_NAME:-Cloudreve Swarm Root CA}"
SWARM_TRUSTSTORE_PASSWORD="${SWARM_TRUSTSTORE_PASSWORD:-changeit}"

service_contains() {
  local target="$1"
  local item

  IFS=',' read -r -a _services <<<"$REQUESTED_SERVICES"
  for item in "${_services[@]}"; do
    item="${item//[[:space:]]/}"
    if [[ "$item" == "all" || "$item" == "$target" ]]; then
      return 0
    fi
  done
  return 1
}

resolve_registry_host() {
  local addr="${PRIVATE_REGISTRY_ADDR:-}"
  addr="${addr#http://}"
  addr="${addr#https://}"
  addr="${addr%%/*}"
  addr="${addr%%:*}"
  printf '%s\n' "$addr"
}

is_ip_literal() {
  [[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]
}

normalize_sans() {
  local raw="$1"
  local entry normalized
  local seen="|"
  local result=""
  local OLD_IFS="$IFS"

  raw="${raw//;/,}"
  raw="${raw// /,}"
  IFS=','
  set -- $raw
  IFS="$OLD_IFS"

  for entry in "$@"; do
    [[ -n "$entry" ]] || continue

    case "$entry" in
      DNS:*|IP:*)
        normalized="$entry"
        ;;
      *)
        if is_ip_literal "$entry"; then
          normalized="IP:$entry"
        else
          normalized="DNS:$entry"
        fi
        ;;
    esac

    if [[ "$seen" != *"|$normalized|"* ]]; then
      if [[ -n "$result" ]]; then
        result="$result $normalized"
      else
        result="$normalized"
      fi
      seen="$seen$normalized|"
    fi
  done

  printf '%s\n' "$result"
}

build_service_sans() {
  local service="$1"
  local sans=""

  case "$service" in
    cloudreve-master-proxy)
      sans="DNS:cloudreve-master-proxy,DNS:tasks.cloudreve-master-proxy,${CLOUDREVE_MASTER_TLS_EXTRA_SANS:-}"
      [[ "${CLOUDREVE_MASTER_SERVER_NAME:-_}" != "_" ]] && sans="$sans,${CLOUDREVE_MASTER_SERVER_NAME}"
      ;;
    cloudreve-slave-proxy)
      sans="DNS:cloudreve-slave-proxy,DNS:tasks.cloudreve-slave-proxy,${CLOUDREVE_SLAVE_TLS_EXTRA_SANS:-}"
      [[ "${CLOUDREVE_SLAVE_SERVER_NAME:-_}" != "_" ]] && sans="$sans,${CLOUDREVE_SLAVE_SERVER_NAME}"
      ;;
    authverse-web)
      sans="DNS:authverse-web,DNS:tasks.authverse-web,${AUTHVERSE_TLS_EXTRA_SANS:-}"
      [[ "${AUTHVERSE_SERVER_NAME:-_}" != "_" ]] && sans="$sans,${AUTHVERSE_SERVER_NAME}"
      ;;
    registry)
      sans="DNS:registry,DNS:tasks.registry,${PRIVATE_REGISTRY_TLS_EXTRA_SANS:-}"
      if [[ -n "${PRIVATE_REGISTRY_SERVER_NAME:-}" ]]; then
        sans="$sans,${PRIVATE_REGISTRY_SERVER_NAME}"
      else
        sans="$sans,$(resolve_registry_host)"
      fi
      ;;
    pgpool)
      sans="DNS:pgpool,DNS:tasks.pgpool,${PGPOOL_TLS_SERVER_NAME:-},${PGPOOL_TLS_EXTRA_SANS:-}"
      ;;
    redis-proxy)
      sans="DNS:redis-proxy,DNS:tasks.redis-proxy,${REDIS_PROXY_TLS_SERVER_NAME:-},${REDIS_PROXY_TLS_EXTRA_SANS:-}"
      ;;
    minio)
      sans="DNS:minio,DNS:tasks.minio,${MINIO_PUBLIC_SERVER_NAME:-},${MINIO_PUBLIC_TLS_EXTRA_SANS:-}"
      ;;
    elasticsearch)
      sans="DNS:elasticsearch,DNS:tasks.elasticsearch,${ELASTICSEARCH_PUBLIC_SERVER_NAME:-},${ELASTICSEARCH_PUBLIC_TLS_EXTRA_SANS:-}"
      ;;
    tika-proxy)
      sans="DNS:tika-proxy,DNS:tasks.tika-proxy,${TIKA_SERVER_NAME:-},${TIKA_TLS_EXTRA_SANS:-}"
      ;;
    onlyoffice-public)
      sans="DNS:onlyoffice-public,DNS:tasks.onlyoffice-public,${ONLYOFFICE_SERVER_NAME:-},${ONLYOFFICE_TLS_EXTRA_SANS:-}"
      ;;
    kafka)
      sans="DNS:kafka,DNS:tasks.kafka,${KAFKA_TLS_SERVER_NAME:-},${KAFKA_TLS_EXTRA_SANS:-}"
      ;;
    kafka-ui-public)
      sans="DNS:kafka-ui-public,DNS:tasks.kafka-ui-public,${KAFKA_UI_SERVER_NAME:-},${KAFKA_UI_TLS_EXTRA_SANS:-}"
      ;;
    *)
      echo "不支持的证书服务名: $service" >&2
      exit 1
      ;;
  esac

  sans="$sans,DNS:localhost,IP:127.0.0.1"
  normalize_sans "$sans"
}

issue_service_cert() {
  local pki_root="$1"
  local service="$2"
  local cert_dir="$pki_root/services/$service"
  local ca_dir="$pki_root/ca"
  local key_file="$cert_dir/tls.key"
  local server_key_file="$cert_dir/server.key"
  local csr_file="$cert_dir/tls.csr"
  local crt_file="$cert_dir/tls.crt"
  local fullchain_file="$cert_dir/fullchain.crt"
  local pem_file="$cert_dir/haproxy.pem"
  local ext_file="$cert_dir/openssl.ext"
  local common_name
  local san_string

  mkdir -p "$cert_dir"
  san_string="$(build_service_sans "$service")"
  common_name="${san_string%% *}"
  common_name="${common_name#DNS:}"
  common_name="${common_name#IP:}"

  if [[ "$FORCE_CERTS" -eq 0 && -f "$key_file" && -f "$crt_file" ]]; then
    cat "$crt_file" "$ca_dir/ca.crt" >"$fullchain_file"
    cat "$key_file" "$crt_file" "$ca_dir/ca.crt" >"$pem_file"
    cp "$key_file" "$server_key_file"
    chmod 0600 "$key_file"
    chmod 0644 "$server_key_file" "$crt_file" "$fullchain_file" "$pem_file"
    echo "[skip] $service 证书已存在"
    return 0
  fi

  cat >"$ext_file" <<EOF
[v3_req]
basicConstraints = CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = ${san_string// /,}
EOF

  openssl genrsa -out "$key_file" 4096 >/dev/null 2>&1
  openssl req -new -key "$key_file" -subj "/CN=$common_name" -out "$csr_file" >/dev/null 2>&1
  openssl x509 -req \
    -in "$csr_file" \
    -CA "$ca_dir/ca.crt" \
    -CAkey "$ca_dir/ca.key" \
    -CAcreateserial \
    -out "$crt_file" \
    -days "$SWARM_CERT_VALID_DAYS" \
    -sha256 \
    -extfile "$ext_file" \
    -extensions v3_req >/dev/null 2>&1

  cat "$crt_file" "$ca_dir/ca.crt" >"$fullchain_file"
  cat "$key_file" "$crt_file" "$ca_dir/ca.crt" >"$pem_file"
  cp "$key_file" "$server_key_file"

  chmod 0600 "$key_file"
  chmod 0644 "$server_key_file"
  chmod 0644 "$crt_file" "$fullchain_file" "$pem_file"
  rm -f "$csr_file" "$ext_file"
  echo "[done] 已签发 $service"
}

generate_ca() {
  local pki_root="$1"
  local ca_dir="$pki_root/ca"

  mkdir -p "$ca_dir" "$pki_root/services"

  if [[ "$FORCE_CA" -eq 0 && -f "$ca_dir/ca.key" && -f "$ca_dir/ca.crt" ]]; then
    echo "[skip] 根 CA 已存在"
    return 0
  fi

  rm -f "$ca_dir/ca.key" "$ca_dir/ca.crt" "$ca_dir/ca.srl"
  openssl req -x509 -newkey rsa:4096 -sha256 -nodes \
    -days "$SWARM_CA_VALID_DAYS" \
    -subj "/CN=$SWARM_CA_COMMON_NAME" \
    -keyout "$ca_dir/ca.key" \
    -out "$ca_dir/ca.crt" >/dev/null 2>&1

  chmod 0600 "$ca_dir/ca.key"
  chmod 0644 "$ca_dir/ca.crt"
  echo "[done] 已生成根 CA"
}

generate_truststore() {
  local pki_root="$1"
  local ca_dir="$pki_root/ca"
  local truststore_file="$ca_dir/truststore.p12"

  if [[ "$FORCE_CA" -eq 0 && -f "$truststore_file" ]]; then
    echo "[skip] Java truststore 已存在"
    return 0
  fi

  rm -f "$truststore_file"
  if command -v keytool >/dev/null 2>&1; then
    keytool \
      -importcert \
      -noprompt \
      -alias swarm-root-ca \
      -file "$ca_dir/ca.crt" \
      -keystore "$truststore_file" \
      -storetype PKCS12 \
      -storepass "$SWARM_TRUSTSTORE_PASSWORD" >/dev/null 2>&1
  else
    docker run --rm \
      --entrypoint keytool \
      -e SWARM_TRUSTSTORE_PASSWORD="$SWARM_TRUSTSTORE_PASSWORD" \
      -v "$ca_dir:/work" \
      eclipse-temurin:21-jre-alpine \
      -importcert \
      -noprompt \
      -alias swarm-root-ca \
      -file /work/ca.crt \
      -keystore /work/truststore.p12 \
      -storetype PKCS12 \
      -storepass "$SWARM_TRUSTSTORE_PASSWORD" >/dev/null 2>&1
  fi

  chmod 0644 "$truststore_file"
  echo "[done] 已生成 Java truststore"
}

sync_to_volume() {
  local stage_dir="$1"
  local volume_name="$2"

  docker run --rm \
    -v "$volume_name:/dest" \
    -v "$stage_dir:/src:ro" \
    nginx:1.27-alpine \
    sh -lc 'mkdir -p /dest && cp -a /src/. /dest/'
}

build_target_services() {
  local -a services=()

  service_contains cloudreve-master && services+=(cloudreve-master-proxy)
  service_contains cloudreve-slave && services+=(cloudreve-slave-proxy)
  service_contains authverse && services+=(authverse-web)
  service_contains registry && services+=(registry)
  service_contains pgpool && services+=(pgpool)
  service_contains redis && services+=(redis-proxy)
  service_contains minio && services+=(minio)
  service_contains elasticsearch && services+=(elasticsearch)
  service_contains tika && services+=(tika-proxy)
  service_contains onlyoffice && services+=(onlyoffice-public)
  service_contains kafka && services+=(kafka)
  service_contains kafka-ui && services+=(kafka-ui-public)

  printf '%s\n' "${services[@]}"
}

if [[ "$SWARM_PKI_MOUNT_TYPE" == "bind" ]]; then
  PKI_ROOT="$SWARM_PKI_MOUNT_SOURCE"
  mkdir -p "$PKI_ROOT"
  WORK_ROOT="$PKI_ROOT"
elif [[ "$SWARM_PKI_MOUNT_TYPE" == "volume" ]]; then
  PKI_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/swarm-pki.XXXXXX")"
  WORK_ROOT="$PKI_ROOT"
else
  echo "不支持的 SWARM_PKI_MOUNT_TYPE: $SWARM_PKI_MOUNT_TYPE" >&2
  exit 1
fi

generate_ca "$WORK_ROOT"
generate_truststore "$WORK_ROOT"

while IFS= read -r service; do
  [[ -n "$service" ]] || continue
  issue_service_cert "$WORK_ROOT" "$service"
done < <(build_target_services)

if [[ "$SWARM_PKI_MOUNT_TYPE" == "volume" ]]; then
  sync_to_volume "$WORK_ROOT" "$SWARM_PKI_MOUNT_SOURCE"
  rm -rf "$WORK_ROOT"
  echo
  echo "已同步到 Docker volume: $SWARM_PKI_MOUNT_SOURCE"
fi

echo
echo "PKI 目录已准备完成："
if [[ "$SWARM_PKI_MOUNT_TYPE" == "bind" ]]; then
  echo "  $SWARM_PKI_MOUNT_SOURCE"
else
  echo "  Docker volume: $SWARM_PKI_MOUNT_SOURCE"
fi
