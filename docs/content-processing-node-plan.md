# 内容处理从节点能力改造方案

本文档用于规划将网盘中的高 IO / 高 CPU / 高网络消耗型“内容处理任务”统一下放到从节点执行。  
当前重点目标包括：

- Tika 全文抽取
- 附件 / 图片 / 嵌入资源递归抽取
- 文档识别与元数据识别
- 缩略图提取
- 媒体元数据提取

本文档基于当前代码现状给出推荐方案，核心抽象为：

```text
NodeCapabilityContentProcessing
```

## 1. 背景与目标

当前系统中：

- 压缩 / 解压已经具备“主站编排 + 从节点执行”的能力
- 远程下载已经具备从节点调度能力
- 缩略图、媒体元数据对远程存储策略已有局部从节点代理能力
- 全文抽取仍然主要在主站执行

这会带来几个明显问题：

1. 大文件全文抽取、递归附件展开、图片抽取、Tika 请求都集中压在主站。
2. 文件在从节点或远端策略上时，主站仍可能承担不必要的数据拉取与中转。
3. 内容处理能力没有统一抽象，后续新增文档识别、OCR、病毒扫描时会继续各自为战。
4. 任务隔离不清，全文、媒体元数据、缩略图等任务容易互相抢占主站资源。

本方案目标：

1. 建立统一的“内容处理从节点能力”抽象。
2. 将重型内容处理任务尽可能下放到从节点。
3. 保持主站作为最终状态收口方，统一负责 PG / ES / 元数据一致性。
4. 为后续扩展内容处理能力预留统一的任务模型。

## 2. 设计原则

### 2.1 主站编排，从节点执行

推荐模式：

- 主站负责：
  - 任务创建
  - 权限校验
  - 文件 / 实体 / owner / policy 上下文解析
  - 选择节点
  - 最终写 PG 元数据
  - 最终写 ES 索引
  - 一致性补偿与重试
- 从节点负责：
  - 读取文件源数据
  - 执行重 CPU / IO / 网络的处理中间步骤
  - 调用本地 Tika / 缩略图 / 媒体元数据工具链
  - 将 sidecar 二进制按存储策略写入目标路径
  - 回传轻量级结果摘要

### 2.2 二进制不上数据库

必须保持当前方向不变：

- 文本 sidecar
- 附件 sidecar
- 图片 sidecar
- rmeta / manifest sidecar

都应按存储策略写路径，不直接写数据库。

数据库中保留：

- sidecar manifest 路径
- sidecar entity 关联
- FTS index 关联
- 必要的任务状态和追踪字段

### 2.3 主站统一收口索引

即使从节点完成内容处理，也不建议把 ES 写入职责完全下放到从节点。

推荐：

- 从节点输出 `content + attachments + manifest metadata + diagnostic summary`
- 主站统一执行：
  - `PatchMetadata`
  - `SearchIndexer.UpsertFile`
  - `DeleteByFileIDs`

这样可以避免：

- 多节点并发更新同一 file 的索引
- 从节点直接持有主站索引写权限带来的边界复杂化
- 索引与文件状态脱节

### 2.4 内容处理能力统一抽象

不建议未来继续按：

- `FullTextNode`
- `ThumbNode`
- `MediaMetaNode`

这种细颗粒度无限扩展。

推荐统一能力：

```text
NodeCapabilityContentProcessing
```

然后在任务层区分：

- `SlaveFullTextExtractTask`
- `SlaveThumbGenerateTask`
- `SlaveMediaMetaTask`
- `SlaveDocumentInspectTask`

## 3. 当前代码现状评估

### 3.1 已有可复用能力

当前已有以下可直接复用的通用框架：

1. 节点分配
   - `pkg/filemanager/workflows/worfklows.go`
   - `allocateNode(...)`
2. 主站编排 + 从节点执行模型
   - `pkg/filemanager/workflows/archive.go`
   - `pkg/filemanager/workflows/extract.go`
3. 从节点任务注册入口
   - `service/node/task.go`
4. 节点能力池
   - `pkg/cluster/pool.go`
5. 远程策略下的从节点接口调用能力
   - `pkg/filemanager/driver/remote/remote.go`

### 3.2 当前不足

当前没有以下关键能力：

