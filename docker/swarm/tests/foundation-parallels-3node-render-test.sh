#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/../../.." && pwd)"
ENV_FILE="$ROOT_DIR/.env.swarm.parallels-3node-test"

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

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/foundation-parallels-3node-render-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

RESOLVED_DIR="$tmp_dir" \
  "$ROOT_DIR/docker/swarm/deploy-stack.sh" foundation --env-file "$ENV_FILE" --render-only >/dev/null

rendered="$tmp_dir/cloudreve-prl3test-foundation-resolved.yaml"

assert_service_contains "postgresql-1" "replicas: 1"
assert_service_contains "postgresql-2" "replicas: 1"
assert_service_contains "postgresql-3" "replicas: 1"
assert_service_contains "redis-1" "replicas: 1"
assert_service_contains "redis-2" "replicas: 1"
assert_service_contains "redis-3" "replicas: 1"
assert_service_contains "redis-1" "endpoint_mode: dnsrr"
assert_service_contains "redis-2" "endpoint_mode: dnsrr"
assert_service_contains "redis-3" "endpoint_mode: dnsrr"
assert_service_contains "pgpool-internal" "replicas: 3"
assert_service_contains "redis-sentinel" "replicas: 3"
assert_service_contains "redis-proxy-internal" "replicas: 3"
assert_update_config_contains "redis-proxy-internal" "order: stop-first"
assert_service_contains "redis-proxy" "replicas: 3"
assert_update_config_contains "redis-proxy" "order: stop-first"
assert_service_contains "redis-proxy" "source: redis_public_tls_cfg_v3"
assert_service_contains "redis-proxy" "start_period: 3m0s"
assert_service_contains "onlyoffice" "replicas: 0"
assert_service_contains "onlyoffice-public" "replicas: 0"
assert_service_contains "onlyoffice-rabbitmq" "replicas: 0"

echo "foundation-parallels-3node-render-test: ok"
