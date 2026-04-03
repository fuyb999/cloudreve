# 全文抽取第三方 Kafka 协作接入方案与实施清单

本文档用于规划在当前 `Cloudreve + Tika + Elasticsearch` 全文搜索链路上，接入一个基于 Kafka 协作的第三方抽取服务。目标是在不打乱现有索引、sidecar、任务编排边界的前提下，支持第三方抽取增强、失败兜底和质量兜底。

本文档基于当前工作区代码现状整理，重点覆盖：

- 接入边界与职责划分
- Kafka 消息协议
- 后台配置与前端表单
- 任务状态机与数据表
- sidecar 持久化方案
- 代码改动清单
- 联调、验收与上线清单

## 1. 目标与范围

本次改造目标：

1. 在现有 Tika 抽取链路上引入第三方抽取能力。
2. 支持按配置决定第三方介入程度，而不是强制替换 Tika。
3. 通过 Kafka 与第三方服务协作，Cloudreve 发起抽取请求并消费成功/失败结果。
4. 将第三方成功结果统一落为 sidecar，再复用现有 ES 索引流程。
5. 在后台界面中配置第三方抽取开关、模式、Kafka 主题和质量兜底规则。

本次明确不做的内容：

1. 不把第三方抽取实现为新的 `searcher.TextExtractor`。
2. 不让第三方直接写 ES。
3. 不在 Kafka 中传输对象存储 `AK/SK`、签名 URL、临时 token。
4. 不在 Cloudreve 中保存第三方对象存储访问密钥。

## 2. 当前代码边界

### 2.1 已有全文抽取与索引链路

当前相关核心入口：

- `application/dependency/dependency.go`
  - `TextExtractor(ctx)` 目前只支持 `tika` 和 `noop`
- `pkg/filemanager/manager/fulltextindex.go`
  - `FullTextIndexTask`
  - `performIndexing`
  - 已有 `await_slave_extract` 异步阶段
- `pkg/filemanager/manager/fulltextsnapshot.go`
  - `BuildFTSFileDocumentWithOptions`
  - `extractFTSContent`
  - `loadFTSContentFromSidecar`
- `pkg/filemanager/manager/fulltextsidecar.go`
  - `persistFTSSidecarsToHandler`
  - 现有 Tika sidecar 落盘与回读逻辑
- `pkg/searcher/indexer.go`
  - `SearchFileDocument`
  - `SearchAttachmentDocument`

### 2.2 当前边界判断

当前代码已经表明：

1. `TextExtractor` 是同步接口，天然不适合 Kafka 异步抽取。
2. 全文抽取真正的收口点在 `fulltextindex + fulltextsnapshot + fulltextsidecar`。
3. ES 结构已经稳定，第三方结果应该被标准化后再进入 ES。
4. Cloudreve 已经内置 Kafka client，可以复用，不需要重新引入一套 Kafka SDK。
5. 消息回写必须具备终态保护，重复或乱序 `result/error` 不能破坏已完成任务。
6. 第三方抽取只适用于能够稳定提供 `bucket + path` 的对象存储策略，`local` 等策略不应误发。

结论：

- 第三方抽取应该被建模为“全文任务中的异步 provider”
- 不应该被建模为“另一个同步 Extractor”

## 3. 总体设计

### 3.1 角色划分

- `Tika`
  - 现有本地同步抽取器
  - 负责默认抽取和本地兜底
- `Third-party Extractor`
  - 通过 Kafka 协作的第三方异步抽取服务
  - 负责增强抽取、异常文件抽取、质量兜底
- `Cloudreve FullText Orchestrator`
  - 判断本次应走 Tika、本地回退还是第三方
  - 负责发 Kafka 请求、等待结果、写 sidecar、写索引
- `Elasticsearch`
  - 继续只接收 `SearchFileDocument`
- `Sidecar`
  - 继续作为全文抽取结果的标准持久化格式

### 3.2 推荐介入模式

推荐保留以下 4 种模式：

1. `disabled`
   - 关闭第三方，只走 Tika
2. `primary`
   - 优先第三方，失败或超时回退 Tika
3. `fallback_on_error`
   - Tika 失败时才走第三方
