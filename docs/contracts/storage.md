# SQLite 存储与事务契约

本文件记录 P2（T2.1–T2.9）存储基础、P3（T3.1–T3.10）记忆/来源生命周期和
P4（T4.1–T4.8）关键词检索落定的契约，补充《系统 AI 记忆服务架构设计.md》和
`tech-stack.md`。SQL、迁移、连接设置和事务实现集中在 `internal/store/sqlite`；业务包只依赖
`memory`、`source`、`ingest`、`retrieval` 中按能力定义的接口。

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
| `memory_revisions` | 完整历史快照；主键 `(memory_id, version)`；操作类型和原因；版本时间取快照 `updated_at`；随记忆永久清除级联删除 |
| `evidence` | 来源引用、可选消息 ID、原文片段、可选定位、模型名和 redacted 状态；来源行保留 |
| `revision_evidence` | 版本完整证据集合；主键 `(memory_id, version, evidence_id)`，关联版本和证据 |
| `ingest_jobs` | 状态、尝试次数、下次执行时间、脱敏错误和 `memory_ids` 结果摘要；不保存原始模型响应 |
| `memory_search_docs` | 派生文档：`row_id` 对应 FTS rowid，`memory_id` 唯一；只保留规范化词项，原文以 `memories` 为准 |
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
- `FailExhaustedJobs` 只把 `attempts` 已达到最大预算的 `queued` 任务置为 `failed`，写入固定
  脱敏原因和结束时间；成功、取消及其他状态不变。Worker 启动恢复先重排 running，再调用它，
  防止中断的第三次尝试变成第四次模型调用。
- `UpdateJob` 持久化终态、脱敏错误和 `memory_ids`；`memory_ids` 始终是 JSON 数组，没有结果时
  为 `[]`。
- `CancelJobsBySource` 只取消该来源的 `queued`/`running` 任务，保留尝试次数，不改变终态任务
  的状态。
- `ScrubJobErrorsBySource` 只替换该来源终态任务中非空且不等于固定清除说明的错误摘要，保留状态、
  尝试次数和 `memory_ids`；用于避免原文通过历史任务错误字段残留。

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

## 7. 统一记忆写入与生命周期（P3）

- `memory.Service` 实现内部 `Writer`，所有写方法接收调用方已开启的 `memory.Tx`，自身不开启
  嵌套事务，因此 Worker 可以把记忆变更与任务成功状态放在同一提交中。
- `Create` 生成 UUID、`active`、`version=1` 和 UTC 时间，在同一事务写当前快照、完整版本、
  证据集合和派生搜索文档。证据 ID/时间缺失时由服务补齐；已存在的证据行只复用、不重写。
- `Revise` 从持久化当前版本递增，不接受调用方预期版本。内容更正不继承旧结论的证据；空证据
  表示新结论没有支持证据，旧版本证据仍可查询。`add_evidence` 在内容不变时合并当前集合，
  重复 ID 合并为一次关联；内容、证据均未变化时不生成无意义版本。
- 状态变化（归档、重新激活、逻辑删除）生成完整新版本并复制当前证据集合。归档保留搜索文档并
  更新 `memories.state`；逻辑删除保留墓碑、历史和证据，但移除搜索文档；重复归档/删除不增加版本。
- 恢复指定历史版本复制其内容、创建应用、`observed_at` 和证据集合，生成高于当前版本的新
  `active` 版本；旧版本不改写，已清除来源的证据保持 `redacted` 且不恢复片段。
- 永久清除在一个事务内删除当前快照、全部版本、版本证据关联、搜索文档和仅被该记忆引用的证据；
  共享证据与被其他记忆引用的证据保留，来源记录永不删除。
- 来源查询与清除由 `source.Service` 提供。清除同一事务清空来源 `content`、全部证据 `quote`
  和定位副本，保留来源/证据 ID 与 `redacted`，取消 `queued`/`running` 任务，并替换可能残留
  原文的终态任务错误摘要。重复清除稳定，首次 `redacted_at` 不变。
