# Cloudreve Docker Swarm 线上部署与运维手册

本文档面向“不熟悉 Swarm，但要把这套模板上线并长期运维”的场景。

对应文件：

- `docker-compose.swarm.yml`
- `docker-compose.swarm.single.yml`
- `docker-compose.swarm.foundation.yml`
- `docker-compose.swarm.registry.yml`
- `docker-compose.swarm.cluster.yml`
- `.env.swarm.example`
- `.env.swarm.prod-4x128g.example`
- `docker/swarm/deploy-private-registry.sh`
- `docker/swarm/deploy-stack.sh`
- `docker/swarm/export-swarm-images.sh`
- `docker/swarm/prepare-bind-paths.sh`
- `docker/swarm/prepare-bitnami-images.sh`
- `docker/swarm/prepare-private-registry.sh`
- `docker/swarm/publish-private-images.sh`
- `docker-compose.swarm.auth.yml`
- `docker/swarm/build-auth-images.sh`
- `docker/swarm/init-authverse-db.sh`
- `docker/swarm/deploy-auth-stack.sh`

如果你现在只想先跑起来，再回来看细节：

- 快速清单：`docs/docker-swarm-quickstart.md`
- 4 台 Linux 上线清单：`docs/docker-swarm-production-checklist.md`
- 部署说明：`docs/docker-swarm-deployment.md`
- 多 manager 下 `.env.swarm` 同步：`docs/docker-swarm-env-sync.md`
- 统一认证接入：`docs/docker-swarm-auth-deployment.md`

## 1. 先说结论

这套模板的生产目标不是“所有服务都随便堆到一台机器上”，而是：

- Cloudreve 主站 + 从站
- PostgreSQL 3 节点 + Pgpool
- Redis 3 节点 + Sentinel + Redis Proxy
- MinIO
- Elasticsearch
- Kafka
- Tika
- OnlyOffice 8

并且满足下面这些约束：

- PostgreSQL / Redis 默认使用宿主机物理目录
- Cloudreve 运行目录、MinIO、Elasticsearch、Tika 字体目录同时支持命名卷和宿主机绝对路径
- Cloudreve 默认文件存储使用 S3 兼容对象存储，不再默认写宿主机用户文件目录
- 支持单节点对象存储/检索模式，也支持 `SWARM_WITH_CLUSTER=yes` 的 MinIO / Elasticsearch / Kafka 集群模式
- 所有镜像都固定版本，不使用 `latest`

如果你的生产环境就是：

- `1` 个 manager
- `3` 个 worker
- 每台 `128GB` 内存
- 每台 `64` 线程

建议直接从 `.env.swarm.prod-4x128g.example` 开始。

## 2. 这些 YML 文件分别干什么

### 2.1 `docker-compose.swarm.registry.yml`

这是单独的私有仓库栈，只负责一件事：

- 在固定节点上跑 1 个 `registry:2`
- 让其它 Swarm 节点通过 `PRIVATE_REGISTRY_ADDR` 远程拉取自定义镜像

它不和主业务栈混在一起，原因很简单：

- `TIKA_IMAGE` 是自定义镜像
- 首次上线前必须先 push 到私有仓库
- 如果把私有仓库和主业务栈绑死在一起，首发顺序会更容易出错

### 2.2 `docker-compose.swarm.single.yml`

这是默认单节点全量模板。

`docker/swarm/deploy-stack.sh` 在未启用 `SWARM_WITH_CLUSTER=yes` 时会直接使用它，
一次起整套单节点服务：

- `cloudreve-master`
- `cloudreve-master-proxy`
- `postgresql-1`
- `pgpool`
- `redis-1`
- `redis-proxy`
- `minio`
- `minio-init`
- `elasticsearch`
- `kafka`
- `kafka-ui`
- `tika`
- `onlyoffice`

### 2.3 `docker-compose.swarm.yml`

这是 Cloudreve 业务层模板，只包含：

- `cloudreve-master`
- `cloudreve-master-proxy`
- `cloudreve-slave`
- `cloudreve-slave-proxy`

### 2.4 `docker-compose.swarm.foundation.yml`

这是多节点模式下共用的基础中间件模板，包含：

- `postgresql-1/2/3`
- `pgpool`
- `redis-1/2/3`
- `redis-sentinel`
- `redis-proxy`
- `tika`
- `onlyoffice`

这个文件里最重要的设计点：

