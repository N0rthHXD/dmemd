# SQLite 存储与事务契约

本文件记录 SQLite 存储、迁移、连接、事务、记忆/来源生命周期和关键词检索的契约，补充
《系统 AI 记忆服务架构设计.md》。SQL、迁移、连接设置和事务实现集中在
`internal/store/sqlite`；业务包只依赖 `memory`、`source`、`ingest`、`retrieval` 中按能力
定义的接口。

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
- 当前不实现备份；运行中的数据库不能只复制主文件充当备份。实现备份时应使用驱动的一致性
  备份能力。

## 2. 顺序迁移

- 迁移文件内嵌于 `internal/store/sqlite/migrations/`，文件名格式为 `<版本>_<名称>.sql`。
- `schema_migrations(version, name, applied_at)` 记录已应用版本。
- 每个待应用文件在独立事务中执行；执行 SQL 或写入版本记录失败时整份回滚，不写入版本。
  已提交的早期版本保留，修复后可继续应用后续版本。
- 重复启动跳过已应用版本。数据库包含程序未知的版本或同名版本对应不同文件名时，迁移失败，
  服务保持不就绪。
- `schema_meta` 保存初始化契约编号（当前 `dmemd-p5-2026-10-27`）。已有迁移记录但契约缺失或
  不匹配时，`Migrate` 在应用任何新迁移前失败，程序不自动删除、清空或重建开发库。空库完整
  初始化后写入并核对同一契约。
- 迁移只创建结构，不填充业务数据。项目尚未发布：当前 `0001_core.sql` 已直接包含时间/
  断言、`scope_key`/`topics_json` 字段和初始化契约；`0004_temporal_conflicts.sql` 增加事实组、
  关系、演化审计、冲突和目录 generation；`0005_governance.sql` 增加持久治理任务；
  `0006_artifacts.sql` 增加派生对象、版本、直接原子依赖、无对象目标评估和派生 FTS；
  `0007_experiences.sql` 增加应用结果反馈、结果依赖和 Skill 来源。不提供存量数据库升级
  或旧字段回填。发布后新增结构变化必须追加新版本文件，不修改已应用文件。

## 3. 表与关键约束

