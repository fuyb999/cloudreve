# Cloudreve Swarm 快速上线清单

这份清单对应当前仓库里的生产默认值：

- `docker-compose.swarm.cloudreve.yml`
- `docker-compose.swarm.single.yml`
- `docker-compose.swarm.foundation.yml`
- `docker-compose.swarm.registry.yml`
- `docker-compose.swarm.infra.yml`
- `.env.swarm.example`
- `.env.swarm.user-test.example`
- `.env.swarm.prod-4x256g.example`
- `docker/swarm/deploy-stack.sh`
- `docker/swarm/export-swarm-images.sh`
- `docker/swarm/prepare-bitnami-images.sh`
- `docker/swarm/prepare-private-registry.sh`
- `docker/swarm/publish-private-images.sh`
- `docker/swarm/sync-swarm-assets.sh`
- `docker-compose.swarm.auth.yml`
- `docker/swarm/build-auth-images.sh`
- `docker/swarm/init-authverse-db.sh`
- `docs/docker-swarm-production-checklist.md`
- `docs/docker-swarm-deployment.md`
- `docs/docker-swarm-env-sync.md`
- `docs/docker-swarm-auth-deployment.md`

如果你需要完整中文运维手册，直接看：

- `docs/docker-swarm-operations-manual.md`

如果你要复刻这次 Parallels 3 节点真实联调，从 ISO 重建、节点规划、私有仓库、分阶段部署到压测排障一步步照着做，直接看：

- `docs/docker-swarm-parallels-3node-full-runbook.md`

如果你这次还要把统一认证前后端一起挂进 Swarm，再看：

- `docs/docker-swarm-auth-deployment.md`

如果你就是按 `1 manager + 3 worker + 256GB/64 线程` 上真实环境，直接看：

- `docs/docker-swarm-production-checklist.md`

## 1. 最短结论

- PostgreSQL 和 Redis 默认就是独立宿主机物理目录，不共享运行数据
- 所有数据/运行目录挂载都统一支持 `*_MOUNT_TYPE + *_MOUNT_SOURCE`
- `pgpool` 默认对外发布 `15432`
- `redis-proxy` 默认对外发布 `16379`
- 所有对外入口现在默认启用 TLS，证书统一由一套自签 CA 管理
- 业务 overlay 网络默认启用跨节点加密，并且不再允许独立容器直接 attach
- 文本配置、启动脚本、Nginx / HAProxy 模板统一通过 Swarm `configs` 下发
- `deploy-stack.sh` 会按 stack 名自动创建 / 复用版本化 Docker `secrets`，敏感值不再直接写进 service 环境变量
- Cloudreve 不再默认把用户文件落到宿主机目录
- `cloudreve-master` / `minio` / `elasticsearch` / `kafka` / `tika` 字体目录现在同时支持命名卷和宿主机绝对路径两种挂载模式
- `Tika` 和 `OnlyOffice` 默认共用一套外挂字体目录
- `PG / Redis` 默认仍是 `bind`，但变量名也统一成了 `*_MOUNT_TYPE + *_MOUNT_SOURCE`
- Cloudreve 首次初始化时，默认存储策略会直接创建成 `S3` 兼容存储
- 默认 `S3` 指向栈内 `minio-internal`，并由 `minio-init` 持续确保 `cloudreve` bucket 存在
- 默认 `docker/swarm/deploy-stack.sh` 会直接使用单节点全量模板
- 默认单节点模板也会把 `authverse-web + authverse-backend` 一起部署进主栈
- 默认单节点部署后会自动执行一次 `docker/swarm/init-authverse-db.sh`
- 拆分发布时，`foundation / infra / cloudreve / auth / registry` 分别是独立 stack，并共享 `SWARM_SHARED_NETWORK`
- Kafka UI 会一起挂上，默认对外端口 `18089`
- 默认单节点模板里 OnlyOffice 8 也会一起起来；生产样例仍可通过 `ONLYOFFICE_REPLICAS=0` 关闭
- 自定义镜像现在建议统一放到单点 `registry:2` 仓库栈，其它节点远程拉取
- `cloudreve-master` 仍建议先保持 `1` 副本

当前文件分组：

- `docker-compose.swarm.single.yml`：默认单节点全量栈
- `docker-compose.swarm.cloudreve.yml`：Cloudreve 主从
- `docker-compose.swarm.foundation.yml`：PG / Redis / Tika / OnlyOffice
- `docker-compose.swarm.infra.yml`：MinIO / minio-init / Kafka / ES / Kafka UI 集群
- `docker-compose.swarm.auth.yml`：Authverse 前后端
- `docker-compose.swarm.registry.yml`：私有仓库