1. 没有 `NodeCapabilityContentProcessing`
2. 没有全文抽取从节点任务类型
3. 没有统一的“内容处理”队列
4. 没有节点级 Tika 配置
5. 没有主站与从节点之间用于传递全文抽取摘要的标准 state 结构

备注：

- 上述问题已在当前代码主干实现中完成收口，本文这一节保留为最初评估背景

### 3.3 当前各类任务状态

#### 压缩 / 解压

已具备完整从节点模式，可作为首要参考实现。

#### 缩略图

当前主要是：

- 本地 `ThumbQueue`
- 远程存储策略通过从节点接口按需生成

它更像“远程服务调用”，不是“统一内容处理调度”。

#### 媒体元数据

当前与缩略图相似：

- 原生驱动优先
- 远程策略支持通过从节点提取

但没有统一成从节点编排式任务。

#### 全文抽取

当前几乎全部在主站执行：

- `FullTextIndexTask`
- `performIndexing`
- `buildFTSFileDocument`
- `extractFTSContent`
- `persistFTSSidecars`

都没有走节点能力选择。

## 4. 推荐总体架构

### 4.1 新增节点能力

新增：

```go
NodeCapabilityContentProcessing
```

并纳入：

- `inventory/types/types.go`
- `pkg/cluster/pool.go`
- 节点配置页 / 管理页
- 数据迁移逻辑

建议保留已有能力：

- `CreateArchive`
- `ExtractArchive`
- `RemoteDownload`

不要立即删除或重命名，避免迁移成本过大。

中长期可考虑：

- 压缩 / 解压也逐渐并入 `ContentProcessing`
- 但第一阶段不强行合并

### 4.2 新增任务分层

推荐新增 4 类任务：

1. `FullTextExtractTask`
   - 主站编排任务
2. `SlaveFullTextExtractTask`
   - 从节点执行任务
3. `FullTextFinalizeTask`
   - 主站收口任务，可选
4. `ContentProcessingReconcileTask`
   - 后续统一补偿任务，可选

第一阶段最小实现可以只做：

- 主站 `FullTextIndexTask`
- 从节点 `SlaveFullTextExtractTask`

其中主站 `FullTextIndexTask` 的职责变为：

1. 选择节点
2. 主站直接执行，或者发从节点任务
3. 等待从节点完成
4. 根据从节点回传结果写 PG / ES

### 4.3 从节点产物边界

从节点完成后，建议只回传轻量摘要，不回传大二进制：

推荐回传字段：

- `file_id`
- `entity_id`
- `manifest_path`
- `sidecar_entity_id` 或 sidecar 关联标识
- `content_excerpt`
- `content_hash`
- `content_size`
- `attachments_summary`
- `diagnostics`
- `tika_status`
- `extract_duration`

其中 `attachments_summary` 每项至少包含：

- `id`
- `parent_id`
- `depth`
- `name`
- `kind`
- `path`
- `mime_type`
- `size`
- `content_excerpt`

真正的正文全文可以不通过任务状态传输，只需：

- 写入 sidecar `content.txt`
- 主站最终从 manifest / sidecar 再加载或按返回摘要填充

### 4.4 主站收口职责

主站在从节点完成后负责：

1. 校验文件当前版本是否仍是目标版本
2. 校验文件是否已被删除 / 回收 / 替换版本
3. patch metadata：
   - `FTSSidecarManifestKey`
   - `FTSSidecarEntityIDKey`
   - `FullTextIndexKey`
4. 构建最终 `SearchFileDocument`
5. 写 ES：
   - `content`
   - `attachments`
6. 失败补偿：
   - sidecar 成功、ES 失败时可重试 finalize
   - 文件已消失时删除残留索引

## 5. 推荐阶段计划

## 5.1 第一阶段：建立内容处理节点能力

目标：

- 只搭基础设施，不改全文逻辑

工作项：

1. 新增 `NodeCapabilityContentProcessing`
2. 更新节点池支持
3. 更新节点迁移与默认能力
4. 更新管理端配置与展示
5. 从节点心跳 / 能力上报增加该能力

交付标准：

- 管理后台可配置节点是否支持内容处理
- 主站可按该能力选节点

## 5.2 第二阶段：全文抽取任务下放

目标：

- 将 Tika 重型处理下放到从节点