| 表 | 关键内容与约束 |
| --- | --- |
| `sources` | 原始 JSON 以 BLOB 原样保存；`app_id`、`external_id` 可选且没有唯一约束；`scope_key` 为可选平面作用域；`redacted` 与 `redacted_at` 表示显式清除 |
| `memories` | 当前快照；`id` 主键；`version >= 1`；含 `assertion_status`、`observed_at` 及其依据、`event_time`/依据/精度、`valid_from/valid_to` 及依据、`temporal_json`、`procedure_json`、`qualifiers_json`、`fact_group_id`、`scope_key`、规范化 `topics_json`；CHECK 保证空边界对应 unknown、非空边界有依据且左闭右开，事件时间为空时依据必须 unknown；`created_by_app` 为创建时来源应用快照；`origin_source_id` 关联首次来源 |
| `memory_revisions` | 完整历史快照，含与 `memories` 相同的时间/断言/组/作用域/主题字段；主键 `(memory_id, version)`；操作类型和原因；版本时间取快照 `updated_at`；随记忆永久清除级联删除 |
| `memory_fact_groups` | 服务生成 ID、显式 `scope_key`、主体/属性/场景、必要条件、规范化身份键、`cardinality`、`generation`；身份唯一约束为 `(scope_key, subject_key, predicate_key, scene_key, conditions_key)`，成员或语义变化递增 |
| `memory_relations` | 只记录 `supersedes`；保存两端决策时版本；未决矛盾只存于冲突成员 |
| `memory_evolution_events` | 确定性演化审计：事实组、后继/前驱及决策版本、`change/correction/reschedule/cancel/supersede`、固定原因码和时间；不复制原文或模型理由 |
| `memory_conflicts` | open/resolved、固定原因码、结构化裁决 JSON、revision、创建/更新时间；`resolution` 是裁决 payload 的唯一来源 |
| `memory_conflict_members` | 冲突 ID、记忆 ID、检测时版本；当前版本读取时计算；记忆永久清除时级联，剩余不足两方时删除冲突 |
| `memory_conflict_events` | 检测、成员变化、解决的不可变审计；只含固定动作和成员引用，不复制原文、原因码或模型理由 |
| `memory_catalog` | 单行语义目录 generation；记忆、事实组和冲突写入时递增，向量完成不递增 |
| `evidence` | 来源引用、可选消息 ID、原文片段、可选定位、模型名和 redacted 状态；来源行保留 |
| `revision_evidence` | 版本完整证据集合；主键 `(memory_id, version, evidence_id)`，关联版本和证据 |
| `ingest_jobs` | 状态（`queued/running/waiting/succeeded/failed/cancelled`）、尝试次数、连续协议失败次数、固定等待原因、下次执行时间、脱敏错误、`memory_ids`、`conflict_ids`、`pending_memory_ids`、固定 `warnings`、业务质量 `quality_outcome`（`empty/complete/partial/unusable`）、分阶段 `extraction_stats`、固定的 `retention_stats`（保留/未保留原因，不含原文）、`timings`（排队、提取、规范化、repair、上下文、决策、提交）、`model_usage`（调用数/input/output token）与不含原文的候选级 `diagnostics`；不保存原始模型响应或推理过程 |
| `governance_jobs` | 固定治理目标；`(kind,target_key)` 唯一；`revision` 保护在途结果，输入变化或取消时递增；状态、尝试次数、连续失败、固定等待/错误码、`input_hash` 和时间字段；`created_at` 同时用于连续摄取下的年龄/饥饿监测 |
| `artifacts` | 派生对象；`(kind,target_key)` 唯一；`kind` 包含 `topic_summary/scope_summary/profile/experience_case/skill`；`current_version=0` 表示尚无可用版本；`state` 仅为 `active/stale/disabled`；`scope_key/topic` 用于作用域召回。`scope_summary` 目标只由 scope 和派生类型确定 |
| `derived_target_evaluations` | 无对象结果的生成目标评估；`(kind,target_key)` 唯一，记录 scope/topic、固定状态（`no_eligible_input/model_unavailable/generation_failed/invalid_output`）、原因码和评估时间。成功提交 revision 时删除；它与没有可用版本的失败对象不同，不保存正文或模型响应 |
| `artifact_revisions` | 不可变派生版本；标题、正文或结构化 payload、内容哈希、生成器/协议版本、固定原因码、截断标记和创建时间；随对象级联删除。摘要 payload 分开记录输入选择计数、引用完整性、语义覆盖、合法引用和省略输入 |
| `artifact_memory_dependencies` | 每个派生版本使用的精确原子修订（保守覆盖全部生成输入）；主键 `(artifact_id, artifact_version, memory_id)`；外键指向 `memory_revisions`，反向索引支持同步失效和清除前枚举 |
| `experience_runs` | 应用报告的结果：服务内部 ID、可选唯一 `run_id`、`source_id`、可选 Skill ID/精确版本、作用域、目标/条件/行动、`outcome=success/failure/unknown`、`state=recorded/revoked` 和时间；不保存验证认证 |
| `artifact_run_dependencies` | 每个派生版本使用的经验运行；主键 `(artifact_id, artifact_version, experience_id)`；外键指向 `experience_runs`，反向索引支持撤销时同步失效 |
| `artifact_search_docs` / `artifact_fts` | 只索引 `active` 对象的当前版本；规范化标题/正文词项与外部内容 FTS5；可重建且不保存来源原文 |
| `schema_meta` | 初始化契约编号等元数据；契约与当前程序不匹配时启动失败 |
| `memory_search_docs` | 派生文档：`row_id` 对应 FTS rowid，`memory_id` 唯一，`content_hash` 标识 Embedding 文档字节；只保留规范化词项，原文以 `memories` 为准 |
| `memory_fts` | 外部内容 FTS5 表，内容表为 `memory_search_docs`，`unicode61` tokenizer |
| `memory_embeddings` | 每条当前记忆一个向量：`memory_id` 主键、`profile_id`、`content_hash`、`dimensions > 0`、little-endian float32 BLOB、`embedded_at`；外键随搜索文档级联删除，BLOB 长度必须为 `dimensions*4` |
| `memory_embedding_jobs` | 持久化向量任务：`memory_id` 主键、Profile/内容哈希、`queued/running/failed`、尝试次数、下次重试时间与脱敏错误码；外键随搜索文档级联删除 |
| `memory_index_meta` | 记录生成当前内容哈希的文档输入契约版本、已登记的 Embedding profile/版本指纹，以及持久化的重建原因与登记时间；profile、维数或文档构造方式变化时旧向量退出召回并登记重建，启动阶段按需执行一次本地文档重建 |
| `schema_migrations` | 顺序迁移版本记录 |

