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
- [`docker-compose.swarm.yml`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker-compose.swarm.yml)
- [`docker-compose.swarm.cluster.yml`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker-compose.swarm.cluster.yml)
- [`docker-compose.swarm.auth.yml`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker-compose.swarm.auth.yml)
- [`docker/swarm/deploy-private-registry.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/deploy-private-registry.sh)
- [`docker/swarm/deploy-stack.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/deploy-stack.sh)
- [`docker/swarm/build-auth-images.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/build-auth-images.sh)
- [`docker/swarm/init-authverse-db.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/init-authverse-db.sh)
- [`docker/swarm/deploy-auth-stack.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/deploy-auth-stack.sh)
- [`docker/swarm/prepare-bind-paths.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/prepare-bind-paths.sh)
- [`docker/swarm/prepare-bitnami-images.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/prepare-bitnami-images.sh)
- [`docker/swarm/prepare-private-registry.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/prepare-private-registry.sh)
- [`docker/swarm/publish-private-images.sh`](/Users/fuyb/Desktop/20260322/code/cloudreve/docker/swarm/publish-private-images.sh)

## 1. 主机名

- `cr-prod-mgr-1`
- `cr-prod-wkr-1`
- `cr-prod-wkr-2`
- `cr-prod-wkr-3`

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

## 3. 节点标签

在 manager 上执行：

```bash
docker node update --label-add cloudreve.master=true cr-prod-mgr-1
docker node update --label-add cloudreve.edge=true cr-prod-mgr-1
docker node update --label-add cloudreve.registry=true cr-prod-mgr-1
docker node update --label-add cloudreve.minio1=true cr-prod-mgr-1
docker node update --label-add cloudreve.tika=true cr-prod-mgr-1
docker node update --label-add cloudreve.kafka-ui=true cr-prod-mgr-1
docker node update --label-add cloudreve.auth-web=true cr-prod-mgr-1

docker node update --label-add cloudreve.slave=true cr-prod-wkr-1
docker node update --label-add cloudreve.edge=true cr-prod-wkr-1
docker node update --label-add cloudreve.pg1=true cr-prod-wkr-1
docker node update --label-add cloudreve.redis1=true cr-prod-wkr-1
docker node update --label-add cloudreve.redis-sentinel=true cr-prod-wkr-1
docker node update --label-add cloudreve.es1=true cr-prod-wkr-1
docker node update --label-add cloudreve.kafka1=true cr-prod-wkr-1
docker node update --label-add cloudreve.minio2=true cr-prod-wkr-1
docker node update --label-add cloudreve.auth-backend=true cr-prod-wkr-1

docker node update --label-add cloudreve.slave=true cr-prod-wkr-2
docker node update --label-add cloudreve.pg2=true cr-prod-wkr-2
docker node update --label-add cloudreve.redis2=true cr-prod-wkr-2
docker node update --label-add cloudreve.redis-sentinel=true cr-prod-wkr-2
docker node update --label-add cloudreve.es2=true cr-prod-wkr-2
docker node update --label-add cloudreve.kafka2=true cr-prod-wkr-2
docker node update --label-add cloudreve.minio3=true cr-prod-wkr-2
docker node update --label-add cloudreve.auth-backend=true cr-prod-wkr-2

docker node update --label-add cloudreve.slave=true cr-prod-wkr-3
docker node update --label-add cloudreve.edge=true cr-prod-wkr-3
docker node update --label-add cloudreve.pg3=true cr-prod-wkr-3
docker node update --label-add cloudreve.redis3=true cr-prod-wkr-3
docker node update --label-add cloudreve.redis-sentinel=true cr-prod-wkr-3
docker node update --label-add cloudreve.es3=true cr-prod-wkr-3
docker node update --label-add cloudreve.kafka3=true cr-prod-wkr-3
docker node update --label-add cloudreve.minio4=true cr-prod-wkr-3
docker node update --label-add cloudreve.tika=true cr-prod-wkr-3
docker node update --label-add cloudreve.auth-web=true cr-prod-wkr-3
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

注意：

- `CR_INIT_S3_ENDPOINT` 保持 `http://minio:9000`
- `CR_INIT_S3_BUCKET` 保持 `cloudreve`
- `MINIO_DISTRIBUTED_NODES` 默认保持 `minio-1,minio-2,minio-3,minio-4`
- `CLOUDREVE_STACK_NAME` 在这份生产模板里默认就是 `cloudreve-prod`，不要改回 `cloudreve`
- `PRIVATE_REGISTRY_ADDR` 默认保持 `cr-prod-mgr-1:5000`
- `TIKA_IMAGE` 默认保持 `${PRIVATE_REGISTRY_ADDR}/cloudreve/tika:3.2.3.0-full-unrar-charset`
- `AUTHVERSE_WEB_IMAGE` 默认保持 `${PRIVATE_REGISTRY_ADDR}/cloudreve/authverse-web:4.0.0-next`
- `AUTHVERSE_BACKEND_IMAGE` 默认保持 `${PRIVATE_REGISTRY_ADDR}/cloudreve/authverse-backend:2025.12-snapshot`
- `.env.swarm` 不会自动同步到其它 manager，生产里固定只从一个 manager 部署

