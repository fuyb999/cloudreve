# legacy_mysql_migrator

用于把旧系统的 MySQL 文件元数据迁移到当前 Cloudreve，并可选把旧 Elasticsearch 里的已抽取全文内容直接同步到新索引。

边界说明：
- 默认只读旧 MySQL；开启 `search_sync.enabled=true` 时还会只读旧 Elasticsearch。
- 默认只写新 Cloudreve 的数据库元数据；开启 `search_sync.enabled=true` 时还会直写新 Elasticsearch 索引。
- 不下载旧文件，也不重新上传文件内容。
- 旧对象在 MinIO / 对象存储里的 key，直接作为新 `entities.source` 写入。
- 个人文件写到对应用户根目录下。
- 公共文件写到 Cloudreve 的隐藏公共根目录下。
- 文件 owner 优先按已同步用户的外部身份或同 ID 回退解析，`migration.user_id_map` 只作为手工覆盖兜底。
- 搜索同步不会改旧 ES，也不会走当前 Cloudreve 的重新抽取 / 重建索引流程。

## 运行方式

```bash
go run ./tools/legacy_mysql_migrator -config ./tools/legacy_mysql_migrator/legacy-mysql-migrator.example.yml
```

建议先把 `migration.dry_run` 设为 `true` 跑一次，确认 owner、path、policy 都对，再改成 `false` 正式执行。

## 配置项

### `source`

- 只在 `migration.skip_metadata_import=false` 时需要
- `driver`: 源数据库驱动，通常是 `mysql`
- `dsn`: 源数据库 DSN，建议带 `parseTime=true`
- `query`: 源查询 SQL，工具只认 alias 后的字段名
- `batch_size`: MySQL 源端分批读取大小，默认 `1000`。大数据量迁移建议保留或调小到 `200~1000`，避免单个长结果集因消费过慢被 MySQL 主动断开并报 `EOF`。开启后不要在 `query` 自己再写 `LIMIT`

### `target`

- `driver`: 目标 Cloudreve 数据库驱动，支持 `mysql` / `postgres` / `sqlite3`
- `dsn`: 目标 Cloudreve 数据库连接串
- `db_type`: Cloudreve 使用的数据库类型，支持 `mysql` / `postgres` / `sqlite`
- `hashid_salt`: 仅用于初始化公共目录服务，建议填当前 Cloudreve 配置中的 salt

### `migration`

- `dry_run`: 只输出计划，不落库
- `continue_on_error`: 单条失败后是否继续
- `skip_existing`: 同路径文件已存在时是否跳过
- `skip_metadata_import`: 跳过 MySQL 元数据导入，只跑 `search_sync`。适合文件已经迁完，只补 ES
- `disable_native_fts_enqueue`: 给导入上下文打一个“不要走当前 Cloudreve 原生全文抽取入队”的开关，供复用上传/导入链路时显式关闭原生 FTS。当前迁移器本身是直接写库，不经过上传管理器，所以默认就不会触发原生 FTS；这个开关主要用于把边界显式化，避免后续代码复用上传链路时误入队
- `user_id_map`: 手工覆盖映射，`旧 owner_id 原始字符串 -> 新 Cloudreve user.id`
- `external_identity_provider`: 如果你先同步了用户并写入 `external_identities`，这里填对应 provider；迁移器会优先用 `owner_external_user_id`，否则退回到原始 `owner_id` 去匹配 `external_identities.external_user_id`
- `external_identity_issuer`: 可选。一个 provider 下如果有多个 issuer，建议填上，避免命中多条身份映射
- `policy_rules`: 按源对象 `bucket/path` 匹配目标策略，按配置顺序命中第一条；适合一个迁移批次里有多个 S3 桶对应不同 Cloudreve 存储策略
- `default_policy_id`: 没命中 `policy_rules` 时的统一默认策略
- `same_id_fallback`: 前面都没命中时，是否允许旧 ID 直接当新 ID 使用。适合你同步用户时保留原始 `user.id`
- `allow_email_lookup`: 前面都没命中时，是否允许用 `owner_email` 去目标库匹配
- `allow_username_lookup`: 前面都没命中时，是否允许用 `owner_username` 去目标库匹配
- `default_public_owner_id`: 公共文件缺少 owner 信息时的兜底归属用户
- `default_personal_policy_id`: 兼容旧配置的个人默认存储策略。不建议新配置继续用
- `default_public_policy_id`: 兼容旧配置的公共默认存储策略。不建议新配置继续用
- `marker_key`: 迁移来源元数据键名，默认 `sys:legacy_mysql_migrator`
- `private_metadata_keys`: 需要按私有元数据写入的键

