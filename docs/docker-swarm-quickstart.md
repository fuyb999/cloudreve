# Cloudreve Swarm 快速上线清单

这份清单是给生产上线时快速对照用的，和详细说明配套使用：

- 详细文档：`docs/docker-swarm-deployment.md`
- Swarm 栈文件：`docker-compose.swarm.yml`
- 环境变量模板：`.env.swarm.example`

## 1. 最短结论

- PostgreSQL 不共享数据目录
- Redis 不共享数据目录
- Cloudreve 文件建议放 MinIO / S3 兼容对象存储
- `cloudreve-master` 没有可靠共享 POSIX 文件系统前，先保持 `1` 副本
- 动态扩容优先扩 `proxy`、`slave`、`tika`，不要直接对 `postgresql-*` / `redis-*` 用 `docker service scale`

## 2. 上线前准备

1. 初始化 Swarm 并把节点加入集群。
2. 给 PostgreSQL / Redis 节点打标签。
3. 构建并推送 `TIKA_IMAGE` 到所有节点可拉取的镜像仓库。
4. 复制环境模板：

```bash
cp .env.swarm.example .env.swarm
```

5. 至少填好这些变量：

- `CLOUDREVE_SITE_URL`
- `CLOUDREVE_SESSION_SECRET`
- `POSTGRESQL_PASSWORD`
- `POSTGRESQL_POSTGRES_PASSWORD`
- `REPMGR_PASSWORD`
- `PGPOOL_ADMIN_PASSWORD`
- `REDIS_PASSWORD`
- `TIKA_IMAGE`
- `CLOUDREVE_SHARED_DATA_PATH`
- `TIKA_CUSTOM_FONTS_HOST_PATH`
- `MINIO_ROOT_USER`
- `MINIO_ROOT_PASSWORD`

## 3. 首次部署顺序

首次部署建议：

- `CLOUDREVE_MASTER_REPLICAS=1`
- `CLOUDREVE_SLAVE_SECRET` 先保留占位值

执行：

```bash
set -a
source ./.env.swarm
set +a

docker stack deploy -c docker-compose.swarm.yml cloudreve
```

检查：

```bash
docker stack services cloudreve
docker stack ps cloudreve
docker service logs -f cloudreve_cloudreve-master
```

## 4. 主站初始化

1. 打开 `http(s)://<master-host-or-lb>/admin`
2. 登录后台
3. 在 `设置 -> 基本设置` 中确认 `Site URL` 和 `CLOUDREVE_SITE_URL` 完全一致

## 5. 回填从节点密钥

1. 在后台进入 `管理面板 -> 节点 -> 新建节点`
2. 创建 slave node
3. 复制生成的 `Slave Key`
4. 把 `.env.swarm` 中的 `CLOUDREVE_SLAVE_SECRET` 替换成真实值
5. 重新执行一次 `docker stack deploy`

## 6. 切换 Cloudreve 文件到 MinIO

注意：

- `MINIO_*` 变量只是在 Swarm 中启动了一个 MinIO 服务
- Cloudreve 不会自动改用 MinIO
- 必须在 Cloudreve 后台新建存储策略并切换用户组默认存储

最短步骤：

1. 在 MinIO 创建 bucket
2. Cloudreve 后台进入 `管理面板 -> 存储策略`
3. 新建 `S3 兼容` / `MinIO` 存储策略
4. 填写 Endpoint、Bucket、Access Key、Secret Key
5. 把用户组默认存储切过去

## 7. 扩容原则

- 可以直接扩：`cloudreve-master-proxy`、`cloudreve-slave`、`cloudreve-slave-proxy`、`tika`、`pgpool`、`redis-sentinel`、`redis-proxy`
- 不要直接扩：`postgresql-1/2/3`、`redis-1/2/3`

示例：

```bash
docker service scale cloudreve_cloudreve-slave=3
docker service scale cloudreve_cloudreve-slave-proxy=3
docker service scale cloudreve_tika=4
```

只有满足下面条件，才建议把 `cloudreve-master` 扩到 `2` 或以上：

- `CLOUDREVE_SHARED_DATA_PATH` 已经是可靠共享 POSIX 文件系统
- 所有可能运行 `cloudreve-master` 的节点都挂载了同一路径
- 你已经验证过多副本滚动更新和故障切换

## 8. PG / Redis 最佳实践

- PostgreSQL：本地卷 + 主从复制 + pgpool + 备份 / WAL 归档到 MinIO
- Redis：本地卷 + 主从复制 + Sentinel + 备份导出
- 不要把 PGDATA / Redis AOF / RDB 放到 MinIO
- 不要让多个实例同时共享一个 PG / Redis 数据目录
