# 第三方全文抽取安装与联调手册

## 1. 目标与边界

本方案用于让 Cloudreve 在全文检索链路中接入第三方抽取服务，并通过 Kafka 与 Tika 协同工作。

边界必须固定如下：

- Cloudreve 负责：任务编排、回退策略、sidecar 落盘、ES 写入、后台配置、状态跟踪。
- 第三方服务负责：根据 Kafka `process` 消息读取对象并完成文本/附件树抽取。
- 第三方服务不负责：写 Cloudreve 数据库、写 ES、管理对象存储凭据。
- Kafka `process` 消息只传 `bucket + path + 文件基础信息`，不传对象存储密钥。

## 2. 已实现能力

当前代码已经支持：

- 后台设置 `fts_external_*` 全量配置。
- 第三方模式：`primary`、`fallback_on_error`、`fallback_on_error_or_quality`。
- 第三方任务表：`fts_external_jobs`。
- `process/result/error` Kafka 闭环。
- `snapshot_token` 防串结果覆盖。
- 第三方结果落 `content.txt`、`attachments.json`、`diagnostics.json`、`manifest.json`。
- 重新索引时复用 external sidecar。
- 文本质量兜底：乱码、控制字符、可打印字符比例，以及正文中连续或高占比的方框字/豆腐块。
- 加密文件默认不发送第三方。
- 重复 `result/error` 消息具备终态保护，已成功任务不会被迟到 `error` 覆盖。
- 已进入 `error` 终态的请求不会再被迟到 `success` 覆盖。
- 第三方附件树 `parent_id` 支持根节点写法 `file:<file_id>`。

## 2.1 本次真实联调结论

2026-04-02 已在本地完成真实链路验证，确认以下结论成立：

- `process -> result -> sidecar -> ES` 闭环在 `http://localhost:5212` 运行时已跑通。
- 真实运行时消费 `result` 后，`fts_external_jobs.status` 会从 `queued` 进入 `success`。
- 真实运行时会为成功结果补齐 `manifest_path`，例如：
  - `cloudreve/fts-sidecar/1/23/11/manifest.json`
  - `cloudreve/fts-sidecar/1/25/12/manifest.json`
- ES 已能检索到第三方结果写入的文件内容。
- 第三方抽取不会对 `local` 存储策略生效，联调账号必须绑定对象存储策略。

## 3. 依赖准备

需要先保证以下依赖可用：

- PostgreSQL
- Redis
- Elasticsearch
- Tika
- MinIO 或其他对象存储
- Kafka

推荐最小联调矩阵：

- PostgreSQL：保存 `settings / tasks / fts_external_jobs`
- Redis：承载设置缓存，直接改库后需要清理 `setting_*`
- Elasticsearch：索引最终写入目标
- Tika：本地抽取与回退链路
- Kafka：第三方抽取协作总线
- MinIO：本地模拟可被第三方按 `bucket + path` 直接访问的对象存储

本地 Kafka 已验证可用的启动方式如下。

## 4. Kafka 启动命令

已实际联调通过的镜像：`apache/kafka:latest`

启动命令：

```bash
docker rm -f cloudreve-kafka >/dev/null 2>&1 || true

docker run -d --name cloudreve-kafka \
  -p 9092:9092 \
  -e KAFKA_NODE_ID=1 \
  -e KAFKA_PROCESS_ROLES=broker,controller \
  -e KAFKA_LISTENERS=PLAINTEXT://0.0.0.0:9092,CONTROLLER://0.0.0.0:9093 \
  -e KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://127.0.0.1:9092 \
  -e KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=PLAINTEXT:PLAINTEXT,CONTROLLER:PLAINTEXT \
  -e KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER \
  -e KAFKA_INTER_BROKER_LISTENER_NAME=PLAINTEXT \
  -e KAFKA_CONTROLLER_QUORUM_VOTERS=1@127.0.0.1:9093 \
  -e KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1 \
  -e KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1 \
  -e KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1 \
  -e KAFKA_NUM_PARTITIONS=1 \
  -e KAFKA_AUTO_CREATE_TOPICS_ENABLE=true \
  apache/kafka:latest
```

可用性检查：

```bash
docker exec cloudreve-kafka /opt/kafka/bin/kafka-topics.sh \
  --bootstrap-server 127.0.0.1:9092 --list
```

如需查看请求是否真正发出：

```bash
docker exec cloudreve-kafka /opt/kafka/bin/kafka-get-offsets.sh \
  --bootstrap-server 127.0.0.1:9092 \
  --topic process --topic result --topic error
```