4. `fallback_on_error_or_quality`
   - Tika 失败或质量异常时走第三方

不建议继续拆更多模式，否则前后端和任务状态会变得不稳定。

### 3.3 对象存储访问边界

第三方服务访问对象存储的前提明确如下：

1. Kafka `process` 消息中只包含 `bucket` 与对象路径 `path`
2. 第三方访问对象存储所需的 `endpoint / access key / secret key / region` 不通过 Cloudreve 下发
3. 这些访问凭据由运维或平台方提前告知第三方提供方，或由第三方在自身配置中心维护

这样做的好处：

1. Cloudreve 不承担第三方对象存储凭据托管职责
2. Kafka 协议更稳定
3. 减少敏感信息暴露面

补充边界：

1. 后台开关打开不等于一定会发 `process`
2. 文件所在策略必须是第三方可直接访问的对象存储
3. 本地联调若测试账号还绑定 `local` 策略，将不会触发第三方 Kafka 抽取

### 3.4 加密文件边界

对于应用层加密文件，默认策略应为：

- 不发第三方
- 直接走本地 Tika 或保留失败

原因：

- 第三方仅凭 `bucket + path` 无法得到明文
- 如果要支持，需要单独设计“解密后临时对象”方案，不应混入本期

## 4. Kafka 协议

## 4.1 Topic 约定

- 请求队列：`process`
- 成功队列：`result`
- 失败队列：`error`

后台允许自定义这三个 topic 名称。

### 4.2 process 消息

`process` 只负责告诉第三方“抽哪个文件”，不负责传凭据。

建议格式：

```json
{
  "version": 1,
  "request_id": "fts-9f4d5c9d",
  "snapshot_token": "file:123:entity:456:size:1048576:updated:2026-04-01T23:10:00+08:00",
  "file": {
    "file_id": 123,
    "owner_id": 7,
    "entity_id": 456,
    "name": "合同.pdf",
    "size": 1048576,
    "mime_type": "application/pdf",
    "ext": "pdf"
  },
  "source": {
    "bucket": "cloudreve",
    "path": "uploads/7/2026/04/contract.pdf"
  },
  "options": {
    "recursive_attachments": true
  }
}
```

说明：

- `request_id`
  - 本次外部抽取请求的幂等主键
- `snapshot_token`
  - 用于防止文件更新后旧结果回写
- `bucket`
  - 对象存储桶
- `path`
  - 对象在桶中的路径

如未来出现多套对象存储，可扩展：

```json
"source": {
  "storage_code": "minio-main",
  "bucket": "cloudreve",
  "path": "uploads/7/2026/04/contract.pdf"
}
```

### 4.3 result 消息

第三方成功结果必须对齐当前 ES 附件结构的平铺树模型。

建议格式：

```json
{
  "version": 1,
  "request_id": "fts-9f4d5c9d",
  "snapshot_token": "file:123:entity:456:size:1048576:updated:2026-04-01T23:10:00+08:00",
  "status": "success",
  "provider": {
    "name": "vendor-x",
    "version": "1.2.3"
  },
  "root": {
    "content": "正文内容",
    "metadata": {
      "title": "合同"
    },
    "warnings": [
      "font_missing"
    ],
    "quality_score": 0.96
  },
  "attachments": [
    {
      "id": "att-1",
      "parent_id": "",
      "depth": 1,
      "type": "embedded",
      "name": "附件1.docx",
      "path": "attachments/附件1.docx",
      "mime_type": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
      "size": 20480,
      "metadata": {
        "embedded_path": "附件1.docx"
      },
      "content": "附件正文"
    }
  ]
}
```

字段约束：

1. `attachments` 必须是平铺树
2. 父子关系通过 `parent_id` 区分
3. `content` 可为空，但不能所有节点都为空
4. `path` 由第三方定义为逻辑路径，不要求必须是对象存储路径

### 4.4 error 消息

建议格式：