外键默认启用。来源清除不删除来源标识；永久清除记忆时只删除该记忆的版本、版本证据关联和
专属证据，共享来源保留。

## 4. 事务能力

- `Store.WithTx` 在单写连接上执行一个短事务，回调收到 `*sqlite.Tx`；该类型不暴露
  `Commit`/`Rollback`。
- 成功返回后提交；回调错误、SQL 错误、context 取消/超时、panic 都回滚。
- 从回调 context 再次调用 `WithTx` 返回 `ErrNestedTransaction`，业务不得自行嵌套或提前提交。
- `MemoryStore`、`SourceStore`、`IngestStore`、`GovernanceStore` 适配器把同一事务分别暴露为
  `memory.Tx`、`source.Tx`、`ingest.Tx`、`governance.Tx`，因此快照、版本、证据、搜索文档、
  来源、任务结果和治理任务登记可在同一个事务中组合。
- 事务回调不得发起模型请求或执行长计算；模型与候选分析必须在事务外完成。
- `memory.Service` 的原子写入在同一事务 upsert `classify` 治理任务；输入哈希不变且任务已
  完成时为空操作。逻辑删除和永久清除取消对应任务并递增 revision，迟到结果不能恢复记录。
- 原子写入启用派生同步时，同一事务把直接依赖对象置为 `stale`、删除当前 FTS 文档，并 upsert
  受影响 `(scope,topic)` 摘要、作用域概览、作用域画像以及 procedure 对应 Skill 的治理任务。
  证据补充不触发重算；删除/永久清除立即生效，不等待去抖。
- 永久清除先在事务内枚举依赖该记忆的派生对象，删除对象全部版本、依赖和索引，再删除原子
  数据；同一事务递增相关治理任务 revision，因此迟到模型结果不能恢复已清除内容。
- 结果反馈在同一事务保存来源、`experience_runs` 和案例 `aggregate` 任务。可选 `run_id`
  由唯一约束去重；重复提交直接读取已有结果，不比较正文、不覆盖、不恢复已撤销记录。
- 撤销结果在同一事务把运行置为 `revoked`、取消案例任务、停用案例、删除其 FTS，并把依赖
  该运行的 Skill 置为 `stale` 后登记 `build_skill`。读取路径额外排除依赖非 `recorded` 运行
  的派生对象，因此迟到提交不能恢复已撤销内容。
- `POST /v1/forget` 在同一事务先枚举来源关联记忆（来源 origin、历史 origin 和证据引用）与
  结果，再按运行、记忆、来源的顺序清除。派生对象在任何外键级联前先按全部历史版本枚举并删除
  对象、依赖和 FTS；结果案例任务取消，受影响 Skill 目标重新排队。来源的最后一步删除 ingest
  任务、证据和来源行。任一步失败整体回滚，不部分清理。
- 遗忘在删除前计算重复事实不动点闭包：按非空 fact group 无上限枚举所有生命周期状态，
  缓存组成员，仅纳入同 scope/kind/phase、格式规范化正文一致、有效时间相同或重叠的副本。
  保留同组不满足条件的 ID 并返回 `related_memories_retained`。不复用会过滤 deleted 的推理扫描。
- 遗忘删除依赖记忆的整个冲突（含 resolved 和审计引用），并取消对应 review 任务；与单记忆
  purge 的成员裁剪行为区分。派生历史依赖单独枚举并递增所有受影响目标任务 revision，即使
  当前版本已不再依赖目标，也不能让在途结果复活已清除的历史对象。
- 入库自动收敛区分状态变化、明确更正、事件改期/取消和独立观测。状态变化关闭旧有效区间并
  保留旧快照；更正拒绝旧断言但不伪造其有效历史；改期/取消把旧计划标为历史；独立观测保留
  各自 `observed_at`，不因数值不同创建冲突。成功收敛在同一事务写入后继、前驱修订、
  `supersedes`、成员检测版本、`memory_evolution_events` 和必要时的 resolved 冲突。未自动
  收敛的候选在任务 `convergence` 中记录 `identity_ambiguous`、`missing_update_evidence`、
  `time_unknown`、`cardinality_unknown` 或 `context_truncated`。