## 5. Cloudreve 后台配置项

后台路径：`管理面板 -> 文件系统 -> 全文搜索 -> 第三方抽取协作`

前置条件必须满足：

- 联调用户所在用户组必须绑定对象存储策略，而不是 `local`
- 对象存储桶与对象路径必须能被第三方直接访问
- 如果你是直接改数据库 `settings` 表，而不是从后台保存，必须同步清理 Redis `setting_*` 缓存
- 若旧 ES 索引 mapping 与当前代码不兼容，需要先处理旧索引后再验证

建议默认值：

- `fts_external_enabled = 0`
- `fts_external_mode = fallback_on_error_or_quality`
- `fts_external_use_global_kafka = 1`
- `fts_external_timeout_seconds = 300`
- `fts_external_retry_max = 2`
- `fts_external_recursive_attachments = 1`
- `fts_external_skip_encrypted_files = 1`
- `fts_external_quality_enabled = 1`
- `fts_external_quality_min_text_length = 32`
- `fts_external_quality_max_replacement_ratio = 0.02`
- `fts_external_quality_max_control_char_ratio = 0.01`
- `fts_external_quality_min_printable_ratio = 0.85`
- `fts_external_quality_font_box_min_count = 4`
- `fts_external_quality_font_box_min_run = 3`
- `fts_external_quality_font_box_min_ratio = 0.35`

联调时可先使用如下专用配置：

- `fts_external_enabled = 1`
- `fts_external_mode = primary`
- `fts_external_use_global_kafka = 0`
- `fts_external_kafka_brokers = 127.0.0.1:9092`
- `fts_external_kafka_security_protocol = PLAINTEXT`
- `fts_external_kafka_process_topic = process`
- `fts_external_kafka_result_topic = result`
- `fts_external_kafka_error_topic = error`
- `fts_external_kafka_consumer_group = cloudreve-fts-external`

说明：

- 如果开启“复用全局 Kafka 连接参数”，本页只保留 topic 和 consumer group 独立配置。
- `fts_external_kafka_password` 已加入后台脱敏字段。
- 后台保存 `fts_external_*` 时会触发 `ReloadFTSExternalKafka(...)` 热重载。
- 字体缺失导致的连续方框字/豆腐块会由系统自动识别，适用于中文及其他语种文档，后台可配置数量、连续长度和占比阈值。
- 本地真实联调时，`管理面板 -> 文件系统 -> 全文搜索` 页面已确认可以正常展示“第三方抽取协作”区块。

## 6. 消息格式

### 6.1 process

Cloudreve 发送给第三方：

```json
{
  "version": 1,
  "request_id": "fts-ext-uuid",
  "snapshot_token": "file:101:entity:201:size:4096:updated:2026-04-02T01:23:45Z",
  "file": {
    "file_id": 101,
    "owner_id": 7,
    "entity_id": 201,
    "name": "report.pdf",
    "size": 4096,
    "mime_type": "application/pdf",
    "ext": "pdf"
  },
  "source": {
    "bucket": "cloudreve-test-bucket",
    "path": "tenant-a/u7/report.pdf"
  },
  "options": {
    "recursive_attachments": true
  }
}
```

### 6.2 result

第三方成功回写：

```json
{
  "version": 1,
  "request_id": "fts-ext-uuid",
  "snapshot_token": "file:101:entity:201:size:4096:updated:2026-04-02T01:23:45Z",
  "status": "success",
  "provider": {
    "name": "third-party-extractor",
    "version": "1.0.0"
  },
  "root": {
    "content": "hello from external extractor",
    "metadata": {
      "language": "zh-CN"
    },
    "warnings": [
      "font fallback used"
    ],
    "quality_score": 0.97
  },
  "attachments": [
    {
      "id": "att-1",
      "parent_id": "file:101",
      "depth": 1,
      "type": "attachment",
      "name": "embedded.txt",
      "path": "embedded/embedded.txt",
      "mime_type": "text/plain",
      "size": 128,
      "metadata": {
        "language": "en"
      },
      "content": "embedded content"
    }
  ]
}
```

说明：

- `attachments[].parent_id` 可为空，表示直接挂到文件根节点。
- `attachments[].parent_id` 也可写为 `file:<file_id>`，Cloudreve 会标准化为 ES 中的文件根父节点。

### 6.3 error

第三方失败回写：