```json
{
  "version": 1,
  "request_id": "fts-9f4d5c9d",
  "snapshot_token": "file:123:entity:456:size:1048576:updated:2026-04-01T23:10:00+08:00",
  "status": "error",
  "stage": "extract",
  "code": "FONT_MISSING",
  "message": "missing fonts",
  "detail": "pdf parser fallback failed",
  "retryable": false,
  "occurred_at": "2026-04-01T23:12:00+08:00"
}
```

### 4.5 结果体积边界

需要提前约束第三方结果体积：

1. 若 `result` 非常大，Kafka 单条消息可能超过 broker 限制
2. 当前一期先按“中小型结果直接回 Kafka”设计
3. 二期预留 `object_ref` 模式
   - 大结果先写对象存储
   - Kafka 只回一个结果对象地址

本期建议先在文档中保留该风险，不纳入首批开发范围。

## 5. 配置模型

### 5.1 默认配置键

建议在 `inventory/setting.go` 的现有 `fts_*` 默认配置后新增：

```text
fts_external_enabled = "0"
fts_external_mode = "fallback_on_error_or_quality"
fts_external_use_global_kafka = "1"
fts_external_kafka_brokers = ""
fts_external_kafka_security_protocol = "PLAINTEXT"
fts_external_kafka_sasl_mechanism = "PLAIN"
fts_external_kafka_username = ""
fts_external_kafka_password = ""
fts_external_kafka_tls_skip_verify = "0"
fts_external_kafka_process_topic = "process"
fts_external_kafka_result_topic = "result"
fts_external_kafka_error_topic = "error"
fts_external_kafka_consumer_group = "cloudreve-fts-external"
fts_external_timeout_seconds = "300"
fts_external_retry_max = "2"
fts_external_quality_enabled = "1"
fts_external_quality_min_text_length = "32"
fts_external_quality_max_replacement_ratio = "0.02"
fts_external_quality_max_control_char_ratio = "0.01"
fts_external_quality_min_printable_ratio = "0.85"
fts_external_quality_font_box_min_count = "4"
fts_external_quality_font_box_min_run = "3"
fts_external_quality_font_box_min_ratio = "0.35"
fts_external_recursive_attachments = "1"
fts_external_skip_encrypted_files = "1"
```

其中：

- `fts_external_kafka_password` 需要加入 `RedactedSettings`
- 如果启用“使用全局 Kafka 配置”，则后台设置中的 brokers 等字段仅作覆盖用途

### 5.2 Go 配置结构

建议在 `pkg/setting/types.go` 新增：

```go
type FTSExternalMode string

const (
    FTSExternalModeDisabled                 FTSExternalMode = "disabled"
    FTSExternalModePrimary                  FTSExternalMode = "primary"
    FTSExternalModeFallbackOnError          FTSExternalMode = "fallback_on_error"
    FTSExternalModeFallbackOnErrorOrQuality FTSExternalMode = "fallback_on_error_or_quality"
)

type FTSExternalKafkaSetting struct {
    UseGlobalKafka   bool
    Brokers          []string
    SecurityProtocol string
    SASLMechanism    string
    Username         string
    Password         string
    TLSSkipVerify    bool
    ProcessTopic     string
    ResultTopic      string
    ErrorTopic       string
    ConsumerGroup    string
}

type FTSExternalQualitySetting struct {
    Enabled             bool
    MinTextLength       int
    MaxReplacementRatio float64
    MaxControlCharRatio float64
    MinPrintableRatio   float64
    FontBoxMinCount     int
    FontBoxMinRun       int
    FontBoxMinRatio     float64
}

type FTSExternalExtractorSetting struct {
    Enabled              bool
    Mode                 FTSExternalMode
    TimeoutSeconds       int
    RetryMax             int
    RecursiveAttachments bool
    SkipEncryptedFiles   bool
    Kafka                FTSExternalKafkaSetting
    Quality              FTSExternalQualitySetting
}
```

并在 `pkg/setting/provider.go` 增加：

```go
FTSExternalExtractor(ctx context.Context) *FTSExternalExtractorSetting
```

## 6. 后台界面设计

### 6.1 入口位置

直接放在现有全文搜索设置页中，不单独拆页：

- `assets/src/component/Admin/FileSystem/Filesystem.tsx`
- `assets/src/component/Admin/FileSystem/FullTextSearch/FullTextSearchSetting.tsx`