- 冲突创建、成员扩展或相关记忆版本变化时，同一事务 upsert `review_conflict` 目标。复查只做
  确定性判断：唯一存续 accepted 成员、正文收敛或已完全已知且互不重叠的区间；复杂情况保持
  open，不调用模型，也不因重复扫描刷新相同输入。

## 5. 来源、任务与状态

- 来源与初始 `queued` 任务通过同一个 `ingest.Tx` 插入；任一步失败不留下孤立行。
- `ClaimNextJob` 选择已到期的 `queued` 或非配置等待的 `waiting` 任务，按
  `created_at ASC, id ASC` 的顺序领取；领取时 `attempts + 1`，进入 `running`。`succeeded`、
  `failed`、`cancelled` 和配置等待任务不会被普通领取。
- `RequeueRunningJobs` 把遗留 `running` 改回 `queued`，保留尝试次数和首次开始时间，并把下次
  执行时间设为当前时间；`RequeueWaitingConfigurationJobs` 在重启后让配置/凭据等待任务重新
  可执行；`RequeueProtocolFailedJobs` 让协议隔离任务在重启后重新评估一次。
- 暂时性网络、超时和限流错误进入 `waiting`，`next_attempt_at` 按 5 秒、30 秒、2 分钟递增，
  之后最长 30 分钟；恢复后继续同一任务。局部 repair 后连续失败达到 `worker.max_attempts`
  （默认 3）次协议/解析错误进入 `failed`；配置和凭据错误
  保持等待，不在进程内高频探测。
- `UpdateJob` 持久化终态、脱敏错误和 `memory_ids`；`memory_ids` 始终是 JSON 数组，没有结果时
  为 `[]`。
- `stage` 和 `reason_code` 是持久化列，waiting/failed 查询返回失败阶段和稳定原因；领取新尝试和
  成功提交时清空旧诊断。总预算耗尽为 `process_budget_exceeded`，单次模型超时为 `model_timeout`，
  均保持 transient 等待，不消耗协议失败次数。初始化契约直接更新，不迁移旧数据库。
- 治理领取与摄取优先级检查使用同一个写事务：存在 running 或已到期可执行的摄取任务时不领取
  治理任务；配置等待和未来退避不阻塞治理。治理内部分类/复查先于 aggregate/build_skill。
  相同 kind/target_key 的聚合由 UpsertJob 合并并递增 revision，旧 revision 无法执行或提交。
- `CancelJobsBySource` 只取消该来源的 `queued`/`running`/`waiting` 任务，保留尝试次数，不改变
  终态任务的状态。
- `ScrubJobErrorsBySource` 只替换该来源终态任务中非空且不等于固定清除说明的错误摘要，保留状态、
  尝试次数和 `memory_ids`；用于避免原文通过历史任务错误字段残留。

## 6. 快照、版本、证据与列表

- `GetMemory` 返回当前快照，包括 `deleted` 墓碑；不存在返回 `memory.ErrNotFound`。
- `InsertMemory`/`UpdateMemory` 只负责当前快照存取；版本递增和业务合并规则由记忆服务的统一
  写入单元决定。
- `InsertRevision` 保存完整快照；`LinkRevisionEvidence` 保存该版本的完整证据集合。
  `GetRevision`、`ListRevisions` 同时返回证据，历史只读。
- `ListRevisions` 按 `version DESC`，零 limit 使用 50。
- `ListMemories` 默认最近更新 50 条，固定 `updated_at DESC, id ASC`；支持类型、状态、创建应用
  和更新时间边界过滤；没有游标、页码或下一页信息。状态默认集合由记忆服务按 HTTP 契约设置，
  存储层零状态表示不过滤。

## 7. 统一记忆写入与生命周期

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
- 事实组查找使用规范化身份键并强制 `scope_key` 隔离：主体、谓词、场景或必要条件任一不同
  即为不同事实。场景或条件缺失时才允许复用同 scope 下唯一兼容组；多个候选保持
  `identity_ambiguous`，不做模糊别名合并。身份时间线与关键词/语义上下文分开加载，任一路
  截断只标记对应候选或事实组。
