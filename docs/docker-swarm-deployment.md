# Cloudreve Docker Swarm 中文部署指南

本文档对应仓库中的以下文件：

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

如果你现在更需要一份最短执行路径，先看：

- `docs/docker-swarm-quickstart.md`

如果你就是按 `1 manager + 3 worker + 128GB/64 线程` 上真实环境，直接看：

- `docs/docker-swarm-production-checklist.md`

如果你现在需要完整的线上部署、巡检、扩缩容、回滚、备份、排障手册，直接看：

- `docs/docker-swarm-operations-manual.md`

如果你还需要把统一认证前后端一起纳入同一套 Swarm / 私有仓库 / overlay 网络，再看：

- `docs/docker-swarm-auth-deployment.md`

如果你现在关注的是多 manager / 多机器下 `.env.swarm` 的生效范围，再看：

- `docs/docker-swarm-env-sync.md`

## 1. 当前默认架构

当前 Swarm 栈默认采用如下拓扑：

- 默认模式：`docker-compose.swarm.single.yml` 单节点全量栈
- 集群模式：`docker-compose.swarm.yml + docker-compose.swarm.foundation.yml + docker-compose.swarm.cluster.yml`

默认单节点全量栈包含：

- `cloudreve-master`
- `cloudreve-master-proxy`
- `postgresql-1 + pgpool`
- `redis-1 + redis-proxy`
- `minio + minio-init`
- `elasticsearch`
- `kafka + kafka-ui`
- `tika`
- `onlyoffice`

集群模式下采用如下拓扑：

- `registry`：单独的自定义镜像私有仓库栈
- `cloudreve-master`：Cloudreve 主站
- `cloudreve-master-proxy`：主站入口 Nginx
- `cloudreve-slave`：Cloudreve 从节点
- `cloudreve-slave-proxy`：从节点入口 Nginx
- `postgresql-1/2/3 + pgpool`：PostgreSQL 高可用
- `redis-1/2/3 + redis-sentinel + redis-proxy`：Redis 高可用
- `minio`：默认 S3 兼容对象存储入口
- `elasticsearch`：全文检索入口
- `kafka`：Kafka 内部 bootstrap 入口
- `kafka-ui`：Kafka 集群管理界面
- `tika`：文档解析
- `onlyoffice`：OnlyOffice 8 文档协作服务，默认关闭

现在 6 组 Swarm YML 的职责是：

- `docker-compose.swarm.single.yml`：默认单节点全量栈
- `docker-compose.swarm.yml`：只放 `cloudreve` 主从与入口代理
- `docker-compose.swarm.foundation.yml`：放多节点模式下共用的 `PG / Redis / Tika / OnlyOffice`
- `docker-compose.swarm.cluster.yml`：放 `MinIO / Kafka / Elasticsearch / Kafka UI` 集群形态
- `docker-compose.swarm.auth.yml`：放 `authverse-web + authverse-backend`
- `docker-compose.swarm.registry.yml`：只放 `registry:2`

默认设计目标：

- 自定义镜像固定放到 1 台 manager 上的 `registry:2`
- PostgreSQL / Redis 固定到带标签的节点上
- PostgreSQL / Redis 使用宿主机物理目录
- Cloudreve 运行目录、MinIO、Elasticsearch、Tika 字体目录同时兼容命名卷和宿主机绝对路径
- Cloudreve 用户文件默认走 S3 兼容对象存储
- Cloudreve 不再默认把用户文件写到宿主机目录

## 2. 核心结论

先把结论定下来：

- PostgreSQL：不要共享运行数据目录
- Redis：不要共享运行数据目录
- PostgreSQL / Redis：每个副本一个独立宿主机目录
- Cloudreve 文件：默认就是 S3 兼容对象存储
- 栈内默认 S3 实现是单节点 `minio`
- `cloudreve-master`：默认还是建议 `1` 副本
- 默认 `docker/swarm/deploy-stack.sh` 直接走单节点全量模板
- `SWARM_WITH_CLUSTER=yes` 时，会切到多节点组合，并额外启用 3 节点 Kafka 集群

这套模板不是“让数据库共享卷跑起来”，而是：

