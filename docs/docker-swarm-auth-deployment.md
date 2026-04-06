# 统一认证 Swarm 接入与上线手册

本文档对应这次新增的统一认证 Swarm 交付，覆盖：

- `docker-compose.swarm.auth.yml`
- `docker/swarm/build-auth-images.sh`
- `docker/swarm/init-authverse-db.sh`
- `docker/swarm/deploy-auth-stack.sh`
- `docker/swarm/export-swarm-images.sh`
- `docker/swarm/prepare-bind-paths.sh`
- `.env.swarm.example`
- `.env.swarm.prod-4x128g.example`

涉及的代码仓库：

- 前端：`../authverse`
- 后端：`../authverse-backend`
- 当前 Swarm 运维仓库：本仓库 `cloudreve`

## 1. 推荐方案

这次统一认证不建议直接硬塞进 `cloudreve` 主业务栈，而是采用：

- `cloudreve` 主栈继续承载网盘、PG、Redis、MinIO、ES、Kafka、Tika
- `authverse` 前端 + `authverse-backend` 后端独立成一个 `authverse` 栈
- 两个栈通过同一条 overlay 网络互通
- 统一认证直接复用 `cloudreve` 主栈里的：
  - `pgpool`
  - `redis-proxy`
  - `elasticsearch`
- 对外只暴露一个统一认证入口端口，由 `authverse-web` 负责：
  - `/` 走 authverse 前端
  - `/admin-api`、`/app-api`、`/.well-known`、`/oidc` 走 authverse-backend
  - `/api`、`/s/`、`/f/`、`/manifest.json` 反代到 Cloudreve 主站

这样做的原因：

- 边界清晰，统一认证可以独立发布、扩容、回滚
- 不额外再起一套 PG / Redis / ES，避免资源浪费
- 外部负载均衡继续由 Swarm ingress + service VIP 控制，不引入第三方 LB
- authverse 前端本身依赖 Cloudreve 的 `/api/v4`、`/s/`、`/f/` 路由，单独做一个网关最稳

## 2. 实际拓扑

推荐生产拓扑：

- `authverse-web`
  - 2 副本
  - 负责静态资源与统一入口反向代理
  - 对外发布 `AUTHVERSE_HTTP_PORT`
- `authverse-backend`
  - 2 副本
  - 负责 OAuth2/OIDC、统一授权、统一接入注册中心
  - 不直接对外发布端口，只走内部 overlay 网络

流量路径：

1. 用户访问 `https://auth.example.com`
2. Swarm ingress 把流量分发到某个 `authverse-web` 副本
3. `authverse-web` 根据路径把请求转发到：
   - `authverse-backend`
   - `cloudreve-master`
4. `authverse-backend` 再访问：
   - `cloudreve_pgpool`
   - `cloudreve_redis-proxy`
   - `cloudreve_elasticsearch`

补充说明：

- 运行期业务流量走 `cloudreve_pgpool`
- 但 `docker/swarm/init-authverse-db.sh` 做的是建库、删库、授权这类 DDL 操作，默认会直连 `cloudreve_postgresql-1`
- 不建议把这类初始化 DDL 走 `pgpool`

## 3. 资源建议

如果你的真实环境是：

- 1 个 manager
- 3 个 worker
- 每台 128GB 内存
- 每台 64 线程

当前默认建议是偏保守、够用、不浪费资源的：

- `authverse-web`
  - 2 副本
  - reservation：`0.5 CPU / 512M`
  - limit：`1 CPU / 1G`
- `authverse-backend`
  - 2 副本
  - reservation：`1.5 CPU / 3G`
  - limit：`4 CPU / 6G`
  - JVM：`-Xms2g -Xmx4g`

这套默认值适合：

- 登录、授权、管理后台并发不算特别离谱
- 统一认证作为控制面，不是大流量文件面
- 仍然保留足够机器余量给 PG / Redis / ES / Cloudreve / Kafka / Tika

如果后续发现：

- 登录峰值高
- 后台列表查询多
- OIDC / introspection 请求量大

优先调：

- `AUTHVERSE_BACKEND_REPLICAS=3`
- `AUTHVERSE_BACKEND_CPU_LIMIT`
- `AUTHVERSE_BACKEND_MEM_LIMIT`

不要一开始就给统一认证单独再起一套数据库或缓存，这对当前规模没有必要。

## 4. 关键环境变量

核心接入变量如下。

### 4.1 统一认证自身

- `AUTHVERSE_STACK_NAME`
- `AUTHVERSE_PUBLIC_BASE_URL`
- `AUTHVERSE_HTTP_PORT`
- `AUTHVERSE_SERVER_NAME`
- `AUTHVERSE_WEB_IMAGE`
- `AUTHVERSE_BACKEND_IMAGE`
- `AUTHVERSE_WEB_BUILD_NODE_OPTIONS`

