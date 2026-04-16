# Cloudreve Swarm Parallels 3 节点真实联调全记录

这份文档不是通用介绍，而是这次真实联调的完整复盘。目标有两个：

- 下次继续联调时，直接按本文从头回放，不用再从多份文档里拼步骤。
- 你自己要复刻一遍时，可以按本文的命令顺序执行。

如果你只想看通用上线与运维规范，再看：

- `docs/docker-swarm-operations-manual.md`
- `docs/docker-swarm-deployment.md`
- `docs/docker-swarm-production-checklist.md`

## 1. 本次联调环境与最终状态

### 宿主环境

- 宿主机：macOS + Parallels Desktop
- Ubuntu ISO：`/Users/fuyb/Desktop/ubuntu-22.04.5-live-server-amd64.iso`
- 虚拟机系统：Ubuntu Server `22.04.5`
- Docker 版本：`25.0.4`，对应 apt 版本 `5:25.0.4-1~ubuntu.22.04~jammy`
- 时区：`Asia/Shanghai`

### 3 个真实 Swarm 节点

- `u22-swarm-mgr-11` / `10.37.129.11` / manager / leader
- `u22-swarm-wkr-12` / `10.37.129.12` / worker
- `u22-swarm-wkr-13` / `10.37.129.13` / worker

### 本次使用的 stack 名称

- Cloudreve 主业务栈：`cloudreve-prl3`
- 统一认证栈：`authverse-prl3`
- 私有仓库栈：`cloudreve-registry-prl3`

### 当前在线结果

当前在线服务已经核对过：

- `cloudreve-prl3`
- `authverse-prl3`
- `cloudreve-registry-prl3`

当前关键入口：

- Cloudreve 主入口：`https://10.37.129.11:28081`
- Cloudreve 从入口：`https://10.37.129.11:28082`
- Authverse：`https://10.37.129.11:28080`
- MinIO：`https://10.37.129.11:29000`
- MinIO Console：`https://10.37.129.11:29001`
- Elasticsearch：`https://10.37.129.11:29200`
- Kafka UI：`https://10.37.129.11:28089`
- Tika：`https://10.37.129.11:29998`
- OnlyOffice：`https://10.37.129.11:28090`
- Pgpool：`10.37.129.11:25432`
- Redis Proxy：`10.37.129.11:26380`
- 私有仓库地址：`10.37.129.11:15000`

当前 `curl -k https://10.37.129.11:28081/api/v4/site/ping` 返回：

```json
{"code":0,"data":"4.15.0","msg":""}
```

## 2. 这次联调保留下来的关键文件

这几个文件就是下次继续联调时最应该先看的落点：

- 环境变量：`.env.swarm.parallels-3node-test`
- Parallels 自动装机脚本：`docker/swarm/create-parallels-ubuntu-cluster.sh`
- VM 首次启动装 Docker 脚本：`docker/swarm/ubuntu-provision-docker.sh`
- 统一发布脚本：`docker/swarm/deploy-stack.sh`
- 私有仓库发布脚本：`docker/swarm/publish-private-images.sh`
- 统一认证构建脚本：`docker/swarm/build-auth-images.sh`
- 统一认证数据库初始化脚本：`docker/swarm/init-authverse-db.sh`
- PKI 生成脚本：`docker/swarm/generate-swarm-pki.sh`
- 节点私有仓库准备脚本：`docker/swarm/prepare-private-registry.sh`
- 宿主机 bind 路径准备脚本：`docker/swarm/prepare-bind-paths.sh`
- 共享资产同步脚本：`docker/swarm/sync-swarm-assets.sh`
- Cloudreve 路由修复：`routers/router.go`

这次联调额外沉淀下来的临时产物：

- VM 安装日志目录：`.tmp/parallels-ubuntu-cluster/logs`
- 自动生成的 autoinstall ISO：`.tmp/parallels-ubuntu-cluster/isos`
- 渲染后的 stack 配置：
  - `.tmp/cloudreve-registry-prl3-resolved.yaml`
  - `.tmp/cloudreve-prl3-resolved.yaml`
  - `.tmp/authverse-prl3-resolved.yaml`
- 压测脚本：
  - `.tmp/run-cloudreve-stress-monitor.sh`
  - `.tmp/run-cloudreve-stress-ab.sh`

## 3. 脚本与执行位置总表