- 所有资源限制都统一放在 `deploy.resources`
- 所有调度约束都统一放在 `deploy.placement.constraints`
- 所有挂载都统一成 `*_MOUNT_TYPE` + `*_MOUNT_SOURCE`
- PostgreSQL / Redis 默认就是 `bind`
- 其他运行目录默认 `volume`，但可以切换成 `bind`

### 2.5 `docker-compose.swarm.cluster.yml`

这是集群覆盖文件，只在下面两种情况生效：

- `.env.swarm` 里设置了 `SWARM_WITH_CLUSTER=yes`
- 或部署时执行 `docker/swarm/deploy-stack.sh --with-cluster`

它会在 `docker-compose.swarm.yml + docker-compose.swarm.foundation.yml` 的基础上再叠加：

- `minio-1/2/3/4` + `minio` 代理入口
- `elasticsearch-1/2/3` + `elasticsearch` 代理入口
- `kafka-1/2/3` + `kafka` 代理入口
- `kafka-ui`

其中 Kafka 明确启用了：

- `deploy.endpoint_mode: dnsrr`

原因：

- Kafka KRaft 固定节点不适合走 Swarm VIP
- 控制面必须尽量直接点到点

### 2.6 为什么不再保留 `docker-compose.swarm.bind.yml`

这个覆盖文件已经废弃，原因很直接：

- `docker-compose.swarm.yml`、`docker-compose.swarm.foundation.yml`、`docker-compose.swarm.auth.yml`
  都已经统一支持 `*_MOUNT_TYPE + *_MOUNT_SOURCE`
- PostgreSQL / Redis / Cloudreve 运行目录 / MinIO / Elasticsearch / Tika 字体 / authverse OIDC 密钥
  都不再需要额外叠加 compose
- 继续保留一个只服务单点场景的小覆盖文件，只会让部署顺序和文档变复杂

现在如果你要切到宿主机绝对路径，直接改 `.env.swarm` 即可，例如：

```env
TIKA_CUSTOM_FONTS_MOUNT_TYPE=bind
TIKA_CUSTOM_FONTS_MOUNT_SOURCE=/srv/cloudreve/tika-fonts
```

## 3. 推荐生产拓扑

### 3.1 推荐主机名

建议先把主机名定好，再让节点加入 Swarm：

- `cr-prod-mgr-1`
- `cr-prod-wkr-1`
- `cr-prod-wkr-2`
- `cr-prod-wkr-3`

这样下面这些命令会非常清楚：

```bash
docker node ls
docker stack ps cloudreve
docker service ps cloudreve_postgresql-1
```

### 3.2 推荐标签规划

`.env.swarm.prod-4x128g.example` 里的默认规划是：

- `cr-prod-mgr-1`
  - `cloudreve.master=true`
  - `cloudreve.edge=true`
  - `cloudreve.registry=true`
  - `cloudreve.minio1=true`
  - `cloudreve.tika=true`
  - `cloudreve.kafka-ui=true`
- `cr-prod-wkr-1`
  - `cloudreve.slave=true`
  - `cloudreve.edge=true`
  - `cloudreve.pg1=true`
  - `cloudreve.redis1=true`
  - `cloudreve.redis-sentinel=true`
  - `cloudreve.es1=true`
  - `cloudreve.kafka1=true`
  - `cloudreve.minio2=true`
- `cr-prod-wkr-2`
  - `cloudreve.slave=true`
  - `cloudreve.pg2=true`
  - `cloudreve.redis2=true`
  - `cloudreve.redis-sentinel=true`
  - `cloudreve.es2=true`
  - `cloudreve.kafka2=true`
  - `cloudreve.minio3=true`
- `cr-prod-wkr-3`
  - `cloudreve.slave=true`
  - `cloudreve.edge=true`
  - `cloudreve.pg3=true`
  - `cloudreve.redis3=true`
  - `cloudreve.redis-sentinel=true`
  - `cloudreve.es3=true`
  - `cloudreve.kafka3=true`
  - `cloudreve.minio4=true`
  - `cloudreve.tika=true`

打标签命令示例：

