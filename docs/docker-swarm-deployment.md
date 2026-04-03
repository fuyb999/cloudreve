# Cloudreve Docker Swarm 中文部署指南

本文档对应仓库中的以下文件：

- `docker-compose.swarm.yml`
- `.env.swarm.example`
- `docker/swarm/nginx/cloudreve-proxy.conf.template`

目标环境为 Docker Engine `25.0.4`。

如果你现在更需要一份最短执行路径，先看：

- `docs/docker-swarm-quickstart.md`

## 1. 架构说明

当前 Swarm 栈采用如下拓扑：

- `cloudreve-master`：Cloudreve 主站服务，支持横向扩容
- `cloudreve-master-proxy`：主站统一入口的 Nginx 反向代理
- `cloudreve-slave`：Cloudreve 从节点服务，支持横向扩容
- `cloudreve-slave-proxy`：从节点统一入口的 Nginx 反向代理
- `postgresql-1/2/3 + pgpool`：PostgreSQL 高可用
- `redis-1/2/3 + redis-sentinel + redis-proxy`：Redis 高可用
- `minio`：推荐作为 Cloudreve 文件 Blob 的对象存储
- `tika`：Apache Tika 服务，支持横向扩容

## 建议结论

如果你当前最关心的是 `PG / Redis` 的“共享存储怎么做”，结论可以直接定下来：

- PostgreSQL：不要共享数据目录，使用本地 SSD / 本地卷 + 主从复制 + 故障切换
- Redis：不要共享数据目录，使用本地 SSD / 本地卷 + 主从复制 + Sentinel
- 备份与归档：可以进入 MinIO，但 MinIO 只承载备份对象，不承载 PG / Redis 运行时数据
- Cloudreve 文件：推荐放到独立 MinIO / S3 兼容对象存储
- `cloudreve-master`：只有在 `CLOUDREVE_SHARED_DATA_PATH` 已经是可靠共享 POSIX 文件系统时，才建议扩到 `2` 个及以上副本

换句话说，`PG / Redis` 的最佳方案不是“共享卷”，而是“每个副本独立本地卷 + 复制 + 故障切换 + 备份”。

## 2. 这套设计依据的官方约束

这份 Swarm 方案是根据 Cloudreve 官方文档里的约束落地出来的：

- Cloudreve 建议部署在反向代理之后
- 从节点必须和主站使用相同版本的 Cloudreve
- 主站 `Site URL` 必须能被从节点访问到
- 如果从节点被用作存储节点，则从节点地址也必须能被最终用户访问到
- 从节点本地存储默认使用 `data/uploads`
- 浏览器直传到从节点时，需要放开对应的 CORS

这份方案也遵循 Docker 官方对 Swarm 的要求：

- `docker stack deploy` 必须在 Swarm manager 节点执行
- 多节点 Swarm 需要所有节点都能拉取镜像
- 有状态服务应通过 placement constraints 固定到指定节点
- 副本扩容通过 `docker service scale` 管理

另外，这份方案有一条非常重要的工程约束：

- PostgreSQL 和 Redis 不做“共享数据目录”
- PostgreSQL 和 Redis 依赖复制和故障切换，不依赖共享文件系统
- MinIO 只用于 Cloudreve 文件数据或备份，不用于 PG / Redis 运行时数据

## 3. 准备 Swarm 集群

选择一台机器作为初始 manager：

```bash
docker swarm init --advertise-addr <MANAGER_IP>
```

在 manager 上获取 worker 加入命令：

```bash
docker swarm join-token worker
```

如果你希望 Swarm 控制面也高可用，可以再获取 manager 加入命令：

```bash
docker swarm join-token manager
```

把其它机器按输出命令加入集群后，检查集群状态：

```bash
docker node ls
```

## 4. 给有状态服务节点打标签

在 manager 节点执行：

```bash
docker node update --label-add cloudreve.pg1=true <node-pg-1>
docker node update --label-add cloudreve.pg2=true <node-pg-2>
docker node update --label-add cloudreve.pg3=true <node-pg-3>

docker node update --label-add cloudreve.redis1=true <node-redis-1>
docker node update --label-add cloudreve.redis2=true <node-redis-2>
docker node update --label-add cloudreve.redis3=true <node-redis-3>
```

如果 PostgreSQL 和 Redis 某些角色落在同一批机器上也可以，只要资源足够。

## 5. 共享存储怎么划分

