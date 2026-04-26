#!/bin/sh

set -eu

HAPROXY_CFG="${SWARM_LB_HAPROXY_CONFIG_PATH:-/etc/cloudreve-lb/haproxy.cfg}"
KEEPALIVED_CFG="${SWARM_LB_KEEPALIVED_CONFIG_PATH:-/etc/cloudreve-lb/keepalived.conf}"
KEEPALIVED_RUN_DIR="${SWARM_LB_KEEPALIVED_RUN_DIR:-/run/keepalived}"
NODE_HOSTNAME="${SWARM_NODE_HOSTNAME:-$(hostname -s)}"

bool_enabled() {
  value="${1:-}"
  default="${2:-false}"

  if [ -z "$value" ]; then
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

require_env() {
  var_name="$1"
  eval "value=\${$var_name:-}"
  if [ -z "$value" ]; then
    echo "missing env: $var_name" >&2
    exit 1
  fi
}

sanitize_name() {
  printf '%s' "$1" | tr -c 'A-Za-z0-9_' '_'
}

require_number() {
  label="$1"
  value="$2"
  case "$value" in
    ''|*[!0-9]*)
      echo "$label must be numeric: $value" >&2
      exit 1
      ;;
  esac
}

parse_edge_nodes() {
  raw="${SWARM_LB_EDGE_NODES:-}"
  LB_NODE_NAMES=""
  LB_NODE_IPS=""
  LB_MATCH_INDEX=""
  LB_LOCAL_IP=""
  idx=0
  node_hostname_sanitized="$(sanitize_name "$NODE_HOSTNAME")"

  old_ifs="$IFS"
  IFS=','
  set -- $raw
  IFS="$old_ifs"

  for item in "$@"; do
    item="$(printf '%s' "$item" | xargs)"
    [ -n "$item" ] || continue
    if printf '%s' "$item" | grep -q '='; then
      raw_name="${item%%=*}"
      ip="${item#*=}"
    else
      ip="$item"
      raw_name="node_${ip}"
    fi
    name="$(sanitize_name "$raw_name")"
    [ -n "$name" ] || continue
    [ -n "$ip" ] || continue
    if [ -z "$LB_NODE_NAMES" ]; then
      LB_NODE_NAMES="$name"
      LB_NODE_IPS="$ip"
    else
      LB_NODE_NAMES="${LB_NODE_NAMES}
$name"
      LB_NODE_IPS="${LB_NODE_IPS}
$ip"
    fi
    if [ "$raw_name" = "$NODE_HOSTNAME" ] || [ "$name" = "$node_hostname_sanitized" ]; then
      LB_MATCH_INDEX="$idx"
      LB_LOCAL_IP="$ip"
    fi
    idx=$((idx + 1))
  done

  if [ -z "$LB_NODE_IPS" ]; then
    echo "missing SWARM_LB_EDGE_NODES" >&2
    exit 1
  fi
}

route_device_for_target() {
  target="$1"
  [ -n "$target" ] || return 1

  ip -o route get "$target" 2>/dev/null | awk '
    {
      for (i = 1; i <= NF; i++) {
        if ($i == "dev" && (i + 1) <= NF) {
          print $(i + 1)
          exit
        }
      }
    }
  '
}

resolve_lb_interface() {
  configured_interface="${SWARM_LB_INTERFACE:-}"
  resolved_interface=""
  peer_ip=""

  if [ -n "$configured_interface" ] && ip link show "$configured_interface" >/dev/null 2>&1; then
    SWARM_LB_INTERFACE_RESOLVED="$configured_interface"
    export SWARM_LB_INTERFACE_RESOLVED
    return 0
  fi

  if [ -n "$configured_interface" ]; then
    echo "configured interface '$configured_interface' not found, auto-detecting" >&2
  fi

  old_ifs="$IFS"
  IFS='
'
  for ip in $LB_NODE_IPS; do
    [ "$ip" = "$LB_LOCAL_IP" ] && continue
    peer_ip="$ip"
    break
  done
  IFS="$old_ifs"

  if [ -n "$peer_ip" ]; then
    resolved_interface="$(route_device_for_target "$peer_ip" || true)"
  fi

  if [ -z "$resolved_interface" ] && [ -n "${SWARM_LB_VIP:-}" ]; then
    resolved_interface="$(route_device_for_target "$SWARM_LB_VIP" || true)"
  fi

  if [ -z "$resolved_interface" ]; then
    resolved_interface="$(ip route show default 2>/dev/null | awk '/default/ { for (i = 1; i <= NF; i++) if ($i == "dev") { print $(i + 1); exit } }')"
  fi

  if [ -z "$resolved_interface" ] || ! ip link show "$resolved_interface" >/dev/null 2>&1; then
    echo "unable to resolve keepalived interface" >&2
    exit 1
  fi

  SWARM_LB_INTERFACE_RESOLVED="$resolved_interface"
  export SWARM_LB_INTERFACE_RESOLVED
  echo "using keepalived interface: $SWARM_LB_INTERFACE_RESOLVED" >&2
}