```bash
docker node update --label-add cloudreve.master=true cr-prod-mgr-1
docker node update --label-add cloudreve.edge=true cr-prod-mgr-1
docker node update --label-add cloudreve.registry=true cr-prod-mgr-1
docker node update --label-add cloudreve.minio1=true cr-prod-mgr-1
docker node update --label-add cloudreve.tika=true cr-prod-mgr-1
docker node update --label-add cloudreve.kafka-ui=true cr-prod-mgr-1

docker node update --label-add cloudreve.slave=true cr-prod-wkr-1
docker node update --label-add cloudreve.edge=true cr-prod-wkr-1
docker node update --label-add cloudreve.pg1=true cr-prod-wkr-1
docker node update --label-add cloudreve.redis1=true cr-prod-wkr-1
docker node update --label-add cloudreve.redis-sentinel=true cr-prod-wkr-1
docker node update --label-add cloudreve.es1=true cr-prod-wkr-1
docker node update --label-add cloudreve.kafka1=true cr-prod-wkr-1
docker node update --label-add cloudreve.minio2=true cr-prod-wkr-1

docker node update --label-add cloudreve.slave=true cr-prod-wkr-2
docker node update --label-add cloudreve.pg2=true cr-prod-wkr-2
docker node update --label-add cloudreve.redis2=true cr-prod-wkr-2
docker node update --label-add cloudreve.redis-sentinel=true cr-prod-wkr-2
docker node update --label-add cloudreve.es2=true cr-prod-wkr-2
docker node update --label-add cloudreve.kafka2=true cr-prod-wkr-2
docker node update --label-add cloudreve.minio3=true cr-prod-wkr-2

docker node update --label-add cloudreve.slave=true cr-prod-wkr-3
docker node update --label-add cloudreve.edge=true cr-prod-wkr-3
docker node update --label-add cloudreve.pg3=true cr-prod-wkr-3
docker node update --label-add cloudreve.redis3=true cr-prod-wkr-3
docker node update --label-add cloudreve.redis-sentinel=true cr-prod-wkr-3
docker node update --label-add cloudreve.es3=true cr-prod-wkr-3
docker node update --label-add cloudreve.kafka3=true cr-prod-wkr-3
docker node update --label-add cloudreve.minio4=true cr-prod-wkr-3
docker node update --label-add cloudreve.tika=true cr-prod-wkr-3
```

查看标签：

```bash
docker node inspect cr-prod-wkr-1 --format '{{json .Spec.Labels}}'
```

## 4. 资源配置基线

以下是 `.env.swarm.prod-4x128g.example` 的生产默认基线，目标是：

- 约 `1000w` 级元数据量
- 不把四台机器打满
- 给页缓存、突发流量、索引重建、批量任务留余量

| 服务 | 默认副本 | Reservation | Limit | 说明 |
| --- | --- | --- | --- | --- |
| `cloudreve-master` | 1 | `2C / 4G` | `4C / 8G` | 主站单副本 |
| `cloudreve-master-proxy` | 2 | `0.5C / 512M` | `1C / 1G` | 对外入口 |
| `cloudreve-slave` | 2 | `2C / 4G` | `4C / 8G` | 从节点 |
| `cloudreve-slave-proxy` | 2 | `0.5C / 512M` | `1C / 1G` | 从节点入口 |
| `postgresql-*` | 3 | `4C / 12G` | `8C / 20G` | 数据库主从 |
| `pgpool` | 2 | `1C / 1G` | `2C / 2G` | PG 对外入口 |
| `redis-*` | 3 | `2C / 4G` | `4C / 8G` | 缓存主从 |
| `redis-sentinel` | 3 | `0.25C / 256M` | `0.5C / 512M` | Redis 仲裁 |
| `redis-proxy` | 2 | `0.5C / 512M` | `1C / 1G` | Redis 对外入口 |
| `minio-1..4` | 4 | `1.5C / 4G` | `4C / 8G` | 对象存储集群 |
| `minio` 代理 | 2 | `0.5C / 512M` | `1C / 1G` | S3/Console 对外入口 |
| `elasticsearch-1..3` | 3 | `6C / 24G` | `10C / 32G` | ES 集群 |
| `elasticsearch` 代理 | 2 | `0.5C / 512M` | `1C / 1G` | ES 对外入口 |
| `kafka-1..3` | 3 | `2C / 4G` | `4C / 8G` | Kafka 集群 |
| `kafka` 代理 | 2 | `0.5C / 512M` | `1C / 1G` | Kafka bootstrap |
| `kafka-ui` | 1 | `0.25C / 256M` | `1C / 1G` | Kafka 管理界面 |
| `tika` | 1~2 | `0.5C / 1G` | `2C / 2G` | 文档解析 |

额外建议：

- Elasticsearch JVM：`-Xms16g -Xmx16g`
- Kafka JVM：`-Xms2g -Xmx2g`
- 如果全文检索和预览解析压力很大，再继续抬高 `elasticsearch-*` 与 `tika`

## 5. 镜像策略

当前模板默认值：