### 4.2 与 Cloudreve 主栈的关系

- `CLOUDREVE_STACK_NAME`
- `AUTHVERSE_SHARED_NETWORK`
- `AUTHVERSE_CLOUDREVE_SERVICE_PREFIX`
- `AUTHVERSE_CLOUDREVE_INTERNAL_UPSTREAM`
- `AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL`
- `AUTHVERSE_CLOUDREVE_HOST_HEADER`

说明：

- `AUTHVERSE_SHARED_NETWORK` 默认是 `${CLOUDREVE_STACK_NAME}_cloudreve_backend`
- `AUTHVERSE_CLOUDREVE_INTERNAL_UPSTREAM` 默认是 `${CLOUDREVE_STACK_NAME}_cloudreve-master:5212`
- 如果统一认证入口域名和 Cloudreve 站点域名不同，通常要额外设置 `AUTHVERSE_CLOUDREVE_HOST_HEADER`

### 4.3 数据与中间件

- `AUTHVERSE_DB_NAME`
- `AUTHVERSE_DB_USERNAME`
- `AUTHVERSE_DB_PASSWORD`
- `AUTHVERSE_DB_MASTER_URL`
- `AUTHVERSE_DB_SLAVE_URL`
- `AUTHVERSE_DB_ADMIN_USER`
- `AUTHVERSE_DB_ADMIN_PASSWORD`
- `AUTHVERSE_REDIS_HOST`
- `AUTHVERSE_REDIS_PASSWORD`
- `AUTHVERSE_ELASTICSEARCH_URI`

### 4.4 OIDC 密钥

- `AUTHVERSE_OIDC_RSA_AUTO_GENERATE`
- `AUTHVERSE_OIDC_KEY_ID`
- `AUTHVERSE_OIDC_PRIVATE_KEY_PATH`
- `AUTHVERSE_OIDC_PUBLIC_KEY_PATH`
- `AUTHVERSE_OIDC_KEYS_MOUNT_TYPE`
- `AUTHVERSE_OIDC_KEYS_MOUNT_SOURCE`

默认是镜像内 classpath 密钥：

```env
AUTHVERSE_OIDC_PRIVATE_KEY_PATH=classpath:oidc/private.pem
AUTHVERSE_OIDC_PUBLIC_KEY_PATH=classpath:oidc/public.pem
AUTHVERSE_OIDC_KEYS_MOUNT_TYPE=volume
```

真实环境更推荐切到宿主机绝对路径：

```env
AUTHVERSE_OIDC_KEYS_MOUNT_TYPE=bind
AUTHVERSE_OIDC_KEYS_MOUNT_SOURCE=/srv/cloudreve/authverse/oidc
AUTHVERSE_OIDC_PRIVATE_KEY_PATH=file:/run/authverse/oidc/private.pem
AUTHVERSE_OIDC_PUBLIC_KEY_PATH=file:/run/authverse/oidc/public.pem
```

注意：

- `authverse-backend` 是多副本服务
- 所有允许调度该服务的节点，都必须有同一份密钥文件
- 所以 bind 模式下，需要把同样的 `private.pem` / `public.pem` 分发到每个后端节点

## 5. 上线顺序

### 5.1 准备 Cloudreve 主栈

先确认主栈已经部署成功，并且这些入口可用：

- `cloudreve_pgpool`
- `cloudreve_redis-proxy`
- `cloudreve_elasticsearch`
- `cloudreve-master`
- `cloudreve_backend` overlay 网络

如果主栈还没起来，先按现有文档完成：

- `docs/docker-swarm-quickstart.md`
- `docs/docker-swarm-production-checklist.md`

### 5.2 准备统一认证参数

如果你是 4 台 128G 生产环境，建议直接从：

```bash
cp .env.swarm.prod-4x128g.example .env.swarm
```

至少确认这些值已经回填：

- `AUTHVERSE_PUBLIC_BASE_URL`
- `AUTHVERSE_HTTP_PORT`
- `AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL`
- `AUTHVERSE_CLOUDREVE_HOST_HEADER`
- `AUTHVERSE_DB_NAME`
- `AUTHVERSE_DB_PASSWORD`
- `AUTHVERSE_DB_ADMIN_PASSWORD`
- `AUTHVERSE_REDIS_PASSWORD`
- `AUTHVERSE_WEB_IMAGE`
- `AUTHVERSE_BACKEND_IMAGE`

如果你直接沿用 4 台生产样例，建议保持：

