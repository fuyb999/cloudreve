# Cloudreve Swarm 快速上线清单

这份清单对应当前仓库里的生产默认值：

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
- `docker/swarm/prepare-bitnami-images.sh`
- `docker/swarm/prepare-private-registry.sh`
- `docker/swarm/publish-private-images.sh`
- `docker-compose.swarm.auth.yml`
- `docker/swarm/build-auth-images.sh`
- `docker/swarm/init-authverse-db.sh`
- `docker/swarm/deploy-auth-stack.sh`
- `docs/docker-swarm-production-checklist.md`
- `docs/docker-swarm-deployment.md`
- `docs/docker-swarm-env-sync.md`
- `docs/docker-swarm-auth-deployment.md`

如果你需要完整中文运维手册，直接看：

- `docs/docker-swarm-operations-manual.md`

如果你这次还要把统一认证前后端一起挂进 Swarm，再看：

- `docs/docker-swarm-auth-deployment.md`

如果你就是按 `1 manager + 3 worker + 128GB/64 线程` 上真实环境，直接看：

- `docs/docker-swarm-production-checklist.md`

## 1. 最短结论

- PostgreSQL 和 Redis 默认就是独立宿主机物理目录，不共享运行数据
- 所有数据/运行目录挂载都统一支持 `*_MOUNT_TYPE + *_MOUNT_SOURCE`
- `pgpool` 默认对外发布 `15432`
- `redis-proxy` 默认对外发布 `16379`
- Cloudreve 不再默认把用户文件落到宿主机目录
- `cloudreve-master` / `cloudreve-slave` / `minio` / `elasticsearch` / `tika` 字体目录现在同时支持命名卷和宿主机绝对路径两种挂载模式
- `PG / Redis` 默认仍是 `bind`，但变量名也统一成了 `*_MOUNT_TYPE + *_MOUNT_SOURCE`
- Cloudreve 首次初始化时，默认存储策略会直接创建成 `S3` 兼容存储
- 默认 `S3` 指向栈内 `MinIO`，并由 `minio-init` 持续确保 `cloudreve` bucket 存在
- 默认 `docker/swarm/deploy-stack.sh` 会直接使用单节点全量模板
- `SWARM_WITH_CLUSTER=yes` 时，会切成 4 节点 MinIO + 3 节点 Elasticsearch + 3 节点 Kafka
- Kafka UI 会一起挂上，默认对外端口 `18089`
- 默认单节点模板里 OnlyOffice 8 也会一起起来；生产样例仍可通过 `ONLYOFFICE_REPLICAS=0` 关闭
- 自定义镜像现在建议统一放到单点 `registry:2` 仓库栈，其它节点远程拉取
- `cloudreve-master` 仍建议先保持 `1` 副本

当前文件分组：

- `docker-compose.swarm.single.yml`：默认单节点全量栈
- `docker-compose.swarm.yml`：Cloudreve 主从
- `docker-compose.swarm.foundation.yml`：PG / Redis / Tika / OnlyOffice
- `docker-compose.swarm.cluster.yml`：MinIO / Kafka / ES / Kafka UI 集群
- `docker-compose.swarm.auth.yml`：Authverse 前后端
- `docker-compose.swarm.registry.yml`：私有仓库

## 2. 上线前准备

1. 初始化 Swarm 并把节点加入集群。
2. 复制环境模板：

```bash
cp .env.swarm.example .env.swarm
```

如果你的拓扑是 `1 manager + 3 worker`，并且每台机器都是 `128GB / 64 线程`，可以直接改用：

```bash
cp .env.swarm.prod-4x128g.example .env.swarm
```

3. 给 PostgreSQL / Redis / registry 节点打标签。
4. 至少回填这些变量：