| 脚本 | 在哪里执行 | 作用 |
| --- | --- | --- |
| `docker/swarm/create-parallels-ubuntu-cluster.sh` | 本机 macOS | 重建 autoinstall ISO、创建 3 台 Parallels VM、自动初始化 Swarm |
| `docker/swarm/ubuntu-provision-docker.sh` | VM 首次启动时由 systemd 自动执行 | 安装 Docker `25.0.4`、写 sysctl、启用日志轮转 |
| `docker/swarm/generate-swarm-pki.sh` | 本机 macOS | 生成默认 CA、服务证书、Java truststore |
| `docker/swarm/sync-swarm-assets.sh` | 本机 macOS | 同步 `.env`、PKI、共享字体到 3 台 Linux |
| `docker/swarm/prepare-private-registry.sh` | 每台 Linux 节点本机视角 | 写入 Docker 私有仓库信任配置 |
| `docker/swarm/prepare-bind-paths.sh` | 每台 Linux 节点本机视角 | 预创建 bind 目录并修正权限 |
| `docker/swarm/deploy-stack.sh` | 本机 macOS，`DOCKER_HOST=ssh://ubuntu@10.37.129.11` | 渲染并部署 registry / foundation / infra / cloudreve / auth |
| `docker/swarm/publish-private-images.sh` | 本机 macOS | 把本机现有镜像重打 tag 并 push 到私有仓库 |
| `docker/swarm/build-auth-images.sh` | 本机 macOS | 构建 `authverse-web`、`authverse-backend` |
| `docker/swarm/init-authverse-db.sh` | 本机 macOS，`DOCKER_HOST=ssh://ubuntu@10.37.129.11` | 初始化 `authverse` 数据库 |
| `.tmp/run-cloudreve-stress-monitor.sh` | 本机 macOS | 监控节点、服务副本、`pgpool` |
| `.tmp/run-cloudreve-stress-ab.sh` | 本机 macOS | 对 Cloudreve 主入口执行 `ab` 压测 |

## 4. 本次联调的实际执行顺序

按最终收敛后的步骤，完整顺序如下：

1. 重新制作带 `autoinstall + nocloud` 的 Ubuntu ISO。
2. 用 Parallels 一次性创建 3 台 Ubuntu VM。
3. 首次启动自动安装 Docker `25.0.4`，并自动初始化 Swarm。
4. 规划节点角色与标签。
5. 准备 `.env.swarm.parallels-3node-test`。
6. 生成 TLS 证书和默认 CA。
7. 同步 `.env`、PKI、字体到 3 台 Linux。
8. 在每台节点写入私有仓库信任配置。
9. 在每台节点创建 bind 挂载目录。
10. 部署 `registry:2` 私有仓库栈。
11. 把本机已有镜像全部 re-tag 后推到私有仓库。
12. 构建并推送 `authverse-web`、`authverse-backend`。
13. 分步部署 `foundation -> infra -> cloudreve -> auth`。
14. 初始化 Authverse 数据库。
15. 做真实压测，监控节点、服务副本和 `pgpool`。
16. 发现 `/api/v4/site/ping` 健康检查过重，修改 Cloudreve 路由。
17. 重建 Cloudreve 镜像并推到私有仓库。
18. 重新滚动发布 Cloudreve 栈，再次压测验证。

## 5. 第一步：重新构建 ISO 与创建 3 台 Parallels VM

### 实际使用的脚本

- `docker/swarm/create-parallels-ubuntu-cluster.sh`
- `docker/swarm/ubuntu-provision-docker.sh`

### 这一步脚本内部做了什么

`create-parallels-ubuntu-cluster.sh` 不是简单开 VM，它会完整做完下面这些动作：

1. 从原始 Ubuntu ISO 解包出基础树到 `.tmp/parallels-ubuntu-cluster/base-iso`。
2. 为每个节点生成一份新的 `grub.cfg`，默认走 `autoinstall`。
3. 为每个节点写入 `nocloud/meta-data` 和 `nocloud/user-data`。
4. 把 `docker/swarm/ubuntu-provision-docker.sh` 注入 ISO。
5. 在 `user-data` 里固定：
   - 主机名
   - 用户名和密码
   - 时区 `Asia/Shanghai`
   - SSH 公钥
   - 两张网卡
   - 固定 cluster 网段 IP
6. 通过 `late-commands` 写入 `cloudreve-firstboot.service`。
7. 首次开机后由 `cloudreve-firstboot.service` 调 `ubuntu-provision-docker.sh`：
   - 安装 Docker `25.0.4`
   - 打开 Docker 开机启动
   - 写入 Docker 日志轮转配置
   - 写入 `vm.max_map_count=262144`
   - 写入 `vm.overcommit_memory=1`
   - 把默认用户加入 `docker` 组
