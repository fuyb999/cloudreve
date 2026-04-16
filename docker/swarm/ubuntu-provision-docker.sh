#!/usr/bin/env bash

set -euo pipefail

TARGET_USER="${VM_USERNAME:-ubuntu}"
DOCKER_APT_VERSION="${DOCKER_APT_VERSION:-5:25.0.4-1~ubuntu.22.04~jammy}"

export DEBIAN_FRONTEND=noninteractive

apt-get update
apt-get install -y ca-certificates curl gnupg lsb-release

install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc

cat >/etc/apt/sources.list.d/docker.sources <<'EOF'
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: jammy
Components: stable
Architectures: amd64
Signed-By: /etc/apt/keyrings/docker.asc
EOF

apt-get update
apt-get install -y \
  "docker-ce=${DOCKER_APT_VERSION}" \
  "docker-ce-cli=${DOCKER_APT_VERSION}" \
  containerd.io \
  docker-buildx-plugin \
  docker-compose-plugin

apt-mark hold docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

cat >/etc/docker/daemon.json <<'EOF'
{
  "log-driver": "json-file",
  "log-opts": {
    "max-size": "10m",
    "max-file": "3"
  }
}
EOF

cat >/etc/sysctl.d/99-cloudreve-elasticsearch.conf <<'EOF'
vm.max_map_count=262144
EOF

cat >/etc/sysctl.d/99-cloudreve-redis.conf <<'EOF'
vm.overcommit_memory=1
EOF

sysctl --system >/dev/null || true

cat >/etc/sudoers.d/90-"${TARGET_USER}"-nopasswd <<EOF
${TARGET_USER} ALL=(ALL) NOPASSWD:ALL
EOF
chmod 0440 /etc/sudoers.d/90-"${TARGET_USER}"-nopasswd

usermod -aG docker "${TARGET_USER}"
systemctl enable docker
systemctl restart docker

docker --version
