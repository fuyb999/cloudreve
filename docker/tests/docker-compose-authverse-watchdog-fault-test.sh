#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"

if ! docker info >/dev/null 2>&1; then
  echo "docker-compose-authverse-watchdog-fault-test: skipped (Docker daemon unavailable)"
  exit 0
fi

set +e
output="$(
  docker compose -f "$ROOT_DIR/docker-compose.yml" run --rm --no-deps \
    -e JAVA_LAUNCH_MODE=command \
    -e "JAVA_APP_COMMAND=sleep 60" \
    -e JAVA_WATCHDOG_ENABLED=true \
    -e JAVA_WATCHDOG_URL=http://127.0.0.1:9/__watchdog_down \
    -e JAVA_WATCHDOG_INTERVAL_SECONDS=1 \
    -e JAVA_WATCHDOG_FAILURE_THRESHOLD=2 \
    -e JAVA_WATCHDOG_START_PERIOD_SECONDS=0 \
    -e JAVA_WATCHDOG_STOP_TIMEOUT_SECONDS=2 \
    -e JAVA_WATCHDOG_REQUEST_TIMEOUT_SECONDS=1 \
    authverse-backend 2>&1
)"
status="$?"
set -e

printf '%s\n' "$output"

case "$status" in
  137|143)
    ;;
  *)
    echo "expected compose watchdog run to exit after terminating app, got status $status" >&2
    exit 1
    ;;
esac

printf '%s\n' "$output" | grep -F "health check failed 2/2" >/dev/null
printf '%s\n' "$output" | grep -F "terminating pid" >/dev/null

echo "docker-compose-authverse-watchdog-fault-test: ok"