8. 使用 `prlctl` 创建 VM、网卡、磁盘、串口日志。
9. 启动 VM，等待 Ubuntu 自动安装结束并自动关机。
10. 再次切换到磁盘启动，等待 SSH 和 Docker 就绪。
11. 如果 `INIT_SWARM=1`，脚本还会自动：
   - 在 manager 执行 `docker swarm init`
   - 让两个 worker 执行 `docker swarm join`

### 节点命名与 IP 规划

当前脚本里已经固化了这组命名和 IP：

- `u22-swarm-mgr-11` -> `10.37.129.11`
- `u22-swarm-wkr-12` -> `10.37.129.12`
- `u22-swarm-wkr-13` -> `10.37.129.13`

这个命名方式和你习惯一致：

- 角色在前面
- 最后一段直接带 IP 最后一位

这样 `docker node ls`、`docker service ps`、容器日志都很好定位。

### 实际执行命令

从仓库根目录执行：

```bash
ISO_PATH="/Users/fuyb/Desktop/ubuntu-22.04.5-live-server-amd64.iso" \
DOCKER_APT_VERSION="5:25.0.4-1~ubuntu.22.04~jammy" \
NODE_INDEXES="0,1,2" \
VM_CPUS="2" \
VM_MEMORY_MB="4096" \
VM_DISK_MB="40960" \
FORCE_RECREATE="1" \
docker/swarm/create-parallels-ubuntu-cluster.sh
```

如果只想先做 1 台烟测，可改成：

```bash
NODE_INDEXES="0" docker/swarm/create-parallels-ubuntu-cluster.sh
```

### 这一步完成后如何核对

看下面几个落点：

- 串口安装日志：`.tmp/parallels-ubuntu-cluster/logs/*.serial.log`
- 自动生成的 ISO：`.tmp/parallels-ubuntu-cluster/isos/*.iso`
- manager 节点状态：

```bash
ssh ubuntu@10.37.129.11 'sudo docker node ls'
```

本次实际状态是：

```text
u22-swarm-mgr-11|Ready|Active|Leader
u22-swarm-wkr-12|Ready|Active|
u22-swarm-wkr-13|Ready|Active|
```

## 6. 第二步：规划 3 个测试节点上的角色和标签

这一步不是理论方案，而是当前在线集群的真实标签分布。

### 当前真实标签

`u22-swarm-mgr-11`：

- `cloudreve.master=true`
- `cloudreve.proxy1=true`
- `cloudreve.registry=true`
- `cloudreve.pg1=true`
- `cloudreve.redis1=true`
- `cloudreve.es1=true`
- `cloudreve.kafka1=true`
- `cloudreve.minio1=true`
- `cloudreve.kafka-ui=true`
- `cloudreve.auth-web=true`

`u22-swarm-wkr-12`：

- `cloudreve.slave=true`
- `cloudreve.proxy2=true`
- `cloudreve.pg2=true`
- `cloudreve.redis2=true`
- `cloudreve.es2=true`
- `cloudreve.kafka2=true`
- `cloudreve.minio2=true`
- `cloudreve.minio4=true`
- `cloudreve.auth-backend=true`
- `cloudreve.tika=true`

`u22-swarm-wkr-13`：

- `cloudreve.proxy3=true`
- `cloudreve.pg3=true`
- `cloudreve.redis3=true`
- `cloudreve.es3=true`
- `cloudreve.kafka3=true`
- `cloudreve.minio3=true`
- `cloudreve.onlyoffice=true`

### 这样规划的原因

- 3 台机器上必须各自承接 1 个 `postgresql-*`
- 3 台机器上必须各自承接 1 个 `redis-*`
- 3 台机器上必须各自承接 1 个 `elasticsearch-*`
- 3 台机器上必须各自承接 1 个 `kafka-*`
- `MinIO` 需要 4 个数据节点，但这里只有 3 台机器，所以把 `minio-4` 额外压到 `wkr-12`
- 3 个公共代理入口拆成了 `proxy1 / proxy2 / proxy3`
- `authverse-web` 放 manager，`authverse-backend` 放 `wkr-12`
- `OnlyOffice` 单独压到 `wkr-13`
- `Tika` 放 `wkr-12`

### 这一步实际执行命令

在 manager 上执行：

