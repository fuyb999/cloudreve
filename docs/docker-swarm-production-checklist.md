# Cloudreve 4 台 Linux 生产上线清单

适用口径：

- `1` 个 manager
- `3` 个 worker
- 每台 `128GB` 内存
- 每台 `64` 线程
- 直接使用 [`.env.swarm.prod-4x128g.example`](/Users/fuyb/Desktop/20260322/code/cloudreve/.env.swarm.prod-4x128g.example) 作为基线

对应模板：

- [`.env.swarm.prod-4x128g.example`](/Users/fuyb/Desktop/20260322/code/cloudreve/.env.swarm.prod-4x128g.example)
- [`docker-compose.swarm.registry.yml`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker-compose.swarm.registry.yml)
- [`docker-compose.swarm.cloudreve.yml`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker-compose.swarm.cloudreve.yml)
- [`docker-compose.swarm.foundation.yml`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker-compose.swarm.foundation.yml)
- [`docker-compose.swarm.infra.yml`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker-compose.swarm.infra.yml)
- [`docker-compose.swarm.auth.yml`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker-compose.swarm.auth.yml)
- [`docker/swarm/deploy-stack.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/deploy-stack.sh)
- [`docker/swarm/export-swarm-images.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/export-swarm-images.sh)
- [`docker/swarm/build-auth-images.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/build-auth-images.sh)
- [`docker/swarm/init-authverse-db.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/init-authverse-db.sh)
- [`docker/swarm/prepare-bind-paths.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/prepare-bind-paths.sh)
- [`docker/swarm/prepare-bitnami-images.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/prepare-bitnami-images.sh)
- [`docker/swarm/prepare-private-registry.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/prepare-private-registry.sh)
- [`docker/swarm/publish-private-images.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/publish-private-images.sh)
- [`docker/swarm/sync-swarm-assets.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/sync-swarm-assets.sh)

## 1. 主机名

- `cr-prod-mgr-11`
- `cr-prod-wkr-12`
- `cr-prod-wkr-13`
- `cr-prod-wkr-14`

这里推荐把主机名最后一位直接做成机器 IP 的最后一位，方便在 `docker node ls`、`docker service ps`、日志和告警里快速定位到具体服务器。

## 1.1 最终执行顺序

按真实 4 台生产机上线，顺序固定为：

1. 配主机名、装 Docker、初始化 Swarm、worker 加入集群。
2. 在 manager 给 4 台机器打好节点标签。
3. 复制 [`.env.swarm.prod-4x128g.example`](/Users/fuyb/Desktop/20260322/code/cloudreve/.env.swarm.prod-4x128g.example) 为 `.env.swarm` 并回填密码、域名、固定 tag。
4. 在 manager 先生成默认根 CA 和整套业务服务证书，再把根 CA 下发到所有节点。
5. 在所有节点执行 `prepare-private-registry.sh`，把固定 manager 上的 `registry:2` 写入 Docker `insecure-registries`。
6. 在所有目标节点分别执行目录准备，先把 `PKI / 共享字体 / bind` 目录建好。
7. 在 manager 部署私有仓库，再构建并推送 `Tika`、`authverse-web`、`authverse-backend`。
8. 在 manager 按 `cloudreve / foundation / infra` 三个模板分别执行 `render-only`，确认无误后正式部署到同一个 `cloudreve-prod` 栈。
9. 主栈稳定后执行 `docker/swarm/init-authverse-db.sh`，再部署 `authverse-prod`。
10. 做外部入口、数据库、Redis、MinIO、Elasticsearch、OIDC discovery、`/app-api` 的整体验收。
11. 首次进入 Cloudreve 后台生成真实 `Slave Key`，回填 `.env.swarm`，最后再滚动部署一次主栈。

## 2. 初始化 Swarm

在 manager：

```bash
docker swarm init --advertise-addr <manager-ip>
docker swarm join-token worker
```

在 3 台 worker：

```bash
docker swarm join --token <worker-token> <manager-ip>:2377
```

放通节点间网络：

- `2377/tcp`
- `7946/tcp`
- `7946/udp`
- `4789/udp`
- `ESP`，也就是 `IP protocol 50`

如果你用的是 Ubuntu `ufw`，可以直接这样放行。

先放行 SSH，避免把自己锁死：

```bash
sudo ufw allow OpenSSH
```

假设 4 台节点的内网网段是 `10.10.0.0/24`：

```bash
NODE_NET="10.10.0.0/24"
```

manager 上执行：

```bash
sudo ufw allow proto tcp from $NODE_NET to any port 2377 comment 'docker swarm manager'
sudo ufw allow proto tcp from $NODE_NET to any port 7946 comment 'docker swarm gossip'
sudo ufw allow proto udp from $NODE_NET to any port 7946 comment 'docker swarm gossip'
sudo ufw allow proto udp from $NODE_NET to any port 4789 comment 'docker overlay vxlan'
sudo ufw allow from $NODE_NET to any proto esp comment 'docker overlay encrypted'
```

worker 上执行：

```bash
sudo ufw allow proto tcp from $NODE_NET to any port 7946 comment 'docker swarm gossip'
sudo ufw allow proto udp from $NODE_NET to any port 7946 comment 'docker swarm gossip'
sudo ufw allow proto udp from $NODE_NET to any port 4789 comment 'docker overlay vxlan'
sudo ufw allow from $NODE_NET to any proto esp comment 'docker overlay encrypted'
```

最后检查：

```bash
sudo ufw status numbered
sudo ufw status verbose
```

## 3. 节点标签

在 manager 上执行：

```bash
docker node update --label-add cloudreve.master=true cr-prod-mgr-11
docker node update --label-add cloudreve.edge=true cr-prod-mgr-11
docker node update --label-add cloudreve.registry=true cr-prod-mgr-11
docker node update --label-add cloudreve.minio1=true cr-prod-mgr-11
docker node update --label-add cloudreve.tika=true cr-prod-mgr-11
docker node update --label-add cloudreve.kafka-ui=true cr-prod-mgr-11
docker node update --label-add cloudreve.auth-web=true cr-prod-mgr-11

docker node update --label-add cloudreve.slave=true cr-prod-wkr-12
docker node update --label-add cloudreve.edge=true cr-prod-wkr-12
docker node update --label-add cloudreve.pg1=true cr-prod-wkr-12
docker node update --label-add cloudreve.redis1=true cr-prod-wkr-12
docker node update --label-add cloudreve.redis-sentinel=true cr-prod-wkr-12
docker node update --label-add cloudreve.es1=true cr-prod-wkr-12
docker node update --label-add cloudreve.kafka1=true cr-prod-wkr-12
docker node update --label-add cloudreve.minio2=true cr-prod-wkr-12
docker node update --label-add cloudreve.auth-backend=true cr-prod-wkr-12

docker node update --label-add cloudreve.slave=true cr-prod-wkr-13
docker node update --label-add cloudreve.pg2=true cr-prod-wkr-13
docker node update --label-add cloudreve.redis2=true cr-prod-wkr-13
docker node update --label-add cloudreve.redis-sentinel=true cr-prod-wkr-13
docker node update --label-add cloudreve.es2=true cr-prod-wkr-13
docker node update --label-add cloudreve.kafka2=true cr-prod-wkr-13
docker node update --label-add cloudreve.minio3=true cr-prod-wkr-13
docker node update --label-add cloudreve.auth-backend=true cr-prod-wkr-13

docker node update --label-add cloudreve.slave=true cr-prod-wkr-14
docker node update --label-add cloudreve.edge=true cr-prod-wkr-14
docker node update --label-add cloudreve.pg3=true cr-prod-wkr-14
docker node update --label-add cloudreve.redis3=true cr-prod-wkr-14
docker node update --label-add cloudreve.redis-sentinel=true cr-prod-wkr-14
docker node update --label-add cloudreve.es3=true cr-prod-wkr-14
docker node update --label-add cloudreve.kafka3=true cr-prod-wkr-14
docker node update --label-add cloudreve.minio4=true cr-prod-wkr-14
docker node update --label-add cloudreve.tika=true cr-prod-wkr-14
docker node update --label-add cloudreve.auth-web=true cr-prod-wkr-14
```

## 4. 环境文件

先在固定的部署 manager 上：

```bash
cp .env.swarm.prod-4x128g.example .env.swarm
```

如果你准备在其它节点直接执行仓库里的准备脚本，也把同一份 `.env.swarm`
同步到对应节点；运行时不需要，但准备脚本会读取它。

至少回填这些变量：

- `CLOUDREVE_SITE_URL`
- `CLOUDREVE_SESSION_SECRET`
- `POSTGRESQL_PASSWORD`
- `POSTGRESQL_POSTGRES_PASSWORD`
- `REPMGR_PASSWORD`
- `PGPOOL_ADMIN_PASSWORD`
- `REDIS_PASSWORD`
- `MINIO_ROOT_PASSWORD`
- `CR_INIT_S3_SECRET_KEY`
- `AUTHVERSE_PUBLIC_BASE_URL`
- `SWARM_PKI_MOUNT_SOURCE`
- `SHARED_CUSTOM_FONTS_MOUNT_SOURCE`

注意：

- `CR_INIT_S3_ENDPOINT` 保持 `http://minio-internal:9000`
- `CR_INIT_S3_BUCKET` 保持 `cloudreve`
- `MINIO_DISTRIBUTED_NODES` 默认保持 `minio-1,minio-2,minio-3,minio-4`
- `CLOUDREVE_STACK_NAME` 在这份生产模板里默认就是 `cloudreve-prod`，不要改回 `cloudreve`
- `PRIVATE_REGISTRY_ADDR` 默认保持 `cr-prod-mgr-11:15000`
- `PRIVATE_REGISTRY_SCHEME` 默认保持 `http`
- `TIKA_IMAGE` 默认保持 `${PRIVATE_REGISTRY_ADDR}/cloudreve/tika:3.2.3.0-full-unrar-charset`
- `AUTHVERSE_WEB_IMAGE` 默认保持 `${PRIVATE_REGISTRY_ADDR}/authverse/authverse-web:2024-local`
- `AUTHVERSE_BACKEND_IMAGE` 默认保持 `${PRIVATE_REGISTRY_ADDR}/authverse/authverse-backend:2024-local`
- `AUTHVERSE_POSTGRES_HOST` 默认保持 `${CLOUDREVE_STACK_NAME}_pgpool-internal`
- `AUTHVERSE_REDIS_HOST` 默认保持 `${CLOUDREVE_STACK_NAME}_redis-proxy-internal`
- `AUTHVERSE_ELASTICSEARCH_URI` 默认保持 `http://${CLOUDREVE_STACK_NAME}_elasticsearch-internal:9200`
- 对外发布端口虽然很多变量名还叫 `*_HTTP_PORT`，但现在默认对外都提供 TLS
- `.env.swarm` 不会自动同步到其它 manager，生产里固定只从一个 manager 部署

## 4.1 生成默认 CA 与服务证书

在 manager 上执行：

```bash
docker/swarm/generate-swarm-pki.sh --env-file .env.swarm --force-certs
```

默认会生成：

- `SWARM_PKI_MOUNT_SOURCE/ca/ca.crt`
- `SWARM_PKI_MOUNT_SOURCE/ca/ca.key`
- `SWARM_PKI_MOUNT_SOURCE/services/<service>/tls.crt`
- `SWARM_PKI_MOUNT_SOURCE/services/<service>/tls.key`

当前默认会签发：

- `cloudreve-master-proxy`
- `cloudreve-slave-proxy`
- `authverse-web`
- `registry`
- `pgpool`
- `redis-proxy`
- `minio`
- `elasticsearch`
- `tika-proxy`
- `onlyoffice-public`
- `kafka-ui-public`

## 4.2 给所有节点安装根 CA

对外业务入口默认走这套私有 CA；私有仓库默认回到 `http`。

在每台 Swarm 节点执行：

```bash
sudo docker/swarm/prepare-private-registry.sh --env-file .env.swarm --check
sudo docker/swarm/prepare-private-registry.sh --env-file .env.swarm --apply --restart-docker
```

如果你的浏览器、堡垒机、运维终端也要直接访问这些 HTTPS 入口，
也应导入同一份 `ca.crt`。

补充：

- `PRIVATE_REGISTRY_SCHEME=http` 时，脚本会把 `PRIVATE_REGISTRY_ADDR` 写入 Docker `insecure-registries`
- 如果你后续要切换成自己的 CA，先替换 `SWARM_PKI_MOUNT_SOURCE/ca/ca.crt` 和 `ca.key`，再执行：

```bash
docker/swarm/generate-swarm-pki.sh --env-file .env.swarm --force-ca --force-certs
sudo docker/swarm/prepare-private-registry.sh --env-file .env.swarm --apply --restart-docker
```

- 如果 `SWARM_PKI_MOUNT_TYPE=bind` 或 `SHARED_CUSTOM_FONTS_MOUNT_TYPE=bind`，再从 manager 执行：

```bash
docker/swarm/sync-swarm-assets.sh --env-file .env.swarm --targets "cr-prod-wkr-12,cr-prod-wkr-13,cr-prod-wkr-14" --check
docker/swarm/sync-swarm-assets.sh --env-file .env.swarm --targets "cr-prod-wkr-12,cr-prod-wkr-13,cr-prod-wkr-14" --ssh-user root --apply
```

## 5. 宿主机准备

在 `elasticsearch` 所在节点：

```bash
sudo sysctl -w vm.max_map_count=262144
echo 'vm.max_map_count=262144' | sudo tee /etc/sysctl.d/99-cloudreve.conf
sudo sysctl --system
```

在每台目标节点执行目录预创建：

```bash
sudo docker/swarm/prepare-bind-paths.sh --env-file .env.swarm --services pg,redis,cloudreve,tika,onlyoffice --check
sudo docker/swarm/prepare-bind-paths.sh --env-file .env.swarm --services pg,redis,cloudreve,tika,onlyoffice --apply
sudo docker/swarm/prepare-bind-paths.sh --env-file .env.swarm --services minio,elasticsearch,kafka --check
sudo docker/swarm/prepare-bind-paths.sh --env-file .env.swarm --services minio,elasticsearch,kafka --apply
```

在 registry 固定节点再补一次：

```bash
sudo docker/swarm/prepare-bind-paths.sh --env-file .env.swarm --services registry
```

如果统一认证的 OIDC RSA 密钥也走宿主机绝对路径，再补一次：

```bash
sudo docker/swarm/prepare-bind-paths.sh --env-file .env.swarm --services authverse
```

说明：

- 这个脚本现在会一起准备 `SWARM_PKI_MOUNT_SOURCE`
- 如果 `SHARED_CUSTOM_FONTS_MOUNT_TYPE=bind`，也会一起准备共享字体目录
- `Tika` 和 `OnlyOffice` 默认共用 `SHARED_CUSTOM_FONTS_MOUNT_SOURCE`

## 6. 镜像准备

先让所有节点信任固定私有仓库：

```bash
sudo docker/swarm/prepare-private-registry.sh --env-file .env.swarm --check
sudo docker/swarm/prepare-private-registry.sh --env-file .env.swarm --apply --restart-docker
```

说明：

- 这个脚本优先使用 `jq`，没有 `jq` 时会自动回退到 `python3`
- 两者都没有时，需要先在节点安装其中一个
- `publish-private-images.sh` 在 `https` 模式下会自动使用 `PRIVATE_REGISTRY_CA_FILE` 验证 registry 证书

如果你想在固定部署 manager 先把 PostgreSQL / Redis / MinIO 的 local 源镜像预拉齐，再执行：

```bash
docker/swarm/prepare-bitnami-images.sh --env-file .env.swarm --check
docker/swarm/prepare-bitnami-images.sh --env-file .env.swarm
```

然后在 manager 上部署私有仓库，并把整套业务镜像推进去：

```bash
docker/swarm/deploy-stack.sh registry --env-file .env.swarm --stack-name cloudreve-registry
docker/swarm/build-auth-images.sh --env-file .env.swarm
docker/swarm/publish-private-images.sh --env-file .env.swarm
```

如果你要在上线前把这一套镜像固化成离线包，再执行：

```bash
docker/swarm/export-swarm-images.sh --env-file .env.swarm --output-dir .
```

产物会落在仓库当前目录，形式是：

- `swarm-images-时间戳/`
- `swarm-images-时间戳.tar.xz`

## 7. 部署

先只渲染检查：

```bash
docker/swarm/deploy-stack.sh cloudreve --env-file .env.swarm --stack-name cloudreve-prod --render-only
docker/swarm/deploy-stack.sh foundation --env-file .env.swarm --stack-name cloudreve-prod --render-only
docker/swarm/deploy-stack.sh infra --env-file .env.swarm --stack-name cloudreve-prod --render-only
```

确认无误后正式上线：

```bash
docker/swarm/deploy-stack.sh cloudreve --env-file .env.swarm --stack-name cloudreve-prod
docker/swarm/deploy-stack.sh foundation --env-file .env.swarm --stack-name cloudreve-prod
docker/swarm/deploy-stack.sh infra --env-file .env.swarm --stack-name cloudreve-prod
```

Cloudreve 主栈起来后，再初始化统一认证数据库并部署统一认证：

```bash
docker/swarm/init-authverse-db.sh --env-file .env.swarm
docker/swarm/deploy-stack.sh auth --env-file .env.swarm --stack-name authverse-prod
```

## 8. 验收

看服务状态：

```bash
docker stack services cloudreve-prod
docker stack ps cloudreve-prod
```

说明：

- `minio-1..4` 的容器 hostname 会固定成 `minio-1..4`
- 这是 MinIO 分布式自识别要求
- 要看它实际落在哪台物理机，请用 `docker service ps cloudreve-prod_minio-1`

对外验收：

```bash
curl -kfsS https://<master-ip-or-lb>:18081/api/v4/site/ping
curl -ksS -o /dev/null -w '%{http_code}\n' https://<minio-ip-or-lb>:19000/minio/health/live
curl -ksS -o /dev/null -w '%{http_code}\n' https://<tika-ip-or-lb>:19998/tika
curl -ksS -o /dev/null -w '%{http_code}\n' https://<es-ip-or-lb>:19200
```

数据库与缓存验收：

```bash
PGPASSWORD='<postgres-password>' psql -h <pgpool-ip-or-lb> -p 15432 -U cloudreve -d cloudreve -c 'select 1;'
redis-cli -h <redis-proxy-ip-or-lb> -p 16379 -a '<redis-password>' PING
```

MinIO bucket 验收：

```bash
docker exec -it $(docker ps --filter label=com.docker.swarm.service.name=cloudreve-prod_minio-init -q | head -n 1) \
  sh -lc '/opt/bitnami/minio-client/bin/mc --config-dir /tmp/.mc ls local'
```

看到 `cloudreve/` 说明默认 bucket 已自动创建。

## 9. 首次初始化

1. 打开 `http(s)://<master-domain-or-ip>/admin`
2. 确认站点 URL 与 `CLOUDREVE_SITE_URL` 完全一致
3. 确认默认存储策略 `ID=1` 已经是 `S3`
4. 创建 slave 节点，复制 `Slave Key`
5. 回填 `.env.swarm` 里的 `CLOUDREVE_SLAVE_SECRET`
6. 重新执行一次部署命令

## 10. 日常运维入口

详细命令手册看：

- [docker-swarm-operations-manual.md](/Users/fuyb/Desktop/20260322/code/cloudreve/docs/docker-swarm-operations-manual.md)

环境文件同步策略看：

- [docker-swarm-env-sync.md](/Users/fuyb/Desktop/20260322/code/cloudreve/docs/docker-swarm-env-sync.md)
