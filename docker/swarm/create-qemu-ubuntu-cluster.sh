#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ISO_PATH="${ISO_PATH:-/Users/fuyb/Desktop/ubuntu-22.04.5-live-server-amd64.iso}"
WORK_DIR="${WORK_DIR:-$ROOT_DIR/.tmp/qemu-ubuntu-cluster}"
HTTP_PORT="${HTTP_PORT:-18080}"
VM_USER="${VM_USER:-ubuntu}"
VM_PASSWORD="${VM_PASSWORD:-CloudreveVm123!}"
DOCKER_APT_VERSION="${DOCKER_APT_VERSION:-5:25.0.4-1~ubuntu.22.04~jammy}"
VM_CPUS="${VM_CPUS:-2}"
VM_MEMORY_MB="${VM_MEMORY_MB:-3072}"
VM_DISK_GB="${VM_DISK_GB:-40}"
INSTALL_TIMEOUT_SEC="${INSTALL_TIMEOUT_SEC:-3600}"
QEMU_ACCEL="${QEMU_ACCEL:-hvf}"
CLUSTER_MCAST="${CLUSTER_MCAST:-230.10.10.10:12345}"
SSH_KEY_PATH="${SSH_KEY_PATH:-}"

VM_NAMES=(
  "u22-swarm-mgr-11"
  "u22-swarm-wkr-12"
  "u22-swarm-wkr-13"
)
VM_ROLES=(
  "manager"
  "worker"
  "worker"
)
VM_CLUSTER_IPS=(
  "10.99.0.11"
  "10.99.0.12"
  "10.99.0.13"
)
VM_SSH_PORTS=(
  "22261"
  "22262"
  "22263"
)
VM_WAN_MACS=(
  "52:54:00:31:00:11"
  "52:54:00:31:00:12"
  "52:54:00:31:00:13"
)
VM_CLUSTER_MACS=(
  "52:54:00:32:00:11"
  "52:54:00:32:00:12"
  "52:54:00:32:00:13"
)

SEED_ROOT="$WORK_DIR/http"
ISO_MOUNT="$WORK_DIR/iso-mount"
BOOT_ROOT="$WORK_DIR/boot"
HTTP_PID_FILE="$WORK_DIR/http.pid"
PROVISION_SCRIPT_SRC="$ROOT_DIR/docker/swarm/qemu-provision-docker.sh"
PROVISION_SCRIPT_DST="$SEED_ROOT/common/qemu-provision-docker.sh"

usage() {
  cat <<EOF
用法：
  docker/swarm/create-qemu-ubuntu-cluster.sh

可用环境变量：
  ISO_PATH                Ubuntu Server ISO 路径
  WORK_DIR                工作目录，默认 .tmp/qemu-ubuntu-cluster
  HTTP_PORT               autoinstall HTTP 服务端口，默认 18080
  VM_USER                 VM 默认用户，默认 ubuntu
  VM_PASSWORD             VM 默认密码
  DOCKER_APT_VERSION      Docker apt 版本，默认 5:25.0.4-1~ubuntu.22.04~jammy
  VM_CPUS                 每台 VM vCPU，默认 2
  VM_MEMORY_MB            每台 VM 内存 MB，默认 3072
  VM_DISK_GB              每台 VM 磁盘 GB，默认 40
  INSTALL_TIMEOUT_SEC     安装超时秒数，默认 3600
  QEMU_ACCEL              QEMU 加速器，默认 hvf
  CLUSTER_MCAST           VM 间二层网络 multicast，默认 230.10.10.10:12345
  SSH_KEY_PATH            指定要注入 VM 的公钥文件
EOF
}

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "缺少命令: $cmd" >&2
    exit 1
  fi
}

wait_for_pid_exit() {
  local pid="$1"
  local timeout="$2"
  local elapsed=0

  while kill -0 "$pid" >/dev/null 2>&1; do
    if (( elapsed >= timeout )); then
      return 1
    fi
    sleep 10
    elapsed=$((elapsed + 10))
  done

  return 0
}