```bash
sudo docker node update --label-add cloudreve.master=true u22-swarm-mgr-11
sudo docker node update --label-add cloudreve.proxy1=true u22-swarm-mgr-11
sudo docker node update --label-add cloudreve.registry=true u22-swarm-mgr-11
sudo docker node update --label-add cloudreve.pg1=true u22-swarm-mgr-11
sudo docker node update --label-add cloudreve.redis1=true u22-swarm-mgr-11
sudo docker node update --label-add cloudreve.es1=true u22-swarm-mgr-11
sudo docker node update --label-add cloudreve.kafka1=true u22-swarm-mgr-11
sudo docker node update --label-add cloudreve.minio1=true u22-swarm-mgr-11
sudo docker node update --label-add cloudreve.kafka-ui=true u22-swarm-mgr-11
sudo docker node update --label-add cloudreve.auth-web=true u22-swarm-mgr-11

sudo docker node update --label-add cloudreve.slave=true u22-swarm-wkr-12
sudo docker node update --label-add cloudreve.proxy2=true u22-swarm-wkr-12
sudo docker node update --label-add cloudreve.pg2=true u22-swarm-wkr-12
sudo docker node update --label-add cloudreve.redis2=true u22-swarm-wkr-12
sudo docker node update --label-add cloudreve.es2=true u22-swarm-wkr-12
sudo docker node update --label-add cloudreve.kafka2=true u22-swarm-wkr-12
sudo docker node update --label-add cloudreve.minio2=true u22-swarm-wkr-12
sudo docker node update --label-add cloudreve.minio4=true u22-swarm-wkr-12
sudo docker node update --label-add cloudreve.auth-backend=true u22-swarm-wkr-12
sudo docker node update --label-add cloudreve.tika=true u22-swarm-wkr-12

sudo docker node update --label-add cloudreve.proxy3=true u22-swarm-wkr-13
sudo docker node update --label-add cloudreve.pg3=true u22-swarm-wkr-13
sudo docker node update --label-add cloudreve.redis3=true u22-swarm-wkr-13
sudo docker node update --label-add cloudreve.es3=true u22-swarm-wkr-13
sudo docker node update --label-add cloudreve.kafka3=true u22-swarm-wkr-13
sudo docker node update --label-add cloudreve.minio3=true u22-swarm-wkr-13
sudo docker node update --label-add cloudreve.onlyoffice=true u22-swarm-wkr-13
```

## 7. 第三步：准备 `.env.swarm.parallels-3node-test`

### 这次联调用的环境变量文件

直接使用：

```bash
.env.swarm.parallels-3node-test
```

如果要按这个环境重复执行，推荐：

```bash
export ENV_FILE=.env.swarm.parallels-3node-test
```

### 这份 env 的核心设计

这份文件已经把 3 节点联调需要的关键点全部收敛好了：

- 所有镜像都拆成：
  - `*_LOCAL_IMAGE`
  - `*_REMOTE_IMAGE`
  - `*_IMAGE`
- `SWARM_IMAGE_SOURCE=remote`
  - Swarm 运行时统一从私有仓库拉镜像
- 业务 overlay：
  - `SWARM_OVERLAY_ENCRYPT=true`
  - `SWARM_OVERLAY_ATTACHABLE=false`
- 所有容器统一：
  - `SWARM_TIMEZONE=Asia/Shanghai`
  - Docker `json-file` 日志轮转
- 证书目录：
  - `SWARM_PKI_MOUNT_SOURCE=/tmp/cloudreve-prl3-assets/pki`
- 共享字体目录：
  - `SHARED_CUSTOM_FONTS_MOUNT_SOURCE=/tmp/cloudreve-prl3-assets/fonts`
- `PG / Redis / MinIO / ES / Kafka / Cloudreve runtime` 全部走宿主机 bind
- 所有对外端口都不是默认端口
- 所有 stack 名称都已经固定成 `*-prl3`
- 所有节点约束已经绑到这次 3 节点的标签设计

### 这份 env 最关键的变量分组

最常要改的是下面这些：

- 镜像来源：
  - `SWARM_IMAGE_SOURCE`
  - `PRIVATE_REGISTRY_ADDR`
  - `*_LOCAL_IMAGE`
  - `*_REMOTE_IMAGE`
- 入口域名与端口：
  - `CLOUDREVE_SITE_URL`
  - `AUTHVERSE_PUBLIC_BASE_URL`
  - `*_HTTP_PORT`
- 证书与字体：
  - `SWARM_PKI_MOUNT_SOURCE`
  - `SHARED_CUSTOM_FONTS_MOUNT_SOURCE`
- bind 路径：
  - `PG_*_DATA_MOUNT_SOURCE`
  - `REDIS_*_DATA_MOUNT_SOURCE`
  - `MINIO_*_DATA_MOUNT_SOURCE`
  - `ELASTICSEARCH_*_DATA_MOUNT_SOURCE`
  - `KAFKA_*_DATA_MOUNT_SOURCE`
  - `CLOUDREVE_*_RUNTIME_MOUNT_SOURCE`
- 节点调度：
  - `*_NODE_CONSTRAINT`