- `source.BuildView` 对来源做确定性遍历：消息、纯文本和结构化字符串字段获得 source 内
  唯一且可重建的 `segment_id`，并保留字段路径、rune 范围、客户端消息 ID 和 `role` 作为
  辅助上下文。相同来源重试得到相同片段映射；原始 JSON 不被修改，也不额外持久化原文副本。
- `source.View.Resolve` 只接受可见 `segment_id` 和逐字 quote。未知片段 ID 不做模糊修正；
  缺失片段 ID 仅在 quote 全来源精确唯一时恢复并记录 `unique_quote`；同一片段内重复出现
  且无偏移时返回歧义。`source.NewEvidence` 生成证据 ID/时间并保存解析后的字段路径和 rune
  偏移；无法解析的引用只保留来源关联，不能用于自动更正或收敛。


## 8. 搜索文档、关键词查询与索引重建

- 生产写入路径使用 `retrieval.DocumentBuilder`，与查询共享同一套规则：先 NFC，再 Unicode
  小写；英文/其他文字和数字按词，汉字使用内置 gse 词典精准分词，空白和标点作为边界。每个
  分词词项作为一个独立查询组写入或编译，索引与查询的词项表示一致。派生文档只写规范化 token
  （`title_terms`/`content_terms`）；原始标题/正文只保存在 `memories`，摘要从当前快照读取。
- `runtime.NewMemoryService` 把生产构造器注入记忆写入；创建、内容更正、纯证据变更、归档、
  删除、恢复和永久清除都沿用该构造器。测试仍可注入 `testutil.DocumentBuilder` 验证事务边界。
- 查询由 `retrieval.Compile` 生成：每个分词词项编译为带引号的字面量短语，`all` 用 `AND`、`any`
  用 `OR` 组合；不再把连续汉字或整句查询编译为一个相邻短语。用户文本不会进入 FTS 语法；
  引号、括号、星号、`OR`/`NEAR` 等只作为普通 token 或被标点边界丢弃。最终表达式通过参数
  绑定传给 `memory_fts MATCH ?`。服务层 keyword 模式的 `any` 会在编译前选择有效词项，
  并在候选层应用覆盖率重排，不设置最低命中数；`Compile` 本身仍只做字面量 `AND`/`OR` 组合。
- `Store.Search` 以 `memories` 当前快照为准执行状态、类型、创建应用和更新时间过滤；默认只查
  `active`，显式请求可查 `archived`，`deleted` 在服务层和存储层都被移除，永不返回。排序固定为
  `bm25(memory_fts, 4, 1) ASC, updated_at DESC, id ASC`，返回 `score=-bm25` 和 Top-K。
  这是存储层顺序；服务层 keyword 模式的 `any` 可在其上按覆盖率重排并返回融合分数。
- `memory_search_docs` 是派生数据，`memory_fts` 是外部内容 FTS5 表。插入、更新、删除文档由
  触发器在同一事务内同步 FTS。归档只更新 `memories.state` 并保留索引；逻辑删除删除搜索文档。
- `Store.RebuildSearchDocuments` 在一个写事务内从 `memories` 重新生成全部未删除（`active` 与
  `archived`）搜索文档，再重建 FTS；`deleted` 不进入派生索引。重建按记忆 ID upsert 并逐条清理
  无效文档，不再先清空整表，因此有效向量不会因重建被级联删除。构造器或 SQL 失败时事务整体
  回滚，返回错误且不报告部分成功。`retrieval.Service.RebuildIndex` 包装该能力并只记录数量、
  耗时和错误，不记录记忆原文。
- `RelatedContext` 供 ingest 在后台选择相关上下文：使用关键词路，启用 Embedding 时补充语义候选；
  传入当前来源的 `scope_key`，只读取同作用域或 `user` 全局的 active/archived accepted/pending
  记忆，最多取去重后的前 32 个分词词项和指定条数；截断时 `Complete=false`，不改变公共搜索协议。
- `RebuildSearchIndex` 仍可用于仅从已有派生文档重建 FTS；生产完整重建使用上述快照重建。

## 9. 混合检索持久化与向量 Worker