- 本地磁盘
- 复制
- 故障切换
- 对象存储

同时要注意：

- `PG / Redis` 仍然强制使用宿主机物理路径
- `cloudreve-master` / `minio` / `elasticsearch` / `tika` 字体目录默认使用命名卷
- 上面这些服务如果你要切换到宿主机绝对路径，可以通过 `*_MOUNT_TYPE=bind` 和 `*_MOUNT_SOURCE=/absolute/path` 切换

## 3. 关于镜像默认值

模板里当前默认使用：

- `bitnamilegacy/postgresql-repmgr:17.6.0-debian-12-r2`
- `bitnamilegacy/pgpool:4.6.3-debian-12-r0`
- `bitnamilegacy/minio:2024.10.2-debian-12-r0`
- `bitnamilegacy/redis:8.2.1-debian-12-r0`
- `bitnamilegacy/redis-sentinel:8.2.1-debian-12-r0`
- `cloudreve/tika:3.2.3.0-full-unrar-charset`

原因很直接：

- 这些默认值现在都和 Docker Hub 上实际可直接 `pull` 的仓库名与 tag 保持一致
- 不再依赖本地 retag 成 `bitnami/*`
- 截至 `2026-04-11`，已用 `docker manifest inspect` 复核，上述固定版本 tag 仍以 `bitnamilegacy/*` 可拉取为准
- `TIKA_IMAGE` 是自定义镜像，不在 Docker Hub 公共仓库里，生产里应先推到固定私有仓库，再让其它节点远程拉取
- 所以生产里建议先在每台节点执行：

```bash
docker/swarm/prepare-bitnami-images.sh --check
docker/swarm/prepare-bitnami-images.sh
```

如果你使用仓库内置的 `registry:2` 方案，推荐顺序是：

```bash
sudo docker/swarm/prepare-private-registry.sh --env-file .env.swarm --apply --restart-docker
docker/swarm/deploy-private-registry.sh --env-file .env.swarm
docker/swarm/publish-private-images.sh --env-file .env.swarm
```

如果你要顺手打出一份离线镜像包，直接执行：

```bash
docker/swarm/export-swarm-images.sh --env-file .env.swarm --output-dir .
```

常见覆盖变量：

- `POSTGRESQL_REPMGR_IMAGE`
- `PGPOOL_IMAGE`
- `MINIO_IMAGE`
- `REDIS_IMAGE`
- `REDIS_SENTINEL_IMAGE`
- `TIKA_IMAGE`
- `AUTHVERSE_WEB_LOCAL_IMAGE`
- `AUTHVERSE_WEB_REMOTE_IMAGE`
- `AUTHVERSE_BACKEND_LOCAL_IMAGE`
- `AUTHVERSE_BACKEND_REMOTE_IMAGE`
- `PRIVATE_REGISTRY_ADDR`
- `TIKA_LOCAL_IMAGE`
- `TIKA_REMOTE_IMAGE`

## 4. 宿主机目录要求

`docker-compose.swarm.yml` 现在把所有挂载统一成了 `*_MOUNT_TYPE + *_MOUNT_SOURCE`。

其中 PostgreSQL / Redis 默认仍然强制走宿主机物理路径：

- `PG_1_DATA_MOUNT_TYPE=bind`
- `PG_1_DATA_MOUNT_SOURCE`
- `PG_2_DATA_MOUNT_TYPE=bind`
- `PG_2_DATA_MOUNT_SOURCE`
- `PG_3_DATA_MOUNT_TYPE=bind`
- `PG_3_DATA_MOUNT_SOURCE`
- `REDIS_1_DATA_MOUNT_TYPE=bind`
- `REDIS_1_DATA_MOUNT_SOURCE`
- `REDIS_2_DATA_MOUNT_TYPE=bind`
- `REDIS_2_DATA_MOUNT_SOURCE`
- `REDIS_3_DATA_MOUNT_TYPE=bind`
- `REDIS_3_DATA_MOUNT_SOURCE`

这几个目录必须满足：

- 在对应节点真实存在
- 属于本机本地磁盘或你可控的块存储
- 不能是多个数据库实例共享的同一路径