- 敏感值：
  - `CLOUDREVE_SESSION_SECRET`
  - `POSTGRESQL_PASSWORD`
  - `REDIS_PASSWORD`
  - `MINIO_ROOT_PASSWORD`
  - `ONLYOFFICE_JWT_SECRET`

说明：

- 这些密码仍然保留在 env 里作为源值
- 真正部署时，`docker/swarm/deploy-stack.sh` 会自动转成 Docker `secret`

## 8. 第四步：统一下发 `.env`、证书、字体

### 实际使用的脚本

- `docker/swarm/generate-swarm-pki.sh`
- `docker/swarm/sync-swarm-assets.sh`

### 这一步做了什么

1. 在本机生成一套默认 CA。
2. 为以下入口签发证书：
   - cloudreve-master
   - cloudreve-slave
   - authverse
   - pgpool
   - redis-proxy
   - minio
   - elasticsearch
   - tika
   - onlyoffice
   - kafka
   - kafka-ui
3. 生成 Java truststore，供 JVM 服务直接信任这套 CA。
4. 把 `.env`、PKI、共享字体同步到 3 台 Linux。

### 实际执行命令

本机执行：

```bash
export ENV_FILE=.env.swarm.parallels-3node-test
export REMOTE_ENV_FILE=/tmp/.env.swarm.parallels-3node-test

docker/swarm/generate-swarm-pki.sh --env-file "$ENV_FILE" --force-certs

docker/swarm/sync-swarm-assets.sh \
  --env-file "$ENV_FILE" \
  --targets "ubuntu@10.37.129.11 ubuntu@10.37.129.12 ubuntu@10.37.129.13" \
  --remote-env-file "$REMOTE_ENV_FILE" \
  --apply
```

说明：

- 当前 `PRIVATE_REGISTRY_SCHEME=http`，所以 registry 本身不需要 Docker 证书信任链
- 但其他对外服务依然全部使用了这套自签 CA

## 9. 第五步：在 3 台 Linux 上准备私有仓库与 bind 挂载目录

### 实际使用的脚本

- `docker/swarm/prepare-private-registry.sh`
- `docker/swarm/prepare-bind-paths.sh`

### 为什么这一步必须在 Linux 节点本机视角完成

因为这两个脚本操作的是宿主机本身：

- `/etc/docker/daemon.json`
- `/etc/docker/certs.d`
- `/srv/cloudreve-prl3/...`
- `/tmp/cloudreve-prl3-assets/...`

所以一定要在节点本机执行，或者从本机通过 `ssh 'bash -s' < script` 的方式远程执行。

### 实际执行命令

本机执行：

```bash
export ENV_FILE=.env.swarm.parallels-3node-test
export REMOTE_ENV_FILE=/tmp/.env.swarm.parallels-3node-test

for host in 10.37.129.11 10.37.129.12 10.37.129.13; do
  ssh "ubuntu@${host}" \
    "sudo bash -s -- --env-file ${REMOTE_ENV_FILE} --apply --restart-docker" \
    < docker/swarm/prepare-private-registry.sh
done

for host in 10.37.129.11 10.37.129.12 10.37.129.13; do
  ssh "ubuntu@${host}" \
    "sudo bash -s -- --env-file ${REMOTE_ENV_FILE} --apply" \
    < docker/swarm/prepare-bind-paths.sh
done
```

### 这一步最终得到什么

- 每台机器都信任了 `10.37.129.11:15000`
- 每台机器都预创建了 bind 目录
- PG / Redis / MinIO / ES / Kafka / Tika / OnlyOffice / registry / Cloudreve runtime 不会再因为目录不存在直接起不来

## 10. 第六步：部署私有仓库并把所有镜像推进去

### 实际使用的脚本

- `docker/swarm/deploy-stack.sh`
- `docker/swarm/publish-private-images.sh`
- `docker/swarm/build-auth-images.sh`

### 这一步的总体原则

- Swarm 运行时全部走私有仓库
- 不允许 worker 再去外部拉取
- `publish-private-images.sh` 只负责把本机已经存在的镜像 tag + push
- 它不会自动去 Docker Hub 拉缺失镜像

### 实际执行顺序

#### 9.3.1 部署私有仓库栈

部署命令要打到 manager：

```bash
export DOCKER_HOST=ssh://ubuntu@10.37.129.11
docker/swarm/deploy-stack.sh registry \
  --env-file .env.swarm.parallels-3node-test \
  --stack-name cloudreve-registry-prl3
unset DOCKER_HOST
```

#### 9.3.2 先推统一认证构建基镜像

这一步在本机执行，本机 Docker 里必须已经有这些源镜像：

