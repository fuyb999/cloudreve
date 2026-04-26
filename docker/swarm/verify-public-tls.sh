#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT_DIR/docker/swarm/lib-env.sh"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
HOSTS="${HOSTS:-}"
CA_FILE="${CA_FILE:-}"
HOST_NAME="$(hostname -s 2>/dev/null || hostname)"

usage() {
  cat <<'EOF'
用法：
  docker/swarm/verify-public-tls.sh [--env-file 文件] [--hosts "IP1,IP2,IP3"] [--ca-file FILE]

说明：
  这个脚本用于从 Swarm 外按真实协议验证所有对外 TLS 入口：
  1. HTTPS 入口：验证 TLS 握手、证书、HTTP 状态
  2. Redis Proxy：验证 TLS 握手、AUTH、PING
  3. Pgpool：先发 PostgreSQL SSLRequest，再验证 TLS 握手
  4. Elasticsearch transport：验证传输层 TLS 握手

重要说明：
  - cloudreve-slave 的 28082 不是匿名公开页面。
  - 正确探测方式是 POST /api/v4/slave/ping。
  - 未带签名时，预期返回 HTTP 200 + JSON code=403。
  - 这表示 TLS、代理和业务鉴权都正常，不应判定为故障。

参数：
  --env-file FILE      环境变量文件，默认 .env.swarm
  --hosts HOSTS        要验证的主机，逗号分隔；默认取 env 里的 *_TLS_EXTRA_SANS
  --ca-file FILE       根 CA 文件路径；默认优先 SWARM_PKI_LOCAL_SOURCE，再回退 SWARM_PKI_MOUNT_SOURCE
  -h, --help           显示帮助
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      ENV_FILE="$2"
      shift 2
      ;;
    --hosts)
      HOSTS="$2"
      shift 2
      ;;
    --ca-file)
      CA_FILE="$2"
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

if ! command -v python3 >/dev/null 2>&1; then
  echo "[$HOST_NAME] 当前系统缺少 python3，无法执行协议级 TLS 验证。" >&2
  exit 1
fi

load_swarm_env "$ENV_FILE"

if [[ -z "$CA_FILE" ]]; then
  if [[ -n "${SWARM_PKI_LOCAL_SOURCE:-}" && -f "${SWARM_PKI_LOCAL_SOURCE}/ca/ca.crt" ]]; then
    CA_FILE="${SWARM_PKI_LOCAL_SOURCE}/ca/ca.crt"
  else
    CA_FILE="${SWARM_PKI_MOUNT_SOURCE:-/srv/cloudreve/pki}/ca/ca.crt"
  fi
fi

if [[ ! -f "$CA_FILE" ]]; then
  echo "[$HOST_NAME] 找不到根 CA 文件: $CA_FILE" >&2
  exit 1
fi

if [[ -z "$HOSTS" ]]; then
  HOSTS="${AUTHVERSE_TLS_EXTRA_SANS:-${CLOUDREVE_MASTER_TLS_EXTRA_SANS:-}}"
fi

if [[ -z "$HOSTS" ]]; then
  echo "[$HOST_NAME] 未指定 hosts，且环境变量里没有可用的 *_TLS_EXTRA_SANS。" >&2
  exit 1
fi