- `memory.DocumentBuilder` 生成 FTS 词项和 `content_hash`。内容哈希是固定文档模板版本与实际
  文档字节的 SHA-256；标题、正文、kind、结构化 procedure 或限定变化时改变，状态、证据、
  版本和普通时间字段变化不改变。流程结构因此拥有自己的派生失效边界，而不是只依赖正文。
- `Tx.UpsertSearchDocument` 在同一事务内比较旧哈希。输入变化时更新 FTS/哈希、删除旧向量与
  任务并创建新任务（尝试次数归零）；输入未变且有有效向量时复用；输入未变但
  缺失或 Profile 不匹配时只补缺失任务，不重置已有任务的尝试次数或重试时间。功能关闭时不创建
  任务，但仍维护哈希并在输入变化时清除过期向量。
- 永久清除或逻辑删除通过 `DeleteSearchDocument`/`PurgeMemory` 删除派生文档，外键级联删除
  向量与任务；在途 Worker 的条件提交会因搜索文档或哈希不存在而失败，不能重新插入结果。
- `Store.ClaimJob` 在短写事务中领取到期 `queued` 任务并递增 `attempts`；`LoadDocument`
  读取当前快照与哈希；`CommitJob` 只在 Profile、内容哈希和当前派生文档哈希全部匹配时
  写向量并删除任务；`FailJob` 只更新仍在 `running` 的同一代际任务。`RecoverJobs` 在
  启动时把遗留 `running` 重排为 `queued` 并保留尝试次数。
- `EnqueueMissingJobs` 为当前文档缺失或 Profile/哈希不匹配的向量补任务；同一代际的
  `queued/running` 任务不会被重置，`failed` 任务在下次启动补任务时重排。
- 向量由 Go 流式精确检索：SQL 先按当前记忆、状态、类型、应用、更新时间和
  `profile_id`/`dimensions`/`e.content_hash = d.content_hash` 过滤，再解码并计算点积；使用
  固定大小堆保留 Top-K，及时响应 context 取消。架构不使用向量扩展或外部向量数据库。

## 10. 测试设施与当前限制

`internal/testutil` 提供：

- `NewStore`/`NewStoreAt`：独立临时真实文件数据库、完整迁移、权限检查与清理；
- `OpenInspector`：用独立只读连接核对实际提交结果；
- `DocumentBuilder`：固定、可重复的已规范化测试文档；
- `FaultInjector`：在命名事务步骤注入失败，用于证明真实回滚边界；
- `FakeModel`：本地 Chat Completions 假服务，可脚本化成功、失败、延迟、阻塞和畸形响应，
  并能等待“请求已开始”后释放。

当前限制：

- 生产搜索文档构造器、关键词查询、过滤/排序、摘要/证据/解释、索引重建和后台相关 active
  记忆选择均已接入；统一来源/任务接收、远端模型适配、容错解析、合并决策、原子结果提交、
  单 Worker、暂时失败等待/恢复、协议失败隔离和启动恢复由 `internal/ingest` 与 `internal/runtime`
  提供，HTTP 业务路由由 `internal/transport/http` 提供。
- 结果事务在同一 `ingest.Tx` 中检查 cancelled、读取来源/记忆当前状态，并复用
  `memory.Writer` 写入快照、版本、完整证据和搜索文档，最后写入任务 `succeeded` 与
  `memory_ids`。事务回调内不调用模型。
- 启动恢复在单写事务中先 `RequeueRunningJobs`，再 `RequeueWaitingConfigurationJobs`；不引入
  租约、心跳或分布式协调。暂时性失败不再按固定总尝试次数永久终止。
- 一万条原子记忆的临时真实数据库容量场景由 `tests/capacity` 覆盖，记录在 `docs/quality.md`：
  11,200 个治理任务收敛、1,200 个派生对象、并发召回和单事务来源遗忘均完成；十万条、长期
  负载和恢复时间尚未验证，不能作为性能 SLA。
- 固定标注质量和可选真实模型评估由 `tests/quality` 提供；离线套件检查协议、来源、作用域、
  截断和确定性冲突规则，不证明真实模型语义质量。真实模型评估未执行时保持未通过。
- 备份/恢复命令尚未实现。


## 11. 有效时间、断言与冲突

