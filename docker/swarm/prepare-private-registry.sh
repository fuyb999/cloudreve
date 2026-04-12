#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
MODE="check"
RESTART_DOCKER="no"
HOST_NAME="$(hostname -s 2>/dev/null || hostname)"
DAEMON_JSON="${DAEMON_JSON:-/etc/docker/daemon.json}"

usage() {
 cat <<'EOF'
用法：
  sudo docker/swarm/prepare-private-registry.sh [--env-file 文件] [--check] [--apply] [--restart-docker]

说明：
  这个脚本用于在每台 Swarm 节点准备私有仓库访问配置：
  1. `PRIVATE_REGISTRY_SCHEME=http` 时，写入 Docker daemon 的 `insecure-registries`
  2. `PRIVATE_REGISTRY_SCHEME=https` 时，写入 Docker `certs.d/<registry>/ca.crt`
  让节点可以从单点部署的 `registry:2` 远程拉取镜像。

参数：
  --env-file FILE       读取的环境变量文件，默认是 .env.swarm
  --check               只检查，不改动
  --apply               应用私有仓库信任配置
  --restart-docker      apply 后自动重启 docker
  -h, --help            显示帮助

示例：
  sudo docker/swarm/prepare-private-registry.sh --env-file .env.swarm --check
  sudo docker/swarm/prepare-private-registry.sh --env-file .env.swarm --apply --restart-docker
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
    --restart-docker)
      RESTART_DOCKER="yes"
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

if [[ -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
elif [[ -z "${PRIVATE_REGISTRY_ADDR:-}" ]]; then
  echo "[$HOST_NAME] 找不到环境变量文件: $ENV_FILE，且当前环境也没有 PRIVATE_REGISTRY_ADDR。" >&2
  exit 1
fi

PRIVATE_REGISTRY_SCHEME="${PRIVATE_REGISTRY_SCHEME:-http}"
PRIVATE_REGISTRY_ADDR="${PRIVATE_REGISTRY_ADDR:-}"
SWARM_PKI_MOUNT_SOURCE="${SWARM_PKI_MOUNT_SOURCE:-/srv/cloudreve/pki}"
PRIVATE_REGISTRY_CA_FILE="${PRIVATE_REGISTRY_CA_FILE:-$SWARM_PKI_MOUNT_SOURCE/ca/ca.crt}"
DOCKER_CERTS_DIR="${DOCKER_CERTS_DIR:-/etc/docker/certs.d}"
REGISTRY_CERT_DIR="$DOCKER_CERTS_DIR/$PRIVATE_REGISTRY_ADDR"
REGISTRY_CA_TARGET="$REGISTRY_CERT_DIR/ca.crt"
JSON_TOOL=""

if [[ -z "$PRIVATE_REGISTRY_ADDR" ]]; then
  echo "[$HOST_NAME] 缺少 PRIVATE_REGISTRY_ADDR，无法准备私有仓库配置。" >&2
  exit 1
fi

case "$PRIVATE_REGISTRY_SCHEME" in
  http|https)
    ;;
  *)
    echo "[$HOST_NAME] 不支持的 PRIVATE_REGISTRY_SCHEME=$PRIVATE_REGISTRY_SCHEME，只允许 http / https。" >&2
    exit 1
    ;;
esac

if [[ "$PRIVATE_REGISTRY_SCHEME" == "http" ]]; then
  if command -v jq >/dev/null 2>&1; then
    JSON_TOOL="jq"
  elif command -v python3 >/dev/null 2>&1; then
    JSON_TOOL="python3"
  else
    echo "[$HOST_NAME] 缺少 jq 或 python3，无法安全修改 $DAEMON_JSON。" >&2
    exit 1
  fi
fi

has_registry() {
  local json_file="$1"
  [[ -f "$json_file" ]] || return 1
  if [[ "$JSON_TOOL" == "jq" ]]; then
    jq -e --arg addr "$PRIVATE_REGISTRY_ADDR" '."insecure-registries" // [] | index($addr) != null' "$json_file" >/dev/null
    return
  fi

  python3 - "$json_file" "$PRIVATE_REGISTRY_ADDR" <<'PY'
import json
import sys
from pathlib import Path

json_file = Path(sys.argv[1])
addr = sys.argv[2]

try:
    data = json.loads(json_file.read_text())
except json.JSONDecodeError as exc:
    print(f"invalid json: {exc}", file=sys.stderr)
    sys.exit(2)

registries = data.get("insecure-registries") or []
sys.exit(0 if addr in registries else 1)
PY
}