wait_for_ssh() {
  local port="$1"
  local timeout="$2"
  local elapsed=0

  while (( elapsed < timeout )); do
    if ssh -o BatchMode=yes \
      -o ConnectTimeout=5 \
      -o StrictHostKeyChecking=no \
      -o UserKnownHostsFile=/dev/null \
      -i "$SSH_PRIVATE_KEY" \
      -p "$port" \
      "$VM_USER@127.0.0.1" "echo ok" >/dev/null 2>&1; then
      return 0
    fi
    sleep 10
    elapsed=$((elapsed + 10))
  done

  return 1
}

cleanup_http() {
  if [[ -f "$HTTP_PID_FILE" ]]; then
    local http_pid
    http_pid="$(cat "$HTTP_PID_FILE")"
    if kill -0 "$http_pid" >/dev/null 2>&1; then
      kill "$http_pid" >/dev/null 2>&1 || true
      wait "$http_pid" 2>/dev/null || true
    fi
    rm -f "$HTTP_PID_FILE"
  fi
}

cleanup_mount() {
  if mount | grep -q "on $ISO_MOUNT "; then
    hdiutil detach "$ISO_MOUNT" >/dev/null 2>&1 || true
  fi
}

cleanup() {
  cleanup_http
  cleanup_mount
}

trap cleanup EXIT

prepare_ssh_key() {
  local pub_path

  if [[ -n "$SSH_KEY_PATH" ]]; then
    pub_path="$SSH_KEY_PATH"
  elif [[ -f "$HOME/.ssh/id_ed25519.pub" ]]; then
    pub_path="$HOME/.ssh/id_ed25519.pub"
  elif [[ -f "$HOME/.ssh/id_rsa.pub" ]]; then
    pub_path="$HOME/.ssh/id_rsa.pub"
  else
    mkdir -p "$WORK_DIR/ssh"
    ssh-keygen -q -t ed25519 -N '' -f "$WORK_DIR/ssh/id_ed25519" >/dev/null
    pub_path="$WORK_DIR/ssh/id_ed25519.pub"
  fi

  if [[ "$pub_path" == *.pub ]]; then
    SSH_PUBLIC_KEY_PATH="$pub_path"
    SSH_PRIVATE_KEY="${pub_path%.pub}"
  else
    echo "SSH_KEY_PATH 必须是公钥文件 *.pub" >&2
    exit 1
  fi

  if [[ ! -f "$SSH_PRIVATE_KEY" ]]; then
    echo "找不到对应私钥: $SSH_PRIVATE_KEY" >&2
    exit 1
  fi

  SSH_PUBLIC_KEY="$(tr -d '\n' <"$SSH_PUBLIC_KEY_PATH")"
}

prepare_boot_assets() {
  local vmlinuz_src
  local initrd_src

  mkdir -p "$BOOT_ROOT" "$ISO_MOUNT"

  if [[ ! -f "$BOOT_ROOT/vmlinuz" || ! -f "$BOOT_ROOT/initrd" ]]; then
    hdiutil attach -nobrowse -readonly -mountpoint "$ISO_MOUNT" "$ISO_PATH" >/dev/null
    vmlinuz_src="$(find "$ISO_MOUNT/casper" -maxdepth 1 -type f -name 'vmlinuz*' | head -n1)"
    initrd_src="$(find "$ISO_MOUNT/casper" -maxdepth 1 -type f -name 'initrd*' | head -n1)"

    if [[ -z "$vmlinuz_src" || -z "$initrd_src" ]]; then
      echo "在 ISO 中找不到 casper/vmlinuz 或 initrd。" >&2
      exit 1
    fi

    cp "$vmlinuz_src" "$BOOT_ROOT/vmlinuz"
    cp "$initrd_src" "$BOOT_ROOT/initrd"
    hdiutil detach "$ISO_MOUNT" >/dev/null
  fi
}

start_http_server() {
  mkdir -p "$SEED_ROOT/common"
  cp "$PROVISION_SCRIPT_SRC" "$PROVISION_SCRIPT_DST"
  chmod +x "$PROVISION_SCRIPT_DST"

  if [[ -f "$HTTP_PID_FILE" ]]; then
    local existing_pid
    existing_pid="$(cat "$HTTP_PID_FILE")"
    if kill -0 "$existing_pid" >/dev/null 2>&1; then
      return 0
    fi
  fi

  nohup python3 -m http.server "$HTTP_PORT" \
    --bind 0.0.0.0 \
    --directory "$SEED_ROOT" \
    >"$WORK_DIR/http.log" 2>&1 &
  echo "$!" >"$HTTP_PID_FILE"
}

