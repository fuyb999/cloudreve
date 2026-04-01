# legacy_mysql_migrator

用于把旧系统的 MySQL 文件元数据迁移到当前 Cloudreve。

边界说明：
- 只读旧 MySQL。
- 只写新 Cloudreve 的数据库元数据。
- 不下载旧文件，也不重新上传文件内容。
- 旧对象在 MinIO / 对象存储里的 key，直接作为新 `entities.source` 写入。
- 个人文件写到对应用户根目录下。
- 公共文件写到 Cloudreve 的隐藏公共根目录下。
- `owner_id` 以新系统用户为准，优先通过 `migration.user_id_map` 显式映射。

## 运行方式

```bash
go run ./tools/legacy_mysql_migrator -config ./tools/legacy_mysql_migrator/legacy-mysql-migrator.example.yml
```

建议先把 `migration.dry_run` 设为 `true` 跑一次，确认 owner、path、policy 都对，再改成 `false` 正式执行。

## 配置项

### `source`

- `driver`: 源数据库驱动，通常是 `mysql`
- `dsn`: 源数据库 DSN，建议带 `parseTime=true`
- `query`: 源查询 SQL，工具只认 alias 后的字段名

### `target`

- `driver`: 目标 Cloudreve 数据库驱动，支持 `mysql` / `postgres` / `sqlite3`
- `dsn`: 目标 Cloudreve 数据库连接串
- `db_type`: Cloudreve 使用的数据库类型，支持 `mysql` / `postgres` / `sqlite`
- `hashid_salt`: 仅用于初始化公共目录服务，建议填当前 Cloudreve 配置中的 salt

### `migration`

- `dry_run`: 只输出计划，不落库
- `continue_on_error`: 单条失败后是否继续
- `skip_existing`: 同路径文件已存在时是否跳过
- `user_id_map`: 旧系统 `owner_id -> 新 Cloudreve user.id`
- `same_id_fallback`: `user_id_map` 没命中时，是否允许旧 ID 直接当新 ID 使用
- `allow_email_lookup`: `user_id_map` 没命中时，是否允许用 `owner_email` 去目标库匹配
- `allow_username_lookup`: `user_id_map` 没命中时，是否允许用 `owner_username` 去目标库匹配
- `default_public_owner_id`: 公共文件缺少 owner 信息时的兜底归属用户
- `default_personal_policy_id`: 个人文件默认存储策略
- `default_public_policy_id`: 公共文件默认存储策略
- `marker_key`: 迁移来源元数据键名，默认 `sys:legacy_mysql_migrator`
- `private_metadata_keys`: 需要按私有元数据写入的键

## 源查询必须提供的字段

### 必填

- `scope`: `personal` 或 `public`
- `kind`: `file` / `folder`
  或者提供 `is_dir`: `1/0`、`true/false`
- `target_path`: 目标相对路径，不要带用户根目录前缀

### 文件行必填

- `object_key`: 对象存储中的现有 key

### 强烈建议提供

- `legacy_id`: 旧系统主键，便于追溯
- `owner_id`: 旧系统用户 ID，用于走 `user_id_map`
- `owner_email`: 映射兜底
- `owner_username`: 映射兜底
- `storage_policy_id`: 行级存储策略
- `size`: 文件大小
- `created_at`
- `updated_at`
- `metadata_json`: JSON 对象，例如 `{"tag":"合同","source":"legacy"}`

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
2. `same_id_fallback=true` 时，直接尝试新库同 ID 用户
3. `allow_email_lookup=true` 时，用 `owner_email` 匹配
4. `allow_username_lookup=true` 时，用 `owner_username` 匹配
5. 公共文件再看 `default_public_owner_id`

建议：

- 生产迁移时，把主体用户都写进 `user_id_map`
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
  CAST(f.id AS CHAR) AS legacy_id,
  CASE WHEN f.scope = 'public' THEN 'public' ELSE 'personal' END AS scope,
  f.owner_id AS owner_id,
  u.email AS owner_email,
  u.username AS owner_username,
  CASE WHEN f.is_dir = 1 THEN 'folder' ELSE 'file' END AS kind,
  f.relative_path AS target_path,
  f.minio_object_key AS object_key,
  f.size_bytes AS size,
  f.policy_id AS storage_policy_id,
  f.created_at AS created_at,
  f.updated_at AS updated_at,
  f.metadata_json AS metadata_json
FROM old_files f
LEFT JOIN old_users u ON u.id = f.owner_id
ORDER BY f.relative_path, f.id;
```

## 注意事项

- 目标存储策略必须能读取到 `object_key` 对应的对象；否则元数据迁过去后文件仍然不可读。
- 如果旧系统公共目录下允许不同 owner 混用同一路径，本工具会按最终文件行修正文件 owner；目录 owner 以最后一个显式目录行为准。
- 如果你还需要迁移分享、标签、回收站、版本历史，这个工具不负责，需要单独补迁移器。