- `cloudreve/cloudreve:4.15.0`
- `nginx:1.27-alpine`
- `cloudreve/tika:3.2.3.0-full-unrar-charset`
- `authverse/authverse-web:2024-local`
- `authverse/authverse-backend:2024-local`
- `elasticsearch:8.12.2`
- `apache/kafka:4.2.0`
- `provectuslabs/kafka-ui:v0.7.2`
- `haproxy:3.0-alpine`
- `bitnamilegacy/postgresql-repmgr:17.6.0-debian-12-r2`
- `bitnamilegacy/pgpool:4.6.3-debian-12-r0`
- `bitnamilegacy/redis:8.2.1-debian-12-r0`
- `bitnamilegacy/redis-sentinel:8.2.1-debian-12-r0`
- `bitnamilegacy/minio:2024.10.2-debian-12-r0`

需要明确说明：

- 截至 `2026-04-11`，已用 `docker manifest inspect` 复核，
  Docker Hub 上 PostgreSQL Repmgr / Pgpool / MinIO / Redis 这些固定版本 tag 仍应以 `bitnamilegacy/*` 为准
- `bitnami/*` 公开可直接 `pull` 的对应固定版本 tag 并不完整
- 所以当前模板没有用 `latest`
- 也没有依赖“本地临时 retag 成 `bitnami/*`”
- `TIKA_IMAGE` 与 `AUTHVERSE_*_IMAGE` 都是自定义镜像，必须提前推到私有仓库，再让其它节点远程拉取

上线前建议每台节点都执行：

```bash
sudo docker/swarm/prepare-private-registry.sh --env-file .env.swarm --apply --restart-docker
docker/swarm/prepare-bitnami-images.sh --check
docker/swarm/prepare-bitnami-images.sh
```

补充：

- `prepare-private-registry.sh` 会优先用 `jq` 修改 `/etc/docker/daemon.json`
- 如果节点没有 `jq`，会自动回退到 `python3`
- 两者都没有时，需要先安装其中一个

然后在固定 manager 上执行：

```bash
docker/swarm/deploy-private-registry.sh --env-file .env.swarm
docker/swarm/publish-private-images.sh --env-file .env.swarm
```

如果你要顺手打一份离线镜像包，再执行：

```bash
docker/swarm/export-swarm-images.sh --env-file .env.swarm --output-dir .
```

关键变量：

- `PRIVATE_REGISTRY_STACK_NAME`
- `PRIVATE_REGISTRY_ADDR`
- `PRIVATE_REGISTRY_NODE_CONSTRAINT`
- `PRIVATE_REGISTRY_DATA_MOUNT_SOURCE`
- `PRIVATE_REGISTRY_IMAGE_KEYS`
- `TIKA_LOCAL_IMAGE`
- `TIKA_REMOTE_IMAGE`
- `TIKA_IMAGE`
- `AUTHVERSE_WEB_LOCAL_IMAGE`
- `AUTHVERSE_WEB_REMOTE_IMAGE`
- `AUTHVERSE_BACKEND_LOCAL_IMAGE`
- `AUTHVERSE_BACKEND_REMOTE_IMAGE`

## 6. 为什么 bind 挂载在 Swarm 下经常报错

最常见的误区是：

- 以为 manager 上写了 `.env.swarm`
- `docker stack deploy` 时写了宿主机绝对路径
- Swarm 就会帮你在所有节点自动创建目录

实际上不是。

Swarm 下 bind 挂载的真实语义是：

1. 任务被调度到某个节点
2. 这个节点本机必须已经存在对应绝对路径
3. 这个路径权限必须允许容器用户访问
4. 如果该节点没有该路径，或者权限不对，容器会直接启动失败

为什么你会看到“用 volume 没事，用绝对路径就挂”：

- 命名卷由 Docker 自己管理
- bind 路径由你自己保证
- 尤其是多机 Swarm 下，manager 的文件系统和 worker 的文件系统不是同一个

所以生产里必须把 bind 路径准备动作前置。

## 7. 宿主机目录规划

### 7.1 PostgreSQL / Redis

这两个默认就是强制物理路径，建议：

```bash
/srv/cloudreve/postgresql/1
/srv/cloudreve/postgresql/2
/srv/cloudreve/postgresql/3

/srv/cloudreve/redis/1
/srv/cloudreve/redis/2
/srv/cloudreve/redis/3
```

### 7.2 Cloudreve 运行目录

如果切换成 bind：

```bash
/srv/cloudreve/runtime/master
/srv/cloudreve/runtime/slave
```

### 7.3 MinIO / Elasticsearch / Kafka

如果启用集群模式并切到 bind：