prepare_vm_seed() {
  local idx="$1"
  local vm_name="${VM_NAMES[$idx]}"
  local vm_dir="$WORK_DIR/$vm_name"
  local password_hash

  mkdir -p "$vm_dir"
  password_hash="$(openssl passwd -6 "$VM_PASSWORD")"

  cat >"$vm_dir/meta-data" <<EOF
instance-id: ${vm_name}
local-hostname: ${vm_name}
EOF

  : >"$vm_dir/vendor-data"

  cat >"$vm_dir/user-data" <<EOF
#cloud-config
autoinstall:
  version: 1
  refresh-installer:
    update: false
  locale: en_US.UTF-8
  keyboard:
    layout: us
  timezone: Asia/Shanghai
  identity:
    hostname: ${vm_name}
    username: ${VM_USER}
    password: "${password_hash}"
  ssh:
    install-server: true
    allow-pw: true
    authorized-keys:
      - ${SSH_PUBLIC_KEY}
  packages:
    - qemu-guest-agent
    - curl
    - ca-certificates
    - gnupg
    - lsb-release
    - jq
    - net-tools
    - openssh-server
    - vim
  network:
    version: 2
    ethernets:
      wan0:
        match:
          macaddress: "${VM_WAN_MACS[$idx]}"
        set-name: enwan0
        dhcp4: true
      cluster0:
        match:
          macaddress: "${VM_CLUSTER_MACS[$idx]}"
        set-name: encluster0
        dhcp4: false
        addresses:
          - ${VM_CLUSTER_IPS[$idx]}/24
  late-commands:
    - "curtin in-target --target=/target -- bash -lc 'curl -fsSL http://10.0.2.2:${HTTP_PORT}/common/qemu-provision-docker.sh -o /root/qemu-provision-docker.sh && chmod +x /root/qemu-provision-docker.sh && VM_USERNAME=${VM_USER} DOCKER_APT_VERSION=${DOCKER_APT_VERSION} /root/qemu-provision-docker.sh'"
EOF
}

ensure_disk() {
  local vm_name="$1"
  local vm_dir="$WORK_DIR/$vm_name"
  local disk_path="$vm_dir/disk.qcow2"

  if [[ ! -f "$disk_path" ]]; then
    qemu-img create -f qcow2 "$disk_path" "${VM_DISK_GB}G" >/dev/null
  fi
}

start_install_vm() {
  local idx="$1"
  local vm_name="${VM_NAMES[$idx]}"
  local vm_dir="$WORK_DIR/$vm_name"
  local ssh_port="${VM_SSH_PORTS[$idx]}"

  ensure_disk "$vm_name"
  rm -f "$vm_dir/install.pid"

  qemu-system-x86_64 \
    -name "${vm_name}-install" \
    -machine "q35,accel=${QEMU_ACCEL}" \
    -cpu host \
    -smp "${VM_CPUS}" \
    -m "${VM_MEMORY_MB}" \
    -drive "if=virtio,file=$vm_dir/disk.qcow2,format=qcow2" \
    -cdrom "$ISO_PATH" \
    -kernel "$BOOT_ROOT/vmlinuz" \
    -initrd "$BOOT_ROOT/initrd" \
    -append "console=ttyS0,115200n8 autoinstall ds=nocloud-net\\;s=http://10.0.2.2:${HTTP_PORT}/${vm_name}/ ---" \
    -nic "user,model=virtio-net-pci,hostfwd=tcp:127.0.0.1:${ssh_port}-:22,mac=${VM_WAN_MACS[$idx]}" \
    -netdev "socket,id=cluster,mcast=${CLUSTER_MCAST}" \
    -device "virtio-net-pci,netdev=cluster,mac=${VM_CLUSTER_MACS[$idx]}" \
    -device virtio-rng-pci \
    -display none \
    -monitor none \
    -serial "file:$vm_dir/install.serial.log" \
    -pidfile "$vm_dir/install.pid" \
    -daemonize \
    -no-reboot
}

