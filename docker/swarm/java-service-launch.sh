#!/bin/sh

set -eu

JAVA_BIN="${JAVA_BIN:-java}"
JAVA_LAUNCH_MODE="${JAVA_LAUNCH_MODE:-jar}"
JAVA_APP_JAR="${JAVA_APP_JAR:-app.jar}"
JAVA_APP_MAIN_CLASS="${JAVA_APP_MAIN_CLASS:-}"
JAVA_APP_CLASSPATH="${JAVA_APP_CLASSPATH:-}"
JAVA_APP_ARGS="${JAVA_APP_ARGS:-}"
JAVA_APP_COMMAND="${JAVA_APP_COMMAND:-}"

export JAVA_BIN
export JAVA_APP_JAR
export JAVA_APP_MAIN_CLASS
export JAVA_APP_CLASSPATH
export JAVA_APP_ARGS
export JAVA_APP_COMMAND

case "$JAVA_LAUNCH_MODE" in
  jar)
    exec /bin/sh -c 'exec "$JAVA_BIN" ${JAVA_OPTS:-} -jar "$JAVA_APP_JAR" ${JAVA_APP_ARGS:-}' java-service-launch
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
    exec /bin/sh -c 'exec "$JAVA_BIN" ${JAVA_OPTS:-} -cp "$JAVA_APP_CLASSPATH" "$JAVA_APP_MAIN_CLASS" ${JAVA_APP_ARGS:-}' java-service-launch
    ;;
  command)
    if [ -z "$JAVA_APP_COMMAND" ]; then
      echo "[java-service-launch] 缺少 JAVA_APP_COMMAND。" >&2
      exit 1
    fi
    exec /bin/sh -c "exec ${JAVA_APP_COMMAND}"
    ;;
  *)
    echo "[java-service-launch] 不支持的 JAVA_LAUNCH_MODE=$JAVA_LAUNCH_MODE，允许值: jar / class / command" >&2
    exit 1
    ;;
esac