建议目录：

```bash
mkdir -p /srv/cloudreve/postgresql/1
mkdir -p /srv/cloudreve/postgresql/2
mkdir -p /srv/cloudreve/postgresql/3
mkdir -p /srv/cloudreve/redis/1
mkdir -p /srv/cloudreve/redis/2
mkdir -p /srv/cloudreve/redis/3
```

也可以直接在目标节点执行仓库内脚本，让它按 `.env.swarm` 自动建目录并修正常见权限：

```bash
sudo docker/swarm/prepare-bind-paths.sh --check
sudo docker/swarm/prepare-bind-paths.sh --services pg,redis
```

另外，下列服务默认不是强制宿主机路径，而是默认使用命名卷：

- `cloudreve-master` 运行目录
- `cloudreve-slave` 运行目录
- `minio` 数据目录
- `elasticsearch` 数据目录
- `tika` 自定义字体目录

如果你要把它们改成宿主机绝对路径，可在 `.env.swarm` 中设置：

```bash
CLOUDREVE_MASTER_RUNTIME_MOUNT_TYPE=bind
CLOUDREVE_MASTER_RUNTIME_MOUNT_SOURCE=/srv/cloudreve/master-runtime

CLOUDREVE_SLAVE_RUNTIME_MOUNT_TYPE=bind
CLOUDREVE_SLAVE_RUNTIME_MOUNT_SOURCE=/srv/cloudreve/slave-runtime

MINIO_DATA_MOUNT_TYPE=bind
MINIO_DATA_MOUNT_SOURCE=/srv/cloudreve/minio

ELASTICSEARCH_DATA_MOUNT_TYPE=bind
ELASTICSEARCH_DATA_MOUNT_SOURCE=/srv/cloudreve/elasticsearch

TIKA_CUSTOM_FONTS_MOUNT_TYPE=bind
TIKA_CUSTOM_FONTS_MOUNT_SOURCE=/srv/cloudreve/tika-fonts
```

如果这些服务已经切到了 `bind`，也可以在对应节点执行：

```bash
sudo docker/swarm/prepare-bind-paths.sh --services cloudreve,minio,elasticsearch,tika
```

如果是多机 Swarm，建议同时把这些服务固定到带标签的节点，避免任务被调度到没有该绝对路径的机器。

兼容性说明：

- 旧的 `PG_*_DATA_PATH` / `REDIS_*_DATA_PATH` 变量仍然会被脚本自动映射
- 但新部署建议统一只使用 `*_MOUNT_TYPE + *_MOUNT_SOURCE`

如果你启用了 `SWARM_WITH_CLUSTER=yes`，MinIO / Elasticsearch / Kafka 要改的是各自节点路径，而不是单节点路径：

```bash
MINIO_1_DATA_MOUNT_TYPE=bind
MINIO_1_DATA_MOUNT_SOURCE=/srv/cloudreve/minio/1
MINIO_2_DATA_MOUNT_TYPE=bind
MINIO_2_DATA_MOUNT_SOURCE=/srv/cloudreve/minio/2
MINIO_3_DATA_MOUNT_TYPE=bind
MINIO_3_DATA_MOUNT_SOURCE=/srv/cloudreve/minio/3
MINIO_4_DATA_MOUNT_TYPE=bind
MINIO_4_DATA_MOUNT_SOURCE=/srv/cloudreve/minio/4

ELASTICSEARCH_1_DATA_MOUNT_TYPE=bind
ELASTICSEARCH_1_DATA_MOUNT_SOURCE=/srv/cloudreve/elasticsearch/1
ELASTICSEARCH_2_DATA_MOUNT_TYPE=bind
ELASTICSEARCH_2_DATA_MOUNT_SOURCE=/srv/cloudreve/elasticsearch/2
ELASTICSEARCH_3_DATA_MOUNT_TYPE=bind
ELASTICSEARCH_3_DATA_MOUNT_SOURCE=/srv/cloudreve/elasticsearch/3

KAFKA_1_DATA_MOUNT_TYPE=bind
KAFKA_1_DATA_MOUNT_SOURCE=/srv/cloudreve/kafka/1
KAFKA_2_DATA_MOUNT_TYPE=bind
KAFKA_2_DATA_MOUNT_SOURCE=/srv/cloudreve/kafka/2
KAFKA_3_DATA_MOUNT_TYPE=bind
KAFKA_3_DATA_MOUNT_SOURCE=/srv/cloudreve/kafka/3
```