当前状态：

- 已完成
- 当前实现采用统一 `slave_content_processing` 任务类型，内部通过 `full_text_extract` kind 区分，而不是单独新增独立 slave task type

工作项：

1. 新增 `SlaveFullTextExtractTaskType`
2. `service/node/task.go` 支持创建该任务
3. 定义 `SlaveFullTextExtractTaskState`
4. 主站 `FullTextIndexTask` 改造成：
   - `allocateNode(..., NodeCapabilityContentProcessing)`
   - 主站直跑 / 从节点执行二选一
5. 从节点任务执行：
   - `ExtractFile`
   - `RMetaFile`
   - `Unpack/UnpackAll`
   - `persistFTSSidecars`
6. 主站 finalize：
   - 构建 ES doc
   - patch metadata

交付标准：

- 主站不再直接承担大部分 Tika 重处理
- sidecar 与 ES 最终结果和当前实现一致

## 5.3 第三阶段：缩略图并入统一内容处理能力

目标：

- 把“按需调用从节点接口”升级成“可调度内容处理任务”

当前状态：

- 已完成第一阶段落地
- 当前实现采用统一 `slave_content_processing` 任务类型，内部通过 `thumbnail_generate` kind 区分
- 主站在 `ThumbProxy` 场景下会优先尝试下发到内容处理节点；从节点生成完成后由主站 finalize 缩略图 entity
- 原本的本地 `ThumbQueue` 路径仍保留，作为 master 本地执行 fallback

工作项：

1. 新增 `SlaveThumbGenerateTaskType`
2. 将 `ThumbQueue` 中的重型本地生成场景改造为可选从节点执行
3. 对远程策略保留原有直连接口作为 fallback
4. 将缩略图 sidecar / entity 写入逻辑与当前行为保持一致

交付标准：

- 缩略图既可按需走从节点接口
- 也可在后台任务中统一调度到内容处理节点

## 5.4 第四阶段：媒体元数据并入统一内容处理能力

目标：

- 媒体元数据提取与全文抽取共享调度基础设施

当前状态：

- 已完成
- 当前实现采用统一 `slave_content_processing` 任务类型，内部通过 `media_meta_extract` kind 区分
- 原生驱动能力仍优先，本地 extractor/proxy 场景可下发到内容处理节点执行，主站回写 metadata 并继续触发 FTS 同步

工作项：

1. 新增 `SlaveMediaMetaTaskType`
2. 对本地 proxy extractor 场景支持下放
3. 原生驱动能力继续优先
4. 非原生场景统一走内容处理节点

交付标准：

- 本地 extractor 场景不再占用主站资源

## 5.5 第五阶段：文档识别 / MIME 识别能力下放

目标：

- 后续扩展 Tika 文档识别、分类、能力探测

当前状态：

- 已完成
- 当前实现采用统一 `slave_content_processing` 任务类型，内部通过 `document_inspect` kind 区分
- 上传新版本后可异步触发文档识别任务，主站负责调度/回写，内容处理节点负责执行 Tika `rmeta` 检测
- 当前回写字段包括：
  - `sys:doc_mime`
  - `sys:doc_parser`
  - `sys:doc_language`
  - `sys:doc_title`
  - `sys:doc_author`
  - `sys:doc_entity_id`
- 回写后会继续触发全文索引同步，便于后续在 ES 中利用文档识别结果

工作项：

1. 新增 `SlaveDocumentInspectTaskType`
2. 定义输出：
   - detected mime
   - parser info
   - language
   - title / author / metadata
3. 可用于：
   - 上传后异步识别
   - 文件预处理
   - 智能分类

交付标准：

- 内容处理节点成为统一的文档识别执行平面

## 6. 全文抽取任务详细设计

### 6.1 主站任务拆分建议

建议将当前全文索引拆成两个逻辑阶段：

1. `Extract`
   - 重处理
   - 适合从节点
2. `Finalize`
   - 最终一致性写入
   - 适合主站

最小改造时不一定要拆两个 Task Type，也可以：

- 一个 `FullTextIndexTask`
- 内部 phase 化

推荐 phase：

- `not_started`
- `await_slave_extract`
- `finalize`

### 6.2 SlaveFullTextExtractTask 输入

建议输入内容：

