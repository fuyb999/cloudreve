# Cloudreve Swarm 的 `.env.swarm` 与多机器部署说明

如果你需要完整线上部署与运维手册，直接看：

- `docs/docker-swarm-operations-manual.md`

## 1. 先说结论

- `worker` 节点本身不需要 `.env.swarm`
- 真正需要 `.env.swarm` 的，是执行 `docker stack deploy` 的那台 `manager`
- 如果你有多台 `manager` 都可能执行部署，就不能只在其中一台保存 `.env.swarm`

原因很简单：

1. 部署脚本会先在本地 `source .env.swarm`
2. 然后把 `docker-compose.swarm.yml` 渲染成最终 stack 配置
3. 最后把渲染后的 service spec 提交给 Swarm

所以：

- `.env.swarm` 不会自动同步到其他节点
- 但它渲染出来的变量值会被固化进最终部署配置

## 2. 哪些节点需要 `.env.swarm`

### 只需要的节点

- 负责执行部署的 `manager`
- 负责执行重新部署、滚动升级、回滚的 `manager`

### 不需要的节点

- 普通 `worker`
- 只负责运行容器但不负责执行部署命令的节点

补充：

- 如果你要在 worker 上直接执行 `docker/swarm/prepare-bind-paths.sh`
- 或 `docker/swarm/prepare-bitnami-images.sh`
- 或 `docker/swarm/prepare-private-registry.sh`
- 那么这个 worker 也需要临时拿到同一份 `.env.swarm`，或者你用环境变量显式传参
- 这属于“运维准备阶段需要变量”，不是“容器运行时需要 `.env.swarm`”

## 3. 节点主机名怎么命名，方便在 Swarm 里看

Swarm 里最常看的两个命令：

- `docker node ls`
- `docker service ps <service>`

这里看到的节点名，本质上就是主机名。

所以建议在节点加入 Swarm 之前，先把主机名定好。

推荐方式：

- `cr-prod-mgr-1`
- `cr-prod-wkr-1`
- `cr-prod-wkr-2`
- `cr-prod-wkr-3`

如果你更喜欢带角色，也可以：

- `cr-prod-pg-1`
- `cr-prod-cache-1`
- `cr-prod-search-1`

但更推荐的原则仍然是：

- 主机名表示物理节点
- label 表示业务角色

这样以后在这些命令里会更清楚：

```bash
docker node ls
docker stack ps cloudreve
docker service ps cloudreve_postgresql-1
```

如果你还会批量在各节点执行 `docker/swarm/prepare-bind-paths.sh` 或
`docker/swarm/prepare-bitnami-images.sh`、`docker/swarm/prepare-private-registry.sh`，
这些脚本都会在输出里带上当前主机名，方便你对照日志。

另外，当前模板里的主要服务容器 `hostname` 也会自动带上 `{{.Node.Hostname}}`，
所以在容器内部或日志里也能直接看出任务实际落在哪台机器。

## 4. 多 manager 时怎么做

推荐只选一种方式，不要混着来。

### 方案 A：固定一台部署 manager

最简单也最稳：

- 只在一台固定 `manager` 上保存 `.env.swarm`
- 所有 `docker/swarm/deploy-stack.sh` 都只从这台机器执行

适用场景：

- 你当前这种联调环境
- 小规模生产
- 暂时没有 CI/CD

### 方案 B：把 `.env.swarm` 同步到所有 manager

如果多台 `manager` 都可能执行部署，就把同一份 `.env.swarm` 同步过去。

常见做法：

- `ansible`
- `rsync`
- 私有 Git 仓库中的加密文件
- `sops + age`
- `vault`

要求：

- 内容必须完全一致
- 更新后所有 manager 都要同步
- 不要让不同 manager 上存在不同版本的 `.env.swarm`

## 5. 为什么有时不是 `.env` 的问题，而是挂载路径的问题

Swarm 多机环境里更常见的问题其实是：

- `.env` 在部署机上已经生效了
- 但任务被调度到另一个节点
- 那个节点没有对应的绝对路径
- 于是容器启动失败

例如：

- PostgreSQL / Redis 的物理路径
- 你切成 `bind` 的 `cloudreve-master`
- 你切成 `bind` 的 `minio`
- 你切成 `bind` 的 `elasticsearch`

所以多机时要同时满足两件事：

1. 执行部署的 manager 有正确的 `.env.swarm`
2. 任务可能落到的节点，必须满足最终配置里的路径、镜像和私有仓库访问要求

## 6. 这套模板里如何避免随机调度到错误节点

当前模板已经支持下面这些可选节点约束：

- `CLOUDREVE_MASTER_NODE_CONSTRAINT`
- `CLOUDREVE_MASTER_PROXY_NODE_CONSTRAINT`
- `CLOUDREVE_SLAVE_NODE_CONSTRAINT`
- `CLOUDREVE_SLAVE_PROXY_NODE_CONSTRAINT`
- `MINIO_NODE_CONSTRAINT`
- `ELASTICSEARCH_NODE_CONSTRAINT`
- `TIKA_NODE_CONSTRAINT`
- `PGPOOL_NODE_CONSTRAINT`
- `REDIS_SENTINEL_NODE_CONSTRAINT`
- `REDIS_PROXY_NODE_CONSTRAINT`