```env
AUTHVERSE_WEB_LOCAL_IMAGE=authverse/authverse-web:2024-local
AUTHVERSE_WEB_REMOTE_IMAGE=${PRIVATE_REGISTRY_ADDR}/cloudreve/authverse-web:2024-local
AUTHVERSE_BACKEND_LOCAL_IMAGE=authverse/authverse-backend:2024-local
AUTHVERSE_BACKEND_REMOTE_IMAGE=${PRIVATE_REGISTRY_ADDR}/cloudreve/authverse-backend:2024-local
```

### 5.3 准备 OIDC 密钥目录

如果你使用 bind 模式：

```bash
sudo docker/swarm/prepare-bind-paths.sh --env-file .env.swarm --services authverse
```

然后把密钥复制到所有后端节点的同一路径，例如：

```bash
/srv/cloudreve/authverse/oidc/private.pem
/srv/cloudreve/authverse/oidc/public.pem
```

### 5.4 初始化统一认证数据库

执行：

```bash
docker/swarm/init-authverse-db.sh --env-file .env.swarm
```

这个脚本会做三件事：

1. 默认直连主库服务 `cloudreve_postgresql-1`
2. 重建专用 `AUTHVERSE_DB_NAME`
3. 导入：
   - `authverse-20260329.sql`
   - `authverse-20260329-authz-integration-registry.sql`
4. 校正 `public` schema 下对象 owner 与 grant，保证应用用户可正常启动

注意：

- 这两份 SQL 是面向统一认证专用库的全量初始化脚本
- 不要对 Cloudreve 主库执行
- 脚本会重建 `AUTHVERSE_DB_NAME`，只适合统一认证专用库初始化，不适合带业务数据的复用库

### 5.5 构建统一认证镜像

在 manager 节点执行：

```bash
docker/swarm/build-auth-images.sh --env-file .env.swarm
```

它会构建：

- `AUTHVERSE_WEB_LOCAL_IMAGE`
- `AUTHVERSE_BACKEND_LOCAL_IMAGE`

如果前端在 `vite build` 阶段出现 Node 堆内存不足，可以提高：

```env
AUTHVERSE_WEB_BUILD_NODE_OPTIONS=--max-old-space-size=6144
```

### 5.6 推送到私有仓库

确认私有仓库已经可用后，执行：

```bash
docker/swarm/publish-private-images.sh --env-file .env.swarm --image-keys AUTHVERSE_WEB,AUTHVERSE_BACKEND
```

如果你要把 Tika 一起推，就执行：

```bash
docker/swarm/publish-private-images.sh --env-file .env.swarm --image-keys TIKA,AUTHVERSE_WEB,AUTHVERSE_BACKEND
```

如果你还需要顺手打出一份上线前镜像归档，可以继续执行：

```bash
docker/swarm/export-swarm-images.sh --env-file .env.swarm --output-dir .
```

### 5.7 部署统一认证栈

执行：

```bash
docker/swarm/deploy-auth-stack.sh --env-file .env.swarm
```

只渲染不部署：

```bash
docker/swarm/deploy-auth-stack.sh --env-file .env.swarm --render-only
```

## 6. 联调与验收

### 6.1 入口健康检查

```bash
curl -fsS http://127.0.0.1:${AUTHVERSE_HTTP_PORT}/healthz
curl -fsS ${AUTHVERSE_PUBLIC_BASE_URL}/.well-known/openid-configuration
curl -i -sS ${AUTHVERSE_PUBLIC_BASE_URL}/app-api/infra/server/get-info | sed -n '1,20p'
```

第二条返回值里重点确认：

- `issuer`
- `authorization_endpoint`
- `token_endpoint`
- `jwks_uri`
- `end_session_endpoint`

都应该指向 `AUTHVERSE_PUBLIC_BASE_URL`。

第三条如果返回 `401 账号未登录`，说明：

- `authverse-web -> authverse-backend` 反代正常
- 统一认证后端已经能对外提供业务 API

### 6.2 统一认证后端烟测

可以直接复用后端仓库已有脚本：

```bash
BASE_URL=${AUTHVERSE_PUBLIC_BASE_URL} \
LOGIN_USERNAME=admin \
LOGIN_PASSWORD=admin123 \
../authverse-backend/script/shell/unified-auth-smoke.sh
```

因为 `authverse-web` 已经把 `/admin-api` 代理到了 `authverse-backend`，所以这里可以直接打公网入口。

### 6.3 Cloudreve 联调检查

至少确认：

1. 打开 authverse 首页时，Cloudreve 文件列表相关请求能正常走 `/api/v4`
2. 直接打开分享链接 `/s/...` 正常
3. 文件直链 `/f/...` 正常
4. 统一认证前端管理页能正常访问 `/admin-api`、`/app-api`