export VERIFY_TLS_CA_FILE="$CA_FILE"
export VERIFY_TLS_HOSTS="$HOSTS"
export VERIFY_TLS_AUTHVERSE_PORT="${SWARM_LB_AUTHVERSE_PORT:-${AUTHVERSE_HTTP_PORT:-28080}}"
export VERIFY_TLS_CLOUDREVE_MASTER_PORT="${SWARM_LB_CLOUDREVE_MASTER_PORT:-${CLOUDREVE_MASTER_HTTP_PORT:-28081}}"
export VERIFY_TLS_CLOUDREVE_SLAVE_PORT="${SWARM_LB_CLOUDREVE_SLAVE_PORT:-${CLOUDREVE_SLAVE_HTTP_PORT:-28082}}"
export VERIFY_TLS_KAFKA_UI_PORT="${SWARM_LB_KAFKA_UI_PORT:-${KAFKA_UI_HTTP_PORT:-28089}}"
export VERIFY_TLS_ONLYOFFICE_PORT="${SWARM_LB_ONLYOFFICE_PORT:-${ONLYOFFICE_HTTP_PORT:-28090}}"
export VERIFY_TLS_MINIO_API_PORT="${SWARM_LB_MINIO_API_PORT:-${MINIO_API_PORT:-29000}}"
export VERIFY_TLS_MINIO_CONSOLE_PORT="${SWARM_LB_MINIO_CONSOLE_PORT:-${MINIO_CONSOLE_PORT:-29001}}"
export VERIFY_TLS_ES_HTTP_PORT="${SWARM_LB_ELASTICSEARCH_HTTP_PORT:-${ELASTICSEARCH_HTTP_PORT:-29200}}"
export VERIFY_TLS_ES_TRANSPORT_PORT="${SWARM_LB_ELASTICSEARCH_TRANSPORT_PORT:-${ELASTICSEARCH_TRANSPORT_PORT:-29300}}"
export VERIFY_TLS_TIKA_PORT="${SWARM_LB_TIKA_PORT:-${TIKA_HTTP_PORT:-29998}}"
export VERIFY_TLS_REDIS_PORT="${SWARM_LB_REDIS_PORT:-${REDIS_PROXY_PUBLIC_PORT:-26379}}"
export VERIFY_TLS_PGPOOL_PORT="${SWARM_LB_PGPOOL_PORT:-${PGPOOL_PUBLIC_PORT:-25432}}"
export VERIFY_TLS_REDIS_PASSWORD="${REDIS_PASSWORD:-}"
export VERIFY_TLS_INCLUDE_KAFKA_UI="${SWARM_LB_INCLUDE_KAFKA_UI:-true}"
export VERIFY_TLS_INCLUDE_MINIO_CONSOLE="${SWARM_LB_INCLUDE_MINIO_CONSOLE:-true}"
export VERIFY_TLS_INCLUDE_ES_HTTP="${SWARM_LB_INCLUDE_ELASTICSEARCH_HTTP:-true}"
export VERIFY_TLS_INCLUDE_ES_TRANSPORT="${SWARM_LB_INCLUDE_ELASTICSEARCH_TRANSPORT:-false}"
export VERIFY_TLS_INCLUDE_TIKA="${SWARM_LB_INCLUDE_TIKA:-true}"
export VERIFY_TLS_INCLUDE_REDIS="${SWARM_LB_INCLUDE_REDIS:-true}"

python3 - <<'PY'
import json
import os
import socket
import ssl
import struct
import http.client
from collections import Counter

CA = os.environ["VERIFY_TLS_CA_FILE"]
HOSTS = [item.strip() for item in os.environ["VERIFY_TLS_HOSTS"].replace(";", ",").split(",") if item.strip()]
REDIS_PASSWORD = os.environ.get("VERIFY_TLS_REDIS_PASSWORD", "")

def env_enabled(name, default="true"):
    value = os.environ.get(name, default).strip().lower()
    return value not in {"0", "false", "no", "off", "disable", "disabled"}

PORTS = {
    "authverse": int(os.environ["VERIFY_TLS_AUTHVERSE_PORT"]),
    "cloudreve_master": int(os.environ["VERIFY_TLS_CLOUDREVE_MASTER_PORT"]),
    "cloudreve_slave": int(os.environ["VERIFY_TLS_CLOUDREVE_SLAVE_PORT"]),
    "kafka_ui": int(os.environ["VERIFY_TLS_KAFKA_UI_PORT"]),
    "onlyoffice": int(os.environ["VERIFY_TLS_ONLYOFFICE_PORT"]),
    "minio_api": int(os.environ["VERIFY_TLS_MINIO_API_PORT"]),
    "minio_console": int(os.environ["VERIFY_TLS_MINIO_CONSOLE_PORT"]),
    "es_http": int(os.environ["VERIFY_TLS_ES_HTTP_PORT"]),
    "es_transport": int(os.environ["VERIFY_TLS_ES_TRANSPORT_PORT"]),
    "tika": int(os.environ["VERIFY_TLS_TIKA_PORT"]),
    "redis": int(os.environ["VERIFY_TLS_REDIS_PORT"]),
    "pgpool": int(os.environ["VERIFY_TLS_PGPOOL_PORT"]),
}

