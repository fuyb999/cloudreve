#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/../../.." && pwd)"
SCRIPT="$ROOT_DIR/docker/swarm/redis-proxy-healthcheck.sh"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/redis-proxy-healthcheck-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

cat >"$tmp_dir/nc" <<'NC'
#!/bin/sh
cat >/dev/null
case "${FAKE_REDIS_REPLY:-master}" in
  master)
    printf '+OK\r\n+PONG\r\n# Replication\r\nrole:master\r\n+OK\r\n'
    ;;
  slave)
    printf '+OK\r\n+PONG\r\n# Replication\r\nrole:slave\r\n+OK\r\n'
    ;;
  no-pong)
    printf '+OK\r\n-ERR unavailable\r\n'
    ;;
  *)
    exit 1
    ;;
esac
NC
chmod +x "$tmp_dir/nc"

PATH="$tmp_dir:$PATH" REDIS_PASSWORD="secret" FAKE_REDIS_REPLY=master sh "$SCRIPT"

if PATH="$tmp_dir:$PATH" REDIS_PASSWORD="secret" FAKE_REDIS_REPLY=slave sh "$SCRIPT"; then
  echo "expected slave response to fail when master is required" >&2
  exit 1
fi

PATH="$tmp_dir:$PATH" REDIS_PASSWORD="secret" REDIS_PROXY_HEALTH_REQUIRE_MASTER=no FAKE_REDIS_REPLY=slave sh "$SCRIPT"

if PATH="$tmp_dir:$PATH" REDIS_PASSWORD="secret" FAKE_REDIS_REPLY=no-pong sh "$SCRIPT"; then
  echo "expected response without PONG to fail" >&2
  exit 1
fi

echo "redis-proxy-healthcheck-test: ok"