```bash
/srv/cloudreve/minio/1
/srv/cloudreve/minio/2
/srv/cloudreve/minio/3
/srv/cloudreve/minio/4

/srv/cloudreve/elasticsearch/1
/srv/cloudreve/elasticsearch/2
/srv/cloudreve/elasticsearch/3

/srv/cloudreve/kafka/1
/srv/cloudreve/kafka/2
/srv/cloudreve/kafka/3
```

### 7.4 Tika 自定义字体

如果切到 bind：

```bash
/srv/cloudreve/tika-fonts
```

### 7.5 私有仓库目录

如果 `registry:2` 也使用 bind，建议：

```bash
/srv/cloudreve/registry
```

## 8. 宿主机前置配置

### 8.1 Elasticsearch

所有 Elasticsearch 节点执行：

```bash
sudo sysctl -w vm.max_map_count=262144
echo 'vm.max_map_count=262144' | sudo tee /etc/sysctl.d/99-cloudreve-elasticsearch.conf
sudo sysctl --system
```

### 8.2 Redis

所有 Redis / Sentinel 节点执行：

```bash
sudo sysctl -w vm.overcommit_memory=1
echo 'vm.overcommit_memory=1' | sudo tee /etc/sysctl.d/99-cloudreve-redis.conf
sudo sysctl --system
```

## 9. `.env.swarm` 在多机器下怎么生效

核心结论：

- `worker` 不需要 `.env.swarm`
- 负责执行 `docker stack deploy` 的 `manager` 需要 `.env.swarm`
- 如果有多台 `manager` 都可能部署，就必须把 `.env.swarm` 同步到这些 manager

因为流程是：

1. 部署脚本先在本地 `source .env.swarm`
2. 然后渲染最终 YAML
3. 再把渲染后的结果提交给 Swarm

所以：

- `.env.swarm` 本身不会自动同步
- 但渲染后的变量值会固化进最终 service spec

生产里推荐两种方式：

### 方式 A：固定一台部署 manager

- 只在一台 manager 上维护 `.env.swarm`
- 所有部署、升级、回滚都从这台机器执行

### 方式 B：同步到所有 manager

可选工具：

- `ansible`
- `rsync`
- `sops + age`
- `vault`

## 10. 用脚本准备物理路径

### 10.1 检查模式

在目标节点执行：

```bash
sudo docker/swarm/prepare-bind-paths.sh --check
```

只检查 PostgreSQL / Redis：

```bash
sudo docker/swarm/prepare-bind-paths.sh --services pg,redis --check
```

### 10.2 应用模式

在目标节点执行：

```bash
sudo docker/swarm/prepare-bind-paths.sh --apply
```

只处理集群型数据目录：

```bash
sudo docker/swarm/prepare-bind-paths.sh --services minio,elasticsearch,kafka --apply
```

说明：

- 这个脚本必须在“任务将要落到的节点本机”执行
- 它会创建目录并修正常见 owner / mode
- 它不会替你执行 `docker stack deploy`

## 11. 初始化 Swarm

### 11.1 manager 初始化

在管理节点执行：

```bash
docker swarm init --advertise-addr <manager-ip>
```

查看加入 token：

```bash
docker swarm join-token manager
docker swarm join-token worker
```

### 11.2 worker 加入

在 worker 节点执行上面输出的 `docker swarm join ...`

### 11.3 验证

在 manager 上执行：

```bash
docker node ls
docker info --format '{{.Swarm.LocalNodeState}}'
```

## 12. 首次部署步骤

### 12.1 复制生产模板

```bash
cp .env.swarm.prod-4x128g.example .env.swarm
```

至少修改这些变量：

- `CLOUDREVE_SITE_URL`
- `CLOUDREVE_SESSION_SECRET`
- `POSTGRESQL_PASSWORD`
- `POSTGRESQL_POSTGRES_PASSWORD`
- `REPMGR_PASSWORD`
- `PGPOOL_ADMIN_PASSWORD`
- `REDIS_PASSWORD`
- `MINIO_ROOT_PASSWORD`
- `CR_INIT_S3_SECRET_KEY`

### 12.2 渲染最终配置

先看渲染结果：

```bash
docker/swarm/deploy-stack.sh --render-only
```

集群模式：

```bash
docker/swarm/deploy-stack.sh --with-cluster --render-only
```

渲染文件默认在：

```bash
.tmp/<stack-name>-resolved.yaml
```

### 12.3 正式部署

单节点对象存储 / 检索模式：

```bash
docker/swarm/deploy-stack.sh
```

集群模式：

