#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ISO_PATH="${ISO_PATH:-/Users/fuyb/Desktop/ubuntu-22.04.5-live-server-amd64.iso}"
WORK_DIR="${WORK_DIR:-$ROOT_DIR/.tmp/parallels-ubuntu-cluster}"
PARALLELS_VM_HOME="${PARALLELS_VM_HOME:-$HOME/Parallels}"
VM_USER="${VM_USER:-ubuntu}"
VM_PASSWORD="${VM_PASSWORD:-CloudreveVm123!}"
DOCKER_APT_VERSION="${DOCKER_APT_VERSION:-5:25.0.4-1~ubuntu.22.04~jammy}"
VM_CPUS="${VM_CPUS:-2}"
VM_MEMORY_MB="${VM_MEMORY_MB:-4096}"
VM_DISK_MB="${VM_DISK_MB:-40960}"
INSTALL_TIMEOUT_SEC="${INSTALL_TIMEOUT_SEC:-5400}"
SSH_TIMEOUT_SEC="${SSH_TIMEOUT_SEC:-900}"
DOCKER_TIMEOUT_SEC="${DOCKER_TIMEOUT_SEC:-1800}"
HOST_ONLY_NET="${HOST_ONLY_NET:-Host-Only}"
FORCE_RECREATE="${FORCE_RECREATE:-0}"
INIT_SWARM="${INIT_SWARM:-1}"
NODE_INDEXES="${NODE_INDEXES:-0,1,2}"
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
  "10.37.129.11"
  "10.37.129.12"
  "10.37.129.13"
)
VM_SHARED_MACS=(
  "00:1C:42:55:10:11"
  "00:1C:42:55:10:12"
  "00:1C:42:55:10:13"
)
VM_CLUSTER_MACS=(
  "00:1C:42:66:10:11"
  "00:1C:42:66:10:12"
  "00:1C:42:66:10:13"
)

BASE_ISO_TREE="$WORK_DIR/base-iso"
BASE_ISO_READY_FILE="$WORK_DIR/.base-iso-ready"
ISO_BUILD_ROOT="$WORK_DIR/iso-build"
ISO_OUTPUT_DIR="$WORK_DIR/isos"
LOG_DIR="$WORK_DIR/logs"
PROVISION_SCRIPT_SRC="$ROOT_DIR/docker/swarm/ubuntu-provision-docker.sh"

usage() {
  cat <<EOF
用法：
  docker/swarm/create-parallels-ubuntu-cluster.sh

可用环境变量：
  ISO_PATH             Ubuntu Server ISO 路径
  WORK_DIR             工作目录，默认 .tmp/parallels-ubuntu-cluster
  PARALLELS_VM_HOME    Parallels 虚拟机目录，默认 \$HOME/Parallels
  VM_USER              VM 默认用户，默认 ubuntu
  VM_PASSWORD          VM 默认密码
  DOCKER_APT_VERSION   Docker apt 版本，默认 5:25.0.4-1~ubuntu.22.04~jammy
  VM_CPUS              每台 VM vCPU，默认 2
  VM_MEMORY_MB         每台 VM 内存 MB，默认 4096
  VM_DISK_MB           每台 VM 磁盘 MB，默认 40960
  INSTALL_TIMEOUT_SEC  安装超时秒数，默认 5400
  SSH_TIMEOUT_SEC      SSH 就绪超时秒数，默认 900
  DOCKER_TIMEOUT_SEC   首次引导安装 Docker 的超时秒数，默认 1800
  HOST_ONLY_NET        Parallels Host-Only 网络名，默认 Host-Only
  FORCE_RECREATE       已存在同名 VM 时是否删除重建，默认 0
  INIT_SWARM           是否自动初始化 Swarm，默认 1
  NODE_INDEXES         需要创建的节点索引，默认 0,1,2；烟测可用 0
  SSH_KEY_PATH         指定要注入 VM 的公钥文件
EOF
}

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "缺少命令: $cmd" >&2
    exit 1
  fi
}

