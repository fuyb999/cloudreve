#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT_DIR/docker/swarm/lib-env.sh"
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
  2. Java truststore：使用 authverse-backend 镜像内的 keytool 生成，供 authverse / kafka-ui 这类 JVM 客户端直接信任根 CA
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

if ! command -v docker >/dev/null 2>&1 && ! command -v keytool >/dev/null 2>&1; then
  echo "当前系统既缺少 docker，也缺少 keytool，无法生成 Java truststore。" >&2
  exit 1
fi

load_swarm_env "$ENV_FILE"

SWARM_PKI_MOUNT_TYPE="${SWARM_PKI_MOUNT_TYPE:-bind}"
SWARM_PKI_MOUNT_SOURCE="${SWARM_PKI_MOUNT_SOURCE:-/srv/cloudreve/pki}"
SWARM_PKI_LOCAL_SOURCE="${SWARM_PKI_LOCAL_SOURCE:-$SWARM_PKI_MOUNT_SOURCE}"
SWARM_CA_VALID_DAYS="${SWARM_CA_VALID_DAYS:-3650}"
SWARM_CERT_VALID_DAYS="${SWARM_CERT_VALID_DAYS:-825}"
SWARM_CA_COMMON_NAME="${SWARM_CA_COMMON_NAME:-Cloudreve Swarm Root CA}"
SWARM_TRUSTSTORE_PASSWORD="${SWARM_TRUSTSTORE_PASSWORD:-changeit}"

resolve_truststore_image() {
  local configured_image="${SWARM_TRUSTSTORE_IMAGE:-}"
  local local_image="${AUTHVERSE_BACKEND_LOCAL_IMAGE:-}"
  local runtime_local_image="${AUTHVERSE_BACKEND_RUNTIME_BASE_LOCAL_IMAGE:-}"
  local remote_image="${AUTHVERSE_BACKEND_IMAGE:-${AUTHVERSE_BACKEND_REMOTE_IMAGE:-}}"

  if [[ -n "$configured_image" ]]; then
    printf '%s\n' "$configured_image"
    return 0
  fi

  if [[ -n "$local_image" ]] && docker image inspect "$local_image" >/dev/null 2>&1; then
    printf '%s\n' "$local_image"
    return 0
  fi

  if [[ -n "$remote_image" ]] && docker image inspect "$remote_image" >/dev/null 2>&1; then
    printf '%s\n' "$remote_image"
    return 0
  fi

  if [[ -n "$local_image" ]]; then
    printf '%s\n' "$local_image"
    return 0
  fi

  if [[ -n "$remote_image" ]]; then
    printf '%s\n' "$remote_image"
    return 0
  fi

  if [[ -n "$runtime_local_image" ]]; then
    printf '%s\n' "$runtime_local_image"
    return 0
  fi

  printf '%s\n' "authverse/authverse-backend:2024-local"
}

validate_truststore_with_local_keytool() {
  local truststore_file="$1"

  keytool \
    -list \
    -alias swarm-root-ca \
    -storetype PKCS12 \
    -keystore "$truststore_file" \
    -storepass "$SWARM_TRUSTSTORE_PASSWORD" >/dev/null 2>&1
}

validate_truststore_with_docker_keytool() {
  local ca_dir="$1"
  local truststore_image="$2"
  local uid_gid="$3"

  docker run --rm \
    --user "$uid_gid" \
    --entrypoint keytool \
    -v "$ca_dir:/work" \
    "$truststore_image" \
    -list \
    -alias swarm-root-ca \
    -storetype PKCS12 \
    -keystore /work/truststore.p12 \
    -storepass "$SWARM_TRUSTSTORE_PASSWORD" >/dev/null 2>&1
}

truststore_matches_current_password() {
  local ca_dir="$1"
  local truststore_file="$ca_dir/truststore.p12"
  local truststore_image
  local uid_gid

  [[ -f "$truststore_file" ]] || return 1

  if command -v keytool >/dev/null 2>&1; then
    validate_truststore_with_local_keytool "$truststore_file"
    return
  fi

  if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    truststore_image="$(resolve_truststore_image)"
    uid_gid="$(id -u):$(id -g)"
    validate_truststore_with_docker_keytool "$ca_dir" "$truststore_image" "$uid_gid"
    return
  fi

  return 1
}

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