### `search_sync`

- `enabled`: 是否开启旧 ES -> 新 ES 同步
- `continue_on_error`: 单条 ES 文档失败后是否继续；不填时继承 `migration.continue_on_error`
- `ensure_target_index`: 写入前是否确保目标索引 mapping 已存在，默认 `true`
- `mark_indexed_metadata`: 成功写入 ES 后是否给目标文件补 `sys:fulltext_index`，默认 `true`
- `batch_size`: 旧 ES scroll 批次大小，默认 `500`
- `scroll_keep_alive`: scroll 保活时间，默认 `2m`
- `source.endpoint` / `source.cloud_id`: 旧 ES 连接信息，二选一
- `source.index`: 旧 ES 索引名
- `source.query`: 可选，直接作为旧 ES `query` 子句
- `target.endpoint` / `target.cloud_id`: 新 ES 连接信息，二选一
- `target.index`: 新 ES 索引名
- `match_rules`: 如何把旧 ES 文档关联到新库文件。规则按顺序匹配，命中第一条唯一记录即停止；左边是迁移 marker 字段，右边是旧 ES `_source` 字段路径
- `field_mappings`: 旧 ES `_source` 到新索引文档的字段映射。支持：
  - 简写：`content: content`
  - 根路径：`$.path.to.field`
  - 嵌套对象：`latest_version.mime_type: latest.mime`
  - 数组对象重命名：见示例里的 `attachments`；`attachments` 内部字段也会按 `fields` 逐个映射
- `defaults`: 可选，给目标 ES 文档补默认值

搜索同步时，`file_id / owner_id / entity_id / tree_path / storage_policy_id` 等结构字段始终以新库里的文件记录为准，只把旧 ES 里已经抽取好的文本类字段按配置覆盖进去。

## 源查询必须提供的字段

### 必填

- `scope`: `personal` 或 `public`
- `kind`: `file` / `folder`
  或者提供 `is_dir`: `1/0`、`true/false`
- `target_path`: 目标相对路径，不要带用户根目录前缀

### 文件行必填

- `object_key` 或 `object_path`: 对象存储中的现有 key

### 强烈建议提供

- `legacy_id`: 旧系统主键，便于追溯
- `owner_id`: 旧系统用户 ID。工具会按原始 long 字符串保留，不需要在 SQL 里 `CAST`
- `owner_external_user_id`: 可选。旧系统外部用户标识；如果你同步用户时把旧 ID 写到了 `external_identities.external_user_id`，可以直接给，不给也会自动回退到 `owner_id`
- `bucket`: 源对象所在桶名，用于命中 `migration.policy_rules`
- `object_path`: 对象在桶内的 key；迁移器会把它写入新 `entities.source`
- `owner_email`: 映射兜底
- `owner_username`: 映射兜底
- `size`: 文件大小
- `created_at`
- `updated_at`
- `metadata_json`: 可选 JSON 对象，例如 `{"tag":"合同","source":"legacy"}`；如果源表没有这个字段可以完全不提供

## `target_path` 规则

`target_path` 是相对路径：

- 个人文件：相对于用户根目录
- 公共文件：相对于 Cloudreve 公共目录

示例：

- 个人文件 `docs/2024/report.pdf`
- 个人文件夹 `docs/2024`
- 公共文件 `共享资料/制度/制度汇编.pdf`

不要传：

