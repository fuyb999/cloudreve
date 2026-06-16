#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
COMPOSE_FILE="$ROOT_DIR/docker-compose.yml"

service_block() {
  service="$1"
  awk -v service="  ${service}:" '
    $0 == service { in_block = 1; print; next }
    in_block && $0 ~ /^  [A-Za-z0-9_.-]+:/ { exit }
    in_block { print }
  ' "$COMPOSE_FILE"
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

assert_service_contains "authverse-backend" 'entrypoint: ["/__cacert_entrypoint.sh", "/usr/local/bin/java-service-launch.sh"]'
assert_service_contains "authverse-backend" "JAVA_LAUNCH_MODE: jar"
assert_service_contains "authverse-backend" "JAVA_APP_JAR: app.jar"
assert_service_contains "authverse-backend" 'JAVA_WATCHDOG_ENABLED: ${AUTHVERSE_BACKEND_WATCHDOG_ENABLED:-true}'
assert_service_contains "authverse-backend" 'JAVA_WATCHDOG_URL: ${AUTHVERSE_BACKEND_WATCHDOG_URL:-http://127.0.0.1:48080/actuator/health/watchdog}'
assert_service_contains "authverse-backend" '"management":{"endpoint":{"health":{"probes":{"enabled":true},"group":{"watchdog":{"include":"*","show-details":"never"}'
assert_service_contains "authverse-backend" "./docker/swarm/java-service-launch.sh:/usr/local/bin/java-service-launch.sh:ro"
assert_service_contains "authverse-backend" "./docker/swarm/java-service-watchdog.sh:/usr/local/bin/java-service-watchdog.sh:ro"
assert_service_contains "authverse-backend" "curl -fsS --max-time 5 http://127.0.0.1:48080/actuator/health/watchdog | grep -Eq"

echo "docker-compose-authverse-watchdog-test: ok"
