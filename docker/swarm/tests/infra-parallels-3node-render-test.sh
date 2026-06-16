#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/../../.." && pwd)"
ENV_FILE="$ROOT_DIR/.env.swarm.parallels-3node-test"

assert_contains() {
  file="$1"
  expected="$2"
  if ! grep -Fq "$expected" "$file"; then
    echo "expected to find: $expected" >&2
    echo "--- $file ---" >&2
    sed -n '1,260p' "$file" >&2
    exit 1
  fi
}

assert_not_contains() {
  file="$1"
  unexpected="$2"
  if grep -Fq "$unexpected" "$file"; then
    echo "expected not to find: $unexpected" >&2
    echo "--- $file ---" >&2
    sed -n '1,180p' "$file" >&2
    exit 1
  fi
}

service_block() {
  service="$1"
  awk -v service="  ${service}:" '
    $0 == service { in_block = 1; print; next }
    in_block && $0 ~ /^  [A-Za-z0-9_.-]+:/ { exit }
    in_block { print }
  ' "$rendered"
}

assert_service_contains() {
  service="$1"
  expected="$2"
  if ! service_block "$service" | grep -Fq "$expected"; then
    echo "expected service $service to contain: $expected" >&2
    echo "--- service $service ---" >&2
    service_block "$service" >&2
    exit 1
  fi
}

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/infra-parallels-3node-render-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

RESOLVED_DIR="$tmp_dir" \
  "$ROOT_DIR/docker/swarm/deploy-stack.sh" infra --env-file "$ENV_FILE" --render-only >/dev/null

rendered="$tmp_dir/cloudreve-prl3test-infra-resolved.yaml"

assert_contains "$rendered" "MINIO_DISTRIBUTED_MODE_ENABLED: \"no\""
assert_contains "$rendered" "MINIO_DISTRIBUTED_NODES: minio-1"
assert_contains "$rendered" "ELASTICSEARCH_DISCOVERY_SEED_HOSTS: tasks.elasticsearch-1"
assert_contains "$rendered" "ELASTICSEARCH_CLUSTER_INITIAL_MASTER_NODES: elasticsearch-1"
assert_contains "$rendered" "KAFKA_CONTROLLER_QUORUM_VOTERS: 1@kafka-1:9093"
assert_contains "$rendered" "KAFKA_DEFAULT_REPLICATION_FACTOR: \"1\""
assert_contains "$rendered" "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR: \"1\""
assert_contains "$rendered" "ES_JAVA_OPTS: -Xms384m -Xmx384m"
assert_contains "$rendered" "KAFKA_HEAP_OPTS: -Xms384m -Xmx384m"
assert_contains "$rendered" "MINIO_ENDPOINT: http://minio-internal:9000"
assert_not_contains "$rendered" "MINIO_ENDPOINT: http://cloudreve-prl3test-infra_minio-internal:9000"

assert_service_contains "minio-1" "replicas: 1"
assert_service_contains "minio-1" "node.hostname==u22-swarm-mgr-11"
assert_service_contains "minio-internal" "source: minio_single_proxy_cfg_v2"
assert_service_contains "minio-2" "replicas: 0"
assert_service_contains "minio-3" "replicas: 0"
assert_service_contains "minio-4" "replicas: 0"
assert_service_contains "elasticsearch-1" "replicas: 1"
assert_service_contains "elasticsearch-1" "node.hostname==u22-swarm-wkr-12"
assert_service_contains "elasticsearch-2" "replicas: 0"
assert_service_contains "elasticsearch-3" "replicas: 0"
assert_service_contains "kafka-1" "replicas: 1"
assert_service_contains "kafka-1" "node.hostname==u22-swarm-wkr-13"
assert_service_contains "kafka-2" "replicas: 0"
assert_service_contains "kafka-3" "replicas: 0"
assert_service_contains "kafka-ui" "replicas: 0"
assert_service_contains "kafka-ui-public" "replicas: 0"

assert_not_contains "$ROOT_DIR/docker/swarm/haproxy/minio-public-tls.cfg" "resolvers docker"
assert_not_contains "$ROOT_DIR/docker/swarm/haproxy/minio-public-tls.cfg" "default-server resolvers docker"
assert_contains "$ROOT_DIR/docker/swarm/haproxy/minio-single.cfg" "tcp-check connect"
assert_contains "$ROOT_DIR/docker/swarm/haproxy/minio-cluster.cfg" "tcp-check connect"
assert_contains "$ROOT_DIR/docker/swarm/haproxy/minio-public-tls.cfg" "tcp-check connect"

echo "infra-parallels-3node-render-test: ok"