write_registry_config() {
  local src_json="$1"
  local dst_json="$2"

  if [[ "$JSON_TOOL" == "jq" ]]; then
    if [[ -f "$src_json" ]]; then
      jq --arg addr "$PRIVATE_REGISTRY_ADDR" \
        '."insecure-registries" = ((."insecure-registries" // []) + [$addr] | unique)' \
        "$src_json" >"$dst_json"
    else
      printf '{}\n' | jq --arg addr "$PRIVATE_REGISTRY_ADDR" \
        '."insecure-registries" = [$addr]' >"$dst_json"
    fi
    return
  fi

  python3 - "$src_json" "$dst_json" "$PRIVATE_REGISTRY_ADDR" <<'PY'
import json
import sys
from pathlib import Path

src = Path(sys.argv[1])
dst = Path(sys.argv[2])
addr = sys.argv[3]

data = {}
if src.exists():
    try:
        data = json.loads(src.read_text())
    except json.JSONDecodeError as exc:
        print(f"invalid json: {exc}", file=sys.stderr)
        sys.exit(2)

registries = data.get("insecure-registries")
if not isinstance(registries, list):
    registries = []
if addr not in registries:
    registries.append(addr)
data["insecure-registries"] = registries

dst.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n")
PY
}

if [[ "$MODE" == "check" ]]; then
  if [[ "$PRIVATE_REGISTRY_SCHEME" == "https" ]]; then
    if [[ ! -f "$PRIVATE_REGISTRY_CA_FILE" ]]; then
      echo "[$HOST_NAME] [MISSING] 私有仓库 CA 源文件不存在：$PRIVATE_REGISTRY_CA_FILE"
    elif [[ ! -f "$REGISTRY_CA_TARGET" ]]; then
      echo "[$HOST_NAME] [MISSING] Docker certs.d 尚未安装私有仓库 CA：$REGISTRY_CA_TARGET"
    elif cmp -s "$PRIVATE_REGISTRY_CA_FILE" "$REGISTRY_CA_TARGET"; then
      echo "[$HOST_NAME] [OK] Docker certs.d 已安装私有仓库 CA：$REGISTRY_CA_TARGET"
    else
      echo "[$HOST_NAME] [STALE] Docker certs.d 中的 CA 与源文件不一致：$REGISTRY_CA_TARGET"
      echo "[$HOST_NAME] [NEXT] 请执行 --apply，把新的 CA 重新下发到 Docker certs.d。"
    fi
  else
    if has_registry "$DAEMON_JSON"; then
      echo "[$HOST_NAME] [OK] Docker daemon 已信任私有仓库：$PRIVATE_REGISTRY_ADDR"
    else
      echo "[$HOST_NAME] [MISSING] Docker daemon 尚未信任私有仓库：$PRIVATE_REGISTRY_ADDR"
    fi
  fi
  exit 0
fi

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
  echo "[$HOST_NAME] apply 模式请使用 root 或 sudo。" >&2
  exit 1
fi

if [[ "$PRIVATE_REGISTRY_SCHEME" == "https" ]]; then
  if [[ ! -f "$PRIVATE_REGISTRY_CA_FILE" ]]; then
    echo "[$HOST_NAME] 找不到私有仓库 CA 文件：$PRIVATE_REGISTRY_CA_FILE" >&2
    exit 1
  fi

  mkdir -p "$REGISTRY_CERT_DIR"
  install -m 0644 "$PRIVATE_REGISTRY_CA_FILE" "$REGISTRY_CA_TARGET"
  echo "[$HOST_NAME] [DONE] 已安装私有仓库 CA -> $REGISTRY_CA_TARGET"
else
  tmp_json="$(mktemp)"
  cleanup() {
    rm -f "$tmp_json"
  }
  trap cleanup EXIT

  write_registry_config "$DAEMON_JSON" "$tmp_json"

  install -m 0644 "$tmp_json" "$DAEMON_JSON"
  echo "[$HOST_NAME] [DONE] 已写入 $DAEMON_JSON -> insecure-registries += $PRIVATE_REGISTRY_ADDR"
fi

if [[ "$RESTART_DOCKER" == "yes" ]]; then
  if command -v systemctl >/dev/null 2>&1; then
    systemctl restart docker
    echo "[$HOST_NAME] [DONE] Docker 已重启。"
  else
    echo "[$HOST_NAME] [WARN] 未检测到 systemctl，请手工重启 Docker。"
  fi
else
  echo "[$HOST_NAME] [NEXT] 需要手工重启 Docker，配置才会生效。"
fi