- `file_id`
- `owner_id`
- `entity_id`
- `file_name`
- `source_uri`
- `primary_entity`
- `storage_policy`
- `user_id`
- `extract_options`

`extract_options` 建议包含：

- `extract_text`
- `extract_assets`
- `extract_inline_images`
- `document_enabled`
- `archive_enabled`
- `sidecar_enabled`

### 6.3 SlaveFullTextExtractTask 输出

建议输出内容：

- `manifest_path`
- `manifest_object_count`
- `content_available`
- `attachments_available`
- `attachment_count`
- `diagnostics`
- `warnings`
- `tika_http_status`
- `duration_ms`

如果需要减少主站二次读取，可再补：

- `content_excerpt`
- `attachments_summary`

不建议通过任务状态回传全文正文和完整附件文本，避免状态膨胀。

### 6.4 主站 finalize 逻辑

主站在 finalize 时应执行：

1. 再次加载 file 当前状态
2. 确认 file 未删除、未进回收站、当前 entity 未变化
3. 从 sidecar 加载：
   - `content.txt`
   - `manifest.json`
   - `attachments`
4. 构建 `SearchFileDocument`
5. patch file metadata
6. 写 ES
7. 成功后记录 `FullTextIndexKey`

### 6.5 幂等与重试

必须保证：

- 从节点重复执行同一抽取任务，不会破坏结果
- 主站 finalize 可重试
- ES upsert 可重复
- 同一 file 多次事件触发时，以最新 entity 为准

建议依赖：

- 现有 `FullTextIndexTaskState` 的 merge 能力
- 当前 file latest entity 校验
- sidecar 路径包含 `owner_id/file_id/entity_id`

## 7. 配置设计建议

### 7.1 节点级配置

建议新增节点级配置：

- `content_processing_enabled`
- `content_processing_max_concurrency`
- `content_processing_temp_path`
- `content_processing_tika_endpoint`
- `content_processing_timeout`

如果从节点本地部署 Tika，推荐：

- 默认从节点内部访问 `http://127.0.0.1:9998`

### 7.2 全局配置

保留现有：

- `fts_tika_document_enabled`
- `fts_tika_archive_enabled`
- `fts_tika_sidecar_enabled`
- `fts_tika_sidecar_text_enabled`
- `fts_tika_sidecar_assets_enabled`
- `fts_tika_extract_inline_images`

并新增建议配置：

- `fts_dispatch_to_content_node`
- `fts_content_node_preferred`
- `fts_content_node_fallback_to_master`
- `content_processing_queue_worker_num`

## 8. 队列设计建议

当前全文挂在 `MediaMetadataQueue`，不利于资源隔离。

建议新增独立队列类型：

```text
QueueTypeContentProcessing
```

用于承载：

- 全文抽取
- 文档识别
- 后续 OCR / 病毒扫描 / 内容分类

缩略图和媒体元数据是否立即合并到该队列，可分阶段推进。

推荐第一阶段：

- 全文抽取独立队列
- 缩略图仍保留 `ThumbQueue`
- 媒体元数据仍保留 `MediaMetaQueue`

第二阶段再逐步合流。

## 9. 失败场景与补偿

### 9.1 从节点抽取成功，主站 finalize 失败

处理方式：

- 保留 sidecar
- 主站重试 finalize
- 不重复跑从节点抽取

### 9.2 从节点抽取时文件版本已变化

处理方式：

- 主站 finalize 前重新校验 `entity_id`
- 若 entity 已变化，放弃旧任务结果
- 按新版本重新排队

### 9.3 从节点抽取成功但 sidecar 部分缺失

处理方式：

- 视为抽取失败
- 不 patch metadata
- 不 upsert ES
- 留下任务诊断信息

### 9.4 ES 写入失败

处理方式：

- 不删除 sidecar
- 保留 finalize 重试能力

### 9.5 Tika 返回 422 或归档编码异常

处理方式：

- 从节点写诊断结果
- sidecar 可只写 `rmeta.json` 或 diagnostics
- 主站可写空 content 或直接跳过索引
- 结合文件类型定义是否需要 fallback

## 10. 与现有 sidecar 设计的关系

当前全文 sidecar 设计方向是正确的，不建议推翻。

原因：