- `source.LocateQuote` 在已解码 JSON 中按可选消息 ID 查找首个匹配字符串，返回点分字段路径和
  字符（rune）偏移；无法定位、消息不存在或 JSON 不可解析时返回空定位，但来源关联仍然保留。
  `source.NewEvidence` 生成证据 ID/时间并应用该定位规则。

## 8. 搜索文档、关键词查询与索引重建（P4）

- 生产写入路径使用 `retrieval.DocumentBuilder`，与查询共享同一套规则：先 NFC，再 Unicode
  小写；英文/其他文字和数字按词，连续汉字逐字成为 token，空白和标点作为边界。派生文档只写
  规范化 token（`title_terms`/`content_terms`）；原始标题/正文只保存在 `memories`，摘要从
  当前快照读取。
- `runtime.NewMemoryService` 把生产构造器注入记忆写入；创建、内容更正、纯证据变更、归档、
  删除、恢复和永久清除都沿用该构造器。测试仍可注入 `testutil.DocumentBuilder` 验证事务边界。
- 查询由 `retrieval.Compile` 生成：每个连续组编译为带引号的相邻短语，`all` 用 `AND`、`any`
  用 `OR` 组合。用户文本不会进入 FTS 语法；引号、括号、星号、`OR`/`NEAR` 等只作为普通 token
  或被标点边界丢弃。最终表达式通过参数绑定传给 `memory_fts MATCH ?`。
- `Store.Search` 以 `memories` 当前快照为准执行状态、类型、创建应用和更新时间过滤；默认只查
  `active`，显式请求可查 `archived`，`deleted` 在服务层和存储层都被移除，永不返回。排序固定为
  `bm25(memory_fts, 4, 1) ASC, updated_at DESC, id ASC`，返回 `score=-bm25` 和 Top-K。
- `memory_search_docs` 是派生数据，`memory_fts` 是外部内容 FTS5 表。插入、更新、删除文档由
  触发器在同一事务内同步 FTS。归档只更新 `memories.state` 并保留索引；逻辑删除删除搜索文档。
- `Store.RebuildSearchDocuments` 在一个写事务内从 `memories` 重新生成全部未删除（`active` 与
  `archived`）搜索文档，再重建 FTS；`deleted` 不进入派生索引。构造器或 SQL 失败时事务整体
  回滚，返回错误且不报告部分成功。`retrieval.Service.RebuildIndex` 包装该能力并只记录数量、
  耗时和错误，不记录记忆原文。
- `RelatedActive` 供 ingest 在后台选择相关上下文：使用同一规范化和排序，`match=any`，只查
  `active`，最多取去重后的前 32 个查询组和 10 条记忆；无匹配返回空切片，不改变公共搜索协议。
- `RebuildSearchIndex` 仍可用于仅从已有派生文档重建 FTS；生产完整重建使用上述快照重建。

## 9. 测试设施与当前限制

`internal/testutil` 提供：

- `NewStore`/`NewStoreAt`：独立临时真实文件数据库、完整迁移、权限检查与清理；
- `OpenInspector`：用独立只读连接核对实际提交结果；
- `DocumentBuilder`：固定、可重复的已规范化测试文档；
- `FaultInjector`：在命名事务步骤注入失败，用于证明真实回滚边界；
- `FakeModel`：本地 Chat Completions 假服务，可脚本化成功、失败、延迟、阻塞和畸形响应，
  并能等待“请求已开始”后释放。

当前限制：

- P4 已提供生产搜索文档构造器、关键词查询、过滤/排序、摘要/证据/解释、索引重建和后台相关
  active 记忆选择；P5 已提供统一来源/任务接收、远端模型适配、容错解析、合并决策、原子结果
  提交、单 Worker、三次尝试和启动恢复；HTTP 业务路由仍由 P6 提供。
- P5 的结果事务在同一 `ingest.Tx` 中检查 cancelled、读取来源/记忆当前状态，并复用
  `memory.Writer` 写入快照、版本、完整证据和搜索文档，最后写入任务 `succeeded` 与
  `memory_ids`。事务回调内不调用模型。
- 启动恢复在单写事务中先 `RequeueRunningJobs`，再 `FailExhaustedJobs`；不引入租约、心跳或
  分布式协调。
- 备份/恢复实现属于暂缓的 P7，当前未执行。