resolve_lb_role() {
  base_state="$(printf '%s' "${SWARM_LB_STATE:-BACKUP}" | tr '[:lower:]' '[:upper:]')"
  base_priority="${SWARM_LB_PRIORITY:-100}"
  master_priority="${SWARM_LB_PRIORITY_MASTER:-120}"
  priority_step="${SWARM_LB_PRIORITY_STEP:-10}"

  require_number "SWARM_LB_PRIORITY" "$base_priority"
  require_number "SWARM_LB_PRIORITY_MASTER" "$master_priority"
  require_number "SWARM_LB_PRIORITY_STEP" "$priority_step"

  if [ -n "$LB_MATCH_INDEX" ] && [ "$LB_MATCH_INDEX" -eq 0 ]; then
    KEEPALIVED_STATE="MASTER"
    KEEPALIVED_PRIORITY="$master_priority"
  elif [ -n "$LB_MATCH_INDEX" ]; then
    KEEPALIVED_STATE="BACKUP"
    KEEPALIVED_PRIORITY=$((master_priority - (LB_MATCH_INDEX * priority_step)))
    if [ "$KEEPALIVED_PRIORITY" -lt 1 ]; then
      KEEPALIVED_PRIORITY=1
    fi
  else
    KEEPALIVED_STATE="$base_state"
    KEEPALIVED_PRIORITY="$base_priority"
  fi
}

append_lb_service() {
  service_name="$1"
  frontend_port="$2"
  backend_port="$3"
  enabled="${4:-true}"

  if ! bool_enabled "$enabled" true; then
    return 0
  fi

  require_number "$service_name frontend port" "$frontend_port"
  require_number "$service_name backend port" "$backend_port"

  if [ -z "${LB_SERVICES:-}" ]; then
    LB_SERVICES="${service_name}|${frontend_port}|${backend_port}"
  else
    LB_SERVICES="${LB_SERVICES}
${service_name}|${frontend_port}|${backend_port}"
  fi
}

collect_lb_services() {
  LB_SERVICES=""
  append_lb_service "cloudreve_master" "${SWARM_LB_CLOUDREVE_MASTER_PORT:-28081}" "${SWARM_LB_CLOUDREVE_MASTER_BACKEND_PORT:-18081}" "true"
  append_lb_service "cloudreve_slave" "${SWARM_LB_CLOUDREVE_SLAVE_PORT:-28082}" "${SWARM_LB_CLOUDREVE_SLAVE_BACKEND_PORT:-18082}" "true"
  append_lb_service "authverse" "${SWARM_LB_AUTHVERSE_PORT:-28080}" "${SWARM_LB_AUTHVERSE_BACKEND_PORT:-18080}" "true"
  append_lb_service "onlyoffice" "${SWARM_LB_ONLYOFFICE_PORT:-28090}" "${SWARM_LB_ONLYOFFICE_BACKEND_PORT:-18090}" "true"
  append_lb_service "pgpool" "${SWARM_LB_PGPOOL_PORT:-25432}" "${SWARM_LB_PGPOOL_BACKEND_PORT:-15432}" "true"
  append_lb_service "minio_api" "${SWARM_LB_MINIO_API_PORT:-29000}" "${SWARM_LB_MINIO_API_BACKEND_PORT:-19000}" "true"
  append_lb_service "tika" "${SWARM_LB_TIKA_PORT:-29998}" "${SWARM_LB_TIKA_BACKEND_PORT:-19998}" "${SWARM_LB_INCLUDE_TIKA:-true}"
  append_lb_service "redis" "${SWARM_LB_REDIS_PORT:-26380}" "${SWARM_LB_REDIS_BACKEND_PORT:-16379}" "${SWARM_LB_INCLUDE_REDIS:-true}"
  append_lb_service "minio_console" "${SWARM_LB_MINIO_CONSOLE_PORT:-29001}" "${SWARM_LB_MINIO_CONSOLE_BACKEND_PORT:-19001}" "${SWARM_LB_INCLUDE_MINIO_CONSOLE:-true}"
  append_lb_service "elasticsearch_http" "${SWARM_LB_ELASTICSEARCH_HTTP_PORT:-29200}" "${SWARM_LB_ELASTICSEARCH_HTTP_BACKEND_PORT:-19200}" "${SWARM_LB_INCLUDE_ELASTICSEARCH_HTTP:-true}"
  append_lb_service "elasticsearch_transport" "${SWARM_LB_ELASTICSEARCH_TRANSPORT_PORT:-29300}" "${SWARM_LB_ELASTICSEARCH_TRANSPORT_BACKEND_PORT:-19300}" "${SWARM_LB_INCLUDE_ELASTICSEARCH_TRANSPORT:-false}"
  append_lb_service "kafka_ui" "${SWARM_LB_KAFKA_UI_PORT:-28089}" "${SWARM_LB_KAFKA_UI_BACKEND_PORT:-18089}" "${SWARM_LB_INCLUDE_KAFKA_UI:-true}"
  append_lb_service "registry" "${SWARM_LB_REGISTRY_PORT:-25000}" "${SWARM_LB_REGISTRY_BACKEND_PORT:-15000}" "${SWARM_LB_INCLUDE_REGISTRY:-false}"
}