```json
{
  "version": 1,
  "request_id": "fts-ext-uuid",
  "snapshot_token": "file:101:entity:201:size:4096:updated:2026-04-02T01:23:45Z",
  "status": "error",
  "stage": "extract",
  "code": "font_missing",
  "message": "font package missing",
  "detail": "Simulated third-party failure",
  "retryable": true,
  "occurred_at": "2026-04-02T01:23:45.123456Z"
}
```

## 7. sidecar 落盘结构

第三方成功后，Cloudreve 会落如下 sidecar：

```text
cloudreve/fts-sidecar/<owner_id>/<file_id>/<entity_id>/
  manifest.json
  content.txt
  attachments.json
  diagnostics.json
```

说明：

- `content.txt`：主文本正文。
- `attachments.json`：附件树平铺结果，使用 `parent_id` 维持层级。
- `diagnostics.json`：provider、warning、quality_score、snapshot_token 等诊断信息。
- `manifest.json`：标记 `provider = external`，供后续重建索引复用。

## 8.1 哪些文件会真正发往第三方

不是所有文件都会发 Kafka，实际要同时满足下面条件：

- 第三方开关已开启
- 当前模式判断本次需要走第三方
- 文件不在回收站
- 文件不是按当前配置应跳过的加密文件
- 文件所在策略满足第三方可读的对象存储前提

其中最容易忽略的一条是最后一条：

- `local` 策略不会发第三方
- 不能稳定给出 `bucket + path` 的策略也不会发第三方

如果联调时一直收不到 `process`，先查测试账号实际绑定的策略，不要只盯后台开关。

## 9. 回退策略

### 8.1 primary

- 先发第三方。
- 第三方超时或失败后，按重试次数继续。
- 重试耗尽后回退到本地 Tika。

### 8.2 fallback_on_error

- 先走本地 Tika。
- 本地抽取失败时转第三方。
- 第三方失败或超时后再回退本地索引流程。

### 8.3 fallback_on_error_or_quality

- 先走本地 Tika。
- 本地失败时转第三方。
- 本地文本质量不达标时转第三方。
- 质量判定规则包括：
  - 文本过短
  - `�` 占比过高
  - 控制字符占比过高
  - 可打印字符占比过低
  - 命中字型异常关键字
  - 正文中出现连续或高占比的方框字/豆腐块

## 10. 联调检查清单

### 9.1 部署检查

- [x] Kafka 容器可访问，`127.0.0.1:9092` 已通。
- [x] Cloudreve 已升级到当前代码并完成数据库迁移。
- [x] `fts_external_jobs` 表已创建。
- [x] 后台页面可见“第三方抽取协作”配置区块。
- [ ] `fts_external_kafka_password` 后台展示为脱敏值。

### 9.2 消息闭环检查

- [x] Cloudreve 能向 `process` 正常投递消息。
- [x] 第三方回写 `result` 后，`fts_external_jobs.status = success`。
- [x] 第三方回写 `error` 后，`fts_external_jobs.status = error`。
- [x] `snapshot_token` 不匹配时，迟到结果不会覆盖新任务。

### 9.3 搜索链路检查

- [x] sidecar 成功落 `content.txt`。
- [x] sidecar 成功落 `attachments.json`。
- [x] sidecar 成功落 `diagnostics.json`。
- [ ] 重建索引时可复用 external sidecar。

### 9.4 策略检查

- [x] `primary` 模式可用。
- [x] `fallback_on_error` 模式可用。
- [x] `fallback_on_error_or_quality` 模式可用。
- [x] 加密文件不会发送第三方。

## 11. 真实环境联调步骤

以下流程是本次已验证通过的一条最小闭环，适合本地或测试环境复现。

### 11.1 准备对象存储策略

关键点：

- 第三方只消费 `bucket + path`
- 因此测试文件必须落在对象存储，而不是 `local`

本地联调时可直接使用 MinIO：

- `endpoint = http://127.0.0.1:9000`
- `access_key = minio`
- `secret_key = minio123456`

桶建议单独创建，例如：

- `cloudreve-external-smoke`

如果只是本地联调，可临时把该桶设为匿名可读，便于 Tika / 第三方模拟服务直接访问对象。

### 11.2 配置 Cloudreve

在后台保存以下最小配置：

- `fts_enabled = 1`
- `fts_index_type = elasticsearch`
- `fts_extractor_type = tika`
- `fts_elasticsearch_endpoint = http://127.0.0.1:9200`
- `fts_tika_endpoint = http://127.0.0.1:9998`
- `fts_external_enabled = 1`
- `fts_external_mode = primary`
- `fts_external_use_global_kafka = 0`
- `fts_external_kafka_brokers = 127.0.0.1:9092`
- `fts_external_kafka_process_topic = process`
- `fts_external_kafka_result_topic = result`
- `fts_external_kafka_error_topic = error`
- `fts_external_kafka_consumer_group = cloudreve-fts-external`