lower_text() {
  printf '%s' "$1" | tr '[:upper:]' '[:lower:]'
}

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

  if [[ "$pub_path" != *.pub ]]; then
    echo "SSH_KEY_PATH 必须是公钥文件 *.pub" >&2
    exit 1
  fi

  SSH_PUBLIC_KEY_PATH="$pub_path"
  SSH_PRIVATE_KEY="${pub_path%.pub}"

  if [[ ! -f "$SSH_PRIVATE_KEY" ]]; then
    echo "找不到对应私钥: $SSH_PRIVATE_KEY" >&2
    exit 1
  fi

  SSH_PUBLIC_KEY="$(tr -d '\n' <"$SSH_PUBLIC_KEY_PATH")"
}

selected_indexes() {
  local value="$NODE_INDEXES"
  local part

  IFS=',' read -r -a SELECTED_INDEXES <<<"$value"
  for part in "${SELECTED_INDEXES[@]}"; do
    if [[ ! "$part" =~ ^[0-2]$ ]]; then
      echo "NODE_INDEXES 仅支持 0,1,2 的逗号组合，当前值: $NODE_INDEXES" >&2
      exit 1
    fi
  done
}

prepare_base_iso_tree() {
  if [[ -f "$BASE_ISO_READY_FILE" ]]; then
    return 0
  fi

  rm -rf "$BASE_ISO_TREE"
  mkdir -p "$BASE_ISO_TREE"
  xorriso -osirrox on -indev "$ISO_PATH" -extract / "$BASE_ISO_TREE" >/dev/null 2>&1
  chmod -R u+w "$BASE_ISO_TREE"
  touch "$BASE_ISO_READY_FILE"
}

write_grub_cfg() {
  local vm_tree="$1"

  cat >"$vm_tree/boot/grub/grub.cfg" <<'EOF'
set timeout=3

loadfont unicode

set menu_color_normal=white/black
set menu_color_highlight=black/light-gray

menuentry "Autoinstall Ubuntu Server" {
	set gfxpayload=keep
	linux	/casper/vmlinuz autoinstall ds=nocloud\;s=/cdrom/nocloud/ console=ttyS0,115200n8 ---
	initrd	/casper/initrd
}
menuentry "Autoinstall Ubuntu Server with the HWE kernel" {
	set gfxpayload=keep
	linux	/casper/hwe-vmlinuz autoinstall ds=nocloud\;s=/cdrom/nocloud/ console=ttyS0,115200n8 ---
	initrd	/casper/hwe-initrd
}
grub_platform
if [ "$grub_platform" = "efi" ]; then
menuentry 'Boot from next volume' {
	exit 1
}
menuentry 'UEFI Firmware Settings' {
	fwsetup
}
else
menuentry 'Test memory' {
	linux16 /boot/memtest86+.bin
}
fi
EOF
}