```bash
docker/swarm/deploy-stack.sh --with-cluster
```

使用自定义环境文件和栈名：

```bash
docker/swarm/deploy-stack.sh --env-file .env.swarm --stack-name cloudreve-prod --with-cluster
```

### 12.4 首次部署后检查

```bash
docker stack services cloudreve-prod
docker stack ps cloudreve-prod
docker service logs -f cloudreve-prod_cloudreve-master
docker service logs -f cloudreve-prod_pgpool
docker service logs -f cloudreve-prod_redis-proxy
```

## 13. Cloudreve 首次初始化

### 13.1 主站初始化

打开：

```text
http(s)://<master-domain-or-ip>/admin
```

确认：

- 站点 URL 与 `CLOUDREVE_SITE_URL` 完全一致
- 默认存储策略 `ID=1` 已经是 `S3`

默认 S3 指向：

- `http://minio:9000`
- bucket：`cloudreve`
- bucket 创建者：`minio-init`

### 13.2 回填 Slave Secret

只有在多节点 Cloudreve 模式下才需要这一步。

1. 进入后台创建从节点
2. 复制 `Slave Key`
3. 写回 `.env.swarm` 里的 `CLOUDREVE_SLAVE_SECRET`
4. 重新部署一次

```bash
docker/swarm/deploy-stack.sh --with-cluster
```

## 14. 日常巡检命令

### 14.1 集群

```bash
docker node ls
docker node inspect <node>
docker info
```

### 14.2 栈

```bash
docker stack ls
docker stack services cloudreve-prod
docker stack ps cloudreve-prod
docker stack rm cloudreve-prod
```

### 14.3 服务

```bash
docker service ls
docker service ps cloudreve-prod_cloudreve-master
docker service inspect cloudreve-prod_cloudreve-master
docker service logs -f cloudreve-prod_cloudreve-master
```

### 14.4 容器

```bash
docker ps
docker ps -a
docker inspect <container-id>
docker exec -it <container-id> sh
```

### 14.5 网络 / 卷 / Config

```bash
docker network ls
docker network inspect cloudreve-prod_cloudreve_backend

docker volume ls
docker volume inspect <volume-name>

docker config ls
docker config inspect <config-name>
```

## 15. 对外健康检查命令

下面这些命令很适合做上线后巡检：

```bash
curl -fsS http://<master-ip-or-lb>:28080/api/v4/site/ping
curl -sS -o /dev/null -w '%{http_code}\n' http://<slave-ip-or-lb>:25213/
curl -sS -o /dev/null -w '%{http_code}\n' http://<minio-ip-or-lb>:29000/minio/health/live
curl -sS -o /dev/null -w '%{http_code}\n' http://<es-ip-or-lb>:29200
curl -sS -o /dev/null -w '%{http_code}\n' http://<tika-ip-or-lb>:29998/tika
curl -sS -o /dev/null -w '%{http_code}\n' http://<kafka-ui-ip-or-lb>:28089
```

数据库与缓存连通性：

```bash
docker run --rm postgres:17-alpine sh -lc \
  "PGPASSWORD='<pg-password>' psql -h <ip> -p 15432 -U cloudreve -d cloudreve -c 'select 1;'"

docker run --rm redis:8.6.2 redis-cli \
  -h <ip> -p 16379 -a '<redis-password>' PING
```

## 16. 升级、回滚、扩缩容

### 16.1 改镜像后滚动升级

1. 修改 `.env.swarm`
2. 重新部署

```bash
docker/swarm/deploy-stack.sh --with-cluster
```

### 16.2 只重启某个服务

```bash
docker service update --force cloudreve-prod_cloudreve-master
docker service update --force cloudreve-prod_pgpool
docker service update --force cloudreve-prod_redis-proxy
```

### 16.3 回滚

```bash
docker service rollback cloudreve-prod_cloudreve-master
docker service rollback cloudreve-prod_pgpool
```

### 16.4 扩容

可以直接扩的：

- `cloudreve-master-proxy`
- `cloudreve-slave`
- `cloudreve-slave-proxy`
- `pgpool`
- `redis-sentinel`
- `redis-proxy`
- `tika`

示例：

```bash
docker service scale cloudreve-prod_cloudreve-slave=3
docker service scale cloudreve-prod_tika=2
```

不要直接扩的：

- `postgresql-1/2/3`
- `redis-1/2/3`
- 固定编号的 `kafka-1/2/3`
- 固定编号的 `minio-1/2/3/4`

## 17. 节点维护

把节点切成维护态：

