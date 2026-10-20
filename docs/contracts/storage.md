# SQLite 存储与事务契约

本文件记录 P2（T2.1–T2.9）落定的存储细节，补充《系统 AI 记忆服务架构设计.md》和
`tech-stack.md`。SQL、迁移、连接设置和事务实现集中在 `internal/store/sqlite`；业务包只依赖
`memory`、`source`、`ingest` 中按能力定义的事务接口。

## 1. 连接与运行参数

| 连接 | 数量 | 初始化设置 |
| --- | --- | --- |
| 写连接 | 固定 1 | `journal_mode=WAL`、`foreign_keys=ON`、`synchronous=FULL`、`busy_timeout` 默认 5000 ms |
| 读连接池 | 默认最多 4，可配置 | 与写连接相同的 WAL/外键/FULL/等待设置，另加 `query_only=ON` |

- PRAGMA 通过驱动 DSN 的 `_pragma` 参数在每条物理连接创建时应用。连接池替换连接后设置仍然有效，
  不依赖只执行一次的初始化 SQL。数据库路径按 file URI 规则转义，路径中的空格、`#`、`?` 和
  非 ASCII 字符不会被当成查询或片段分隔符。
- 所有时间以固定宽度 UTC RFC 3339（纳秒）持久化，保证文本排序与时间顺序一致。
- 数据库文件由服务以 `0600` 创建；数据目录为 `0700`。WAL/SHM/journal 伴随文件在启动、迁移后
  检查，权限过宽时收窄为 `0600`；`CheckPermissions` 覆盖数据库文件和存在的伴随文件。
- 迁移完成前 `Store.Ready` 返回错误；迁移完成后通过写、读连接探活。模型配置和连通性不参与
  就绪判断。
- P2 不实现备份；P7/T7.6 使用驱动的一致性备份能力，运行中不能只复制主文件。

## 2. 顺序迁移

- 迁移文件内嵌于 `internal/store/sqlite/migrations/`，文件名格式为 `<版本>_<名称>.sql`。
- `schema_migrations(version, name, applied_at)` 记录已应用版本。
- 每个待应用文件在独立事务中执行；执行 SQL 或写入版本记录失败时整份回滚，不写入版本。
  已提交的早期版本保留，修复后可继续应用后续版本。
- 重复启动跳过已应用版本。数据库包含程序未知的版本或同名版本对应不同文件名时，迁移失败，
  服务保持不就绪。
- 迁移只创建结构，不填充业务数据；后续结构变化必须追加新版本文件，不修改已应用文件。

## 3. 表与关键约束

| 表 | 关键内容与约束 |
| --- | --- |
| `sources` | 原始 JSON 以 BLOB 原样保存；`app_id`、`external_id` 可选且没有唯一约束；`redacted` 与 `redacted_at` 表示显式清除 |
| `memories` | 当前快照；`id` 主键；`version >= 1`；`created_by_app` 为创建时来源应用快照；`origin_source_id` 关联首次来源 |
| `memory_revisions` | 完整历史快照；主键 `(memory_id, version)`；操作类型和原因；随记忆永久清除级联删除 |
| `evidence` | 来源引用、可选消息 ID、原文片段、可选定位、模型名和 redacted 状态；来源行保留 |
| `revision_evidence` | 版本完整证据集合；主键 `(memory_id, version, evidence_id)`，关联版本和证据 |
| `ingest_jobs` | 状态、尝试次数、下次执行时间、脱敏错误和 `memory_ids` 结果摘要；不保存原始模型响应 |
| `memory_search_docs` | 派生文档：`row_id` 对应 FTS rowid，`memory_id` 唯一，保留原文和规范化词项 |
| `memory_fts` | 外部内容 FTS5 表，内容表为 `memory_search_docs`，`unicode61` tokenizer |
| `schema_migrations` | 顺序迁移版本记录 |

外键默认启用。来源清除不删除来源标识；永久清除记忆时只删除该记忆的版本、版本证据关联和
专属证据，共享来源保留。

## 4. 事务能力

