#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env.swarm}"
MODE="render"
OUTPUT_DIR="${OUTPUT_DIR:-$ROOT_DIR/.tmp/swarm-vip-lb}"
HAPROXY_OUTPUT=""
KEEPALIVED_OUTPUT=""
SYSCTL_OUTPUT=""
ROLE=""
STATE=""
PRIORITY=""
HOST_NAME="$(hostname -s 2>/dev/null || hostname)"

usage() {
  cat <<'EOF'
用法：
  sudo docker/swarm/setup-swarm-vip-lb.sh [--env-file 文件] [--apply]
  docker/swarm/setup-swarm-vip-lb.sh [--env-file 文件] [--output-dir 目录]

说明：
  生成 Docker Swarm 对外统一四层 VIP/LB 配置。
  - HAProxy 只做 TCP 透传，不终止 TLS。
  - Keepalived 负责 VIP 漂移。
  - Pgpool / Redis / MinIO / ES / HTTPS 入口都保持原协议穿透。
  - 默认监听 SWARM_LB_VIP 上的非标准 2xxxx 端口，转发到 Swarm published port。

参数：
  --env-file FILE       读取的环境变量文件，默认 .env.swarm
  --output-dir DIR      render 模式输出目录，默认 .tmp/swarm-vip-lb
  --apply               写入 /etc/haproxy/haproxy.cfg、/etc/keepalived/keepalived.conf 和 sysctl 配置
  --role ROLE           当前节点角色：master 或 backup；会覆盖 SWARM_LB_STATE
  --state STATE         Keepalived state：MASTER 或 BACKUP
  --priority N          Keepalived priority
  -h, --help            显示帮助

示例：
  docker/swarm/setup-swarm-vip-lb.sh --env-file .env.swarm.prod-4x256g.example
  sudo docker/swarm/setup-swarm-vip-lb.sh --env-file .env.swarm --role master --priority 120 --apply
  sudo docker/swarm/setup-swarm-vip-lb.sh --env-file .env.swarm --role backup --priority 100 --apply
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      ENV_FILE="$2"
      shift 2
      ;;
    --output-dir)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    --apply)
      MODE="apply"
      shift
      ;;
    --role)
      ROLE="$2"
      shift 2
      ;;
    --state)
      STATE="$2"
      shift 2
      ;;
    --priority)
      PRIORITY="$2"
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
  echo "[$HOST_NAME] apply 模式需要 root 或 sudo。" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

ROLE_LOWER="$(printf '%s' "$ROLE" | tr '[:upper:]' '[:lower:]')"
case "$ROLE_LOWER" in
  master)
    STATE="MASTER"
    PRIORITY="${PRIORITY:-120}"
    ;;
  backup)
    STATE="BACKUP"
    PRIORITY="${PRIORITY:-100}"
    ;;
  "")
    ;;
  *)
    echo "[$HOST_NAME] --role 只支持 master 或 backup。" >&2
    exit 1
    ;;
esac

STATE="${STATE:-${SWARM_LB_STATE:-BACKUP}}"
STATE="$(printf '%s' "$STATE" | tr '[:lower:]' '[:upper:]')"
PRIORITY="${PRIORITY:-${SWARM_LB_PRIORITY:-100}}"
SWARM_LB_AUTH_PASS="${SWARM_LB_AUTH_PASS:-vipauth1}"

case "$STATE" in
  MASTER|BACKUP)
    ;;
  *)
    echo "[$HOST_NAME] Keepalived state 必须是 MASTER 或 BACKUP，当前：$STATE" >&2
    exit 1
    ;;
esac

if [[ "${#SWARM_LB_AUTH_PASS}" -gt 8 ]]; then
  echo "[$HOST_NAME] SWARM_LB_AUTH_PASS 最长建议 8 个字符，当前长度=${#SWARM_LB_AUTH_PASS}。" >&2
  exit 1
fi