这里要把“应用运行目录”和“数据库运行目录”分开看。

### 5.1 PostgreSQL / Redis

PostgreSQL 和 Redis 都不应该使用共享存储。

正确做法是：

- PostgreSQL：每个副本一个独立数据卷
- Redis：每个副本一个独立数据卷
- 通过主从复制、Sentinel、pgpool 完成高可用
- 通过节点标签把卷固定到指定节点
- PostgreSQL 备份、WAL 归档可以进入 MinIO 或其它对象存储
- Redis RDB / AOF 备份可以定时导出到 MinIO 或备份系统

不要这样做：

- 多个 PostgreSQL 实例共享同一个数据目录
- 多个 Redis 实例共享同一个数据目录
- 把 PGDATA / Redis AOF / RDB 放到 MinIO
- 把 PGDATA / Redis 数据目录放到共享文件系统上让多个副本同时读写

也就是说，你真正要设计的是：

- 副本复制
- 故障切换
- 备份归档

而不是让多个数据库实例共享同一个运行目录。

### 5.2 Cloudreve 主站

Cloudreve 主站多副本时，仍然需要一个共享运行目录：

- `CLOUDREVE_SHARED_DATA_PATH`

这个目录主要用于主站运行时数据同步，不应该作为用户文件 Blob 的长期存储方案。
为了避免首次部署因为宿主机目录不存在而直接失败，默认 `docker-compose.swarm.yml` 改为使用命名卷。
只有当你已经准备好共享 POSIX 文件系统时，再叠加 `docker-compose.swarm.bind.yml`。

### 5.3 Cloudreve 文件数据

Cloudreve 文件数据推荐放到 MinIO。

也就是说，推荐架构是：

- Cloudreve 元数据：PostgreSQL
- Cloudreve 会话 / 缓存：Redis
- Cloudreve 用户文件：MinIO

### 5.4 Tika

Tika 只需要字体目录：

- `TIKA_CUSTOM_FONTS_HOST_PATH`

默认栈会挂一个空的命名卷到 `/tika-fonts/custom`，这样不依赖宿主机目录也能启动。
如果你确实要加载宿主机上的自定义字体，再准备目录并启用 bind override。

建议目录：

```bash
mkdir -p /srv/cloudreve/master-data
mkdir -p /srv/cloudreve/tika-fonts
```

## 6. 先准备 Tika 镜像

`docker stack deploy` 不会自动构建镜像，所以必须先把 Tika 镜像构建并推送到所有 Swarm 节点都能访问的镜像仓库。

示例：

```bash
docker build -f docker/tika-unrar/Dockerfile -t registry.example.com/cloudreve/tika:3.2.3.0-full-unrar-charset .
docker push registry.example.com/cloudreve/tika:3.2.3.0-full-unrar-charset
```

如果你暂时没有独立镜像仓库，Docker 官方也给过一个临时 registry 的用法：

```bash
docker service create --name registry --publish published=5000,target=5000 registry:2
```

之后把 `TIKA_IMAGE` 改成你自己的 Swarm 可访问地址即可。

## 7. 准备环境变量文件

先复制模板：

```bash
cp .env.swarm.example .env.swarm
```

然后编辑 `.env.swarm`，至少设置这些值：

- `CLOUDREVE_SITE_URL`
- `CLOUDREVE_SESSION_SECRET`
- `MINIO_ROOT_USER`
- `MINIO_ROOT_PASSWORD`
- `POSTGRESQL_PASSWORD`
- `POSTGRESQL_POSTGRES_PASSWORD`
- `REPMGR_PASSWORD`
- `PGPOOL_ADMIN_PASSWORD`
- `REDIS_PASSWORD`
- `TIKA_IMAGE`

首次部署建议保持：

- `CLOUDREVE_MASTER_REPLICAS=1`
- `CLOUDREVE_SLAVE_SECRET` 保持占位值，等主站起来后再回填正式值

如果你还没有为 `CLOUDREVE_SHARED_DATA_PATH` 准备稳定的共享 POSIX 文件系统，那么先不要把 master 扩到 `2`。
如果你已经准备好了共享 POSIX 文件系统或宿主机字体目录，再额外设置：

- `CLOUDREVE_SHARED_DATA_PATH`
- `TIKA_CUSTOM_FONTS_HOST_PATH`

并在部署时叠加 `docker-compose.swarm.bind.yml`。