然后在对应节点执行：

```bash
sudo docker/swarm/prepare-bind-paths.sh --services minio,elasticsearch,kafka
```

## 5. Colima 说明

如果你是在 macOS + Colima 下联调或部署：

- `PG_*_DATA_MOUNT_SOURCE` / `REDIS_*_DATA_MOUNT_SOURCE` 必须是 macOS 宿主机上的真实目录
- 这些目录必须已经共享进 Colima 虚拟机
- 不要把目录写成容器内部路径
- 不要指望 `network_mode: host`

实操上至少要保证：

- 目录位于 Colima 已共享的宿主机路径下
- 或者你通过 Colima 的 mount 配置显式挂进去
- 如果不满足这两个条件，就优先使用默认命名卷模式，不要强行切 `bind`

## 6. 默认端口

当前默认对外端口如下：

- `cloudreve-master-proxy`: `80`
- `pgpool`: `15432`
- `redis-proxy`: `16379`
- `minio api`: `9000`
- `minio console`: `9001`
- `elasticsearch`: `9200/9300`
- `kafka` 内部入口：`9092`
- `kafka-ui`: `18089`
- `tika`: `9998`

其中这两个就是为了避免直接占用默认数据库端口而改掉的：

- `PGPOOL_PUBLIC_PORT=15432`
- `REDIS_PROXY_PUBLIC_PORT=16379`

## 7. 默认 S3 初始化逻辑

当前模板已经不是“第一次启动后手动去后台把默认存储切成 MinIO”。

现在的行为是：

1. `cloudreve-master` 首次初始化数据库时检查是否存在 `storage_policy ID=1`
2. 如果不存在，则读取 `CR_INIT_DEFAULT_STORAGE`
3. 当值为 `s3` 时，直接创建 `S3` 类型的默认存储策略

默认 `.env.swarm.example` 给出的值是：

- `CR_INIT_DEFAULT_STORAGE=s3`
- `CR_INIT_S3_ENDPOINT=http://minio:9000`
- `CR_INIT_S3_BUCKET=cloudreve`
- `CR_INIT_S3_ACCESS_KEY=minio`
- `CR_INIT_S3_SECRET_KEY=<same secret model as MinIO>`
- `CR_INIT_S3_FORCE_PATH_STYLE=true`
- `CR_INIT_S3_RELAY=true`
- `CR_INIT_S3_INTERNAL_PROXY=true`

这样做的目的：

- 上传不要求浏览器直连内部 `minio`
- 下载不要求客户端直接访问内部对象存储地址
- 默认链路在内网和反向代理场景下更稳

注意：

- 这只在第一次数据库初始化时生效
- 如果数据库已经初始化过，`ID=1` 默认策略不会被自动重建
- 如果你要切换到外部 S3，请在第一次部署前修改 `CR_INIT_S3_*`

如果启用了 `SWARM_WITH_CLUSTER=yes`，这里仍然保持 `http://minio:9000` 不变。

原因是：

- 集群模式下，`minio` 这个服务名会变成代理入口
- 后端实际数据节点是 `minio-1` 到 `minio-4`
- 所以 Cloudreve 初始化参数不需要改成 `minio-proxy`

## 8. MinIO 默认行为

模板里的 `minio` 相关服务默认开启：

- `MINIO_DEFAULT_BUCKETS=cloudreve`

这意味着首次部署后会由 `minio-init` 服务自动创建默认 bucket，避免“策略有了但 bucket 不存在”。

要清楚一点：

- 栈内 `minio` 只是单副本默认对象存储
- 它适合真实环境联调或中小规模生产起步
- 如果你要最终高可用对象存储，还是应该切到独立 MinIO 集群或外部 S3

如果你要直接在 Swarm 里启用集群版 MinIO / Elasticsearch / Kafka，打开：