### 6.2 表单分组

建议新增分组：

#### 第三方抽取

- 启用第三方抽取
- 第三方介入模式
- 请求超时（秒）
- 失败最大重试次数
- 允许递归抽取附件
- 加密文件跳过第三方

#### Kafka

- 使用全局 Kafka 配置
- Broker 地址
- 安全协议
- SASL 机制
- 用户名
- 密码
- 跳过 TLS 校验
- 请求 Topic
- 成功 Topic
- 失败 Topic
- 消费组

#### 质量兜底

- 启用质量检测
- 最小正文长度
- 最大替换字符占比
- 最大控制字符占比
- 最小可打印字符占比
- 缺字体方框字/豆腐块阈值

### 6.3 模式中文文案建议

- `关闭`
  - 只使用 Tika
- `优先第三方，失败回退 Tika`
  - 先发 Kafka，第三方失败或超时后改走本地
- `Tika 失败时使用第三方`
  - 仅在 Tika 抛错、超时、空结果时发第三方
- `Tika 失败或质量异常时使用第三方`
  - 在失败、乱码、缺字体、方框字/豆腐块、文本异常短等场景发第三方

## 7. sidecar 方案

### 7.1 设计原则

第三方抽取结果不要伪装成 Tika 的 `rmeta.json`。推荐扩展为 provider-neutral sidecar 格式：

```text
cloudreve/fts-sidecar/{owner_id}/{file_id}/{entity_id}/
  manifest.json
  content.txt
  attachments.json
  diagnostics.json
```

### 7.2 文件含义

- `manifest.json`
  - sidecar 版本
  - provider 类型
  - request_id
  - snapshot_token
  - 对象列表
  - 生成时间
- `content.txt`
  - 主文档正文
- `attachments.json`
  - 第三方平铺附件树
- `diagnostics.json`
  - warnings
  - quality 报告
  - provider 版本
  - 外部错误摘要

### 7.3 兼容策略

`fulltextsnapshot.go` 中读取 sidecar 时：

1. 先尝试读取第三方 `attachments.json`
2. 读不到时，继续按当前逻辑走 `rmeta.json + 归档附件清单`

这样可以：

1. 保持历史 Tika sidecar 完全兼容
2. 第三方不需要伪造 Tika 输出
3. 现有 ES 构建逻辑只需要加一层标准化

## 8. 任务编排与状态机

### 8.1 为什么不放到 TextExtractor

原因：

1. `searcher.TextExtractor` 是同步接口
2. Kafka 抽取是异步模型
3. 第三方结果需要等待、超时、重试、幂等和延迟结果处理
4. 这些能力属于任务编排层，不属于 Extractor 抽象

### 8.2 推荐状态

在 `FullTextIndexTask` 中新增外部阶段，推荐状态：

- `pending`
- `tika_extracting`
- `quality_rejected`
- `third_party_queued`
- `await_external_extract`
- `third_party_succeeded`
- `third_party_failed`
- `fallback_to_tika`
- `indexed`
- `failed`

### 8.3 推荐流程

#### 模式一：disabled

1. 直接走当前 Tika
2. 结果落 sidecar
3. 写 ES

#### 模式二：primary

1. 发布 `process`
2. 创建外部抽取作业
3. 任务进入 `await_external_extract`
4. 收到 `result`
   - 落 third-party sidecar
   - 组装 `SearchFileDocument`
   - 写 ES
5. 收到 `error` 或超时
   - 回退本地 Tika

#### 模式三：fallback_on_error

1. 先跑 Tika
2. Tika 失败时发布 `process`
3. 等待第三方结果
4. 第三方成功则落 sidecar 再索引
5. 第三方失败则任务失败

#### 模式四：fallback_on_error_or_quality

1. 先跑 Tika
2. 若 Tika 失败，转第三方
3. 若 Tika 成功但质量检查失败，转第三方
4. 第三方成功则以第三方结果为准
5. 第三方失败则保留失败或按配置回退 Tika

## 9. 质量判定

### 9.1 判定目标

第三方兜底除了“失败”之外，还要覆盖：