1. 已满足“二进制按存储策略落路径”的要求。
2. 已支持 manifest 和层级附件树。
3. 已天然适合主从分离：
   - 从节点负责写文件
   - 主站负责读 manifest 并入库 / 入 ES

因此从节点化改造应尽量复用：

- `persistFTSSidecars`
- `loadFTSContentFromSidecar`
- manifest 结构
- attachments 层级定义

而不是重新定义另一套抽取结果模型。

## 11. 推荐实施顺序

推荐按下面顺序推进：

1. 新增 `NodeCapabilityContentProcessing`
2. 新增节点池与管理端支持
3. 新增 `SlaveFullTextExtractTask`
4. 将 `FullTextIndexTask` phase 化并支持从节点
5. 引入独立 `ContentProcessingQueue`
6. 跑通全文抽取真实链路
7. 再把缩略图迁入统一能力
8. 再把媒体元数据迁入统一能力
9. 最后扩展文档识别 / MIME 识别

截至当前代码状态：

- 1-9 已完成主体实现
- `ContentProcessingQueue` 已落地，现有全文抽取、媒体元数据、文档识别共享同一内容处理队列
- 缩略图仍保留独立 `ThumbQueue` 作为 fallback，本地直跑与统一内容处理节点并存
- 管理后台已支持节点 `content_processing` 能力开关与能力徽标展示
- 管理后台任务列表/详情已支持按内容处理子类型展示、筛选与清理
- 后端任务响应已统一下发 `display_type`，前端不再依赖手工解析 `private_state`
- 从节点 `slave_content_processing` 查询结果已补充 `display_type + summary`
- 主站等待从节点内容处理失败时，会在错误信息中附带结构化诊断上下文，便于线上排障
- 用户任务详情页与管理后台任务详情页已支持展示 `file_id`、`entity_id`、`policy_id`、`manifest_path`、`save_path`、`meta_count` 等结构化诊断字段
- 队列关键日志已补充统一诊断串，日志聚合可直接看到 `task_type`、`display_type` 与结构化 `summary`
- 队列页已联动展示内容处理从节点概况，节点页也已展示内容处理节点总览，便于从“队列负载”和“节点准备度”两个入口排障
- 内容处理队列卡片已能直接提示活跃从节点数与配置 worker 数，并在节点或 worker 配置不足时给出显式风险提示
- 队列页已新增内容处理检查面板，可直接查看活跃/停用节点名单、worker 配置与当前显式风险项，降低线上排障路径长度
- 节点页已支持按 `content_processing` 能力做服务端过滤，可直接切换到“仅内容处理节点”视图进行巡检
- 队列页检查面板已可直接跳转到节点页的内容处理过滤视图，形成从队列风险到节点巡检的排障闭环
- 后台任务列表已支持 `task_type=content_processing` 聚合过滤，可统一查看全文索引、文档识别、媒体元数据和从节点缩略图等内容处理任务
- 队列页检查面板与节点页总览均已补充直达内容处理任务视图的入口，形成“队列风险 -> 节点巡检 -> 任务诊断”的运维闭环

## 12.1 当前剩余增强项

虽然主体链路已经完成，但仍有几类增强项可继续推进：

1. 从节点任务摘要进一步接入更外层诊断入口
   - 当前任务详情页、节点页、队列页与队列日志已可见，后续可继续接入告警输出、专门的节点巡检页
2. 新内容处理子能力接入统一抽象
   - OCR
   - 杀毒扫描
   - 更细粒度的文档结构识别
3. 更系统的真实环境压测与稳定性验证
   - 长时间队列堆积
   - 多节点并发分发
   - 节点失联 / 重试 / 恢复后的最终一致性

## 13. 最终推荐

推荐正式采用：

```text
NodeCapabilityContentProcessing
```

作为未来网盘“内容处理中台能力”的统一入口。

原因：

1. 抽象层次合适，既不太细，也不空泛。
2. 能覆盖当前最重的 Tika 全文抽取场景。
3. 能顺滑纳入缩略图、媒体元数据、文档识别。
4. 与现有压缩 / 解压从节点编排模型兼容。
5. 能明显降低主站在 IO / CPU / 网络上的集中压力。

如果后续按此方案实施，建议优先完成全文抽取从节点化，因为它是当前资源压力最大、收益最直接的一块。