- `/` 开头的绝对系统路径
- `../` 这样的上跳路径
- 带用户根目录 hash 或 `/public` 前缀的路径

## `owner_id` 对齐策略

处理顺序如下：

1. `migration.user_id_map[owner_id]`
2. 配置了 `migration.external_identity_provider` 时，优先用 `owner_external_user_id`，否则退回原始 `owner_id` 去匹配 `external_identities.external_user_id`
3. `same_id_fallback=true` 时，直接尝试新库同 ID 用户
4. `allow_email_lookup=true` 时，用 `owner_email` 匹配
5. `allow_username_lookup=true` 时，用 `owner_username` 匹配
6. 公共文件再看 `default_public_owner_id`

## 存储策略解析

处理顺序如下：

1. 源查询里显式给了 `storage_policy_id`
2. 按 `migration.policy_rules` 顺序匹配 `bucket` 和可选 `path_prefix`
3. `migration.default_policy_id`
4. 兼容旧配置时再回退到 `default_personal_policy_id` / `default_public_policy_id`

建议：

- 如果你会先同步用户，优先把旧用户标识写进 `external_identities.external_user_id`，这样文件迁移不需要维护一大份 `user_id_map`
- 如果同步用户时保留原始 `user.id`，可以直接开 `same_id_fallback=true`
- 邮箱/用户名兜底只作为补漏，不要反过来当主路径

## 行为细节

- 找不到个人用户会直接失败。
- 找不到公共 owner 时，如果配置了 `default_public_owner_id`，会挂到该用户下。
- 目标用户根目录不存在时，工具会自动创建。
- 公共隐藏根目录不存在时，工具会自动创建。
- 同路径文件已存在时：
  - `skip_existing=true` 则跳过
  - `skip_existing=false` 则报错停止或按 `continue_on_error` 继续
- 目录如果已经存在，不重复创建；显式目录行会补写元数据与时间戳。
- 每个迁移出的文件/目录都会写入一条私有元数据，记录旧系统来源信息。

## 一个实际查询模板

```sql
SELECT
  f.id AS legacy_id,
  CASE WHEN f.scope = 'public' THEN 'public' ELSE 'personal' END AS scope,
  f.owner_id AS owner_id,
  f.bucket AS bucket,
  f.object_path AS object_path,
  u.email AS owner_email,
  u.username AS owner_username,
  CASE WHEN f.is_dir = 1 THEN 'folder' ELSE 'file' END AS kind,
  f.relative_path AS target_path,
  f.size_bytes AS size,
  f.created_at AS created_at,
  f.updated_at AS updated_at
FROM old_files f
LEFT JOIN old_users u ON u.id = f.owner_id
ORDER BY f.relative_path, f.id;
```

## 注意事项

- 大量数据从 MySQL 迁移时，工具默认按 `source.batch_size` 分批拉取并在每批读完后关闭源 `rows`，避免长时间占用一个结果集触发 MySQL `net_write_timeout` 一类的主动断连。
- 如果源库仍然容易断连，除了减小 `source.batch_size`，也建议在 DSN 上显式带 `timeout` / `readTimeout` / `writeTimeout`，并检查源 MySQL 的 `net_write_timeout`。
- 旧 ES 同步依赖 MySQL 迁移阶段写入的 `migration.marker_key` 私有元数据，所以要么同一次运行里先迁元数据再同步 ES，要么在历史迁移数据已经带 marker 的前提下单独开 `skip_metadata_import=true` 补跑。
- 旧 ES 同步是直接搬运旧索引里的已抽取文本，不会重新读取文件内容，也不会走当前 Cloudreve 的全文抽取逻辑。
- 目标存储策略必须能读取到 `object_key` 对应的对象；否则元数据迁过去后文件仍然不可读。
- 如果旧系统公共目录下允许不同 owner 混用同一路径，本工具会按最终文件行修正文件 owner；目录 owner 以最后一个显式目录行为准。
- 如果你还需要迁移分享、标签、回收站、版本历史，这个工具不负责，需要单独补迁移器。