- 抽取乱码
- 替换字符过多
- 缺字体导致文本缺失
- 解析成功但正文极短

### 9.2 推荐规则

建议新增本地质量规则引擎，支持：

1. 文本为空
2. 文本长度低于阈值
3. `�` 占比过高
4. 控制字符占比过高
5. 可打印字符占比过低
6. 命中缺字体方框字/豆腐块阈值

### 9.3 规则位置

新增：

- `pkg/filemanager/manager/fulltextexternal_quality.go`

负责：

- 输入 Tika 抽取结果
- 输出质量报告
- 决定是否触发第三方兜底

## 10. 数据表设计

### 10.1 建议新增表

建议新增：

```text
fts_external_jobs
```

### 10.2 建议字段

- `id`
- `request_id`
- `file_id`
- `owner_id`
- `entity_id`
- `snapshot_token`
- `status`
- `mode`
- `trigger_reason`
- `attempt`
- `manifest_path`
- `quality_report`
- `error_code`
- `error_message`
- `requested_at`
- `deadline_at`
- `completed_at`
- `created_at`
- `updated_at`

### 10.3 推荐索引

- `UNIQUE(request_id)`
- `INDEX(file_id, entity_id)`
- `INDEX(status, deadline_at)`

### 10.4 用途

该表用于：

1. Kafka 请求幂等
2. 任务恢复与轮询
3. 迟到结果识别
4. 审计本次为什么走第三方
5. 失败重试与超时处理

## 11. Kafka Consumer 设计

### 11.1 复用现有 Kafka client

当前已有：

- `pkg/kafka/client.go`
- `dependency.KafkaClient()`

因此不应重新引入新的 Kafka 客户端层。

### 11.2 Consumer 职责

新增第三方抽取消费者时，其 handler 只负责：

1. 解析 `result/error`
2. 校验 `request_id`
3. 校验 `snapshot_token`
4. 更新 `fts_external_jobs`
5. 对成功结果落 sidecar

不建议在 consumer 中直接写 ES，避免和全文任务状态机冲突。

### 11.3 注册建议

新增一个统一注册入口，例如：

- `pkg/filemanager/manager/fulltextexternal_consumer.go`

在应用启动后注册：

- `result` consumer
- `error` consumer

## 12. 代码改动清单

### 12.1 后端配置

- `inventory/setting.go`
  - 增加默认设置
  - 将 `fts_external_kafka_password` 纳入脱敏字段
- `pkg/setting/types.go`
  - 增加第三方抽取配置结构
- `pkg/setting/provider.go`
  - 增加 `FTSExternalExtractor(ctx)`

### 12.2 全文任务编排

- `pkg/filemanager/manager/fulltextindex.go`
  - 新增外部抽取阶段
  - 新增等待第三方结果逻辑
  - 新增超时、重试、回退逻辑

### 12.3 sidecar 与快照

- `pkg/filemanager/manager/fulltextsidecar.go`
  - 新增第三方 sidecar 落盘入口
  - 新增 `attachments.json` / `diagnostics.json` 支持
- `pkg/filemanager/manager/fulltextsnapshot.go`
  - 新增第三方 sidecar 回读逻辑
  - 将第三方附件树转换为 `SearchAttachmentDocument`

### 12.4 Kafka 协作

新增文件建议：

- `pkg/filemanager/manager/fulltextexternal.go`
- `pkg/filemanager/manager/fulltextexternal_kafka.go`
- `pkg/filemanager/manager/fulltextexternal_consumer.go`
- `pkg/filemanager/manager/fulltextexternal_quality.go`
- `pkg/filemanager/manager/fulltextexternal_sidecar.go`

### 12.5 后台保存后处理

- `service/admin/site.go`
  - 配置保存后 reload 第三方抽取配置
  - 如启用了单独 Kafka 覆盖配置，处理对应 post processor

### 12.6 前端设置页

- `assets/src/component/Admin/FileSystem/Filesystem.tsx`
  - 将新增设置键加入 `SettingsWrapper`
- `assets/src/component/Admin/FileSystem/FullTextSearch/FullTextSearchSetting.tsx`
  - 新增第三方抽取表单
