# Docker Swarm 对外入口 LB/VIP 方案

本文只描述 4 台 `256GB / 64 线程` 生产机口径下的对外入口负载均衡。内部服务负载均衡继续交给 Swarm overlay/VIP、服务自身代理层和各集群组件处理。

## 1. 结论

不需要给每个服务单独部署一套 LB。

推荐只建设一套统一的四层 LB/VIP，所有对外服务端口都挂到这套 VIP 上：

```text
客户端
  -> VIP / 外部四层 LB
  -> 多台 cloudreve.edge 节点的 Swarm published port
  -> Swarm ingress routing mesh
  -> 对应服务副本
  -> 内部真实服务或集群
```

所有入口端口继续使用非标准端口，不使用 `80/443/5432/6379/9000/9200` 这类默认端口。

Pgpool、Redis、MinIO、Elasticsearch、HTTP TLS 入口都建议在 VIP/LB 层做 TCP 透传，不在外部 LB 上终止 TLS。尤其 Pgpool 不能用普通 TLS 终止，否则 PostgreSQL 客户端可能因为协议级 `SSLRequest` 错配报 `EOF`。

## 2. 生产对外入口清单

以下端口来自 `.env.swarm.prod-4x256g.example`、`docker-compose.swarm.registry.yml`、`docker-compose.swarm.cloudreve.yml`、`docker-compose.swarm.auth.yml`、`docker-compose.swarm.foundation.yml`、`docker-compose.swarm.infra.yml`。

| 服务 | VIP 端口 | Swarm published port | 协议 | 生产副本 | 是否接入 VIP/LB | 说明 |
| --- | --- | --- | --- | --- | --- | --- |
| `cloudreve-master-proxy` | `28081` | `18081` | TCP/TLS/HTTPS | `4` | 是 | Cloudreve 主站入口 |
| `cloudreve-slave-proxy` | `28082` | `18082` | TCP/TLS/HTTPS | `4` | 是 | Cloudreve 从站入口 |
| `authverse-web` | `28080` | `18080` | TCP/TLS/HTTPS | `4` | 是 | 统一认证前端和认证 API 统一入口 |
| `onlyoffice-public` | `28090` | `18090` | TCP/TLS/HTTPS | `4` | 是 | OnlyOffice 入口 |
| `tika-proxy` | `29998` | `19998` | TCP/TLS/HTTPS | `4` | 按需，默认启用 | 内部调用可不对公网暴露；外部调试或第三方调用时接入 |
| `minio` API | `29000` | `19000` | TCP/TLS/S3 API | `4` | 是 | S3 API 入口 |
| `minio` Console | `29001` | `19001` | TCP/TLS/HTTPS | `4` | 按需，默认启用 | 管理控制台，建议限制来源 IP |
| `elasticsearch` HTTP | `29200` | `19200` | TCP/TLS/HTTP API | `4` | 按需，默认启用 | 一般只给内网或运维机使用 |
| `elasticsearch` Transport | `29300` | `19300` | TCP/TLS/Transport | `4` | 默认关闭 | 没有外部 ES 节点或客户端需求时不要暴露 |
| `kafka-ui-public` | `28089` | `18089` | TCP/TLS/HTTPS | `4` | 按需，默认启用 | 管理界面，建议限制来源 IP |
| `pgpool` | `25432` | `15432` | TCP/PostgreSQL TLS | `4` | 是或内网专用 | 必须 TCP 透传，不要 TLS 终止 |
| `redis-proxy` | `26379` | `16379` | TCP/Redis TLS | `4` | 默认启用，建议内网专用 | 必须 TCP 透传；不建议对公网开放 |
| `registry` | `25000` | `15000` | TCP/HTTP Registry | `1` | 默认关闭 | 内部镜像仓库，固定单节点，给 Swarm 节点拉镜像用 |

`registry` 是特殊项：当前设计是固定节点上的 `registry:2`，数据目录是本机 bind 路径。它不应该简单扩成多副本，否则多个副本会看到不同本地存储。要做 registry 高可用，需要改成共享对象存储后端或外部高可用镜像仓库。

## 3. LB 后端节点

推荐所有对外入口都转发到打了 `cloudreve.edge=true` 的节点。

当前 4 台生产示例中，4 台都作为 `edge` 后端节点：

```text
cr-prod-mgr-11
cr-prod-wkr-12
cr-prod-wkr-13
cr-prod-wkr-14
```

`.env.swarm.prod-4x256g.example` 默认：

```bash
SWARM_LB_EDGE_NODES=cr-prod-mgr-11=10.10.0.11,cr-prod-wkr-12=10.10.0.12,cr-prod-wkr-13=10.10.0.13,cr-prod-wkr-14=10.10.0.14
```

上线前把这里的 IP 改成 4 台机器真实内网 IP。

## 4. 端口映射策略

LB/VIP 前端端口和 Swarm published port 都必须是非标准端口。

如果 HAProxy/Keepalived 与 Swarm 节点共机，推荐使用不同端口：

- VIP 前端端口：`2xxxx`
- Swarm published port：`1xxxx`