补充：

- `.env.swarm.example` 默认让单节点 authverse 直接使用本地镜像 tag
- `.env.swarm.user-test.example` 是把 4 节点生产基线裁成 1 台 Linux 的用户测试版
- `.env.swarm.prod-4x256g.example` 仍然按私有仓库远程 tag 组织

## 2. 上线前准备

1. 初始化 Swarm 并把节点加入集群。
2. 复制环境模板：

如果机器有多网卡，初始化 / 加入 Swarm 时要同时指定：

```bash
docker swarm init --advertise-addr <manager-cluster-ip> --data-path-addr <manager-cluster-ip>
docker swarm join --token <worker-token> --advertise-addr <worker-cluster-ip> --data-path-addr <worker-cluster-ip> <manager-cluster-ip>:2377
```

不要只写 `--advertise-addr`。

如果你是多节点 Swarm，并且已经启用了业务 overlay 加密，还要确认节点间防火墙 / 安全组至少放通：

- `2377/tcp`
- `7946/tcp`
- `7946/udp`
- `4789/udp`
- `ESP`，也就是 `IP protocol 50`

如果你用的是 Ubuntu `ufw`，推荐直接看运维手册里的现成命令：

- `docs/docker-swarm-operations-manual.md`

```bash
cp .env.swarm.example .env.swarm
```

如果你现在是把原来的 4 台生产方案先裁成 1 台 Linux 做用户测试，直接用：

```bash
cp .env.swarm.user-test.example .env.swarm
```

这份模板的特点是：

- 默认走 `docker-compose.swarm.single.yml`
- 默认 `SWARM_IMAGE_SOURCE=remote`
- 已经把外部端口、TLS、Authverse、MinIO、ES、Kafka、Tika、OnlyOffice 全部收进一台机器
- 默认 bind 路径统一落到 `/srv/cloudreve-user-test/...`
- 运行镜像与 authverse 构建基镜像默认都走私有仓库

如果你的拓扑是 `1 manager + 3 worker`，并且每台机器都是 `256GB / 64 线程`，可以直接改用：

