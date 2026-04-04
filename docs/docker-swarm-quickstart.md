# Cloudreve Swarm 快速上线清单

这份清单对应当前仓库里的生产默认值：

- `docker-compose.swarm.yml`
- `docker-compose.swarm.cluster.yml`
- `docker-compose.swarm.bind.yml`
- `.env.swarm.example`
- `docker/swarm/deploy-stack.sh`
- `docs/docker-swarm-deployment.md`
- `docs/docker-swarm-env-sync.md`

## 1. 最短结论

- PostgreSQL 和 Redis 默认就是独立宿主机物理目录，不共享运行数据
- `pgpool` 默认对外发布 `15432`
- `redis-proxy` 默认对外发布 `16379`
- Cloudreve 不再默认把用户文件落到宿主机目录
- `cloudreve-master` / `cloudreve-slave` / `minio` / `elasticsearch` / `tika` 字体目录现在同时支持命名卷和宿主机绝对路径两种挂载模式
- Cloudreve 首次初始化时，默认存储策略会直接创建成 `S3` 兼容存储
- 默认 `S3` 指向栈内 `MinIO`，并自动创建 `cloudreve` bucket
- `SWARM_WITH_CLUSTER=yes` 时，会切成 4 节点 MinIO + 3 节点 Elasticsearch + 3 节点 Kafka
- Kafka UI 会一起挂上，默认对外端口 `18089`
- `cloudreve-master` 仍建议先保持 `1` 副本

## 2. 上线前准备

1. 初始化 Swarm 并把节点加入集群。
2. 给 PostgreSQL / Redis 节点打标签。
3. 构建并推送 `TIKA_IMAGE` 到所有节点可拉取的镜像仓库。
4. 复制环境模板：

```bash
cp .env.swarm.example .env.swarm
```

5. 在各个有状态节点提前创建目录：

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

6. 至少填好这些变量：

- `CLOUDREVE_SITE_URL`
- `CLOUDREVE_SESSION_SECRET`
- `POSTGRESQL_PASSWORD`
- `POSTGRESQL_POSTGRES_PASSWORD`
- `REPMGR_PASSWORD`
- `PGPOOL_ADMIN_PASSWORD`
- `REDIS_PASSWORD`
- `MINIO_ROOT_PASSWORD`
- `CR_INIT_S3_SECRET_KEY`
- `TIKA_IMAGE`

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
- 如果是 bind 模式，建议上线前先在目标节点执行 `docker/swarm/prepare-bind-paths.sh`
- 如果是 Elasticsearch 集群节点，记得先在宿主机执行 `sysctl -w vm.max_map_count=262144`
- 如果你准备让 Cloudreve 直接使用栈内 Kafka，再把 `CLOUDREVE_GLOBAL_KAFKA_ENABLED=true`
- Kafka UI 默认访问地址是 `http://<node-or-lb>:18089`

## 3. Colima 说明

如果你是在 macOS + Colima 上跑 Swarm：

- `PG_*_DATA_PATH` / `REDIS_*_DATA_PATH` 必须是宿主机真实路径
- 这些路径必须已经被 Colima 共享进虚拟机，或者通过 Colima mount 显式挂进去
- 不要把它们写成容器里的路径
- 其他运行目录如果不确定 Colima mount 是否可靠，优先保持默认 `volume` 模式

## 4. 首次部署顺序

首次部署建议：

- `CLOUDREVE_MASTER_REPLICAS=1`
- `CLOUDREVE_SLAVE_SECRET` 先保留占位值

执行：

```bash
docker/swarm/deploy-stack.sh
```

如果要启用集群版 MinIO / Elasticsearch / Kafka：

```bash
docker/swarm/deploy-stack.sh --with-cluster
```

只有在你确实需要自定义字体目录时，才额外叠加：

```bash
WITH_BIND=yes docker/swarm/deploy-stack.sh
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

## 9. 最佳实践

- PostgreSQL：本地 SSD + 主从复制 + pgpool
- Redis：本地 SSD + 主从复制 + Sentinel
- Cloudreve 文件：S3 / MinIO / 对象存储
- MinIO 集群：4 个独立节点或 4 个独立本地数据目录
- Elasticsearch 集群：3 个独立节点，并提前处理 `vm.max_map_count`
- Kafka 集群：3 个独立节点，优先只给 Swarm 内部服务使用
- 不要把 PGDATA / Redis AOF / RDB 放到对象存储
- 如果改外部 S3，优先在第一次初始化前改 `CR_INIT_S3_*`