- `assets/public/locales/zh-CN/dashboard.json`
  - 新增中文文案
- 其他语言包
  - 补齐占位翻译

### 12.7 数据层

新增：

- `ent/schema`
  - 新增 `FTSExternalJob` schema
- 对应 migration
- repository / client 查询方法

## 13. 开发实施清单

### 13.1 第一阶段：配置与数据结构

- [x] 新增 `fts_external_*` 默认设置
- [x] 新增 `FTSExternalExtractorSetting`
- [x] 新增后台设置 provider
- [x] 新增后台设置页字段
- [x] 新增 `fts_external_jobs` 表与 ent schema

### 13.2 第二阶段：Kafka 发布与消费

- [x] 封装第三方抽取请求发布器
- [x] 封装 `result` 消费者
- [x] 封装 `error` 消费者
- [x] 支持 `request_id` 幂等
- [x] 支持 `snapshot_token` 校验

### 13.3 第三阶段：任务编排

- [x] 在 `FullTextIndexTask` 中引入 `await_external_extract`
- [x] 按模式接入 `primary / fallback_on_error / fallback_on_error_or_quality`
- [x] 实现超时回退
- [x] 实现失败重试

### 13.4 第四阶段：sidecar 接入

- [x] 第三方成功结果落 `content.txt`
- [x] 第三方成功结果落 `attachments.json`
- [x] 第三方成功结果落 `diagnostics.json`
- [x] 兼容旧 Tika sidecar 回读

### 13.5 第五阶段：质量兜底

- [x] 实现质量规则引擎
- [x] 将 Tika 结果接入质量判定
- [x] 在 `fallback_on_error_or_quality` 模式启用

### 13.6 第六阶段：验收与回归

- [x] 单文件抽取成功
- [ ] 递归附件树抽取成功
- [ ] 第三方失败时回退 Tika
- [ ] Tika 乱码时回退第三方
- [x] 文件更新后旧结果不覆盖
- [x] 文件删除后迟到结果被丢弃
- [x] 加密文件不会发送到第三方

## 14. 测试与联调清单

### 14.1 单元测试

- [x] 配置解析测试
- [x] 质量规则测试
- [x] `snapshot_token` 校验测试
- [x] sidecar 序列化与回读测试
- [x] 结果标准化测试

### 14.2 集成测试

- [x] Cloudreve 发布 `process`
- [x] 模拟第三方返回 `result`
- [x] 模拟第三方返回 `error`
- [ ] 超时未返回
- [x] 重复 `result` 消息
- [x] 重复 `error` 消息
- [x] 迟到结果

### 14.3 真实浏览器验收

后台全文设置页：

- [ ] 能保存第三方抽取配置
- [ ] 模式切换正常
- [x] 文案清晰
- [x] 密码字段不回显

文件搜索链路：

- [ ] 正常文档可检索
- [ ] 递归附件内容可检索
- [ ] 质量异常文档走第三方后可检索

## 15. 上线注意事项

### 15.1 默认策略

建议默认：

- `fts_external_enabled = 0`
- `fts_external_mode = fallback_on_error_or_quality`
- `fts_external_skip_encrypted_files = 1`

这样在未配置第三方前不会影响当前 Tika 逻辑。

### 15.2 回滚策略

如联调不稳定，直接回滚为：

- 关闭 `fts_external_enabled`

即可恢复当前纯 Tika 模式，无需改 ES 结构。

### 15.3 兼容性说明

本方案对当前：

- `SearchIndexer`
- `SearchFileDocument`
- `SearchAttachmentDocument`
- Tika sidecar
- 现有 ES mapping

都不要求破坏式修改。

## 16. 推荐实施顺序

推荐按以下顺序落地：

1. 配置模型与后台表单
2. `fts_external_jobs` 数据表
3. Kafka 发布与消费
4. 任务状态机
5. third-party sidecar
6. 质量规则
7. 联调与回归

该顺序的优点：

1. 每一步都可独立验证
2. 出问题时容易定位在“配置、消息、编排、sidecar、索引”中的哪一层
3. 可以在不影响现有 Tika 的前提下逐步上线