```bash
SWARM_WITH_CLUSTER=yes
```

然后使用：

```bash
docker/swarm/deploy-stack.sh --with-cluster
```

集群模式下的服务拓扑是：

- `minio`：对内对外统一入口代理
- `minio-1` / `minio-2` / `minio-3` / `minio-4`：MinIO 分布式数据节点
- `minio-init`：默认 bucket 持续初始化服务
- `elasticsearch`：对内对外统一入口代理
- `elasticsearch-1` / `elasticsearch-2` / `elasticsearch-3`：Elasticsearch 集群节点
- `kafka`：Swarm 内部 bootstrap 入口
- `kafka-1` / `kafka-2` / `kafka-3`：Kafka KRaft 集群节点
- `kafka-ui`：Kafka Web 管理界面

这时：

- Cloudreve 默认 S3 初始化地址仍是 `http://minio:9000`
- Cloudreve 后台里的 FTS Elasticsearch 地址应填写 `http://elasticsearch:9200`
- 如果 Cloudreve 要直接使用栈内 Kafka，全局 Kafka brokers 填 `kafka:9092`
- Elasticsearch 所在宿主机必须先执行 `sysctl -w vm.max_map_count=262144`
- Kafka UI 默认访问地址是 `http://<node-or-lb>:18089`

补充说明：

- `minio-1..4` 的容器 hostname 会固定成 `minio-1..4`
- 这是为了满足 MinIO 分布式节点自识别
- 真正落在哪台物理机上，看 `docker service ps <stack>_minio-1` 这类命令，不要只看容器 hostname

Kafka 这里默认只提供 Swarm 内部入口，不直接给 Swarm 外部客户端暴露 broker 地址。

原因：

- Kafka 客户端会基于 broker metadata 继续直连各节点
- 只做一个对外 TCP 代理并不能完整替代外部 advertised listeners
- 所以当前模板先保证 Swarm 内部服务稳定可用

如果你的第三方抽取器也在 Swarm 内部网络里，直接用：

```bash
kafka:9092
```

Kafka UI 本身也走这个内部 bootstrap 地址，所以它会自动看到 `kafka-1/2/3` 这组 broker。

建议在每台 Elasticsearch 节点持久化：

```bash
echo 'vm.max_map_count=262144' | sudo tee /etc/sysctl.d/99-cloudreve-elasticsearch.conf
sudo sysctl --system
```

## 9. 资源默认值

当前模板按 `1000w` 级别元数据规模给出基础默认值：

- `cloudreve-master`: reservation `1C / 2G`, limit `2C / 4G`
- `cloudreve-slave`: reservation `0.5C / 1G`, limit `1.5C / 2G`
- `postgresql-*`: reservation `2C / 4G`, limit `4C / 8G`
- `pgpool`: reservation `0.5C / 512M`, limit `2C / 2G`
- `redis-*`: reservation `1C / 2G`, limit `2C / 4G`
- `redis-sentinel`: reservation `0.25C / 256M`, limit `1C / 512M`
- `redis-proxy`: reservation `0.25C / 256M`, limit `1C / 512M`
- `minio`: reservation `1C / 2G`, limit `2C / 4G`
- `elasticsearch`: reservation `2C / 4G`, limit `4C / 8G`
- `tika`: reservation `0.5C / 1G`, limit `2C / 2G`

如果启用了集群模式，建议起步值改成：

- `minio` 代理：reservation `0.25C / 256M`, limit `1C / 512M`
- `minio-1..4`：每节点 reservation `1C / 2G`, limit `2C / 4G`
- `elasticsearch` 代理：reservation `0.25C / 256M`, limit `1C / 512M`
- `elasticsearch-1..3`：每节点 reservation `2C / 6G`, limit `4C / 8G`
- `ELASTICSEARCH_CLUSTER_NODE_JAVA_OPTS=-Xms4g -Xmx4g`
- `kafka` 代理：reservation `0.25C / 256M`, limit `1C / 512M`
- `kafka-1..3`：每节点 reservation `1C / 2G`, limit `2C / 4G`
- `KAFKA_CLUSTER_NODE_JVM_HEAP_OPTS=-Xms1g -Xmx1g`
- `kafka-ui`：reservation `0.25C / 256M`, limit `1C / 1G`