bool_enabled() {
  local value="${1:-}"
  local default="${2:-false}"

  if [[ -z "$value" ]]; then
    value="$default"
  fi

  value="$(printf '%s' "$value" | tr '[:upper:]' '[:lower:]')"
  case "$value" in
    1|yes|y|true|on)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

sanitize_name() {
  printf '%s' "$1" | tr -c 'A-Za-z0-9_' '_'
}

parse_edge_nodes() {
  local raw="${SWARM_LB_EDGE_NODES:-}"
  local item name ip

  LB_NODE_NAMES=()
  LB_NODE_IPS=()

  raw="${raw//,/ }"
  for item in $raw; do
    [[ -n "$item" ]] || continue
    if [[ "$item" == *=* ]]; then
      name="${item%%=*}"
      ip="${item#*=}"
    else
      ip="$item"
      name="node_${ip//./_}"
    fi
    [[ -n "$name" && -n "$ip" ]] || continue
    LB_NODE_NAMES+=("$(sanitize_name "$name")")
    LB_NODE_IPS+=("$ip")
  done

  if [[ "${#LB_NODE_IPS[@]}" -eq 0 ]]; then
    echo "[$HOST_NAME] 缺少 SWARM_LB_EDGE_NODES，例如：cr-prod-mgr-11=10.10.0.11,cr-prod-wkr-12=10.10.0.12" >&2
    exit 1
  fi
}

require_number() {
  local label="$1"
  local value="$2"

  if [[ ! "$value" =~ ^[0-9]+$ ]]; then
    echo "[$HOST_NAME] $label 必须是数字端口，当前：$value" >&2
    exit 1
  fi
}

declare -a LB_SERVICES=()
add_lb_service() {
  local name="$1"
  local frontend_port="$2"
  local backend_port="$3"
  local enabled="${4:-true}"

  bool_enabled "$enabled" true || return 0
  require_number "$name frontend port" "$frontend_port"
  require_number "$name backend port" "$backend_port"
  LB_SERVICES+=("$name|$frontend_port|$backend_port")
}

collect_lb_services() {
  LB_SERVICES=()

  add_lb_service "cloudreve_master" "${SWARM_LB_CLOUDREVE_MASTER_PORT:-28081}" "${SWARM_LB_CLOUDREVE_MASTER_BACKEND_PORT:-${CLOUDREVE_MASTER_HTTP_PORT:-18081}}" true
  add_lb_service "cloudreve_slave" "${SWARM_LB_CLOUDREVE_SLAVE_PORT:-28082}" "${SWARM_LB_CLOUDREVE_SLAVE_BACKEND_PORT:-${CLOUDREVE_SLAVE_HTTP_PORT:-18082}}" true
  add_lb_service "authverse" "${SWARM_LB_AUTHVERSE_PORT:-28080}" "${SWARM_LB_AUTHVERSE_BACKEND_PORT:-${AUTHVERSE_HTTP_PORT:-18080}}" true
  add_lb_service "onlyoffice" "${SWARM_LB_ONLYOFFICE_PORT:-28090}" "${SWARM_LB_ONLYOFFICE_BACKEND_PORT:-${ONLYOFFICE_HTTP_PORT:-18090}}" true
  add_lb_service "pgpool" "${SWARM_LB_PGPOOL_PORT:-25432}" "${SWARM_LB_PGPOOL_BACKEND_PORT:-${PGPOOL_PUBLIC_PORT:-15432}}" true
  add_lb_service "minio_api" "${SWARM_LB_MINIO_API_PORT:-29000}" "${SWARM_LB_MINIO_API_BACKEND_PORT:-${MINIO_API_PORT:-19000}}" true
  add_lb_service "tika" "${SWARM_LB_TIKA_PORT:-29998}" "${SWARM_LB_TIKA_BACKEND_PORT:-${TIKA_HTTP_PORT:-19998}}" "${SWARM_LB_INCLUDE_TIKA:-true}"
  add_lb_service "redis" "${SWARM_LB_REDIS_PORT:-26379}" "${SWARM_LB_REDIS_BACKEND_PORT:-${REDIS_PROXY_PUBLIC_PORT:-16379}}" "${SWARM_LB_INCLUDE_REDIS:-true}"
  add_lb_service "minio_console" "${SWARM_LB_MINIO_CONSOLE_PORT:-29001}" "${SWARM_LB_MINIO_CONSOLE_BACKEND_PORT:-${MINIO_CONSOLE_PORT:-19001}}" "${SWARM_LB_INCLUDE_MINIO_CONSOLE:-true}"
  add_lb_service "elasticsearch_http" "${SWARM_LB_ELASTICSEARCH_HTTP_PORT:-29200}" "${SWARM_LB_ELASTICSEARCH_HTTP_BACKEND_PORT:-${ELASTICSEARCH_HTTP_PORT:-19200}}" "${SWARM_LB_INCLUDE_ELASTICSEARCH_HTTP:-true}"
  add_lb_service "elasticsearch_transport" "${SWARM_LB_ELASTICSEARCH_TRANSPORT_PORT:-29300}" "${SWARM_LB_ELASTICSEARCH_TRANSPORT_BACKEND_PORT:-${ELASTICSEARCH_TRANSPORT_PORT:-19300}}" "${SWARM_LB_INCLUDE_ELASTICSEARCH_TRANSPORT:-false}"
  add_lb_service "kafka_ui" "${SWARM_LB_KAFKA_UI_PORT:-28089}" "${SWARM_LB_KAFKA_UI_BACKEND_PORT:-${KAFKA_UI_HTTP_PORT:-18089}}" "${SWARM_LB_INCLUDE_KAFKA_UI:-true}"
  add_lb_service "registry" "${SWARM_LB_REGISTRY_PORT:-25000}" "${SWARM_LB_REGISTRY_BACKEND_PORT:-${PRIVATE_REGISTRY_HTTP_PORT:-15000}}" "${SWARM_LB_INCLUDE_REGISTRY:-false}"
}