```bash
docker/swarm/publish-private-images.sh \
  --env-file .env.swarm.parallels-3node-test \
  --image-keys AUTHVERSE_WEB_BUILDER_BASE,AUTHVERSE_WEB_RUNTIME_BASE,AUTHVERSE_BACKEND_BUILDER_BASE,AUTHVERSE_BACKEND_RUNTIME_BASE
```

#### 9.3.3 构建统一认证镜像

```bash
docker/swarm/build-auth-images.sh --env-file .env.swarm.parallels-3node-test
```

#### 9.3.4 推整套业务镜像

```bash
docker/swarm/publish-private-images.sh --env-file .env.swarm.parallels-3node-test
```

### 本次实际使用的镜像策略

关键点：

- `TIKA_LOCAL_IMAGE=cloudreve/tika:3.2.3.0-full-unrar-charset`
- `TIKA_REMOTE_IMAGE=10.37.129.11:15000/cloudreve/tika:3.2.3.0-full-unrar-charset`
- `AUTHVERSE_WEB_REMOTE_IMAGE=10.37.129.11:15000/authverse/authverse-web:2024-local`
- `AUTHVERSE_BACKEND_REMOTE_IMAGE=10.37.129.11:15000/authverse/authverse-backend:2024-local`
- `POSTGRESQL_REPMGR_REMOTE_IMAGE=10.37.129.11:15000/bitnamilegacy/postgresql-repmgr:17.6.0-debian-12-r2`
- `PGPOOL_REMOTE_IMAGE=10.37.129.11:15000/bitnamilegacy/pgpool:4.6.3-debian-12-r0`
- `REDIS_REMOTE_IMAGE=10.37.129.11:15000/bitnamilegacy/redis:8.2.1-debian-12-r0`
- `MINIO_REMOTE_IMAGE=10.37.129.11:15000/bitnamilegacy/minio:2024.10.2-debian-12-r0`
- `KAFKA_REMOTE_IMAGE=10.37.129.11:15000/apache/kafka:4.2.0`
- `ELASTICSEARCH_REMOTE_IMAGE=10.37.129.11:15000/elasticsearch:8.12.2`
- `ONLYOFFICE_REMOTE_IMAGE=10.37.129.11:15000/onlyoffice/documentserver:8.2.2`

## 11. 第七步：分阶段部署业务栈

### 实际使用的脚本

- `docker/swarm/deploy-stack.sh`
- `docker/swarm/init-authverse-db.sh`

### 实际部署顺序

推荐按下面顺序，理由是依赖链更稳：

1. `foundation`
2. `infra`
3. `cloudreve`
4. `auth`
5. `init-authverse-db`

### 实际执行命令

```bash
export DOCKER_HOST=ssh://ubuntu@10.37.129.11

docker/swarm/deploy-stack.sh foundation \
  --env-file .env.swarm.parallels-3node-test \
  --stack-name cloudreve-prl3

docker/swarm/deploy-stack.sh infra \
  --env-file .env.swarm.parallels-3node-test \
  --stack-name cloudreve-prl3

docker/swarm/deploy-stack.sh cloudreve \
  --env-file .env.swarm.parallels-3node-test \
  --stack-name cloudreve-prl3

docker/swarm/deploy-stack.sh auth \
  --env-file .env.swarm.parallels-3node-test \
  --stack-name authverse-prl3

unset DOCKER_HOST

export DOCKER_HOST=ssh://ubuntu@10.37.129.11
docker/swarm/init-authverse-db.sh --env-file .env.swarm.parallels-3node-test
unset DOCKER_HOST
```

### 这一阶段完成后的真实服务分布

`cloudreve-prl3` 当前实际落点：

- `u22-swarm-mgr-11`
  - `cloudreve-master`
  - `cloudreve-master-proxy`
  - `postgresql-1`
  - `redis-1`
  - `elasticsearch-1`
  - `kafka-1`
  - `kafka-ui`
  - `kafka-ui-public`
  - `minio-1`
  - `minio-internal`
  - `minio`
  - `minio-init`
- `u22-swarm-wkr-12`
  - `cloudreve-slave`
  - `cloudreve-slave-proxy`
  - `postgresql-2`
  - `redis-2`
  - `redis-proxy-internal`
  - `redis-proxy`
  - `elasticsearch-2`
  - `kafka-2`
  - `kafka`
  - `minio-2`
  - `minio-4`
  - `tika`
  - `tika-proxy`
- `u22-swarm-wkr-13`
  - `postgresql-3`
  - `pgpool-internal`
  - `pgpool`
  - `redis-3`
  - `redis-sentinel`
  - `elasticsearch-3`
  - `elasticsearch-internal`
  - `elasticsearch`
  - `kafka-3`
  - `minio-3`
  - `onlyoffice`
  - `onlyoffice-public`
  - `onlyoffice-rabbitmq`