build_vm_iso() {
  local idx="$1"
  local vm_name="${VM_NAMES[$idx]}"
  local vm_tree="$ISO_BUILD_ROOT/$vm_name"
  local vm_iso="$ISO_OUTPUT_DIR/$vm_name-autoinstall.iso"
  local password_hash

  rm -rf "$vm_tree" "$vm_iso"
  mkdir -p "$vm_tree" "$ISO_OUTPUT_DIR"
  rsync -a "$BASE_ISO_TREE/" "$vm_tree/"
  chmod -R u+w "$vm_tree"

  write_grub_cfg "$vm_tree"

  mkdir -p "$vm_tree/nocloud"
  cp "$PROVISION_SCRIPT_SRC" "$vm_tree/nocloud/ubuntu-provision-docker.sh"
  chmod 0755 "$vm_tree/nocloud/ubuntu-provision-docker.sh"
  password_hash="$(openssl passwd -6 "$VM_PASSWORD")"

  cat >"$vm_tree/nocloud/meta-data" <<EOF
instance-id: ${vm_name}
local-hostname: ${vm_name}
EOF

  cat >"$vm_tree/nocloud/user-data" <<EOF
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
  storage:
    layout:
      name: direct
  packages:
    - ca-certificates
    - curl
    - gnupg
    - jq
    - lsb-release
    - net-tools
    - openssh-server
    - vim
  network:
    version: 2
    ethernets:
      wan0:
        match:
          macaddress: "$(lower_text "${VM_SHARED_MACS[$idx]}")"
        set-name: enwan0
        dhcp4: true
      cluster0:
        match:
          macaddress: "$(lower_text "${VM_CLUSTER_MACS[$idx]}")"
        set-name: encluster0
        dhcp4: false
        addresses:
          - ${VM_CLUSTER_IPS[$idx]}/24
  late-commands:
    - cp /cdrom/nocloud/ubuntu-provision-docker.sh /target/root/ubuntu-provision-docker.sh
    - |
      cat >/target/etc/systemd/system/cloudreve-firstboot.service <<'EOUNIT'
      [Unit]
      Description=Cloudreve first boot provisioning
      Wants=network-online.target
      After=network-online.target
      ConditionPathExists=/root/ubuntu-provision-docker.sh
      ConditionPathExists=!/var/lib/cloudreve-firstboot.done

      [Service]
      Type=oneshot
      TimeoutStartSec=0
      ExecStart=/usr/bin/bash -lc 'chmod +x /root/ubuntu-provision-docker.sh && VM_USERNAME=${VM_USER} DOCKER_APT_VERSION=${DOCKER_APT_VERSION} /root/ubuntu-provision-docker.sh'
      ExecStartPost=/usr/bin/touch /var/lib/cloudreve-firstboot.done
      ExecStartPost=/usr/bin/systemctl disable cloudreve-firstboot.service
      StandardOutput=append:/var/log/cloudreve-firstboot.log
      StandardError=append:/var/log/cloudreve-firstboot.log

      [Install]
      WantedBy=multi-user.target
      EOUNIT
    - curtin in-target --target=/target -- systemctl enable cloudreve-firstboot.service
  shutdown: poweroff
EOF

  xorriso \
    -indev "$ISO_PATH" \
    -outdev "$vm_iso" \
    -blank as_needed \
    -update_r "$vm_tree" / \
    -boot_image any replay \
    -commit \
    -end \
    >/dev/null 2>&1
}

prl_mac() {
  local mac
  mac="$(lower_text "$1")"
  echo "${mac//:/}"
}

vm_exists() {
  local vm_name="$1"
  prlctl list -a | awk 'NR > 1 {print $NF}' | rg -x "$vm_name" >/dev/null 2>&1
}

ensure_vm_absent() {
  local vm_name="$1"

  if ! vm_exists "$vm_name"; then
    return 0
  fi

  if [[ "$FORCE_RECREATE" != "1" ]]; then
    echo "VM 已存在，请先删除或设置 FORCE_RECREATE=1: $vm_name" >&2
    exit 1
  fi

  prlctl stop "$vm_name" --kill >/dev/null 2>&1 || true
  prlctl delete "$vm_name" >/dev/null
  rm -rf "$PARALLELS_VM_HOME/$vm_name.pvm"
}

create_vm() {
  local idx="$1"
  local vm_name="${VM_NAMES[$idx]}"
  local vm_iso="$ISO_OUTPUT_DIR/$vm_name-autoinstall.iso"
  local vm_log="$LOG_DIR/$vm_name.serial.log"

  ensure_vm_absent "$vm_name"
  mkdir -p "$PARALLELS_VM_HOME" "$LOG_DIR"
  rm -f "$vm_log"

  prlctl create "$vm_name" -d ubuntu --dst "$PARALLELS_VM_HOME" --no-hdd >/dev/null
  prlctl set "$vm_name" --cpus "$VM_CPUS" --memsize "$VM_MEMORY_MB" >/dev/null
  prlctl set "$vm_name" --startup-view headless --autostop shutdown --on-window-close keep-running >/dev/null
  prlctl set "$vm_name" --sync-host-printers off --shared-profile off >/dev/null
  prlctl set "$vm_name" --shf-host-defined off --shf-host-automount off --shf-guest off --shf-guest-automount off >/dev/null
  prlctl set "$vm_name" --sh-app-host-to-guest off --sh-app-guest-to-host off --shared-clipboard off >/dev/null
  prlctl set "$vm_name" --video-adapter-type virtio --videosize 64 --3d-accelerate off >/dev/null
  prlctl set "$vm_name" --device-set net0 --type shared --adapter-type virtio --mac "$(prl_mac "${VM_SHARED_MACS[$idx]}")" >/dev/null
  prlctl set "$vm_name" --device-add hdd --size "$VM_DISK_MB" --iface sata >/dev/null
  prlctl set "$vm_name" --device-add cdrom --image "$vm_iso" >/dev/null
  prlctl set "$vm_name" --device-add net --type host-only --iface "$HOST_ONLY_NET" --adapter-type virtio --mac "$(prl_mac "${VM_CLUSTER_MACS[$idx]}")" >/dev/null
  prlctl set "$vm_name" --device-add serial --output "$vm_log" >/dev/null
  prlctl set "$vm_name" --device-set cdrom0 --disconnect >/dev/null 2>&1 || true
  prlctl set "$vm_name" --device-bootorder "cdrom1 hdd0 cdrom0" >/dev/null
}