如果首页能开、后台能登录、OIDC discovery 正常，说明这一轮“统一认证前后端 + Cloudreve 主站”联调已经打通。

## 7. 运维命令

查看服务：

```bash
docker stack services authverse
docker service ps authverse_authverse-web
docker service ps authverse_authverse-backend
```

查看日志：

```bash
docker service logs -f authverse_authverse-web
docker service logs -f authverse_authverse-backend
```

扩容：

```bash
docker service scale authverse_authverse-web=3
docker service scale authverse_authverse-backend=3
```

强制滚动更新：

```bash
docker service update --force authverse_authverse-web
docker service update --force authverse_authverse-backend
```

回滚：

```bash
docker service rollback authverse_authverse-web
docker service rollback authverse_authverse-backend
```

删除统一认证栈：

```bash
docker stack rm authverse
```

## 8. 常见问题

### 8.1 `shared overlay network does not exist`

原因：

- Cloudreve 主栈还没部署
- 或 `AUTHVERSE_SHARED_NETWORK` 填错了

处理：

- 先执行 `docker network ls`
- 确认真实网络名
- 再把 `AUTHVERSE_SHARED_NETWORK` 改成真实值

### 8.2 `authverse-backend` 启动后报数据库连接失败

先检查：

- `AUTHVERSE_DB_MASTER_URL`
- `AUTHVERSE_DB_USERNAME`
- `AUTHVERSE_DB_PASSWORD`
- `docker/swarm/init-authverse-db.sh` 是否执行过

再检查：

```bash
docker service logs authverse_authverse-backend
```

如果日志里出现：

- `permission denied for table dual`

基本就是初始化 SQL 导入后的对象权限不对。直接重新执行：

```bash
docker/swarm/init-authverse-db.sh --env-file .env.swarm
```

新版脚本会重建专用库并修正 owner / grant。

### 8.3 `/.well-known/openid-configuration` 返回的地址不对

基本就是：

- `AUTHVERSE_PUBLIC_BASE_URL` 没配对
- 或入口代理层没把统一认证公网域名打通

`issuer`、`token_endpoint`、`jwks_uri` 都应统一指向公网入口。

### 8.4 authverse 首页能开，但 Cloudreve 请求 401 / 404

优先看：

- `AUTHVERSE_CLOUDREVE_INTERNAL_UPSTREAM`
- `AUTHVERSE_CLOUDREVE_PUBLIC_BASE_URL`
- `AUTHVERSE_CLOUDREVE_HOST_HEADER`

如果 auth 域名和 Cloudreve 主域名不同，通常需要显式设置 `AUTHVERSE_CLOUDREVE_HOST_HEADER=cloudreve.example.com`。

### 8.5 `/.well-known` 或 `/app-api` 间歇性 502，但 backend 明明是健康的

这通常不是 Java 后端挂了，而是 `authverse-web` 的 Nginx 在启动时把 `authverse-backend` 解析成了旧 task IP。

处理：

1. 确认前端镜像已经包含动态 DNS 版本的 `authverse.conf.template`
2. 重建 `AUTHVERSE_WEB_IMAGE`
3. 强制滚动更新：

```bash
docker service update --force authverse_authverse-web
```

如果你直接在 `authverse-web` 容器里访问 `http://authverse-backend:48080/...` 能通，但公网入口还是 502，优先怀疑就是这个问题。

### 8.6 bind 模式下后端多副本只有一部分节点正常

原因通常是：

- 某些节点没有 `private.pem` / `public.pem`
- 或路径存在，但权限不可读

处理：

```bash
sudo docker/swarm/prepare-bind-paths.sh --env-file .env.swarm --services authverse --check
```

然后逐台节点确认：

- 目录存在
- 文件存在
- Docker 运行用户可读

## 9. 最短执行路径

如果你现在就要按真实环境往前推，最短路径就是：

```bash
cp .env.swarm.prod-4x128g.example .env.swarm
sudo docker/swarm/prepare-bind-paths.sh --env-file .env.swarm --services authverse
docker/swarm/init-authverse-db.sh --env-file .env.swarm
docker/swarm/build-auth-images.sh --env-file .env.swarm
docker/swarm/publish-private-images.sh --env-file .env.swarm --image-keys AUTHVERSE_WEB,AUTHVERSE_BACKEND
docker/swarm/deploy-auth-stack.sh --env-file .env.swarm
BASE_URL=https://auth.example.com ../authverse-backend/script/shell/unified-auth-smoke.sh
```

如果这条链跑通，统一认证前后端在 Swarm 里的真实环境接入就已经完成。