`authverse-prl3` 当前实际落点：

- `authverse-web` -> `u22-swarm-mgr-11`
- `authverse-backend` -> `u22-swarm-wkr-12`

`cloudreve-registry-prl3` 当前实际落点：

- `registry` -> `u22-swarm-mgr-11`

## 12. 第八步：真实联调时怎么验收

### 节点与副本

```bash
ssh ubuntu@10.37.129.11 'sudo docker node ls'
ssh ubuntu@10.37.129.11 'sudo docker stack services cloudreve-prl3'
ssh ubuntu@10.37.129.11 'sudo docker stack services authverse-prl3'
ssh ubuntu@10.37.129.11 'sudo docker stack services cloudreve-registry-prl3'
```

### 主入口健康检查

```bash
curl -k https://10.37.129.11:28081/api/v4/site/ping
```

当前结果：

```json
{"code":0,"data":"4.15.0","msg":""}
```

### Pgpool 三节点状态

```bash
ssh ubuntu@10.37.129.11 '
cid=$(sudo docker ps -q --filter label=com.docker.swarm.service.name=cloudreve-prl3_postgresql-1 | head -n1)
pwd=$(sudo docker exec "$cid" sh -lc "tr \"\\0\" \"\\n\" </proc/1/environ | sed -n \"s/^POSTGRESQL_PASSWORD=//p\" | head -n1")
sudo docker exec "$cid" sh -lc "PGPASSWORD=\"$pwd\" /opt/bitnami/postgresql/bin/psql -At -F \"|\" -h pgpool-internal -U cloudreve -d postgres -c \"show pool_nodes;\""
'
```

本次最终稳定结果：

```text
0|postgresql-1|5432|up|up|0.333333|primary|primary|26757|true|0|||2026-04-16 08:13:33
1|postgresql-2|5432|up|up|0.333333|standby|standby|0|false|0|||2026-04-16 08:13:33
2|postgresql-3|5432|up|up|0.333333|standby|standby|0|false|0|||2026-04-16 08:15:44
```

## 13. 第九步：正式压测时实际用了什么脚本

### 监控脚本

文件：

- `.tmp/run-cloudreve-stress-monitor.sh`

用途：

- 每 10 秒打印一次 `docker node ls`
- 每 10 秒打印一次 `docker stack services cloudreve-prl3`
- 每 10 秒打印一次 `show pool_nodes;`

### 压测脚本

文件：

- `.tmp/run-cloudreve-stress-ab.sh`

内容是：

```bash
url='https://10.37.129.11:28081/api/v4/site/ping'
ab -n 2000 -c 50 -k "$url"
ab -n 10000 -c 100 -k "$url"
ab -n 5000 -c 200 -k "$url"
```

### 本次压测最终结果

修复完成后的真实结果：

- `ab -n 2000 -c 50 -k`：`0` 失败，`492.09 req/s`
- `ab -n 10000 -c 100 -k`：`0` 失败，`579.26 req/s`
- `ab -n 5000 -c 200 -k`：`0` 失败，`132.92 req/s`

压测期间实际观察结论：

- 3 个 Swarm 节点一直 `Ready / Active`
- `cloudreve-prl3` 所有关键服务副本一直正常
- `pgpool show pool_nodes;` 一直是 3 个节点 `up|up`
- 没有出现节点掉线
- 没有出现 `pgpool` 后端掉线

## 14. 第十步：这次真实联调发现的问题与修复过程

### 发现的问题

正式压 `/api/v4/site/ping` 时，之前会出现：

- 延迟异常升高
- 入口日志出现 `499`
- `pgpool` 被无意义打压
- 健康检查居然走到了 Redis / PostgreSQL 这一整条用户态链路

### 根因

`/api/v4/site/ping` 原来挂在带 `Session + CurrentUser` 的 `v4` 路由组后面，
也就是它不是“纯健康检查”，而是会进入普通用户请求链路。

### 实际代码修复

修复文件：

- `routers/router.go`

最终做法：

- 新建无鉴权 `noAuth := r.Group(constants.APIPrefix)`
- 在 `siteNoAuth := noAuth.Group("site")` 下只保留 `CacheControl`
- 把 `siteNoAuth.GET("ping", controllers.Ping)` 放到这个无鉴权组
- 从旧的 `v4.Group("site")` 里删掉 `site.GET("ping", controllers.Ping)`

这次中间还踩过一个坑：