这样可以避免 HAProxy 与 Docker ingress 在同一台机器上争抢 `0.0.0.0:<port>`。

示例：

```text
VIP:28081 -> cr-prod-mgr-11:18081, cr-prod-wkr-12:18081, cr-prod-wkr-13:18081, cr-prod-wkr-14:18081
VIP:28080 -> cr-prod-mgr-11:18080, cr-prod-wkr-12:18080, cr-prod-wkr-13:18080, cr-prod-wkr-14:18080
VIP:25432 -> cr-prod-mgr-11:15432, cr-prod-wkr-12:15432, cr-prod-wkr-13:15432, cr-prod-wkr-14:15432
VIP:29000 -> cr-prod-mgr-11:19000, cr-prod-wkr-12:19000, cr-prod-wkr-13:19000, cr-prod-wkr-14:19000
```

如果 LB 与 Swarm 节点共机，不能让宿主机上的 HAProxy 监听和 Docker Swarm published port 相同的地址/端口。共机时有两个选择：

1. 推荐使用独立 LB 节点或云厂商 NLB。
2. 如果必须共机，HAProxy 只绑定 VIP 地址，Swarm published port 继续绑定节点主 IP；同时确认内核、Keepalived 和 HAProxy 都不会抢占 `0.0.0.0:<port>`。

实践上，独立 LB 节点最简单、最不容易和 Docker ingress 冲突。

## 5. 生成 HAProxy / Keepalived 配置

配置由脚本统一生成：

```bash
docker/swarm/setup-swarm-vip-lb.sh --env-file .env.swarm --output-dir .tmp/swarm-vip-lb
```

生成文件：

- `.tmp/swarm-vip-lb/haproxy.cfg`
- `.tmp/swarm-vip-lb/keepalived.conf`
- `.tmp/swarm-vip-lb/99-cloudreve-vip-lb.conf`

正式下发到 LB 节点：

```bash
sudo apt-get update
sudo apt-get install -y haproxy keepalived
sudo docker/swarm/setup-swarm-vip-lb.sh --env-file .env.swarm --role master --priority 120 --apply
```

备节点：

```bash
sudo apt-get update
sudo apt-get install -y haproxy keepalived
sudo docker/swarm/setup-swarm-vip-lb.sh --env-file .env.swarm --role backup --priority 100 --apply
```

脚本会写入：

- `/etc/haproxy/haproxy.cfg`
- `/etc/keepalived/keepalived.conf`
- `/etc/sysctl.d/99-cloudreve-vip-lb.conf`

并设置 `net.ipv4.ip_nonlocal_bind=1`，让 BACKUP 节点也能预绑定尚未漂移到本机的 VIP。

Pgpool 必须保持 TCP 透传。不要写 `bind ... ssl crt ...`，否则 PostgreSQL 客户端的协议级 `SSLRequest` 会被外层 TLS 终止破坏。

## 6. Keepalived VIP 说明

脚本生成的核心结构如下：

```conf
vrrp_script chk_haproxy {
    script "pidof haproxy"
    interval 2
    weight -20
}

vrrp_instance VI_CLOUDREVE {
    state MASTER
    interface eth0
    virtual_router_id 81
    priority 120
    advert_int 1
    authentication {
        auth_type PASS
        auth_pass <SWARM_LB_AUTH_PASS>
    }
    virtual_ipaddress {
        <SWARM_LB_VIP>/<SWARM_LB_VIP_CIDR>
    }
    track_script {
        chk_haproxy
    }
}
```

主节点用 `--role master --priority 120`，备节点用 `--role backup --priority 100`。

## 7. 验收命令

从客户端或运维机验证 VIP：

```bash
curl -kfsS https://<vip>:28081/api/v4/site/ping
curl -kfsS https://<vip>:28080/
curl -ksS -o /dev/null -w '%{http_code}\n' https://<vip>:29000/minio/health/live
curl -ksS -o /dev/null -w '%{http_code}\n' https://<vip>:29998/tika
```

Pgpool 需要按 PostgreSQL TLS 方式验证：

```bash
PGPASSWORD='<pg-password>' \
PGSSLMODE=verify-ca \
PGSSLROOTCERT=/srv/cloudreve/pki/ca/ca.crt \
psql -h <vip> -p 25432 -U cloudreve -d cloudreve -c 'select 1;'
```

Redis 如果确实需要外部访问，建议只在内网验证：

```bash
redis-cli --tls --cacert /srv/cloudreve/pki/ca/ca.crt \
  -h <vip> -p 26379 -a '<redis-password>' PING
```

## 8. 当前仍需注意

- `kafka-ui-public` 已按生产建议设置为 4 副本并放入 `cloudreve.edge` 池。
- `registry` 仍保持单副本固定节点，这是为了避免本地文件系统存储多副本不一致。
- Kafka broker 本身不建议直接对外做通用 LB；如果后续要给外部 Kafka client 使用，需要单独设计 advertised listeners、证书和 broker 级端口映射。
- Elasticsearch `19300` 通常不应对外开放，除非你明确有跨集群或外部 transport client 需求。