http_tests = [
    ("authverse", PORTS["authverse"], "/.well-known/openid-configuration", {200}, None),
    ("cloudreve-master", PORTS["cloudreve_master"], "/api/v4/site/ping", {200}, None),
    ("cloudreve-slave", PORTS["cloudreve_slave"], "/api/v4/slave/ping", {200}, "expect_slave_auth_error"),
    ("onlyoffice", PORTS["onlyoffice"], "/healthcheck", {200}, None),
    ("minio-api", PORTS["minio_api"], "/minio/health/live", {200}, None),
]

optional_http_tests = [
    ("VERIFY_TLS_INCLUDE_KAFKA_UI", "kafka-ui", PORTS["kafka_ui"], "/", {200, 301, 302, 307, 308}, None, "kafka-ui"),
    ("VERIFY_TLS_INCLUDE_MINIO_CONSOLE", "minio-console", PORTS["minio_console"], "/", {200, 301, 302, 307, 308}, None, "minio-console"),
    ("VERIFY_TLS_INCLUDE_ES_HTTP", "elasticsearch-http", PORTS["es_http"], "/_cluster/health?pretty", {200}, None, "elasticsearch-http"),
    ("VERIFY_TLS_INCLUDE_TIKA", "tika", PORTS["tika"], "/version", {200}, None, "tika"),
]

for env_name, test_name, port, path, ok_codes, mode, label in optional_http_tests:
    if env_enabled(env_name):
        http_tests.append((test_name, port, path, ok_codes, mode))
    else:
        print(f"[SKIP] {label} 未启用外部 LB/TLS，跳过验证")

def peer_cn(cert):
    for item in cert.get("subject", []):
        for key, value in item:
            if key == "commonName":
                return value
    return ""

def https_get(host, port, path):
    ctx = ssl.create_default_context(cafile=CA)
    conn = http.client.HTTPSConnection(host, port, context=ctx, timeout=10)
    conn.request("GET", path)
    resp = conn.getresponse()
    body = resp.read(256)
    cert = conn.sock.getpeercert() if conn.sock else {}
    conn.close()
    return resp.status, body, peer_cn(cert)

def https_post_json(host, port, path, payload):
    ctx = ssl.create_default_context(cafile=CA)
    conn = http.client.HTTPSConnection(host, port, context=ctx, timeout=10)
    body = json.dumps(payload).encode()
    conn.request("POST", path, body=body, headers={"Content-Type": "application/json"})
    resp = conn.getresponse()
    resp_body = resp.read(256)
    cert = conn.sock.getpeercert() if conn.sock else {}
    conn.close()
    return resp.status, resp_body, peer_cn(cert)

def redis_resp(parts):
    chunks = [f"*{len(parts)}\r\n"]
    for item in parts:
        encoded = item.encode()
        chunks.append(f"${len(encoded)}\r\n")
        chunks.append(item)
        chunks.append("\r\n")
    return "".join(chunks).encode()

def redis_check(host, port):
    if not REDIS_PASSWORD:
        return False, "REDIS_PASSWORD is empty", "", "", ""
    ctx = ssl.create_default_context(cafile=CA)
    with socket.create_connection((host, port), timeout=10) as raw:
        with ctx.wrap_socket(raw, server_hostname=host) as tls:
            cert = tls.getpeercert()
            tls.sendall(redis_resp(["AUTH", REDIS_PASSWORD]))
            auth = tls.recv(1024).decode("utf-8", "replace").strip()
            tls.sendall(redis_resp(["PING"]))
            pong = tls.recv(1024).decode("utf-8", "replace").strip()
            ok = auth.startswith("+OK") and pong.startswith("+PONG")
            return ok, auth, pong, peer_cn(cert), tls.version()