```bash
docker node update --availability drain <node-name>
```

恢复调度：

```bash
docker node update --availability active <node-name>
```

查看：

```bash
docker node inspect <node-name> --format '{{.Spec.Availability}}'
```

## 18. 备份与恢复

### 18.1 PostgreSQL

逻辑备份：

```bash
docker run --rm postgres:17-alpine sh -lc \
  "PGPASSWORD='<pg-password>' pg_dump -h <ip> -p 15432 -U cloudreve -d cloudreve -Fc -f /tmp/cloudreve.dump && cat /tmp/cloudreve.dump" \
  > cloudreve-$(date +%F-%H%M%S).dump
```

恢复：

```bash
docker cp cloudreve.dump <pg-container>:/tmp/cloudreve.dump
docker exec -it <pg-container> sh -lc \
  "PGPASSWORD='<pg-password>' pg_restore -U cloudreve -d cloudreve /tmp/cloudreve.dump"
```

如果你要做物理级备份：

- 优先在 PostgreSQL 节点上做
- 直接备份宿主机 bind 数据目录前，先确认复制状态和停机窗口

### 18.2 Redis

Redis 最实用的做法是：

- 保留 AOF
- 备份 bind 数据目录

临时触发落盘：

```bash
docker exec -it <redis-container> redis-cli -a '<redis-password>' BGSAVE
```

查看持久化文件位置：

```bash
docker exec -it <redis-container> redis-cli -a '<redis-password>' CONFIG GET dir
docker exec -it <redis-container> redis-cli -a '<redis-password>' CONFIG GET appendfilename
```

### 18.3 MinIO

备份最简单的方法是 `mc mirror`：

```bash
docker run --rm bitnamilegacy/minio:2024.10.2-debian-12-r0 sh -lc '
  /opt/bitnami/minio-client/bin/mc alias set src http://<minio-ip>:9000 <access-key> <secret-key>
  /opt/bitnami/minio-client/bin/mc mirror --overwrite src/cloudreve /backup/cloudreve
'
```

恢复：

```bash
docker run --rm bitnamilegacy/minio:2024.10.2-debian-12-r0 sh -lc '
  /opt/bitnami/minio-client/bin/mc alias set dst http://<minio-ip>:9000 <access-key> <secret-key>
  /opt/bitnami/minio-client/bin/mc mirror --overwrite /backup/cloudreve dst/cloudreve
'
```

### 18.4 Elasticsearch

建议用 Snapshot Repository，不建议直接复制正在运行中的数据目录。

### 18.5 配置与运行目录

至少要备份：

- `.env.swarm`
- Cloudreve 运行目录
- 反向代理域名 / LB 配置
- 宿主机 bind 数据目录规划文档

## 19. 常见故障排查

### 19.1 bind 路径报错

排查顺序：

1. 看任务落在哪台节点
2. 上这台节点确认绝对路径是否存在
3. 看 owner / mode
4. 再看 `.env.swarm` 里是不是写错路径

命令：

```bash
docker service ps cloudreve-prod_postgresql-1
ls -ld /srv/cloudreve/postgresql/1
```

### 19.2 `.env.swarm` 改了但别的 manager 不生效

原因：

- `.env.swarm` 不会自动同步

处理：

- 固定单一部署 manager
- 或同步到所有 manager

### 19.3 旧失败 task 一堆，看起来像“还没恢复”

`docker stack ps` 会保留历史失败任务。

要区分：

- 当前最新 task 是否 `Running`
- 旧 task 是否只是历史记录

### 19.4 Nginx / HAProxy 代理是 0/1

先看真实 task：

```bash
docker service ps cloudreve-prod_cloudreve-master-proxy --no-trunc
docker service logs cloudreve-prod_cloudreve-master-proxy
```

如果是本地 Colima 联调里遇到“早期 backend 不健康，代理 task 退出后没有自动补新 task”，可以临时执行：

```bash
docker service update --force cloudreve-prod_cloudreve-master-proxy
docker service update --force cloudreve-prod_cloudreve-slave-proxy
docker service update --force cloudreve-prod_minio
docker service update --force cloudreve-prod_elasticsearch
```

这是本地联调绕行，不应作为真实 Linux 多机生产的常规操作。

### 19.5 Redis Proxy 启动后 Cloudreve 连 Redis 报 EOF

当前模板已经修复了一个关键问题：

- `redis-proxy` 启动时不再盲目信任 Sentinel 第一时间返回的地址
- 会先做一次真实探测
- 如果不稳定，再回退到 `REDIS_MASTER_HOST`

