#!/usr/bin/env bash

set -euo pipefail

ONLYOFFICE_CUSTOM_FONTS_DIR="${ONLYOFFICE_CUSTOM_FONTS_DIR:-/usr/share/fonts/truetype/custom}"

if [[ -d "$ONLYOFFICE_CUSTOM_FONTS_DIR" ]]; then
  fc-cache -f "$ONLYOFFICE_CUSTOM_FONTS_DIR" >/dev/null 2>&1 || fc-cache -f >/dev/null 2>&1 || true
  documentserver-generate-allfonts.sh >/dev/null 2>&1 || true
fi

exec /app/ds/run-document-server.sh "$@"