- `CLOUDREVE_SITE_URL`
- `CLOUDREVE_SESSION_SECRET`
- `POSTGRESQL_PASSWORD`
- `POSTGRESQL_POSTGRES_PASSWORD`
- `REPMGR_PASSWORD`
- `PGPOOL_ADMIN_PASSWORD`
- `REDIS_PASSWORD`
- `MINIO_ROOT_PASSWORD`
- `CR_INIT_S3_SECRET_KEY`
- `PRIVATE_REGISTRY_ADDR`
- `TIKA_REMOTE_IMAGE`

5. 在所有 Swarm 节点写入私有仓库 daemon 配置：

```bash
sudo docker/swarm/prepare-private-registry.sh --env-file .env.swarm --check
sudo docker/swarm/prepare-private-registry.sh --env-file .env.swarm --apply --restart-docker
```

说明：

- 如果这些脚本是在其它 worker 节点执行，也要提前把同一份 `.env.swarm` 同步过去
- `.env.swarm` 只在部署 manager 上是必须品，但准备脚本想复用同一套变量时，其它节点也需要拿到一份

6. 如果你要提前把 PostgreSQL / Redis / MinIO 镜像预拉到各节点，执行：

```bash
docker/swarm/prepare-bitnami-images.sh --env-file .env.swarm --check
docker/swarm/prepare-bitnami-images.sh --env-file .env.swarm
```

7. 在各个有状态节点提前创建目录：

```bash
mkdir -p /srv/cloudreve/postgresql/1
mkdir -p /srv/cloudreve/postgresql/2
mkdir -p /srv/cloudreve/postgresql/3
mkdir -p /srv/cloudreve/redis/1
mkdir -p /srv/cloudreve/redis/2
mkdir -p /srv/cloudreve/redis/3
```

如果你不想手工建目录，也可以在对应 worker 节点直接执行：

```bash
sudo docker/swarm/prepare-bind-paths.sh --check
sudo docker/swarm/prepare-bind-paths.sh --services pg,redis
```

如果你启用了私有仓库的 bind 路径，再在 registry 所在节点执行：

```bash
sudo docker/swarm/prepare-bind-paths.sh --env-file .env.swarm --services registry
```

8. 先部署私有仓库栈，再发布自定义镜像：

```bash
docker/swarm/deploy-private-registry.sh --env-file .env.swarm
docker/swarm/publish-private-images.sh --env-file .env.swarm
```

如果你要顺手打一份离线镜像包：

```bash
docker/swarm/export-swarm-images.sh --env-file .env.swarm --output-dir .
```

如果你要直接启用栈内 MinIO / Elasticsearch / Kafka 集群，再额外确认：

- `SWARM_WITH_CLUSTER=yes`
- `MINIO_1_DATA_MOUNT_TYPE` 到 `MINIO_4_DATA_MOUNT_TYPE`
- `MINIO_1_DATA_MOUNT_SOURCE` 到 `MINIO_4_DATA_MOUNT_SOURCE`
- `ELASTICSEARCH_1_DATA_MOUNT_TYPE` 到 `ELASTICSEARCH_3_DATA_MOUNT_TYPE`
- `ELASTICSEARCH_1_DATA_MOUNT_SOURCE` 到 `ELASTICSEARCH_3_DATA_MOUNT_SOURCE`
- `KAFKA_1_DATA_MOUNT_TYPE` 到 `KAFKA_3_DATA_MOUNT_TYPE`
- `KAFKA_1_DATA_MOUNT_SOURCE` 到 `KAFKA_3_DATA_MOUNT_SOURCE`

说明：

