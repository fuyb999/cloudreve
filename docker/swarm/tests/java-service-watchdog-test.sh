#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/../../.." && pwd)"
SCRIPT="$ROOT_DIR/docker/swarm/java-service-watchdog.sh"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/java-service-watchdog-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

cat >"$tmp_dir/curl" <<'CURL'
#!/bin/sh
exit 1
CURL
chmod +x "$tmp_dir/curl"

sleep 60 &
app_pid="$!"

PATH="$tmp_dir:$PATH" \
  JAVA_WATCHDOG_ENABLED=true \
  JAVA_WATCHDOG_URL=http://127.0.0.1:48080/actuator/health \
  JAVA_WATCHDOG_INTERVAL_SECONDS=1 \
  JAVA_WATCHDOG_FAILURE_THRESHOLD=2 \
  JAVA_WATCHDOG_START_PERIOD_SECONDS=0 \
  JAVA_WATCHDOG_STOP_TIMEOUT_SECONDS=2 \
  sh "$SCRIPT" "$app_pid"

set +e
wait "$app_pid"
app_status="$?"
set -e

case "$app_status" in
  143|137|130)
    ;;
  *)
    echo "expected watchdog to terminate app process, got wait status $app_status" >&2
    exit 1
    ;;
esac

cat >"$tmp_dir/curl" <<'CURL'
#!/bin/sh
printf '%s\n' '{"code":404,"msg":"not found","data":null}'
exit 0
CURL
chmod +x "$tmp_dir/curl"

sleep 4 &
app_pid="$!"

PATH="$tmp_dir:$PATH" \
  JAVA_WATCHDOG_ENABLED=true \
  JAVA_WATCHDOG_URL=http://127.0.0.1:48080/actuator/health/watchdog \
  JAVA_WATCHDOG_INTERVAL_SECONDS=1 \
  JAVA_WATCHDOG_FAILURE_THRESHOLD=2 \
  JAVA_WATCHDOG_START_PERIOD_SECONDS=0 \
  JAVA_WATCHDOG_STOP_TIMEOUT_SECONDS=2 \
  sh "$SCRIPT" "$app_pid"

set +e
wait "$app_pid"
app_status="$?"
set -e

case "$app_status" in
  143|137|130)
    ;;
  *)
    echo "expected watchdog to reject non-actuator JSON response, got wait status $app_status" >&2
    exit 1
    ;;
esac

sleep 60 &
app_pid="$!"
JAVA_WATCHDOG_ENABLED=false sh "$SCRIPT" "$app_pid"
if ! kill -0 "$app_pid" 2>/dev/null; then
  echo "expected disabled watchdog to leave app process running" >&2
  exit 1
fi
kill "$app_pid" >/dev/null 2>&1 || true
wait "$app_pid" >/dev/null 2>&1 || true

echo "java-service-watchdog-test: ok"
