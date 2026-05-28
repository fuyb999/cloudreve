# Cloudreve Docker Compose Dev Environment

This compose file is a local debug base for Cloudreve, Authverse, search, and file extraction dependencies. It is intentionally single-node and does not try to emulate Swarm HA coordination.

## Start

```bash
docker --context colima compose up -d
```

Optional Cloudreve container:

```bash
docker --context colima compose --profile cloudreve up -d cloudreve
```

Cloudreve can also be run from the host or IDE and connect to the exposed ports below.

## Services

| Service | Container DNS | Host URL / Port |
| --- | --- | --- |
| PostgreSQL 17 | `postgresql-1:5432` | `127.0.0.1:15432` |
| Redis | `redis-1:6379` | `127.0.0.1:16379` |
| MinIO API | `minio:9000` | `http://127.0.0.1:19000` |
| MinIO Console | `minio:9001` | `http://127.0.0.1:19001` |
| Elasticsearch | `elasticsearch:9200` | `http://127.0.0.1:19200` |
| Kafka internal | `kafka:9092` | container-only |
| Kafka external | `localhost:19092` | `127.0.0.1:19092` |
| Kafka UI | `kafka-ui:8080` | `http://127.0.0.1:18089` |
| Tika | `tika:9998` | `http://127.0.0.1:19998` |
| Authverse backend | `authverse-backend:48080` | `http://127.0.0.1:48080` |
| Authverse web | `authverse-web:80` | `http://127.0.0.1:18080` |

## Default Credentials

PostgreSQL:

```text
user: cloudreve
password: cloudreve
database: cloudreve
authverse database: authverse
```

MinIO:

```text
access key: minio
secret key: minio123456
default bucket: cloudreve
```

## Host Debug Config

When running Cloudreve from the host, use these local endpoints:

```ini
[Database]
Type = postgres
Host = 127.0.0.1
Port = 15432
Name = cloudreve
User = cloudreve
Password = cloudreve

[Redis]
Server = 127.0.0.1:16379

[Kafka]
Enabled = true
Brokers = 127.0.0.1:19092
SecurityProtocol = PLAINTEXT
```

Use these runtime service endpoints in Cloudreve settings or env overrides:

```text
MinIO endpoint: http://127.0.0.1:19000
Elasticsearch: http://127.0.0.1:19200
Tika: http://127.0.0.1:19998
Authverse public entry: http://127.0.0.1:18080
Authverse backend: http://127.0.0.1:48080
```

## Notes

- `docker-compose.yml` uses `paradedb/paradedb:latest-pg17` as the local PostgreSQL image because it already exists in Colima and avoids Docker Hub pulls. It can be overridden with `CLOUDREVE_DEV_POSTGRES_IMAGE=postgres:17`.
- Authverse SQL initializes once when `public.system_users` does not exist in the `authverse` database. Later `compose up` runs skip destructive SQL import.
- Tika does not define a Docker healthcheck because the image does not include `curl` or `wget`; verify it with `curl http://127.0.0.1:19998/version`.
- The optional `cloudreve` profile expects `cloudreve/cloudreve:4.15.0` to exist locally. If that image is not loaded, run Cloudreve from the host instead.