- `CR_INIT_S3_*` 必须在第一次初始化数据库前就确定
- 如果你要改成外部 S3 / MinIO，而不是用栈内 `minio`，就在第一次部署前改掉 `CR_INIT_S3_*`
- 如果数据库已经初始化过，默认存储策略 `ID=1` 不会被自动重建
- `PG / Redis` 仍然必须使用宿主机物理路径
- `cloudreve-master` / `cloudreve-slave` / `minio` / `elasticsearch` / `tika` 字体目录默认走命名卷
- 如果你确实要改成宿主机绝对路径，就把对应的 `*_MOUNT_TYPE=bind`，并把 `*_MOUNT_SOURCE` 改成真实绝对路径
- 如果是多 manager / 多机器部署，再看 `docs/docker-swarm-env-sync.md`
- PostgreSQL / Redis / MinIO 默认镜像现在就是 Docker Hub 可直接 pull 的名字与 tag
- 默认 `TIKA_IMAGE` 是自定义镜像，生产里建议写成 `${PRIVATE_REGISTRY_ADDR}/cloudreve/tika:...`
- 如果是 bind 模式，建议上线前先在目标节点执行 `docker/swarm/prepare-bind-paths.sh`
- 如果是 Elasticsearch 集群节点，记得先在宿主机执行 `sysctl -w vm.max_map_count=262144`
- 如果你准备让 Cloudreve 直接使用栈内 Kafka，再把 `CLOUDREVE_GLOBAL_KAFKA_ENABLED=true`
- Kafka UI 默认访问地址是 `http://<node-or-lb>:18089`

## 3. Colima 说明

如果你是在 macOS + Colima 上跑 Swarm：

- `PG_*_DATA_MOUNT_SOURCE` / `REDIS_*_DATA_MOUNT_SOURCE` 必须是宿主机真实路径
- 这些路径必须已经被 Colima 共享进虚拟机，或者通过 Colima mount 显式挂进去
- 不要把它们写成容器里的路径
- 其他运行目录如果不确定 Colima mount 是否可靠，优先保持默认 `volume` 模式

## 4. 首次部署顺序

首次部署建议：

- `CLOUDREVE_MASTER_REPLICAS=1`
- `CLOUDREVE_SLAVE_SECRET` 先保留占位值
- 先确认 `docker/swarm/deploy-private-registry.sh` 和 `docker/swarm/publish-private-images.sh` 已经执行完成

执行主业务栈：

```bash
docker/swarm/deploy-stack.sh
```

如果要启用集群版 MinIO / Elasticsearch / Kafka：

```bash
docker/swarm/deploy-stack.sh --with-cluster
```

如果你确实需要给 Tika 指定宿主机字体目录，直接在 `.env.swarm` 里设置：

```env
TIKA_CUSTOM_FONTS_MOUNT_TYPE=bind
TIKA_CUSTOM_FONTS_MOUNT_SOURCE=/absolute/path/tika-fonts
```

如果你想把某些默认命名卷切换成宿主机绝对路径，在 `.env.swarm` 中这样改：

```bash
CLOUDREVE_MASTER_RUNTIME_MOUNT_TYPE=bind
CLOUDREVE_MASTER_RUNTIME_MOUNT_SOURCE=/absolute/path/cloudreve-master

MINIO_DATA_MOUNT_TYPE=bind
MINIO_DATA_MOUNT_SOURCE=/absolute/path/minio

ELASTICSEARCH_DATA_MOUNT_TYPE=bind
ELASTICSEARCH_DATA_MOUNT_SOURCE=/absolute/path/elasticsearch
```

检查：

```bash
docker stack services cloudreve
docker stack ps cloudreve
docker service logs -f cloudreve_cloudreve-master
```

## 5. 主站初始化

1. 打开 `http(s)://<master-host-or-lb>/admin`
2. 登录后台
3. 在 `设置 -> 基本设置` 中确认“站点 URL（Site URL）”和 `CLOUDREVE_SITE_URL` 完全一致
4. 在 `管理面板 -> 存储策略` 中确认默认策略 `ID=1` 已经是 `S3` 类型

默认情况下，这个策略会指向：

- 对象存储地址：`http://minio:9000`
- 存储桶：`cloudreve`
- 访问密钥：`minio`
- 上传方式：中继上传（Relay）
- 下载方式：内部代理（Internal Proxy）

如果启用了 `SWARM_WITH_CLUSTER=yes`，这里仍然保持 `http://minio:9000` 不变。