- 有效区间为左闭右开 `[valid_from, valid_to)`，两端非空时要求起点早于终点。空边界表示未知；
  记录时间、陈述/观测/事件时间和事实有效时间分开存储；`observed_at_basis=recorded` 明确标记
  接收时间回退，不能作为新事实更晚的收敛依据。事件时间使用独立过滤，未来计划不占用未来
  `valid_from`。日期原文、精度和时区状态保存在 `temporal_json`，无时区日期不被宣称为
  明确 UTC 实例。
- `procedure_json` 和 `qualifiers_json` 随当前快照与每个历史修订保存，参与内容 hash、派生
  失效和清除闭包；永久遗忘删除当前行时修订和证据按既有外键闭包一并删除。
  冲突检查把未知边界按可能重叠保守处理。
- 普通召回只读取 `assertion_status=accepted`，按生命周期和 `as_of` 区间过滤；`pending` 只
  通过冲突/管理接口读取，`rejected` 保留审计但不召回。
- FTS SQL、向量扫描 SQL 与 Go 层最终过滤使用同一组时间/断言条件，过滤发生在候选截断之前。
- `memory_catalog.generation` 在记忆写入、事实组写入/成员 generation 变化、冲突写入和清除时
  递增。ingest 在模型调用前记录该值，写入事务再次读取；不一致则返回内部可重试错误，由现有
  Worker 按暂时失败退避重跑整个来源，不做部分提交。
- 事实组写入只增加成员 generation；不要把 `(group_id, accepted)` 设为唯一键，历史非重叠段
  必须允许共存。
- 关系两端引用已存在的 revision；当前只写 `supersedes`，新版本不会自动解决旧冲突。冲突成员只记录检测时版本，当前版本读取时计算。
- 永久清除记忆时，关系随 revision 级联删除，冲突成员随之移除；冲突剩余成员不足两方时整条
  冲突和事件删除。来源清除只清空证据原文和定位，不复制正文到冲突审计中。
- 认证/配置错误进入 `waiting` 并在重启后重评；网络、限流和超时按递增退避继续同一任务；
  连续三次协议/解析错误进入 `failed`。`unresolved` 与 `pending` 是业务成功，不触发网络重试。

## 12. 治理任务

- `governance_jobs` 以 `(kind,target_key)` 唯一，固定类型为 `classify/review_conflict/
  aggregate/build_skill/reconcile`。业务事务直接 upsert；不建设事件表、控制器游标或调用账本。
- `input_hash` 相同的已存在目标为空操作；输入变化时递增 `revision`、清空失败计数并按去抖时间
  重新排队。逻辑删除、永久清除和显式取消在任务 revision 上留下变化，迟到结果不能提交。
- `ClaimNextGovernanceJob` 只领取到期且非配置等待的任务。`FinishJob` 与 `RescheduleJob` 均要求
  同一任务 revision 且状态仍为 `running`；否则不写入，处理器的结果事务整体回滚。
- 启动把遗留 `running` 置回 `queued`，重评配置等待，并让连续协议失败隔离的任务在进程重启
  后获得一次新的评估机会（模型/协议版本可能已变化）。暂时失败等待 5 秒、30 秒、2 分钟，之后
  最长 30 分钟；连续三次协议错误隔离为 `failed`。
- 单 Worker 串行执行；进程内软限速默认最少调用间隔 2 分钟、每小时约 30 次，仅推迟治理模型
  调用。周期对账默认每 15 分钟扫描最多 100 个目标。
- `aggregate` 目标包含主题摘要、画像和单运行经验案例；`build_skill` 目标为 `(scope,topic)`
  指导型 Skill。Skill 和案例只追加不可变版本并原子切换 `current_version`，成功即 `active`；
  失效先退出召回再后台重建，历史保留用于追溯。
- 经验案例与 Skill 以原子修订和 `experience_runs` 为叶子依据，没有 artifact→artifact 持久
  依赖。输入上限由 `governance.max_inputs` 限制；超过上限的版本标记 `truncated`，不声称
  覆盖全部输入。
- `review_conflict` 目标键使用 `conflict:` 前缀加无歧义编码的冲突 ID。输入哈希覆盖冲突
  revision、原因码、成员检测版本和当前版本；复查不调用模型，未变化集合不重复刷新任务。