wait_for_vm_state() {
  local vm_name="$1"
  local expect_state="$2"
  local timeout="$3"
  local elapsed=0
  local current_state

  while (( elapsed < timeout )); do
    current_state="$(prlctl status "$vm_name" | awk '{print $NF}')"
    if [[ "$current_state" == "$expect_state" ]]; then
      return 0
    fi
    sleep 10
    elapsed=$((elapsed + 10))
  done

  echo "[$vm_name] 等待状态 ${expect_state} 超时，当前状态: ${current_state}" >&2
  return 1
}

wait_for_ssh() {
  local ip="$1"
  local timeout="$2"
  local elapsed=0

  while (( elapsed < timeout )); do
    if ssh -o BatchMode=yes \
      -o ConnectTimeout=5 \
      -o StrictHostKeyChecking=no \
      -o UserKnownHostsFile=/dev/null \
      -i "$SSH_PRIVATE_KEY" \
      "$VM_USER@$ip" "echo ok" >/dev/null 2>&1; then
      return 0
    fi
    sleep 10
    elapsed=$((elapsed + 10))
  done

  echo "[$ip] SSH 未在预期时间内就绪。" >&2
  return 1
}

wait_for_docker() {
  local idx="$1"
  local timeout="$2"
  local ip="${VM_CLUSTER_IPS[$idx]}"
  local elapsed=0

  while (( elapsed < timeout )); do
    if ssh -o BatchMode=yes \
      -o ConnectTimeout=5 \
      -o StrictHostKeyChecking=no \
      -o UserKnownHostsFile=/dev/null \
      -i "$SSH_PRIVATE_KEY" \
      "$VM_USER@$ip" "sudo docker info >/dev/null 2>&1" >/dev/null 2>&1; then
      return 0
    fi
    sleep 10
    elapsed=$((elapsed + 10))
  done

  echo "[$ip] Docker 未在预期时间内完成安装。" >&2
  ssh -o BatchMode=yes \
    -o ConnectTimeout=5 \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    -i "$SSH_PRIVATE_KEY" \
    "$VM_USER@$ip" "sudo tail -n 200 /var/log/cloudreve-firstboot.log" >&2 || true
  return 1
}

start_install() {
  local vm_name="$1"
  prlctl start "$vm_name" >/dev/null
}

start_runtime() {
  local vm_name="$1"
  prlctl set "$vm_name" --device-bootorder "hdd0 cdrom1 cdrom0" >/dev/null
  prlctl start "$vm_name" >/dev/null
}

verify_vm() {
  local idx="$1"
  local ip="${VM_CLUSTER_IPS[$idx]}"

  wait_for_ssh "$ip" "$SSH_TIMEOUT_SEC"
  wait_for_docker "$idx" "$DOCKER_TIMEOUT_SEC"
  ssh -o BatchMode=yes \
    -o ConnectTimeout=5 \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    -i "$SSH_PRIVATE_KEY" \
    "$VM_USER@$ip" \
    "hostname && ip -4 addr show encluster0 && sudo docker --version && sudo docker info --format '{{.ServerVersion}} {{.Swarm.LocalNodeState}}'"
}

ssh_vm() {
  local idx="$1"
  shift
  ssh -o BatchMode=yes \
    -o ConnectTimeout=5 \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    -i "$SSH_PRIVATE_KEY" \
    "$VM_USER@${VM_CLUSTER_IPS[$idx]}" \
    "$@"
}