def pg_check(host, port):
    ctx = ssl.create_default_context(cafile=CA)
    with socket.create_connection((host, port), timeout=10) as raw:
        raw.sendall(struct.pack("!II", 8, 80877103))
        resp = raw.recv(1)
        if resp != b"S":
            return False, f"SSLRequest={resp!r}", "", ""
        with ctx.wrap_socket(raw, server_hostname=host) as tls:
            cert = tls.getpeercert()
            return True, "SSLRequest=S", peer_cn(cert), tls.version()

def tls_check(host, port):
    ctx = ssl.create_default_context(cafile=CA)
    with socket.create_connection((host, port), timeout=10) as raw:
        with ctx.wrap_socket(raw, server_hostname=host) as tls:
            cert = tls.getpeercert()
            return True, peer_cn(cert), tls.version()

results = []

print("=== HTTPS / 应用层入口 ===")
for host in HOSTS:
    for name, port, path, ok_codes, mode in http_tests:
        try:
            if mode == "expect_slave_auth_error":
                status, body, cn = https_post_json(host, port, path, {})
                text = body.decode("utf-8", "replace").strip().replace("\n", " ")[:120]
                ok = status in ok_codes and '"code":403' in text and "authorization header is missing" in text
                results.append(ok)
                mark = "PASS" if ok else "FAIL"
                print(f"[{mark}] {host}:{port} {name} HTTP {status} CN={cn} BODY={text}")
            else:
                status, body, cn = https_get(host, port, path)
                text = body.decode("utf-8", "replace").strip().replace("\n", " ")[:120]
                ok = status in ok_codes
                results.append(ok)
                mark = "PASS" if ok else "FAIL"
                print(f"[{mark}] {host}:{port} {name} HTTP {status} CN={cn} BODY={text}")
        except Exception as exc:
            results.append(False)
            print(f"[FAIL] {host}:{port} {name} {type(exc).__name__}: {exc}")

print("\n=== Redis TLS ===")
if env_enabled("VERIFY_TLS_INCLUDE_REDIS"):
    for host in HOSTS:
        try:
            ok, auth, pong, cn, ver = redis_check(host, PORTS["redis"])
            results.append(ok)
            mark = "PASS" if ok else "FAIL"
            print(f"[{mark}] {host}:{PORTS['redis']} redis AUTH={auth} PING={pong} TLS={ver} CN={cn}")
        except Exception as exc:
            results.append(False)
            print(f"[FAIL] {host}:{PORTS['redis']} redis {type(exc).__name__}: {exc}")
else:
    print("[SKIP] redis 未启用外部 LB/TLS，跳过验证")

print("\n=== Pgpool TLS ===")
for host in HOSTS:
    try:
        ok, state, cn, ver = pg_check(host, PORTS["pgpool"])
        results.append(ok)
        mark = "PASS" if ok else "FAIL"
        print(f"[{mark}] {host}:{PORTS['pgpool']} pgpool {state} TLS={ver} CN={cn}")
    except Exception as exc:
        results.append(False)
        print(f"[FAIL] {host}:{PORTS['pgpool']} pgpool {type(exc).__name__}: {exc}")

print("\n=== Elasticsearch 传输层 TLS ===")
if env_enabled("VERIFY_TLS_INCLUDE_ES_TRANSPORT", "false"):
    for host in HOSTS:
        try:
            ok, cn, ver = tls_check(host, PORTS["es_transport"])
            results.append(ok)
            mark = "PASS" if ok else "FAIL"
            print(f"[{mark}] {host}:{PORTS['es_transport']} elasticsearch-transport TLS={ver} CN={cn}")
        except Exception as exc:
            results.append(False)
            print(f"[FAIL] {host}:{PORTS['es_transport']} elasticsearch-transport {type(exc).__name__}: {exc}")
else:
    print("[SKIP] elasticsearch transport 未启用外部 LB/TLS，跳过验证")

counter = Counter(results)
print(f"\nSUMMARY pass={counter[True]} fail={counter[False]} total={len(results)}")
if counter[False]:
    raise SystemExit(1)
PY