write_haproxy_config() {
  local output="$1"
  local bind_address="${SWARM_LB_BIND_ADDRESS:-${SWARM_LB_VIP:-*}}"
  local bind_prefix
  local service name frontend_port backend_port backend_name index node_name node_ip

  if [[ "$bind_address" == "*" || "$bind_address" == "0.0.0.0" ]]; then
    bind_prefix="*"
  else
    bind_prefix="$bind_address"
  fi

  {
    cat <<EOF
# 由 docker/swarm/setup-swarm-vip-lb.sh 生成。
# 只做四层 TCP 透传，不在 LB 层终止 TLS。
global
    log /dev/log local0
    maxconn ${SWARM_LB_HAPROXY_MAXCONN:-200000}

defaults
    mode tcp
    log global
    option tcplog
    timeout connect ${SWARM_LB_CONNECT_TIMEOUT:-5s}
    timeout client ${SWARM_LB_CLIENT_TIMEOUT:-1h}
    timeout server ${SWARM_LB_SERVER_TIMEOUT:-1h}

EOF

    if bool_enabled "${SWARM_LB_STATS_ENABLED:-true}" true; then
      cat <<EOF
listen stats
    mode http
    bind ${bind_prefix}:${SWARM_LB_STATS_PORT:-28404}
    stats enable
    stats uri /
    stats refresh 10s
    stats auth ${SWARM_LB_STATS_USER:-cloudreve}:${SWARM_LB_STATS_PASSWORD:-change_me}

EOF
    fi

    for service in "${LB_SERVICES[@]}"; do
      IFS='|' read -r name frontend_port backend_port <<<"$service"
      backend_name="${name}_${frontend_port}_backends"

      cat <<EOF
frontend ${name}_${frontend_port}
    bind ${bind_prefix}:${frontend_port}
    default_backend ${backend_name}

backend ${backend_name}
    balance ${SWARM_LB_BALANCE:-roundrobin}
    option tcp-check
EOF

      for index in "${!LB_NODE_IPS[@]}"; do
        node_name="${LB_NODE_NAMES[$index]}"
        node_ip="${LB_NODE_IPS[$index]}"
        printf '    server %s %s:%s check inter 3s fall 3 rise 2\n' "$node_name" "$node_ip" "$backend_port"
      done
      printf '\n'
    done
  } >"$output"
}