另外：

- Redis 默认启用 `AOF`
- Elasticsearch 默认 `ES_JAVA_OPTS=-Xms4g -Xmx4g`

这些值是默认起点，不是容量上限。你仍然需要根据：

- 并发用户数
- 文件上传吞吐
- 全文索引规模
- 预览和转码压力
- 对象存储 RTT

继续调优。

## 10. 准备 Swarm 集群

建议先把各台机器的主机名定好，再让它们加入 Swarm。

现在模板里的主要服务也会自动把容器 `hostname` 带上当前 `Swarm` 节点名，
格式类似：

- `cr-prod-wkr-1-pg1`
- `cr-prod-mgr-1-cr-master-1`
- `cr-prod-wkr-3-kafka3`

这样你在容器内部、日志或故障排查时，也能直接看出任务落在哪台机器。

原因：

- `docker node ls` 里显示的是节点主机名
- `docker service ps` / `docker stack ps` 的 `NODE` 列也直接显示这个名字
- 名字提前规划好，后续排查“服务具体跑在哪台机器”会非常直观

推荐命名方式：

- `cr-prod-mgr-1`
- `cr-prod-wkr-1`
- `cr-prod-wkr-2`
- `cr-prod-wkr-3`

如果你更喜欢主机名里直接体现角色，也可以：

- `cr-prod-pg-1`
- `cr-prod-cache-1`
- `cr-prod-search-1`

但更推荐的原则是：

- 主机名表达物理节点身份
- Swarm label 表达调度角色

也就是：

- 主机名看机器
- label 看业务角色

在 Linux 上，建议在节点加入 Swarm 之前先设置：

```bash
sudo hostnamectl set-hostname cr-prod-wkr-1
```

然后再执行 `docker swarm init` 或 `docker swarm join`。

选择一台机器作为初始 manager：

```bash
docker swarm init --advertise-addr <MANAGER_IP>
```

在 manager 上获取 worker 加入命令：

```bash
docker swarm join-token worker
```

检查集群状态：

```bash
docker node ls
```

## 11. 给状态服务打标签

在 manager 节点执行：

```bash
docker node update --label-add cloudreve.pg1=true <node-pg-1>
docker node update --label-add cloudreve.pg2=true <node-pg-2>
docker node update --label-add cloudreve.pg3=true <node-pg-3>

docker node update --label-add cloudreve.redis1=true <node-redis-1>
docker node update --label-add cloudreve.redis2=true <node-redis-2>
docker node update --label-add cloudreve.redis3=true <node-redis-3>
```

如果你把可选 bind 服务也固定到某些节点，还可以继续打这些标签：

```bash
docker node update --label-add cloudreve.master=true <node-master-runtime>
docker node update --label-add cloudreve.slave=true <node-slave-runtime>
docker node update --label-add cloudreve.edge=true <node-edge>
docker node update --label-add cloudreve.minio=true <node-minio>
docker node update --label-add cloudreve.elasticsearch=true <node-elasticsearch>
docker node update --label-add cloudreve.tika=true <node-tika>
docker node update --label-add cloudreve.redis-sentinel=true <node-redis-sentinel>
```

如果启用了 MinIO / Elasticsearch 集群，再补这些标签：

```bash
docker node update --label-add cloudreve.minio1=true <node-minio-1>
docker node update --label-add cloudreve.minio2=true <node-minio-2>
docker node update --label-add cloudreve.minio3=true <node-minio-3>
docker node update --label-add cloudreve.minio4=true <node-minio-4>

docker node update --label-add cloudreve.es1=true <node-es-1>
docker node update --label-add cloudreve.es2=true <node-es-2>
docker node update --label-add cloudreve.es3=true <node-es-3>

docker node update --label-add cloudreve.kafka1=true <node-kafka-1>
docker node update --label-add cloudreve.kafka2=true <node-kafka-2>
docker node update --label-add cloudreve.kafka3=true <node-kafka-3>
docker node update --label-add cloudreve.kafka-ui=true <node-kafka-ui>
```

