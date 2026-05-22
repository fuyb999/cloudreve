#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/../../.." && pwd)"
SCRIPT="$ROOT_DIR/docker/swarm/postgresql-repmgr-run.sh"

assert_contains() {
  file="$1"
  expected="$2"
  if ! grep -Fq "$expected" "$file"; then
    echo "expected to find: $expected" >&2
    echo "--- $file ---" >&2
    sed -n '1,180p' "$file" >&2
    exit 1
  fi
}

assert_contains "$SCRIPT" "ensure_local_repmgr_hostname"
assert_contains "$SCRIPT" 'if ! echo "${local_host} ${hostnames}" >>/etc/hosts; then'
assert_contains "$SCRIPT" "Could not update /etc/hosts; ensure the service uses extra_hosts"
assert_contains "$SCRIPT" "REPMGR_NODE_NETWORK_NAME"
assert_contains "$SCRIPT" "REPMGR_ADVERTISE_HOST"

echo "postgresql-repmgr-run-test: ok"
