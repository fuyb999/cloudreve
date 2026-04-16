#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

"${SCRIPT_DIR}/ubuntu-provision-docker.sh" "$@"

if systemctl list-unit-files qemu-guest-agent.service >/dev/null 2>&1; then
  systemctl enable qemu-guest-agent >/dev/null 2>&1 || true
  systemctl restart qemu-guest-agent >/dev/null 2>&1 || true
fi