```bash
cp .env.swarm.prod-4x256g.example .env.swarm
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
- `SWARM_PKI_MOUNT_SOURCE`
- `SHARED_CUSTOM_FONTS_MOUNT_SOURCE`

5. 在 manager 生成默认 CA 和服务证书：

```bash
docker/swarm/generate-swarm-pki.sh --env-file .env.swarm --force-certs
```

说明：

- 默认根 CA 会生成到 `PRIVATE_REGISTRY_CA_FILE`
- 以后如果你要替换成自己的 CA，直接覆盖 `ca.crt + ca.key`，然后重新执行 `generate-swarm-pki.sh --force-ca --force-certs`
- `.env.swarm` 里的密码 / secret 仍然需要维护，但正式部署时 `deploy-stack.sh` 会把它们转换成 `${STACK_NAME}_secret_<secret-key>_<hash>` 形式的 Docker `secret`
- 同一个 `stack-name` 下如果密码发生变化，重新执行对应的 `deploy-stack.sh` 即可，脚本会引用新 secret，并尽力清理旧 secret
- `worker` 节点不需要 `.env.swarm`；只有执行部署的 manager 需要

6. 在所有 Swarm 节点写入私有仓库 `insecure-registries` 并刷新 Docker：

```bash
sudo docker/swarm/prepare-private-registry.sh --env-file .env.swarm --check
sudo docker/swarm/prepare-private-registry.sh --env-file .env.swarm --apply --restart-docker
```

说明：

- 默认 `PRIVATE_REGISTRY_SCHEME=http`
- 这个脚本会把 `PRIVATE_REGISTRY_ADDR` 写入 Docker `insecure-registries`
- 如果是多机并且 `SWARM_PKI_MOUNT_TYPE=bind` 或 `SHARED_CUSTOM_FONTS_MOUNT_TYPE=bind`，建议在 manager 再执行：

```bash
docker/swarm/sync-swarm-assets.sh --env-file .env.swarm --targets "node2,node3,node4" --check
docker/swarm/sync-swarm-assets.sh --env-file .env.swarm --targets "node2,node3,node4" --ssh-user root --apply
```

- 如果这些脚本是在其它 worker 节点执行，也要提前把同一份 `.env.swarm` 同步过去
- `.env.swarm` 只在部署 manager 上是必须品，但准备脚本想复用同一套变量时，其它节点也需要拿到一份

8. 默认严格模式不会自动从外部仓库补拉缺失镜像。上线前先确认 manager 本地已经具备待发布的源镜像：

```bash
docker/swarm/prepare-bitnami-images.sh --env-file .env.swarm --check
```

如果你只是做一次性引导，并且明确允许固定 manager 从 Docker Hub 拉取源镜像，再显式执行：

```bash
docker/swarm/prepare-bitnami-images.sh --env-file .env.swarm --pull
```

9. 在各个有状态节点提前创建目录：

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

10. 先部署私有仓库栈，再先推 authverse 构建基镜像，最后推整套业务镜像：

```bash
docker/swarm/deploy-stack.sh registry --env-file .env.swarm
docker/swarm/publish-private-images.sh --env-file .env.swarm --image-keys AUTHVERSE_WEB_BUILDER_BASE,AUTHVERSE_WEB_RUNTIME_BASE,AUTHVERSE_BACKEND_BUILDER_BASE,AUTHVERSE_BACKEND_RUNTIME_BASE
docker/swarm/build-auth-images.sh --env-file .env.swarm
docker/swarm/publish-private-images.sh --env-file .env.swarm
```

如果你要顺手打一份离线镜像包：

```bash
docker/swarm/export-swarm-images.sh --env-file .env.swarm --output-dir .
```

如果你要启用 `infra` 这组集群型基础设施，再额外确认：

- `MINIO_1_DATA_MOUNT_TYPE` 到 `MINIO_4_DATA_MOUNT_TYPE`
- `MINIO_1_DATA_MOUNT_SOURCE` 到 `MINIO_4_DATA_MOUNT_SOURCE`
- `ELASTICSEARCH_1_DATA_MOUNT_TYPE` 到 `ELASTICSEARCH_3_DATA_MOUNT_TYPE`
- `ELASTICSEARCH_1_DATA_MOUNT_SOURCE` 到 `ELASTICSEARCH_3_DATA_MOUNT_SOURCE`
- `KAFKA_1_DATA_MOUNT_TYPE` 到 `KAFKA_3_DATA_MOUNT_TYPE`
- `KAFKA_1_DATA_MOUNT_SOURCE` 到 `KAFKA_3_DATA_MOUNT_SOURCE`
- 对应节点在部署前执行：
  `sudo docker/swarm/prepare-bind-paths.sh --services minio,elasticsearch,kafka`

说明：

- `CR_INIT_S3_*` 必须在第一次初始化数据库前就确定
- 如果你要改成外部 S3 / MinIO，而不是用栈内 `minio`，就在第一次部署前改掉 `CR_INIT_S3_*`
- 如果数据库已经初始化过，默认存储策略 `ID=1` 不会被自动重建
- `PG / Redis` 仍然必须使用宿主机物理路径
- `cloudreve-master` / `minio` / `elasticsearch` / `kafka` / `tika` 字体目录默认走命名卷
- 如果你确实要改成宿主机绝对路径，就把对应的 `*_MOUNT_TYPE=bind`，并把 `*_MOUNT_SOURCE` 改成真实绝对路径
- 如果是多 manager / 多机器部署，再看 `docs/docker-swarm-env-sync.md`
- `SWARM_IMAGE_SOURCE=remote` 时，`*_REMOTE_IMAGE` 应统一指向 `${PRIVATE_REGISTRY_ADDR}`
- `publish-private-images.sh` 不会自动外部拉取缺失镜像；如果本地缺镜像，会直接失败
- `build-auth-images.sh` 默认也不会自动 `--pull` 基础镜像；`AUTHVERSE_*_BASE_IMAGE` 应先推入私有仓库
- 如果是 bind 模式，建议上线前先在目标节点执行 `docker/swarm/prepare-bind-paths.sh`
- 如果是 Elasticsearch 集群节点，记得先在宿主机执行 `sysctl -w vm.max_map_count=262144`
- 如果你准备让 Cloudreve 直接使用栈内 Kafka，再把 `CLOUDREVE_GLOBAL_KAFKA_ENABLED=true`
- Kafka UI 默认访问地址是 `https://<node-or-lb>:18089`

## 3. Colima 说明

如果你是在 macOS + Colima 上跑 Swarm：