write_haproxy_config() {
  bind_address="${SWARM_LB_BIND_ADDRESS:-${SWARM_LB_VIP:-*}}"
  if [ "$bind_address" = "*" ] || [ "$bind_address" = "0.0.0.0" ]; then
    bind_prefix="*"
  else
    bind_prefix="$bind_address"
  fi

  mkdir -p "$(dirname "$HAPROXY_CFG")"
  cat >"$HAPROXY_CFG" <<EOF
# 由 edge-lb-bootstrap.sh 动态生成。
# 只做四层 TCP 透传，不在 LB 层终止 TLS。
global
    log stdout format raw local0
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
    cat >>"$HAPROXY_CFG" <<EOF
listen stats
    mode http
    bind ${bind_prefix}:${SWARM_LB_STATS_PORT:-28404}
    stats enable
    stats uri /
    stats refresh 10s
    stats auth ${SWARM_LB_STATS_USER:-cloudreve}:${SWARM_LB_STATS_PASSWORD}

EOF
  fi

  old_ifs="$IFS"
  IFS='
'
  for service in $LB_SERVICES; do
    IFS='|' read -r name frontend_port backend_port <<EOF
$service
EOF
    IFS='
'
    backend_name="${name}_${frontend_port}_backends"
    cat >>"$HAPROXY_CFG" <<EOF
frontend ${name}_${frontend_port}
    bind ${bind_prefix}:${frontend_port}
    default_backend ${backend_name}

backend ${backend_name}
    balance ${SWARM_LB_BALANCE:-roundrobin}
    option tcp-check
EOF
    paste_file_1="$(mktemp)"
    paste_file_2="$(mktemp)"
    printf '%s\n' "$LB_NODE_NAMES" >"$paste_file_1"
    printf '%s\n' "$LB_NODE_IPS" >"$paste_file_2"
    pair_lines="$(paste "$paste_file_1" "$paste_file_2")"
    while IFS="$(printf '\t')" read -r node_name node_ip; do
      [ -n "$node_name" ] || continue
      [ -n "$node_ip" ] || continue
      printf '    server %s %s:%s check inter 3s fall 3 rise 2\n' "$node_name" "$node_ip" "$backend_port" >>"$HAPROXY_CFG"
    done <<EOF
$pair_lines
EOF
    printf '\n' >>"$HAPROXY_CFG"
    rm -f "$paste_file_1" "$paste_file_2"
  done
  IFS="$old_ifs"
}

write_keepalived_config() {
  mkdir -p "$(dirname "$KEEPALIVED_CFG")"
  cat >"$KEEPALIVED_CFG" <<EOF
# 由 edge-lb-bootstrap.sh 动态生成。
global_defs {
    enable_script_security
    script_user root
}

vrrp_script chk_haproxy {
    script "/usr/bin/pidof haproxy"
    interval 2
    weight -20
}

vrrp_instance VI_CLOUDREVE {
    state ${KEEPALIVED_STATE}
    interface ${SWARM_LB_INTERFACE_RESOLVED}
    virtual_router_id ${SWARM_LB_VIRTUAL_ROUTER_ID:-81}
    priority ${KEEPALIVED_PRIORITY}
    advert_int ${SWARM_LB_ADVERT_INT:-1}
    authentication {
        auth_type PASS
        auth_pass ${SWARM_LB_AUTH_PASS}
    }
    virtual_ipaddress {
        ${SWARM_LB_VIP}/${SWARM_LB_VIP_CIDR:-24}
    }
    track_script {
        chk_haproxy
    }
}
EOF
}

require_env SWARM_LB_VIP
require_env SWARM_LB_EDGE_NODES
require_env SWARM_LB_STATS_PASSWORD
require_env SWARM_LB_AUTH_PASS

mkdir -p "$KEEPALIVED_RUN_DIR"

parse_edge_nodes
resolve_lb_role
resolve_lb_interface
collect_lb_services
write_haproxy_config
write_keepalived_config

haproxy -c -f "$HAPROXY_CFG"

haproxy -W -db -f "$HAPROXY_CFG" &
HAPROXY_PID="$!"

keepalived \
  -n \
  -l \
  -P \
  -f "$KEEPALIVED_CFG" &
KEEPALIVED_PID="$!"

terminate_children() {
  kill -TERM "$HAPROXY_PID" "$KEEPALIVED_PID" 2>/dev/null || true
  wait "$HAPROXY_PID" 2>/dev/null || true
  wait "$KEEPALIVED_PID" 2>/dev/null || true
}

trap 'terminate_children; exit 0' INT TERM

status=1
while :; do
  if ! kill -0 "$HAPROXY_PID" 2>/dev/null; then
    if wait "$HAPROXY_PID"; then
      status=1
    else
      status=$?
    fi
    echo "haproxy exited unexpectedly" >&2
    break
  fi

  if ! kill -0 "$KEEPALIVED_PID" 2>/dev/null; then
    if wait "$KEEPALIVED_PID"; then
      status=1
    else
      status=$?
    fi
    echo "keepalived exited unexpectedly" >&2
    break
  fi

  sleep 2
done

terminate_children
exit "$status"