## 8. 首次部署

在 manager 节点执行：

```bash
set -a
source ./.env.swarm
set +a

docker stack deploy -c docker-compose.swarm.yml cloudreve
```

如果你已经准备好所有宿主机目录并确认每个候选节点都能访问，再执行：

```bash
docker stack deploy -c docker-compose.swarm.yml -c docker-compose.swarm.bind.yml cloudreve
```

查看服务状态：

```bash
docker stack services cloudreve
docker stack ps cloudreve
```

查看日志：

```bash
docker service logs -f cloudreve_cloudreve-master
docker service logs -f cloudreve_cloudreve-master-proxy
```

## 9. 完成主站基础配置

主站首次启动后：

1. 打开 `http(s)://<master-host-or-lb>/admin`
2. 登录主站后台
3. 进入 `设置 -> 基本设置`
4. 确认 `Site URL` 与 `CLOUDREVE_SITE_URL` 完全一致

这一步很关键，因为 Cloudreve 官方要求从节点通过主站 `Site URL` 回调和通信。

## 10. 注册从节点

在主站后台执行：

1. 进入 `管理面板 -> 节点 -> 新建节点`
2. 新建一个 slave node
3. 复制后台生成的 `Slave Key`
4. 节点地址填写从节点代理入口，例如：

```text
http://<slave-host-or-lb>:<CLOUDREVE_SLAVE_HTTP_PORT>
```

然后把 `.env.swarm` 中的：

```bash
CLOUDREVE_SLAVE_SECRET=<the-generated-slave-key>
```

替换成真实值，再重新发布：

```bash
set -a
source ./.env.swarm
set +a

docker stack deploy -c docker-compose.swarm.yml cloudreve
```

发布完成后，再到主站后台测试该节点连通性。

## 11. 把 Cloudreve 文件存储切到 MinIO

Cloudreve 首次启动后，默认会创建一个本地存储策略。

这里有个很容易误解的点：

- `.env.swarm.example` 里的 `MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD` 只是用于启动栈内的 `minio` 服务
- 它不会自动把 Cloudreve 的默认存储策略改成 MinIO
- Cloudreve 仍然需要你在后台新建存储策略并切换用户组默认策略

如果你追求文件层面的生产高可用，优先推荐：

- 独立 MinIO 集群
- 已有对象存储平台
- 云厂商 S3 兼容对象存储

当前栈内的 `minio` 更适合“先跑通链路”的自建方案，不建议把单副本 `minio` 当成最终高可用形态。

如果你希望 Cloudreve 文件不要依赖本地共享目录，而是走对象存储，那么上线后应尽快在后台把文件存储切到 MinIO。

推荐步骤：

1. 先在 MinIO 创建 bucket
2. 登录 Cloudreve 后台
3. 进入 `管理面板 -> 存储策略`
4. 新建 `S3 兼容` 或 `MinIO` 存储策略
5. 填写 MinIO 的：
   - Endpoint
   - Bucket
   - Access Key
   - Secret Key
6. 如果你的 MinIO 走 path-style，打开对应选项
7. 把用户组的首选存储策略切到这个 MinIO 策略
8. 后续新上传文件就会进入 MinIO，而不是主站本地目录

如果你已经在用 MinIO，那么当前 Swarm 模板里的：

- `CLOUDREVE_SHARED_DATA_PATH`

只需要承载主站运行目录，不再承担用户文件数据。

## 12. 从节点怎么理解

当前模板里的 slave 节点更适合用来承载：

- 远程节点能力
- 内容处理能力
- 任务扩展能力

它不假设“多个 slave 副本共同组成一个共享本地文件存储集群”。

如果某个从节点要被用作本地存储策略，需要确认：

- 从节点代理地址对浏览器可达
- 从节点 CORS 保持开启
- 该从节点应该设计成“单实例 + 独占本地盘”，而不是多个副本共享目录

如果你的主目标是高可用和简化存储，优先推荐直接使用 MinIO，而不是让 slave 承担本地文件存储。

## 13. 扩容服务

等首次部署稳定后，再扩主站和从节点副本。

例如：

```bash
docker service scale cloudreve_cloudreve-master=2
docker service scale cloudreve_cloudreve-master-proxy=2
docker service scale cloudreve_cloudreve-slave=2
docker service scale cloudreve_cloudreve-slave-proxy=2
docker service scale cloudreve_tika=4
```

后续继续扩容：

