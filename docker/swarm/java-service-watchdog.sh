#!/bin/sh

set -eu

app_pid="${1:-}"
if [ -z "$app_pid" ]; then
  echo "[java-service-watchdog] missing application pid." >&2
  exit 2
fi

case "${JAVA_WATCHDOG_ENABLED:-false}" in
  true|TRUE|1|yes|YES|on|ON)
    ;;
  *)
    exit 0
    ;;
esac

watch_url="${JAVA_WATCHDOG_URL:-http://127.0.0.1:48080/actuator/health}"
interval_seconds="${JAVA_WATCHDOG_INTERVAL_SECONDS:-15}"
failure_threshold="${JAVA_WATCHDOG_FAILURE_THRESHOLD:-8}"
start_period_seconds="${JAVA_WATCHDOG_START_PERIOD_SECONDS:-180}"
stop_timeout_seconds="${JAVA_WATCHDOG_STOP_TIMEOUT_SECONDS:-30}"
request_timeout_seconds="${JAVA_WATCHDOG_REQUEST_TIMEOUT_SECONDS:-5}"
healthy_status_pattern='^[[:space:]]*\{[[:space:]]*"status"[[:space:]]*:[[:space:]]*"UP"'

sleep_if_alive() {
  seconds="$1"
  elapsed=0
  while [ "$elapsed" -lt "$seconds" ]; do
    if ! kill -0 "$app_pid" 2>/dev/null; then
      exit 0
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
}

terminate_app() {
  echo "[java-service-watchdog] health check failed ${failure_threshold} consecutive times; terminating pid ${app_pid}." >&2
  kill -TERM "$app_pid" 2>/dev/null || exit 0

  elapsed=0
  while [ "$elapsed" -lt "$stop_timeout_seconds" ]; do
    if ! kill -0 "$app_pid" 2>/dev/null; then
      exit 0
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done

  echo "[java-service-watchdog] pid ${app_pid} did not stop within ${stop_timeout_seconds}s; killing." >&2
  kill -KILL "$app_pid" 2>/dev/null || true
}

health_check_ok() {
  curl -fsS --max-time "$request_timeout_seconds" "$watch_url" \
    | grep -Eq "$healthy_status_pattern"
}

sleep_if_alive "$start_period_seconds"

failures=0
while kill -0 "$app_pid" 2>/dev/null; do
  if health_check_ok; then
    failures=0
  else
    failures=$((failures + 1))
    echo "[java-service-watchdog] health check failed ${failures}/${failure_threshold}: ${watch_url}" >&2
    if [ "$failures" -ge "$failure_threshold" ]; then
      terminate_app
      exit 0
    fi
  fi
  sleep_if_alive "$interval_seconds"
done