start_runtime_vm() {
  local idx="$1"
  local vm_name="${VM_NAMES[$idx]}"
  local vm_dir="$WORK_DIR/$vm_name"
  local ssh_port="${VM_SSH_PORTS[$idx]}"

  rm -f "$vm_dir/runtime.pid"

  qemu-system-x86_64 \
    -name "${vm_name}" \
    -machine "q35,accel=${QEMU_ACCEL}" \
    -cpu host \
    -smp "${VM_CPUS}" \
    -m "${VM_MEMORY_MB}" \
    -drive "if=virtio,file=$vm_dir/disk.qcow2,format=qcow2" \
    -nic "user,model=virtio-net-pci,hostfwd=tcp:127.0.0.1:${ssh_port}-:22,mac=${VM_WAN_MACS[$idx]}" \
    -netdev "socket,id=cluster,mcast=${CLUSTER_MCAST}" \
    -device "virtio-net-pci,netdev=cluster,mac=${VM_CLUSTER_MACS[$idx]}" \
    -device virtio-rng-pci \
    -display none \
    -monitor none \
    -serial "file:$vm_dir/runtime.serial.log" \
    -pidfile "$vm_dir/runtime.pid" \
    -daemonize
}

verify_vm() {
  local idx="$1"
  local ssh_port="${VM_SSH_PORTS[$idx]}"
  local vm_name="${VM_NAMES[$idx]}"

  if ! wait_for_ssh "$ssh_port" 600; then
    echo "[$vm_name] SSH 未在预期时间内就绪。" >&2
    return 1
  fi

  ssh -o BatchMode=yes \
    -o ConnectTimeout=5 \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    -i "$SSH_PRIVATE_KEY" \
    -p "$ssh_port" \
    "$VM_USER@127.0.0.1" \
    "hostname && ip -4 addr show encluster0 && docker --version"
}

main() {
  if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
    exit 0
  fi

  require_cmd openssl
  require_cmd hdiutil
  require_cmd python3
  require_cmd qemu-img
  require_cmd qemu-system-x86_64
  require_cmd ssh
  require_cmd ssh-keygen

  if [[ ! -f "$ISO_PATH" ]]; then
    echo "找不到 ISO: $ISO_PATH" >&2
    exit 1
  fi

  mkdir -p "$WORK_DIR"

  prepare_ssh_key
  prepare_boot_assets
  start_http_server

  echo "ISO: $ISO_PATH"
  echo "工作目录: $WORK_DIR"
  echo "Docker 版本: $DOCKER_APT_VERSION"
  echo "VM 用户: $VM_USER"
  echo "SSH 私钥: $SSH_PRIVATE_KEY"

  for idx in "${!VM_NAMES[@]}"; do
    prepare_vm_seed "$idx"
    start_install_vm "$idx"
    echo "[${VM_NAMES[$idx]}] 已启动自动安装，SSH 端口 ${VM_SSH_PORTS[$idx]}"
  done

  for idx in "${!VM_NAMES[@]}"; do
    local vm_name="${VM_NAMES[$idx]}"
    local vm_dir="$WORK_DIR/$vm_name"
    local install_pid

    install_pid="$(cat "$vm_dir/install.pid")"
    echo "[${vm_name}] 等待安装完成..."
    if ! wait_for_pid_exit "$install_pid" "$INSTALL_TIMEOUT_SEC"; then
      echo "[${vm_name}] 安装超时，请检查 $vm_dir/install.serial.log" >&2
      exit 1
    fi

    start_runtime_vm "$idx"
    echo "[${vm_name}] 已切换到磁盘启动。"
  done

  for idx in "${!VM_NAMES[@]}"; do
    verify_vm "$idx"
  done

  echo
  echo "三台 Ubuntu VM 已启动完成："
  for idx in "${!VM_NAMES[@]}"; do
    echo "  ${VM_NAMES[$idx]}  role=${VM_ROLES[$idx]}  ssh=-p ${VM_SSH_PORTS[$idx]}  cluster_ip=${VM_CLUSTER_IPS[$idx]}"
  done
}

main "$@"