```bash
docker service scale cloudreve_cloudreve-master=3 cloudreve_cloudreve-slave=3 cloudreve_tika=6
```

这里要区分两类扩容：

- `cloudreve-master-proxy`、`cloudreve-slave`、`cloudreve-slave-proxy`、`tika`、`pgpool`、`redis-sentinel`、`redis-proxy` 可以按 Swarm 常规方式扩容
- `postgresql-1/2/3` 和 `redis-1/2/3` 是固定成员的有状态角色，不能直接靠 `docker service scale` 随手加副本

如果你未来真要扩 PostgreSQL / Redis 数据节点，正确方式是：

- 新增独立服务定义
- 新增独立数据卷
- 新增节点标签约束
- 按数据库 / 缓存集群自己的成员加入流程做扩容

另外，只有当 `CLOUDREVE_SHARED_DATA_PATH` 已经是可靠共享 POSIX 文件系统时，才建议把 `cloudreve-master` 从 `1` 扩到 `2` 及以上。
否则应保持 `master=1`，把横向扩容重点放在 `proxy`、`slave` 和 `tika`。

查看任务分布：

```bash
docker service ps cloudreve_cloudreve-master
docker service ps cloudreve_cloudreve-slave
docker service ps cloudreve_tika
```

## 14. 滚动发布

后续只要你修改了：

- `.env.swarm`
- `docker-compose.swarm.yml`
- 镜像 tag

都可以通过重新部署触发滚动更新：

```bash
set -a
source ./.env.swarm
set +a

docker stack deploy -c docker-compose.swarm.yml cloudreve
```

当前栈文件已经给主要副本服务配了滚动更新策略。

## 15. 回滚

单个服务回滚：

```bash
docker service rollback cloudreve_cloudreve-master
docker service rollback cloudreve_cloudreve-master-proxy
docker service rollback cloudreve_cloudreve-slave
docker service rollback cloudreve_tika
```

## 16. 常见问题排查

### 1. 从节点测试失败，提示签名或认证错误

检查：

- `CLOUDREVE_SLAVE_SECRET` 是否与主站后台生成的 `Slave Key` 完全一致
- 主站和从节点机器时间是否同步

### 2. 从节点无法访问主站

检查：

- `CLOUDREVE_SITE_URL` 是否正确
- 从节点所在机器是否能访问主站代理地址
- 防火墙、WAF、网关策略是否拦截了请求

### 3. 上传报 `413 Request Entity Too Large`

增大：

- `CLOUDREVE_MASTER_CLIENT_MAX_BODY_SIZE`
- `CLOUDREVE_SLAVE_CLIENT_MAX_BODY_SIZE`

### 4. 从节点任务堆积

检查：

- 从节点日志
- CPU、内存、磁盘 IO 是否饱和
- 是否需要增加 slave 副本
- 是否需要在 Cloudreve 后台调整节点权重

### 5. PG / Redis 想用共享存储

不建议这样做。

正确方向是：

- PG 用主从复制 + 故障切换
- Redis 用主从复制 + Sentinel
- 备份归档可以进 MinIO
- 运行时数据不能放 MinIO

## 17. 常用命令

查看栈服务：

```bash
docker stack services cloudreve
```

查看栈任务：

```bash
docker stack ps cloudreve
```

查看日志：

```bash
docker service logs -f cloudreve_cloudreve-master
docker service logs -f cloudreve_cloudreve-slave
docker service logs -f cloudreve_pgpool
docker service logs -f cloudreve_redis-proxy
```

删除整套栈：

```bash
docker stack rm cloudreve
```

## 18. 参考文档

- Cloudreve Reverse Proxy: <https://docs.cloudreve.org/en/overview/deploy/configure>
- Cloudreve MinIO Storage: <https://docs.cloudreve.org/en/usage/storage/minio>
- Cloudreve Slave Node: <https://docs.cloudreve.org/en/usage/slave-node>
- Cloudreve Slave Storage: <https://docs.cloudreve.org/en/usage/storage/remote>
- Docker Swarm Stack Deploy: <https://docs.docker.com/engine/swarm/stack-deploy/>
- Docker Swarm Join Nodes: <https://docs.docker.com/engine/swarm/join-nodes/>
- Docker Swarm Services: <https://docs.docker.com/engine/swarm/services/>
- Docker Service Scale: <https://docs.docker.com/reference/cli/docker/service/scale/>
