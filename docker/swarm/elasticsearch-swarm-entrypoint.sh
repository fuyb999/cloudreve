#!/usr/bin/env bash

set -euo pipefail

DATA_DIR="${ELASTICSEARCH_DATA_DIR:-/usr/share/elasticsearch/data}"
NODE_NAME="${ELASTICSEARCH_NODE_NAME:?set ELASTICSEARCH_NODE_NAME}"
SEED_HOSTS="${ELASTICSEARCH_DISCOVERY_SEED_HOSTS:?set ELASTICSEARCH_DISCOVERY_SEED_HOSTS}"
INITIAL_MASTER_NODES="${ELASTICSEARCH_CLUSTER_INITIAL_MASTER_NODES:?set ELASTICSEARCH_CLUSTER_INITIAL_MASTER_NODES}"

bootstrap_args=()

if [[ -d "$DATA_DIR/nodes" ]] && find "$DATA_DIR/nodes" -type f -path '*/_state/*' | grep -q .; then
  :
else
  bootstrap_args+=("-Ecluster.initial_master_nodes=${INITIAL_MASTER_NODES}")
fi

exec /usr/local/bin/docker-entrypoint.sh eswrapper \
  -Enode.name="${NODE_NAME}" \
  -Ediscovery.seed_hosts="${SEED_HOSTS}" \
  "${bootstrap_args[@]}"