- `Store.WithTx` 在单写连接上执行一个短事务，回调收到 `*sqlite.Tx`；该类型不暴露
  `Commit`/`Rollback`。
- 成功返回后提交；回调错误、SQL 错误、context 取消/超时、panic 都回滚。
- 从回调 context 再次调用 `WithTx` 返回 `ErrNestedTransaction`，业务不得自行嵌套或提前提交。
- `MemoryStore`、`SourceStore`、`IngestStore` 适配器把同一事务分别暴露为
  `memory.Tx`、`source.Tx`、`ingest.Tx`，因此快照、版本、证据、搜索文档、来源和任务结果可在
  一个事务中组合。
- 事务回调不得发起模型请求或执行长计算；模型与候选分析必须在事务外完成。

## 5. 来源、任务与状态

- 来源与初始 `queued` 任务通过同一个 `ingest.Tx` 插入；任一步失败不留下孤立行。
- `ClaimNextJob` 只选择 `queued` 且 `next_attempt_at` 已到期的任务，按 `created_at ASC, id ASC`
  的顺序领取；领取时 `attempts + 1`，进入 `running`。`succeeded`、`failed`、`cancelled` 永不
  被普通领取。
- `RequeueRunningJobs` 把遗留 `running` 改回 `queued`，保留尝试次数和首次开始时间，并把下次
  执行时间设为当前时间。
- `UpdateJob` 持久化终态、脱敏错误和 `memory_ids`；`memory_ids` 始终是 JSON 数组，没有结果时
  为 `[]`。
- `CancelJobsBySource` 只取消该来源的 `queued`/`running` 任务，保留尝试次数，不改变已成功或
  已失败任务。

## 6. 快照、版本、证据与列表

- `GetMemory` 返回当前快照，包括 `deleted` 墓碑；不存在返回 `memory.ErrNotFound`。
- `InsertMemory`/`UpdateMemory` 只负责当前快照存取；版本递增和业务合并规则由 P3 的统一写入
  单元决定。
- `InsertRevision` 保存完整快照；`LinkRevisionEvidence` 保存该版本的完整证据集合。
  `GetRevision`、`ListRevisions` 同时返回证据，历史只读。
- `ListRevisions` 按 `version DESC`，零 limit 使用 50。
- `ListMemories` 默认最近更新 50 条，固定 `updated_at DESC, id ASC`；支持类型、状态、创建应用
  和更新时间边界过滤；没有游标、页码或下一页信息。状态默认集合由 P3 服务按 HTTP 契约设置，
  存储层零状态表示不过滤。

## 7. 搜索文档与索引

- 写入记忆时由调用方提供 `memory.SearchDocument`；P2 使用测试构造器，T4.1 接入生产 NFC/分词
  构造器。
- `memory_search_docs` 是派生数据，`memory_fts` 是外部内容 FTS5 表。插入、更新、删除文档由
  触发器在同一事务内同步 FTS。
- 归档只更新 `state` 并保留索引；逻辑删除由 P3 删除搜索文档，因此全文索引不再包含删除记录。
- `RebuildSearchIndex` 从 `memory_search_docs` 重建 FTS；重建失败返回错误，不修改业务快照。
  生产索引重建入口在 T4.6 接入。

## 8. 测试设施与当前限制

`internal/testutil` 提供：

- `NewStore`/`NewStoreAt`：独立临时真实文件数据库、完整迁移、权限检查与清理；
- `OpenInspector`：用独立只读连接核对实际提交结果；
- `DocumentBuilder`：固定、可重复的已规范化测试文档；
- `FaultInjector`：在命名事务步骤注入失败，用于证明真实回滚边界；
- `FakeModel`：本地 Chat Completions 假服务，可脚本化成功、失败、延迟、阻塞和畸形响应，
  并能等待“请求已开始”后释放。

当前限制：

- P2 只提供存储与事务基础，不提供生产搜索查询、记忆写入服务、来源清除服务、Worker 或 HTTP
  业务路由。
- 搜索文档生产构造器在 T4.1 实现；索引重建入口在 T4.6 接入维护流程。
- 备份/恢复实现属于暂缓的 P7，当前未执行。
