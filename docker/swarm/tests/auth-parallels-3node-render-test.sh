#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/../../.." && pwd)"
ENV_FILE="$ROOT_DIR/.env.swarm.parallels-3node-test"
AUTHVERSE_APP_TEMPLATE="$ROOT_DIR/docker/swarm/templates/authverse-backend/application-swarm.yml.template"

service_block() {
  service="$1"
  awk -v service="  ${service}:" '
    $0 == service { in_block = 1; print; next }
    in_block && $0 ~ /^  [A-Za-z0-9_.-]+:/ { exit }
    in_block { print }
  ' "$rendered"
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

assert_contains() {
  file="$1"
  expected="$2"
  if ! grep -Fq "$expected" "$file"; then
    echo "expected to find: $expected" >&2
    echo "--- $file ---" >&2
    sed -n '1,260p' "$file" >&2
    exit 1
  fi
}

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/auth-parallels-3node-render-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

RESOLVED_DIR="$tmp_dir" \
  "$ROOT_DIR/docker/swarm/deploy-stack.sh" auth --env-file "$ENV_FILE" --render-only >/dev/null

rendered="$tmp_dir/authverse-prl3test-resolved.yaml"

assert_service_contains "authverse-backend" "JAVA_WATCHDOG_ENABLED: \"true\""
assert_service_contains "authverse-backend" "JAVA_WATCHDOG_URL: http://127.0.0.1:48080/actuator/health/watchdog"
assert_service_contains "authverse-backend" "trustStorePassword=changeit"
assert_service_contains "authverse-backend" "source: java_service_launch_v3"
assert_service_contains "authverse-backend" "source: java_service_watchdog_v2"
assert_service_contains "authverse-backend" "target: /usr/local/bin/java-service-watchdog.sh"
assert_service_contains "authverse-backend" "curl -fsS --max-time 5 http://127.0.0.1:48080/actuator/health/watchdog | grep -Eq"
assert_service_contains "authverse-backend" "restart_policy:"
assert_service_contains "authverse-backend" "condition: any"
assert_contains "$rendered" "java_service_watchdog_v2:"
assert_contains "$AUTHVERSE_APP_TEMPLATE" "management:"
assert_contains "$AUTHVERSE_APP_TEMPLATE" "watchdog:"
assert_contains "$AUTHVERSE_APP_TEMPLATE" 'include: "${AUTHVERSE_ACTUATOR_WATCHDOG_INCLUDE}"'
assert_contains "$AUTHVERSE_APP_TEMPLATE" "readiness:"
assert_contains "$AUTHVERSE_APP_TEMPLATE" 'include: "readinessState,${AUTHVERSE_ACTUATOR_WATCHDOG_INCLUDE}"'
assert_contains "$AUTHVERSE_APP_TEMPLATE" "liveness:"

echo "auth-parallels-3node-render-test: ok"