- 一度误写成 `noAuth.Group("v4/site")`
- 实际路径变成 `/api/v4/v4/site/ping`
- 导致容器健康检查 404

最终正确路径是：

- `/api/v4/site/ping`

### 修复后重新构建与发布

这一步不是单独脚本，而是手工校验代码后，再复用私有仓库发布脚本：

1. `gofmt`
2. `go test`
3. `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build`
4. 重新构建本地 `cloudreve/cloudreve:4.15.0`
5. 再执行：

```bash
docker/swarm/publish-private-images.sh \
  --env-file .env.swarm.parallels-3node-test \
  --image-keys CLOUDREVE
```

6. 最后重新部署 `cloudreve` 栈：

```bash
export DOCKER_HOST=ssh://ubuntu@10.37.129.11
docker/swarm/deploy-stack.sh cloudreve \
  --env-file .env.swarm.parallels-3node-test \
  --stack-name cloudreve-prl3
unset DOCKER_HOST
```

### 这次必须记住的正确与错误镜像 digest

错误 digest，不要再回退：

- `sha256:b4336182f474bc6c999f8fe9ea9f001c7abe8a2a79d9fd9670a513ff6c269756`

当前正确 digest：

- `10.37.129.11:15000/cloudreve/cloudreve@sha256:05b21ef817c3c217e3f6c79988f53332f9457ee6eaf2cb3d2a3aff6e0d447be9`

当前在线服务已经确认跑的是正确 digest。

## 15. 当前还没彻底收尾的问题

Cloudreve 当前仍有一个遗留项：

- 启动日志里还会出现
  `Static resource version mismatch [Current 4.14.0, Desired: 4.15.0]`

这不会影响 `/api/v4/site/ping` 和当前基础压测，但会影响：

- Web UI 联调
- 前端静态资源一致性
- 后续真实业务链路压测

所以下次继续联调时，优先级建议是：

1. 先把 Cloudreve 静态资源版本不一致处理掉。
2. 再做真实业务链路压测：
   - 登录
   - 文件列表
   - 上传
   - 下载
   - 预览

## 16. 下次继续联调时，最短恢复路径

### 如果 3 台 VM 还在

直接从这里开始：

1. 确认节点还在：

```bash
ssh ubuntu@10.37.129.11 'sudo docker node ls'
```

2. 确认关键栈还在：

```bash
ssh ubuntu@10.37.129.11 'sudo docker stack ls'
```

3. 确认主入口：

```bash
curl -k https://10.37.129.11:28081/api/v4/site/ping
```

4. 确认 `pgpool`：

```bash
ssh ubuntu@10.37.129.11 '
cid=$(sudo docker ps -q --filter label=com.docker.swarm.service.name=cloudreve-prl3_postgresql-1 | head -n1)
pwd=$(sudo docker exec "$cid" sh -lc "tr \"\\0\" \"\\n\" </proc/1/environ | sed -n \"s/^POSTGRESQL_PASSWORD=//p\" | head -n1")
sudo docker exec "$cid" sh -lc "PGPASSWORD=\"$pwd\" /opt/bitnami/postgresql/bin/psql -At -F \"|\" -h pgpool-internal -U cloudreve -d postgres -c \"show pool_nodes;\""
'
```

5. 然后再决定是否需要重新发版或继续压测。

### 如果需要整套重来

按本文完整顺序重新执行：

1. `create-parallels-ubuntu-cluster.sh`
2. 打标签
3. `generate-swarm-pki.sh`
4. `sync-swarm-assets.sh`
5. `prepare-private-registry.sh`
6. `prepare-bind-paths.sh`
7. `deploy-stack.sh registry`
8. `publish-private-images.sh`
9. `build-auth-images.sh`
10. `publish-private-images.sh`
11. `deploy-stack.sh foundation`
12. `deploy-stack.sh infra`
13. `deploy-stack.sh cloudreve`
14. `deploy-stack.sh auth`
15. `init-authverse-db.sh`
16. 压测与验收

## 17. 最后一段结论

这次 3 节点 Parallels 真实联调已经把下面几件事真正打通了：

- 从 Ubuntu ISO 自动安装开始，到 Swarm 三节点上线
- 从私有仓库起步，到所有运行镜像只走私有仓库
- 从 bind 目录准备，到 `PG / Redis / MinIO / ES / Kafka / Tika / OnlyOffice / Cloudreve runtime` 全部真实启动
- 从 Cloudreve 到 Authverse 的分栈部署
- 从证书与字体下发，到多节点一致性
- 从压测监控，到 `site/ping` 路由问题定位与修复

下次继续时，不需要再回忆“上次都干了什么”，直接按本文继续即可。
