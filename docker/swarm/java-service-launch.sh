#!/bin/sh

set -eu

JAVA_BIN="${JAVA_BIN:-java}"
JAVA_LAUNCH_MODE="${JAVA_LAUNCH_MODE:-jar}"
JAVA_APP_JAR="${JAVA_APP_JAR:-app.jar}"
JAVA_APP_MAIN_CLASS="${JAVA_APP_MAIN_CLASS:-}"
JAVA_APP_CLASSPATH="${JAVA_APP_CLASSPATH:-}"
JAVA_APP_ARGS="${JAVA_APP_ARGS:-}"
JAVA_APP_COMMAND="${JAVA_APP_COMMAND:-}"
JAVA_WATCHDOG_SCRIPT="${JAVA_WATCHDOG_SCRIPT:-/usr/local/bin/java-service-watchdog.sh}"

export JAVA_BIN
export JAVA_APP_JAR
export JAVA_APP_MAIN_CLASS
export JAVA_APP_CLASSPATH
export JAVA_APP_ARGS
export JAVA_APP_COMMAND
export JAVA_WATCHDOG_SCRIPT

validate_launch_mode() {
  case "$JAVA_LAUNCH_MODE" in
    jar)
      ;;
    class)
      if [ -z "$JAVA_APP_MAIN_CLASS" ]; then
        echo "[java-service-launch] 缺少 JAVA_APP_MAIN_CLASS。" >&2
        exit 1
      fi
      if [ -z "$JAVA_APP_CLASSPATH" ]; then
        echo "[java-service-launch] 缺少 JAVA_APP_CLASSPATH。" >&2
        exit 1
      fi
      ;;
    command)
      if [ -z "$JAVA_APP_COMMAND" ]; then
        echo "[java-service-launch] 缺少 JAVA_APP_COMMAND。" >&2
        exit 1
      fi
      ;;
    *)
      echo "[java-service-launch] 不支持的 JAVA_LAUNCH_MODE=$JAVA_LAUNCH_MODE，允许值: jar / class / command" >&2
      exit 1
      ;;
  esac
}

watchdog_enabled() {
  case "${JAVA_WATCHDOG_ENABLED:-false}" in
    true|TRUE|1|yes|YES|on|ON)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

exec_app() {
  case "$JAVA_LAUNCH_MODE" in
    jar)
      exec /bin/sh -c 'exec "$JAVA_BIN" ${JAVA_OPTS:-} -jar "$JAVA_APP_JAR" ${JAVA_APP_ARGS:-}' java-service-launch
      ;;
    class)
      exec /bin/sh -c 'exec "$JAVA_BIN" ${JAVA_OPTS:-} -cp "$JAVA_APP_CLASSPATH" "$JAVA_APP_MAIN_CLASS" ${JAVA_APP_ARGS:-}' java-service-launch
      ;;
    command)
      exec /bin/sh -c "exec ${JAVA_APP_COMMAND}"
      ;;
  esac
}

start_app() {
  case "$JAVA_LAUNCH_MODE" in
    jar)
      /bin/sh -c 'exec "$JAVA_BIN" ${JAVA_OPTS:-} -jar "$JAVA_APP_JAR" ${JAVA_APP_ARGS:-}' java-service-launch &
      ;;
    class)
      /bin/sh -c 'exec "$JAVA_BIN" ${JAVA_OPTS:-} -cp "$JAVA_APP_CLASSPATH" "$JAVA_APP_MAIN_CLASS" ${JAVA_APP_ARGS:-}' java-service-launch &
      ;;
    command)
      /bin/sh -c "exec ${JAVA_APP_COMMAND}" &
      ;;
  esac
  app_pid="$!"
}

validate_launch_mode

if ! watchdog_enabled; then
  exec_app
fi

if [ ! -x "$JAVA_WATCHDOG_SCRIPT" ]; then
  echo "[java-service-launch] watchdog enabled but script is not executable: $JAVA_WATCHDOG_SCRIPT" >&2
  exit 1
fi

start_app
watchdog_pid=""

terminate() {
  trap - TERM INT
  kill -TERM "$app_pid" 2>/dev/null || true
  if [ -n "$watchdog_pid" ]; then
    kill -TERM "$watchdog_pid" 2>/dev/null || true
  fi
  set +e
  wait "$app_pid"
  status="$?"
  set -e
  exit "$status"
}

trap terminate TERM INT

"$JAVA_WATCHDOG_SCRIPT" "$app_pid" &
watchdog_pid="$!"

set +e
wait "$app_pid"
app_status="$?"
set -e

if [ -n "$watchdog_pid" ]; then
  kill -TERM "$watchdog_pid" 2>/dev/null || true
  wait "$watchdog_pid" 2>/dev/null || true
fi

exit "$app_status"