在你现在的 `1 manager + 3 worker` 拓扑里，比较实用的映射是：

- `minio1` 放 manager
- `minio2/3/4` 放 3 台 worker
- `es1/2/3` 放 3 台 worker
- `kafka1/2/3` 放 3 台 worker
- `kafka-ui` 放 manager 或任意可直接访问的入口节点

如果你的机器规格就是 `4 台 x 128GB 内存 / 64 线程`，并且准备直接使用仓库内的
`.env.swarm.prod-4x128g.example`，建议按下面打标签：

- `cr-prod-mgr-1`：`cloudreve.master`, `cloudreve.edge`, `cloudreve.registry`, `cloudreve.minio1`, `cloudreve.tika`, `cloudreve.kafka-ui`
- `cr-prod-wkr-1`：`cloudreve.slave`, `cloudreve.edge`, `cloudreve.pg1`, `cloudreve.redis1`, `cloudreve.redis-sentinel`, `cloudreve.es1`, `cloudreve.kafka1`, `cloudreve.minio2`
- `cr-prod-wkr-2`：`cloudreve.slave`, `cloudreve.pg2`, `cloudreve.redis2`, `cloudreve.redis-sentinel`, `cloudreve.es2`, `cloudreve.kafka2`, `cloudreve.minio3`
- `cr-prod-wkr-3`：`cloudreve.slave`, `cloudreve.edge`, `cloudreve.pg3`, `cloudreve.redis3`, `cloudreve.redis-sentinel`, `cloudreve.es3`, `cloudreve.kafka3`, `cloudreve.minio4`, `cloudreve.tika`

这样做的目的不是把机器吃满，而是：

- PostgreSQL / Elasticsearch / Kafka / Redis 固定在可预期节点
- `edge` 标签承接入口代理、`pgpool`、`redis-proxy`、`minio/elasticsearch/kafka` 代理
- `tika` 只放 2 台机器，避免无意义铺满 4 台
- 每台机器都保留大量系统缓存和扩容余量

## 12. 准备环境变量

复制模板：

```bash
cp .env.swarm.example .env.swarm
```

如果你的环境就是 `1 manager + 3 worker`，并且每台机器都是 `128GB 内存 / 64 线程`，可以直接从这个生产基线开始：

```bash
cp .env.swarm.prod-4x128g.example .env.swarm
```

至少填好这些值：

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

如果你要用外部对象存储，再改：

- `CR_INIT_S3_ENDPOINT`
- `CR_INIT_S3_BUCKET`
- `CR_INIT_S3_ACCESS_KEY`
- `CR_INIT_S3_SECRET_KEY`
- `CR_INIT_S3_REGION`

如果你要启用栈内 MinIO / Elasticsearch / Kafka 集群，再确认这些变量：

- `SWARM_WITH_CLUSTER=yes`
- `MINIO_1_DATA_MOUNT_TYPE` 到 `MINIO_4_DATA_MOUNT_TYPE`
- `MINIO_1_DATA_MOUNT_SOURCE` 到 `MINIO_4_DATA_MOUNT_SOURCE`
- `ELASTICSEARCH_1_DATA_MOUNT_TYPE` 到 `ELASTICSEARCH_3_DATA_MOUNT_TYPE`
- `ELASTICSEARCH_1_DATA_MOUNT_SOURCE` 到 `ELASTICSEARCH_3_DATA_MOUNT_SOURCE`
- `MINIO_NODE_1_CONSTRAINT` 到 `MINIO_NODE_4_CONSTRAINT`
- `ELASTICSEARCH_NODE_1_CONSTRAINT` 到 `ELASTICSEARCH_NODE_3_CONSTRAINT`
- `KAFKA_1_DATA_MOUNT_TYPE` 到 `KAFKA_3_DATA_MOUNT_TYPE`
- `KAFKA_1_DATA_MOUNT_SOURCE` 到 `KAFKA_3_DATA_MOUNT_SOURCE`
- `KAFKA_NODE_1_CONSTRAINT` 到 `KAFKA_NODE_3_CONSTRAINT`
- `CLOUDREVE_GLOBAL_KAFKA_ENABLED`
- `CLOUDREVE_GLOBAL_KAFKA_BROKERS`
- `KAFKA_UI_HTTP_PORT`
- `KAFKA_UI_NODE_CONSTRAINT`