如果你不是通过后台保存，而是直接改库：

1. 更新 `settings` 表
2. 清 Redis `setting_*` 缓存
3. 重启或热重载 Cloudreve

### 11.3 触发一次真实请求

1. 让测试用户绑定对象存储策略
2. 上传一个 `txt/pdf/docx` 文件
3. 查看 `process` topic offset 是否增加
4. 查看数据库是否生成 `fts_external_jobs` 记录

示例查询：

```bash
docker exec postgresql psql -U cloudreve -d cloudreve -Atc \
  "select id,request_id,status,file_id,attempt,manifest_path from fts_external_jobs order by id desc limit 10;"
```

### 11.4 模拟第三方成功回写

向 `result` topic 回写与 `request_id` 对应的成功消息，然后确认：

- `fts_external_jobs.status = success`
- `manifest_path` 被写入
- `tasks` 中对应的 `full_text_index` 任务进入 `completed`
- ES 可以搜到该文件

示例查询：

```bash
docker exec postgresql psql -U cloudreve -d cloudreve -Atc \
  "select id,status,private_state from tasks where type='full_text_index' order by id desc limit 5;"
```

```bash
curl -s 'http://127.0.0.1:9200/cloudreve_files/_search?q=file_id:123'
```

### 11.5 处理旧索引兼容问题

如果 ES 已存在旧 mapping，而当前代码要求新的时间格式或字段类型，常见现象是：

- 后台设置已保存
- Kafka 也回调成功
- 但最终建索引时报 mapping 错误

如果旧索引兼容性不需要保留，最直接的处理方式是删除旧索引后重建。

## 12. 自动化验证命令

默认回归：

```bash
go test ./pkg/setting ./pkg/filemanager/manager -count=1
```

真实 Kafka 集成测试：

```bash
CLOUDREVE_FTS_EXTERNAL_KAFKA_ADDR=127.0.0.1:9092 \
  go test ./pkg/filemanager/manager -run 'TestFTSExternalKafka.*Integration' -count=1 -v
```

以上命令已在本地跑通，覆盖：

- Cloudreve 发布 `process`
- 模拟第三方回写 `result`
- 模拟第三方回写 `error`
- `fts_external_jobs` 状态流转
- 重复 `result/error` 不覆盖既有终态
- `error` 终态后的迟到 `success` 被忽略
- `snapshot_token` 不匹配时忽略迟到结果
- external sidecar 的 `content/attachments/diagnostics/manifest` 序列化与回读
- `fallback_on_error` 与 `fallback_on_error_or_quality` 的任务编排分支
- 文件已删除时，迟到第三方结果只触发旧索引清理，不再重建 sidecar/索引
- 后台保存 `fts_external_*` 设置时会进入 Kafka runtime reload 后置处理

## 13. 故障排查

### 11.1 收不到 process

优先检查：

- `fts_external_enabled` 是否打开。
- 当前模式是否真的会触发第三方。
- 文件是否是加密文件。
- 存储策略是否属于本方案允许的远端对象存储类型。
- 当前测试用户组是否仍绑定 `local` 策略。
- Kafka brokers 和 topic 是否配置正确。
- 如果是直接改数据库配置，Redis `setting_*` 是否仍缓存旧值。

### 11.2 收到 result 但状态不更新

优先检查：

- `request_id` 是否与 `fts_external_jobs` 一致。
- `snapshot_token` 是否与当前任务一致。
- Cloudreve 的 `result/error` consumer group 是否已启动。
- 后台保存配置后是否触发了 Kafka runtime 热重载。
- 当前任务私有状态中的 `external_request_id` 是否已经因为超时重试切到新的请求。

### 11.3 sidecar 落盘后仍不可检索

优先检查：

- 索引器是否可用。
- ES 索引是否启用。
- 当前全文任务是否已经进入最终 `fullTextPerformIndexing`。
- 是否被后续重建任务覆盖。
- ES 是否还保留着旧 mapping，导致当前写入被拒绝。

## 14. 回滚方式

如果第三方链路不稳定，直接关闭：

- `fts_external_enabled = 0`

即可恢复到纯 Tika 模式。

该回滚不要求修改 ES mapping，也不要求删除已落盘 sidecar。