如果启用了集群模式，还会再用到：

- `MINIO_NODE_1_CONSTRAINT` 到 `MINIO_NODE_4_CONSTRAINT`
- `ELASTICSEARCH_NODE_1_CONSTRAINT` 到 `ELASTICSEARCH_NODE_3_CONSTRAINT`
- `KAFKA_NODE_1_CONSTRAINT` 到 `KAFKA_NODE_3_CONSTRAINT`
- `MINIO_PROXY_NODE_CONSTRAINT`
- `ELASTICSEARCH_PROXY_NODE_CONSTRAINT`
- `KAFKA_PROXY_NODE_CONSTRAINT`
- `KAFKA_UI_NODE_CONSTRAINT`

默认值都是：

```bash
node.platform.os==linux
```

如果某个服务改成了 `bind`，建议把它固定到有该绝对路径的节点。

例如：

```bash
CLOUDREVE_MASTER_RUNTIME_MOUNT_TYPE=bind
CLOUDREVE_MASTER_RUNTIME_MOUNT_SOURCE=/srv/cloudreve/master-runtime
CLOUDREVE_MASTER_NODE_CONSTRAINT=node.labels.cloudreve.master==true

MINIO_DATA_MOUNT_TYPE=bind
MINIO_DATA_MOUNT_SOURCE=/srv/cloudreve/minio
MINIO_NODE_CONSTRAINT=node.labels.cloudreve.minio==true

CLOUDREVE_MASTER_PROXY_NODE_CONSTRAINT=node.labels.cloudreve.edge==true
PGPOOL_NODE_CONSTRAINT=node.labels.cloudreve.edge==true
REDIS_PROXY_NODE_CONSTRAINT=node.labels.cloudreve.edge==true
REDIS_SENTINEL_NODE_CONSTRAINT=node.labels.cloudreve.redis-sentinel==true
```

对应节点先打标签：

```bash
docker node update --label-add cloudreve.master=true <node-name>
docker node update --label-add cloudreve.minio=true <node-name>
docker node update --label-add cloudreve.edge=true <node-name>
docker node update --label-add cloudreve.redis-sentinel=true <node-name>
```

如果是集群模式，再补：

```bash
docker node update --label-add cloudreve.minio1=true <node-name>
docker node update --label-add cloudreve.minio2=true <node-name>
docker node update --label-add cloudreve.minio3=true <node-name>
docker node update --label-add cloudreve.minio4=true <node-name>

docker node update --label-add cloudreve.es1=true <node-name>
docker node update --label-add cloudreve.es2=true <node-name>
docker node update --label-add cloudreve.es3=true <node-name>

docker node update --label-add cloudreve.kafka1=true <node-name>
docker node update --label-add cloudreve.kafka2=true <node-name>
docker node update --label-add cloudreve.kafka3=true <node-name>
docker node update --label-add cloudreve.kafka-ui=true <node-name>
docker node update --label-add cloudreve.registry=true <node-name>
```

## 7. 最稳的实践建议

生产里建议直接按下面执行：

1. 固定一台 `manager` 作为部署入口
2. `.env.swarm` 只在这台机器维护，或者通过自动化同步到全部 manager
3. 在每台节点先执行 `docker/swarm/prepare-private-registry.sh`
4. 在每台节点再执行 `docker/swarm/prepare-bitnami-images.sh`
5. `PG / Redis` 一律使用宿主机物理路径
6. 可选 `bind` 服务一旦切成绝对路径，就同时加节点标签和约束
7. 如果不确定某个路径能否在多机上保持一致，就继续使用默认 `volume` 模式
8. 如果启用 `SWARM_WITH_CLUSTER=yes`，在所有 Elasticsearch 节点先设置 `vm.max_map_count=262144`
9. 集群模式下，Cloudreve 默认 S3 地址仍用 `http://minio:9000`，FTS Elasticsearch 地址填 `http://elasticsearch:9200`
10. 如果启用栈内 Kafka，并让 Cloudreve 使用全局 Kafka 配置，就把 brokers 填 `kafka:9092`
11. 如果要从浏览器直接查看 Kafka 集群，就访问 `kafka-ui` 对外端口；UI 本身仍然走内部 `kafka:9092`
12. 自定义镜像统一先 push 到固定 manager 上的 `registry:2`，再部署主业务栈，不要逐台 `docker load`

## 8. 相关文件

- `docker-compose.swarm.registry.yml`
- `docker-compose.swarm.yml`
- `docker-compose.swarm.cluster.yml`
- `.env.swarm.example`
- `.env.swarm.prod-4x128g.example`
- `docker/swarm/deploy-private-registry.sh`
- `docker/swarm/deploy-stack.sh`
- `docker/swarm/prepare-bind-paths.sh`
- `docker/swarm/prepare-bitnami-images.sh`
- `docker/swarm/prepare-private-registry.sh`
- `docker/swarm/publish-private-images.sh`
- `docs/docker-swarm-deployment.md`
- `docs/docker-swarm-quickstart.md`