## 5. 宿主机准备

在 `elasticsearch` 所在节点：

```bash
sudo sysctl -w vm.max_map_count=262144
echo 'vm.max_map_count=262144' | sudo tee /etc/sysctl.d/99-cloudreve.conf
sudo sysctl --system
```

在每台目标节点执行目录预创建：

```bash
sudo docker/swarm/prepare-bind-paths.sh --env-file .env.swarm --check
sudo docker/swarm/prepare-bind-paths.sh --env-file .env.swarm --apply
```

在 registry 固定节点再补一次：

```bash
sudo docker/swarm/prepare-bind-paths.sh --env-file .env.swarm --services registry
```

如果统一认证的 OIDC RSA 密钥也走宿主机绝对路径，再补一次：

```bash
sudo docker/swarm/prepare-bind-paths.sh --env-file .env.swarm --services authverse
```

## 6. 镜像准备

先让所有节点信任固定私有仓库：

```bash
sudo docker/swarm/prepare-private-registry.sh --env-file .env.swarm --check
sudo docker/swarm/prepare-private-registry.sh --env-file .env.swarm --apply --restart-docker
```

说明：

- 这个脚本优先使用 `jq`，没有 `jq` 时会自动回退到 `python3`
- 两者都没有时，需要先在节点安装其中一个

先准备 PostgreSQL / Redis / MinIO 基础镜像：

```bash
docker/swarm/prepare-bitnami-images.sh --env-file .env.swarm --check
docker/swarm/prepare-bitnami-images.sh --env-file .env.swarm
```

然后在 manager 上部署私有仓库，并把 Tika 与统一认证镜像推进去：

```bash
docker/swarm/deploy-private-registry.sh --env-file .env.swarm --stack-name cloudreve-registry
docker/swarm/build-auth-images.sh --env-file .env.swarm
docker/swarm/publish-private-images.sh --env-file .env.swarm --image-keys TIKA,AUTHVERSE_WEB,AUTHVERSE_BACKEND
```

## 7. 部署

先只渲染检查：

```bash
docker/swarm/deploy-stack.sh --env-file .env.swarm --stack-name cloudreve-prod --with-cluster --render-only
```

确认无误后正式上线：

```bash
docker/swarm/deploy-stack.sh --env-file .env.swarm --stack-name cloudreve-prod --with-cluster
```

Cloudreve 主栈起来后，再初始化统一认证数据库并部署统一认证：

```bash
docker/swarm/init-authverse-db.sh --env-file .env.swarm
docker/swarm/deploy-auth-stack.sh --env-file .env.swarm --stack-name authverse-prod
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
curl -fsS http://<master-ip-or-lb>/api/v4/site/ping
curl -sS -o /dev/null -w '%{http_code}\n' http://<minio-ip-or-lb>:9000/minio/health/live
curl -sS -o /dev/null -w '%{http_code}\n' http://<tika-ip-or-lb>:9998/tika
curl -sS -o /dev/null -w '%{http_code}\n' http://<es-ip-or-lb>:9200
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
