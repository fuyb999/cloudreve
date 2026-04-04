# fts_external_smoke

用于对 Cloudreve 第三方全文抽取链路做真实联调、回退验证和样本清理。

边界说明：

- 不是伪造任务。
- 先把测试文件写入 MinIO / S3。
- 再通过 `ImportPhysical` 导入 Cloudreve。
- 等待 `fts_external_jobs` 创建。
- 向 Kafka `result` 主题回写结果。
- 等待附属文件与 ES 最终收口。

## 运行方式

推荐直接使用包装脚本：

```bash
./tools/fts_external_smoke/run.sh success
./tools/fts_external_smoke/run.sh success 4
./tools/fts_external_smoke/run.sh fallback
./tools/fts_external_smoke/run.sh fallback 2 12
./tools/fts_external_smoke/run.sh cleanup-prefix fts-real-smoke-
./tools/fts_external_smoke/run.sh cleanup-ids 30,31,32
```

也可以直接运行 Go 工具：

```bash
go run ./tools/fts_external_smoke
```

## 子命令说明

### `success [count]`

顺序执行标准成功样本。

- `count` 默认为 `1`
- 会自动把 `fts_external_timeout_seconds` 设为 `300`

### `fallback [count] [delay_seconds]`

顺序执行本地回退验证。

- `count` 默认为 `2`
- `delay_seconds` 默认为 `12`
- 会自动把 `fts_external_timeout_seconds` 设为 `1`
- 结束后自动恢复为 `300`

说明：

- `delay_seconds=3` 不保证一定触发回退
- 当前实现是在挂起任务下次恢复检查时判定超时
- 要稳定强制回退，建议使用 `12` 秒或更长延迟

### `cleanup-prefix <prefix>`

按文件名前缀删除 smoke 样本，并等待 ES 文档消失。

示例：

```bash
./tools/fts_external_smoke/run.sh cleanup-prefix fts-real-smoke-
```

### `cleanup-ids <id1,id2,...>`

按文件 ID 列表删除 smoke 样本，并等待 ES 文档消失。

示例：

```bash
./tools/fts_external_smoke/run.sh cleanup-ids 37,38,39
```

## 直接环境变量

如果你需要绕过包装脚本直接调工具，可使用以下环境变量：

- `REAL_FTS_SMOKE_BATCH_COUNT`
- `REAL_FTS_SMOKE_RESULT_DELAY_SECONDS`
- `REAL_FTS_SMOKE_SKIP_RESULT`
- `REAL_FTS_SMOKE_CLEANUP_PREFIX`
- `REAL_FTS_SMOKE_CLEANUP_FILE_IDS`

## 注意事项

- 本地如果使用 SQLite，不要并起多个本工具进程做并发压测。
- 多进程会在依赖初始化阶段竞争写库，触发 `SQLITE_BUSY`。
- 本地多样本验证请使用单进程顺序批量模式。
- 如果要验证真实并发，建议切到 PostgreSQL 后再做多进程或多实例压测。