docker_vm() {
  local idx="$1"
  shift
  ssh_vm "$idx" "sudo docker $*"
}

init_swarm_cluster() {
  local idx
  local worker_token
  local manager_idx="${SELECTED_INDEXES[0]}"

  if [[ "${VM_ROLES[$manager_idx]}" != "manager" ]]; then
    echo "NODE_INDEXES 里必须包含 0 号 manager 节点，当前值: $NODE_INDEXES" >&2
    exit 1
  fi

  if [[ "$INIT_SWARM" != "1" ]]; then
    return 0
  fi

  if [[ "$(docker_vm "$manager_idx" "info --format '{{.Swarm.LocalNodeState}}'")" != "active" ]]; then
    docker_vm "$manager_idx" "swarm init --advertise-addr ${VM_CLUSTER_IPS[$manager_idx]} --data-path-addr ${VM_CLUSTER_IPS[$manager_idx]}" >/dev/null
  fi

  if [[ "${#SELECTED_INDEXES[@]}" -eq 1 ]]; then
    docker_vm "$manager_idx" "node ls"
    return 0
  fi

  worker_token="$(docker_vm "$manager_idx" "swarm join-token -q worker")"

  for idx in "${SELECTED_INDEXES[@]}"; do
    if [[ "${VM_ROLES[$idx]}" != "worker" ]]; then
      continue
    fi

    if [[ "$(docker_vm "$idx" "info --format '{{.Swarm.LocalNodeState}}'")" != "active" ]]; then
      docker_vm "$idx" "swarm join --token $worker_token --advertise-addr ${VM_CLUSTER_IPS[$idx]} --data-path-addr ${VM_CLUSTER_IPS[$idx]} ${VM_CLUSTER_IPS[$manager_idx]}:2377" >/dev/null
    fi
  done

  docker_vm "$manager_idx" "node ls"
}

main() {
  local idx
  local vm_name

  if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
    exit 0
  fi

  require_cmd openssl
  require_cmd prlctl
  require_cmd rsync
  require_cmd rg
  require_cmd ssh
  require_cmd ssh-keygen
  require_cmd xorriso

  if [[ ! -f "$ISO_PATH" ]]; then
    echo "找不到 ISO: $ISO_PATH" >&2
    exit 1
  fi

  mkdir -p "$WORK_DIR" "$ISO_BUILD_ROOT" "$ISO_OUTPUT_DIR" "$LOG_DIR"
  prepare_ssh_key
  selected_indexes
  prepare_base_iso_tree

  echo "ISO: $ISO_PATH"
  echo "工作目录: $WORK_DIR"
  echo "Parallels VM 目录: $PARALLELS_VM_HOME"
  echo "节点索引: $NODE_INDEXES"
  echo "Docker 版本: $DOCKER_APT_VERSION"
  echo "SSH 私钥: $SSH_PRIVATE_KEY"

  for idx in "${SELECTED_INDEXES[@]}"; do
    build_vm_iso "$idx"
    create_vm "$idx"
    echo "[${VM_NAMES[$idx]}] 已生成 autoinstall ISO 并完成 VM 配置。"
  done

  for idx in "${SELECTED_INDEXES[@]}"; do
    vm_name="${VM_NAMES[$idx]}"
    start_install "$vm_name"
    echo "[${vm_name}] 已启动自动安装。"
  done

  for idx in "${SELECTED_INDEXES[@]}"; do
    vm_name="${VM_NAMES[$idx]}"
    echo "[${vm_name}] 等待安装自动关机..."
    wait_for_vm_state "$vm_name" "stopped" "$INSTALL_TIMEOUT_SEC"
    start_runtime "$vm_name"
    echo "[${vm_name}] 已切换到磁盘启动。"
  done

  for idx in "${SELECTED_INDEXES[@]}"; do
    verify_vm "$idx"
  done

  init_swarm_cluster

  echo
  echo "Parallels Ubuntu 节点已就绪："
  for idx in "${SELECTED_INDEXES[@]}"; do
    echo "  ${VM_NAMES[$idx]}  role=${VM_ROLES[$idx]}  ip=${VM_CLUSTER_IPS[$idx]}"
  done
}

main "$@"