同时在 Cloudreve 后台配置全文检索时，应填写：

- Elasticsearch 地址：`http://elasticsearch:9200`

如果你准备把第三方抽取链路也切到栈内 Kafka：

- Cloudreve 全局 Kafka brokers：`kafka:9092`
- 第三方抽取器如果也在 Swarm 内部网络，Kafka brokers 也填 `kafka:9092`
- Kafka UI 也直接连 `kafka:9092`

## 6. 回填从节点密钥

1. 在后台进入 `管理面板 -> 节点 -> 新建节点`
2. 创建 slave node
3. 复制生成的从节点密钥（`Slave Key`）
4. 把 `.env.swarm` 中的 `CLOUDREVE_SLAVE_SECRET` 替换成真实值
5. 重新执行一次 `docker/swarm/deploy-stack.sh`

如果你不是用默认栈名，也可以直接：

```bash
STACK_NAME=cloudreve-debug docker/swarm/deploy-stack.sh
```

## 7. 扩容原则

- 可以直接扩：`cloudreve-master-proxy`、`cloudreve-slave`、`cloudreve-slave-proxy`、`tika`、`pgpool`、`redis-sentinel`、`redis-proxy`
- 不要直接扩：`postgresql-1/2/3`、`redis-1/2/3`
- 如果 `cloudreve-master` 还是单机命名卷或单机 bind 运行目录，就不要直接扩到多个副本

## 8. 默认资源建议

当前模板默认值按约 `1000w` 级别元数据量做了基础预留：

- `cloudreve-master`: `1C / 2G` reservation, `2C / 4G` limit
- `postgresql-*`: `2C / 4G` reservation, `4C / 8G` limit
- `redis-*`: `1C / 2G` reservation, `2C / 4G` limit
- `elasticsearch`: `2C / 4G` reservation, `4C / 8G` limit

如果启用集群模式，额外建议：

- `minio-1..4`: 每节点 `1C / 2G` reservation, `2C / 4G` limit
- `elasticsearch-1..3`: 每节点 `2C / 6G` reservation, `4C / 8G` limit
- `kafka-1..3`: 每节点 `1C / 2G` reservation, `2C / 4G` limit
- `kafka-ui`: `0.25C / 256M` reservation, `1C / 1G` limit
- `ELASTICSEARCH_CLUSTER_NODE_JAVA_OPTS="-Xms4g -Xmx4g"`
- `KAFKA_CLUSTER_NODE_JVM_HEAP_OPTS="-Xms1g -Xmx1g"`

这只是生产默认起点，不是所有场景的上限。你仍然需要根据：

- 并发上传量
- 全文检索规模
- 预览转换量
- 对象存储吞吐

继续调高。

如果你的拓扑就是 `1 manager + 3 worker`，并且每台 `128GB / 64 线程`，仓库里已经给了
可直接复制的生产基线：

- `.env.swarm.prod-4x128g.example`

它默认启用：

- `SWARM_WITH_CLUSTER=yes`
- PostgreSQL / Redis / MinIO / Elasticsearch / Kafka / Cloudreve 运行目录全部宿主机绝对路径
- 更保守但不浪费资源的 reservation / limit
- `edge` / `redis-sentinel` / `tika` / `kafka-ui` 这些额外标签约束

## 9. 最佳实践

- PostgreSQL：本地 SSD + 主从复制 + pgpool
- Redis：本地 SSD + 主从复制 + Sentinel
- Cloudreve 文件：S3 / MinIO / 对象存储
- MinIO 集群：4 个独立节点或 4 个独立本地数据目录
- Elasticsearch 集群：3 个独立节点，并提前处理 `vm.max_map_count`
- Kafka 集群：3 个独立节点，优先只给 Swarm 内部服务使用
- 不要把 PGDATA / Redis AOF / RDB 放到对象存储
- 如果改外部 S3，优先在第一次初始化前改 `CR_INIT_S3_*`