- `PG_*_DATA_MOUNT_SOURCE` / `REDIS_*_DATA_MOUNT_SOURCE` 必须是宿主机真实路径
- 这些路径必须已经被 Colima 共享进虚拟机，或者通过 Colima mount 显式挂进去
- 不要把它们写成容器里的路径
- 其他运行目录如果不确定 Colima mount 是否可靠，优先保持默认 `volume` 模式

## 4. 首次部署顺序

首次部署建议：

- `CLOUDREVE_MASTER_REPLICAS=1`
- 先确认 `docker/swarm/deploy-stack.sh registry` 和 `docker/swarm/publish-private-images.sh` 已经执行完成

执行主业务栈：

```bash
docker/swarm/deploy-stack.sh
```

如果要按拆分模板补齐生产多栈：

```bash
docker/swarm/deploy-stack.sh foundation --stack-name cloudreve-prod-foundation
docker/swarm/deploy-stack.sh infra --stack-name cloudreve-prod-infra
docker/swarm/deploy-stack.sh cloudreve --stack-name cloudreve-prod-app
```

如果你确实需要给 Tika 指定宿主机字体目录，直接在 `.env.swarm` 里设置：

```env
SHARED_CUSTOM_FONTS_MOUNT_TYPE=bind
SHARED_CUSTOM_FONTS_MOUNT_SOURCE=/absolute/path/shared-fonts
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
docker secret ls | grep '^cloudreve.*_secret_'
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

如果启用了 `infra` 模板，这里仍然保持 `http://minio:9000` 不变。

同时在 Cloudreve 后台配置全文检索时，应填写：

- Elasticsearch 地址：`http://elasticsearch:9200`

如果你准备把第三方抽取链路也切到栈内 Kafka：

- `CLOUDREVE_GLOBAL_KAFKA_TLS_MODE=internal-plaintext`
- Cloudreve 全局 Kafka brokers：`${CLOUDREVE_INFRA_SERVICE_PREFIX}kafka:9092`
- 第三方抽取器如果也在 Swarm 内部网络，Kafka brokers 也填 `${CLOUDREVE_INFRA_SERVICE_PREFIX}kafka:9092`
- `CLOUDREVE_GLOBAL_KAFKA_SECURITY_PROTOCOL` 会由 `CLOUDREVE_GLOBAL_KAFKA_TLS_MODE=internal-plaintext` 自动落成 `PLAINTEXT`
- Kafka UI 也直接连 `${CLOUDREVE_INFRA_SERVICE_PREFIX}kafka:9092`，并保持 `KAFKA_UI_SECURITY_PROTOCOL=PLAINTEXT`
- 如果 `SWARM_OVERLAY_ENCRYPT=true`，跨主机 overlay 流量会由 Swarm 加密；当前 Kafka 模板就依赖这一层
- 如果后续要让 Cloudreve 直连外部 TLS / SASL Kafka，把 `CLOUDREVE_GLOBAL_KAFKA_TLS_MODE` 改成 `external-tls` 或 `external-sasl-ssl`，再回填 brokers 与证书/SASL 变量

## 6. 多节点模式才需要回填从节点密钥

1. 在后台进入 `管理面板 -> 节点 -> 新建节点`
2. 创建 slave node
3. 复制生成的从节点密钥（`Slave Key`）
4. 把 `.env.swarm` 中的 `CLOUDREVE_SLAVE_SECRET` 替换成真实值
5. 重新执行一次 `docker/swarm/deploy-stack.sh cloudreve --stack-name cloudreve-prod-app`

如果你不是用默认栈名，也可以直接：

```bash
STACK_NAME=cloudreve-debug docker/swarm/deploy-stack.sh cloudreve
```

## 7. 扩容原则

- 可以直接扩：`cloudreve-master-proxy`、`tika`、`pgpool`、`redis-sentinel`、`redis-proxy`
- 如果启用了多节点 Cloudreve 从站，再扩：`cloudreve-slave`、`cloudreve-slave-proxy`
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
- `ELASTICSEARCH_CLUSTER_NODE_JAVA_OPTS="-Xms31g -Xmx31g"`
- `KAFKA_CLUSTER_NODE_JVM_HEAP_OPTS="-Xms8g -Xmx8g"`

这只是生产默认起点，不是所有场景的上限。你仍然需要根据：

- 并发上传量
- 全文检索规模
- 预览转换量
- 对象存储吞吐

继续调高。

如果你的拓扑就是 `1 manager + 3 worker`，并且每台 `256GB / 64 线程`，仓库里已经给了
可直接复制的生产基线：

- `.env.swarm.prod-4x256g.example`

它默认启用：

- `cloudreve + foundation + infra` 拆分发布
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