## 13. 首次部署

在 manager 节点执行：

```bash
docker/swarm/prepare-private-registry.sh --env-file .env.swarm --apply --restart-docker
docker/swarm/prepare-bitnami-images.sh
docker/swarm/deploy-private-registry.sh --env-file .env.swarm
docker/swarm/publish-private-images.sh --env-file .env.swarm
docker/swarm/deploy-stack.sh
```

如果要直接启用集群版 MinIO / Elasticsearch：

```bash
docker/swarm/deploy-stack.sh --with-cluster
```

如果你需要 Tika 自定义字体，直接在 `.env.swarm` 中设置：

```env
TIKA_CUSTOM_FONTS_MOUNT_TYPE=bind
TIKA_CUSTOM_FONTS_MOUNT_SOURCE=/srv/cloudreve/tika-fonts
```

然后正常部署：

```bash
docker/swarm/deploy-stack.sh --with-cluster
```

说明：

- 现在统一只用 `.env.swarm` 里的 `*_MOUNT_TYPE` / `*_MOUNT_SOURCE` 控制命名卷或宿主机路径
- `docker-compose.swarm.bind.yml` 已经废弃，不再需要额外叠加 compose 文件

检查服务状态：

```bash
docker stack services cloudreve
docker stack ps cloudreve
docker service logs -f cloudreve_cloudreve-master
```

## 14. 主站初始化

主站首次启动后：

1. 打开 `http(s)://<master-host-or-lb>/admin`
2. 登录后台
3. 在 `设置 -> 基本设置` 中确认“站点 URL（Site URL）”与 `CLOUDREVE_SITE_URL` 完全一致
4. 在 `管理面板 -> 存储策略` 中确认默认策略是 `S3`

如果这里看到的还是本地策略，一般只有两种情况：

- 数据库不是首次初始化
- `CR_INIT_DEFAULT_STORAGE` / `CR_INIT_S3_*` 没有在第一次初始化前生效

## 15. 多节点模式下注册从节点

在主站后台执行：

1. 进入 `管理面板 -> 节点 -> 新建节点`
2. 新建一个 slave node
3. 复制后台生成的从节点密钥（`Slave Key`）
4. 把 `.env.swarm` 中的 `CLOUDREVE_SLAVE_SECRET` 替换成真实值
5. 重新发布：

```bash
docker/swarm/deploy-stack.sh --with-cluster
```

如果你要使用非默认栈名，例如联调用的 `cloudreve-debug`：

```bash
STACK_NAME=cloudreve-debug docker/swarm/deploy-stack.sh
```

## 16. 扩容原则

可以直接横向扩的服务：

- `cloudreve-master-proxy`
- `tika`
- `pgpool`
- `redis-sentinel`
- `redis-proxy`

如果启用了多节点 Cloudreve 从站，也可以继续扩：

- `cloudreve-slave`
- `cloudreve-slave-proxy`

不要直接扩容的服务：

- `postgresql-1`
- `postgresql-2`
- `postgresql-3`
- `redis-1`
- `redis-2`
- `redis-3`

当前模板里 `cloudreve-master` 默认仍然建议保持 `1` 副本。

原因不是文件默认还在本地，而是：

- 运行目录仍然是本地命名卷
- 你没有显式共享运行时目录前，多 master 不适合直接打开

## 17. 运维建议

- PostgreSQL：做常规备份和 WAL 归档
- Redis：定期导出 RDB / AOF 备份
- MinIO / 外部 S3：做 bucket 生命周期和版本治理
- Elasticsearch：单独监控 heap、segment、磁盘水位
- Tika：按文档解析峰值调副本

不要做的事：

- 不要把 PGDATA 放到对象存储
- 不要把 Redis 运行目录放到对象存储
- 不要让多个 PG / Redis 实例共享同一目录
- 不要在未验证共享运行目录前把 `cloudreve-master` 扩到多个副本