normalize_sans_for_compare() {
  local raw="$1"

  printf '%s\n' "$raw" \
    | tr ' ' '\n' \
    | sed '/^$/d' \
    | sort -u
}

extract_cert_sans_for_compare() {
  local cert_file="$1"

  openssl x509 -in "$cert_file" -noout -ext subjectAltName 2>/dev/null \
    | tail -n +2 \
    | tr ',' '\n' \
    | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' -e 's/^IP Address:/IP:/' \
    | sed '/^$/d' \
    | sort -u
}

cert_matches_current_ca() {
  local cert_file="$1"
  local ca_file="$2"

  [[ -f "$cert_file" && -f "$ca_file" ]] || return 1
  openssl verify -CAfile "$ca_file" "$cert_file" >/dev/null 2>&1
}

cert_matches_desired_sans() {
  local cert_file="$1"
  local desired_sans="$2"

  [[ -f "$cert_file" ]] || return 1
  diff -u \
    <(normalize_sans_for_compare "$desired_sans") \
    <(extract_cert_sans_for_compare "$cert_file") >/dev/null 2>&1
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
    if cert_matches_current_ca "$crt_file" "$ca_dir/ca.crt" && cert_matches_desired_sans "$crt_file" "$san_string"; then
    cp "$crt_file" "$fullchain_file"
    cat "$key_file" "$crt_file" >"$pem_file"
      cp "$key_file" "$server_key_file"
      chmod 0600 "$key_file"
      chmod 0644 "$server_key_file" "$crt_file" "$fullchain_file" "$pem_file"
      echo "[skip] $service 证书已存在且与当前 CA / SAN 配置一致"
      return 0
    fi

    echo "[warn] $service 现有证书与当前 CA 或 SAN 配置不一致，自动重签"
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

  cp "$crt_file" "$fullchain_file"
  cat "$key_file" "$crt_file" >"$pem_file"
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
  local truststore_image
  local uid_gid
  local used_local_keytool=0

  truststore_image="$(resolve_truststore_image)"
  uid_gid="$(id -u):$(id -g)"

  if [[ "$FORCE_CA" -eq 0 && -f "$truststore_file" ]]; then
    if truststore_matches_current_password "$ca_dir"; then
      echo "[skip] Java truststore 已存在且与当前密码一致"
      return 0
    fi

    echo "[warn] 现有 Java truststore 无法被当前 SWARM_TRUSTSTORE_PASSWORD 打开，自动重建"
  fi

  rm -f "$truststore_file"

  if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    if ! docker run --rm \
      --user "$uid_gid" \
      --entrypoint keytool \
      -v "$ca_dir:/work" \
      "$truststore_image" \
      -importcert \
      -noprompt \
      -alias swarm-root-ca \
      -file /work/ca.crt \
      -keystore /work/truststore.p12 \
      -storetype PKCS12 \
      -storepass "$SWARM_TRUSTSTORE_PASSWORD" >/dev/null 2>&1; then
      if ! command -v keytool >/dev/null 2>&1; then
        echo "无法通过 docker 镜像 $truststore_image 生成 Java truststore。" >&2
        exit 1
      fi
      used_local_keytool=1
    fi
  elif command -v keytool >/dev/null 2>&1; then
    used_local_keytool=1
  else
    echo "当前 docker daemon 不可用，且系统未安装 keytool，无法生成 Java truststore。" >&2
    exit 1
  fi

  if [[ "$used_local_keytool" -eq 1 ]]; then
    keytool \
      -importcert \
      -noprompt \
      -alias swarm-root-ca \
      -file "$ca_dir/ca.crt" \
      -keystore "$truststore_file" \
      -storetype PKCS12 \
      -storepass "$SWARM_TRUSTSTORE_PASSWORD" >/dev/null 2>&1
  fi

  if ! truststore_matches_current_password "$ca_dir"; then
    echo "生成后的 Java truststore 仍无法被当前 SWARM_TRUSTSTORE_PASSWORD 打开，请检查证书生成环境。" >&2
    exit 1
  fi

  chmod 0644 "$truststore_file"
  if [[ "$used_local_keytool" -eq 1 ]]; then
    echo "[done] 已使用本机 keytool 生成 Java truststore"
  else
    echo "[done] 已使用 $truststore_image 生成 Java truststore"
  fi
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
  PKI_ROOT="$SWARM_PKI_LOCAL_SOURCE"
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