write_keepalived_config() {
  local output="$1"
  local vip="${SWARM_LB_VIP:?set SWARM_LB_VIP}"
  local cidr="${SWARM_LB_VIP_CIDR:-24}"
  local interface="${SWARM_LB_INTERFACE:?set SWARM_LB_INTERFACE}"

  cat >"$output" <<EOF
# 由 docker/swarm/setup-swarm-vip-lb.sh 生成。
vrrp_script chk_haproxy {
    script "pidof haproxy"
    interval 2
    weight -20
}

vrrp_instance VI_CLOUDREVE {
    state ${STATE}
    interface ${interface}
    virtual_router_id ${SWARM_LB_VIRTUAL_ROUTER_ID:-81}
    priority ${PRIORITY}
    advert_int ${SWARM_LB_ADVERT_INT:-1}
    authentication {
        auth_type PASS
        auth_pass ${SWARM_LB_AUTH_PASS}
    }
    virtual_ipaddress {
        ${vip}/${cidr}
    }
    track_script {
        chk_haproxy
    }
}
EOF
}

write_sysctl_config() {
  local output="$1"

  cat >"$output" <<'EOF'
# 允许 HAProxy 在 BACKUP 节点预绑定尚未漂移到本机的 VIP。
net.ipv4.ip_nonlocal_bind = 1
EOF
}

parse_edge_nodes
collect_lb_services

if [[ "${#LB_SERVICES[@]}" -eq 0 ]]; then
  echo "[$HOST_NAME] 没有启用任何 LB 服务。" >&2
  exit 1
fi

if [[ "$MODE" == "apply" ]]; then
  HAPROXY_OUTPUT="/etc/haproxy/haproxy.cfg"
  KEEPALIVED_OUTPUT="/etc/keepalived/keepalived.conf"
  SYSCTL_OUTPUT="/etc/sysctl.d/99-cloudreve-vip-lb.conf"
  mkdir -p /etc/haproxy /etc/keepalived /etc/sysctl.d
else
  HAPROXY_OUTPUT="$OUTPUT_DIR/haproxy.cfg"
  KEEPALIVED_OUTPUT="$OUTPUT_DIR/keepalived.conf"
  SYSCTL_OUTPUT="$OUTPUT_DIR/99-cloudreve-vip-lb.conf"
  mkdir -p "$OUTPUT_DIR"
fi

write_haproxy_config "$HAPROXY_OUTPUT"
write_keepalived_config "$KEEPALIVED_OUTPUT"
write_sysctl_config "$SYSCTL_OUTPUT"

echo "[$HOST_NAME] 已生成 HAProxy 配置：$HAPROXY_OUTPUT"
echo "[$HOST_NAME] 已生成 Keepalived 配置：$KEEPALIVED_OUTPUT"
echo "[$HOST_NAME] 已生成 sysctl 配置：$SYSCTL_OUTPUT"
echo "[$HOST_NAME] 当前 LB 服务："
for service in "${LB_SERVICES[@]}"; do
  IFS='|' read -r name frontend_port backend_port <<<"$service"
  echo "[$HOST_NAME] - ${name}: VIP:${frontend_port} -> edge:${backend_port}"
done

if command -v haproxy >/dev/null 2>&1; then
  haproxy -c -f "$HAPROXY_OUTPUT"
else
  echo "[$HOST_NAME] 未找到 haproxy 命令，跳过配置语法检查。"
fi

if [[ "$MODE" == "apply" ]]; then
  sysctl --system >/dev/null
  if command -v systemctl >/dev/null 2>&1; then
    systemctl restart haproxy
    systemctl restart keepalived
    systemctl --no-pager --full status haproxy keepalived || true
  else
    echo "[$HOST_NAME] 当前系统没有 systemctl，请手动重启 haproxy 和 keepalived。"
  fi
fi
