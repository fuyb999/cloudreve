#!/bin/sh
set -eu

DEFAULT_FONT_DIR="/tika-fonts/custom"
LINK_DIR="/tmp/tika-font-links"
FONT_PATHS="$DEFAULT_FONT_DIR:$LINK_DIR"

mkdir -p "$LINK_DIR" /tmp/.cache/fontconfig

find "$LINK_DIR" -mindepth 1 -maxdepth 1 -exec rm -rf {} +

if [ -n "${TIKA_FONT_DIRS:-}" ]; then
  OLD_IFS=$IFS
  IFS=':'
  set -- $TIKA_FONT_DIRS
  IFS=$OLD_IFS

  idx=0
  for dir in "$@"; do
    if [ -z "$dir" ] || [ "$dir" = "$DEFAULT_FONT_DIR" ]; then
      continue
    fi

    if [ -d "$dir" ]; then
      ln -s "$dir" "$LINK_DIR/$idx"
      idx=$((idx + 1))
    elif [ -f "$dir" ]; then
      ln -s "$dir" "$LINK_DIR/$idx-$(basename "$dir")"
      idx=$((idx + 1))
    fi
  done
fi

export XDG_CACHE_HOME="${XDG_CACHE_HOME:-/tmp/.cache}"
export TIKA_FONT_DIRS="${TIKA_FONT_DIRS:-$DEFAULT_FONT_DIR}"

case "${JAVA_TOOL_OPTIONS:-}" in
  *-Dsun.java2d.fontpath=*)
    ;;
  *)
    if [ -n "${JAVA_TOOL_OPTIONS:-}" ]; then
      export JAVA_TOOL_OPTIONS="${JAVA_TOOL_OPTIONS} -Dsun.java2d.fontpath=${FONT_PATHS}"
    else
      export JAVA_TOOL_OPTIONS="-Dsun.java2d.fontpath=${FONT_PATHS}"
    fi
    ;;
esac

# Refresh font cache so mounted custom fonts are available to PDF/Office parsers.
fc-cache -f "$DEFAULT_FONT_DIR" "$LINK_DIR" >/dev/null 2>&1 || fc-cache -f >/dev/null 2>&1 || true

if [ "${TIKA_FONT_DEBUG:-0}" = "1" ]; then
  echo "[tika-font] TIKA_FONT_DIRS=${TIKA_FONT_DIRS}" >&2
  echo "[tika-font] JAVA_TOOL_OPTIONS=${JAVA_TOOL_OPTIONS}" >&2
  fc-list | grep -E 'Noto (Sans|Serif) CJK|PingFang|Microsoft YaHei|SimSun|SimHei|Songti' | head -n 20 >&2 || true
fi

exec java -cp "/tika-server-standard-${TIKA_VERSION}.jar:/tika-extras/*" \
  org.apache.tika.server.core.TikaServerCli -h 0.0.0.0 "$@"