如果仍报错，先验证：

```bash
docker service logs cloudreve-prod_redis-proxy
docker run --rm redis:8.6.2 redis-cli -h <ip> -p 16379 -a '<redis-password>' PING
```

### 19.6 Elasticsearch 节点 1/1，但入口 503

这通常意味着：

- ES 节点本身起来了
- 但代理层健康检查没通过

先分别测：

```bash
docker run --rm --network cloudreve-prod_cloudreve_backend curlimages/curl:8.12.1 \
  -sS -o /dev/null -w '%{http_code}\n' http://elasticsearch-1:9200

curl -sS -o /dev/null -w '%{http_code}\n' http://<ip>:29200
```

### 19.7 Kafka 一直 0/1

先看日志：

```bash
docker service logs cloudreve-prod_kafka-1
docker service logs cloudreve-prod_kafka-2
docker service logs cloudreve-prod_kafka-3
```

如果是下面这些报错：

- `Election timed out before receiving sufficient vote responses`
- `Node X disconnected`
- `Unable to register the broker because the RPC got timed out before it could be sent`

重点排查：

- 是否真的跑在多台 Linux 机器上
- 是否仍被 Swarm VIP / 特殊 DNS 行为干扰
- `KAFKA_CONTROLLER_QUORUM_VOTERS`
- `KAFKA_*_ADVERTISED_LISTENER`

## 20. 2026-04-06 本地真实联调记录

联调环境：

- macOS 宿主机
- Colima 单节点 Swarm
- 栈名：`cloudreve-fulltest`
- 环境文件：`.tmp/.env.swarm.fulltest`
- 部署命令：`docker/swarm/deploy-stack.sh --env-file .tmp/.env.swarm.fulltest --stack-name cloudreve-fulltest --with-cluster`

已验证通过：

- `cloudreve-master`：`1/1`
- `cloudreve-master-proxy`：`1/1`
- `cloudreve-slave`：`1/1`
- `cloudreve-slave-proxy`：`1/1`
- `postgresql-1/2/3`：`1/1`
- `pgpool`：`1/1`
- `redis-1/2/3`：`1/1`
- `redis-sentinel`：`1/1`
- `redis-proxy`：`1/1`
- `minio`：`1/1`
- `minio-1/2/3/4`：`1/1`
- `minio-init`：`1/1`
- `tika`：`1/1`
- `elasticsearch-1/2/3`：`1/1`

外部联调结果：

- `2026-04-06` 实测 `http://192.168.106.2:28080/api/v4/site/ping` 返回 `200`
- `2026-04-06` 实测 `http://192.168.106.2:29998/tika` 返回 `200`
- `2026-04-06` 实测 `PGPASSWORD=... psql -h 192.168.106.2 -p 25432 ... 'select 1;'` 成功
- `2026-04-06` 实测 `redis-cli -h 192.168.106.2 -p 26379 ... PING` 返回 `PONG`
- `2026-04-06` 实测 `http://192.168.106.2:29000/minio/health/live` 返回 `200`
- `2026-04-06` 实测 `docker exec <minio-init-container> ... mc ls local` 可看到 `cloudreve/`
- `2026-04-06` 实测 `http://192.168.106.2:29200` 返回 `200`
- `2026-04-06` 实测 `http://192.168.106.2:25213/...` 端口可达，但 `site/ping` 返回 `404`

仍未完全打通：

- `kafka-1/2/3` 仍未稳定到 `1/1`
- `kafka-ui` 因 Kafka 集群未稳定仍不可用

需要明确：

- 这些问题是在 `2026-04-06` 的 Colima 单节点 Swarm 环境里复现的
- 不能直接等同于真实 `1 manager + 3 worker Linux` 生产环境
- 真实生产是否稳定，要以真实 Linux 多机 Swarm 实测为准

## 21. 生产上线建议

如果你现在要上真实环境，建议顺序就是：

1. 按本手册先规划主机名、节点标签、目录和 sysctl
2. 在每台目标节点执行 `prepare-bitnami-images.sh`
3. 在每台目标节点执行 `prepare-bind-paths.sh --apply`
4. 在固定 manager 上维护 `.env.swarm`
5. 先 `--render-only` 检查最终 YAML
6. 再正式 `deploy-stack.sh --with-cluster`
7. 先打通 `Cloudreve / PG / Redis / Tika`
8. 再单独验 `MinIO / Elasticsearch / Kafka`
9. 最后再接 LB、域名、HTTPS、监控与备份

如果你照着这个顺序做，排障成本会明显低很多。
