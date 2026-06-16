#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/../../.." && pwd)"
ENV_FILE="$ROOT_DIR/.env.swarm.prod-5x256g.example"

assert_contains() {
  file="$1"
  expected="$2"
  if ! grep -Fq "$expected" "$file"; then
    echo "expected to find: $expected" >&2
    echo "--- $file ---" >&2
    sed -n '1,220p' "$file" >&2
    exit 1
  fi
}

service_block() {
  service="$1"
  awk -v service="  ${service}:" '
    $0 == service { in_block = 1; print; next }
    in_block && $0 ~ /^  [A-Za-z0-9_.-]+:/ { exit }
    in_block { print }
  ' "$rendered"
}

update_config_block() {
  service="$1"
  service_block "$service" | awk '
    /^[[:space:]]+update_config:/ {
      in_block = 1
      indent = match($0, /[^ ]/) - 1
      print
      next
    }
    in_block {
      current_indent = match($0, /[^ ]/) - 1
      if ($0 ~ /^[[:space:]]+[A-Za-z0-9_.-]+:/ && current_indent <= indent) {
        exit
      }
      print
    }
  '
}

assert_service_contains() {
  service="$1"
  expected="$2"
  if ! service_block "$service" | grep -Fq "$expected"; then
    echo "expected service $service to contain: $expected" >&2
    echo "--- service $service ---" >&2
    service_block "$service" >&2
    exit 1
  fi
}

assert_update_config_contains() {
  service="$1"
  expected="$2"
  if ! update_config_block "$service" | grep -Fq "$expected"; then
    echo "expected service $service update_config to contain: $expected" >&2
    echo "--- service $service update_config ---" >&2
    update_config_block "$service" >&2
    exit 1
  fi
}

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/foundation-prod-5x256g-render-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

RESOLVED_DIR="$tmp_dir" \
  "$ROOT_DIR/docker/swarm/deploy-stack.sh" foundation --env-file "$ENV_FILE" --render-only >/dev/null

rendered="$tmp_dir/cloudreve-prod-foundation-resolved.yaml"

assert_contains "$rendered" "redis-proxy-internal:"
assert_service_contains "redis-1" "endpoint_mode: vip"
assert_service_contains "redis-2" "endpoint_mode: vip"
assert_service_contains "redis-3" "endpoint_mode: vip"
assert_service_contains "redis-proxy-internal" "replicas: 5"
assert_update_config_contains "pgpool-internal" "order: stop-first"
assert_update_config_contains "pgpool" "order: stop-first"
assert_update_config_contains "redis-proxy-internal" "order: stop-first"
assert_service_contains "redis-proxy" "replicas: 5"
assert_update_config_contains "redis-proxy" "order: stop-first"
assert_service_contains "redis-proxy" "source: redis_public_tls_cfg_v3"
assert_contains "$rendered" "REDIS_SENTINEL_QUORUM: \"3\""
assert_contains "$rendered" "POSTGRESQL_LOCAL_START_TIMEOUT: \"1200\""
assert_contains "$rendered" "POSTGRESQL_NUM_SYNCHRONOUS_REPLICAS: \"0\""
assert_contains "$rendered" "REPMGR_CONNECT_TIMEOUT: \"10\""
assert_contains "$rendered" "REPMGR_RECONNECT_ATTEMPTS: \"12\""
assert_contains "$rendered" "REPMGR_RECONNECT_INTERVAL: \"5\""
assert_contains "$rendered" "REPMGR_DEGRADED_MONITORING_TIMEOUT: \"180\""
assert_contains "$rendered" "postgresql-1:127.0.0.1"
assert_contains "$rendered" "postgresql-2:127.0.0.1"
assert_contains "$rendered" "postgresql-3:127.0.0.1"
assert_contains "$rendered" "ONLYOFFICE_DB_READY_TIMEOUT_SECONDS: \"1800\""
assert_contains "$rendered" "ONLYOFFICE_DB_READY_INTERVAL_SECONDS: \"2\""
assert_contains "$rendered" "test:"
assert_contains "$rendered" "source: redis_proxy_healthcheck_v1"
assert_contains "$rendered" "target: /usr/local/bin/redis-proxy-healthcheck.sh"
assert_contains "$rendered" "REDIS_PROXY_HEALTH_HOST=redis-proxy-internal /bin/sh /usr/local/bin/redis-proxy-healthcheck.sh"
assert_contains "$rendered" "interval: 15s"
assert_contains "$rendered" "timeout: 5s"
assert_service_contains "redis-proxy-internal" "retries: 20"
assert_service_contains "redis-proxy" "retries: 20"
assert_service_contains "redis-proxy-internal" "start_period: 5m0s"
assert_service_contains "redis-proxy" "start_period: 5m0s"

echo "foundation-prod-5x256g-render-test: ok"
