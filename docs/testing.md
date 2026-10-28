# 测试与统一验证命令

## 1. 前提

- 使用 `.go-version` 固定的 Go `1.27.3`（或同一精确补丁版本）。
- 首次构建需要能下载 `go.mod` 锁定的模块；测试本身不访问外网、不调用真实模型。
- 生产构建和默认测试不要求 CGO。竞争检测需要 CGO，属于可选检查，不是默认 `make check` 门禁。

## 2. 统一命令

```sh
make fmt-check        # gofmt -l 检查；只报告，不改写源码
make vet              # go vet ./...
make test             # 全部单元与集成测试；使用临时真实 SQLite
make test-integration # 只运行 tests/integration
make test-quality     # 固定标注样例；默认不访问真实模型
make test-capacity    # 可选/显式：10,000 条原子记忆容量场景，默认测试跳过
make test-race        # 可选：CGO_ENABLED=1 go test -race ./...
make build            # CGO_ENABLED=0，输出 bin/dmemd
make check            # fmt-check + vet + test + test-quality + build
```

失败时命令返回非零。`make build` 可通过 `VERSION=...` 注入构建版本。
[技术与使用指南](technical-guide.md#构建与验证)中的开发入口与这些命令保持一致。

真实模型语义评估是单独入口，默认跳过，不进入 CI：

```sh
DMEMD_EVAL_BASE_URL=http://127.0.0.1:8000/v1 \
DMEMD_EVAL_MODEL=<model-name> \
DMEMD_EVAL_API_KEY=<optional-key> \
go test -count=1 -run TestLiveModelQuality -v ./tests/quality
```

`make test-capacity` 默认使用 10,000 条；可用 `DMEMD_CAPACITY_MEMORIES` 调整本次规模。

## 3. 隔离与依赖

稳定性回归覆盖：配置预算默认值/覆盖/非法值、模型阶段 timeout 与总预算、waiting 阶段和
原因码的 SQLite/HTTP 往返、`separate` 无调用修复、`change` 定向修复且不猜目标、只修复同批
无效候选、摄取优先级及退避不阻塞治理、聚合目标合并与旧 revision 失效，以及分操作调用统计。
相关测试位于 `internal/ingest/{stability,budget}_test.go`、
`internal/governance/stability_test.go`、`internal/model/remote_test.go` 和
`tests/integration/http_contract_test.go`。这些测试不调用真实模型。

- 每个测试使用 `testing.T.TempDir()` 创建独立目录，不共享全局数据库。
- SQLite 测试使用真实文件数据库和固定驱动，不依赖系统 SQLite。
- 不需要真实模型、凭据、生产数据库或用户内容；模型测试使用 `internal/testutil.FakeModel` 本地 HTTP 假服务。
- 配置文件测试会显式设置权限为 `0600`；服务拒绝组/其他用户可读的配置文件。
- 测试不修改仓库外的用户数据目录。涉及默认目录的测试通过 `t.Setenv` 指向临时目录。
- `internal/testutil` 提供独立临时数据库、只读检查连接、确定性搜索文档构造器、事务故障注入点
  和可控假模型；存储契约见 `docs/contracts/storage.md`。

## 4. 测试覆盖

| 测试 | 覆盖内容 |
| --- | --- |
| `cmd/dmemd/main_test.go` | `--version`、`--check-config`、显式配置缺失时报错 |
| `internal/idgen` | UUID v4 格式、变体位与唯一性 |
| `internal/runtime/config_test.go` | 默认值、配置优先级、未知字段、权限、目录创建、架构参数校验 |
| `internal/runtime/logging_test.go` | 结构化日志、凭据脱敏、正文/模型响应不落日志 |
| `internal/runtime/app_test.go` | `/healthz`、`/readyz` 的 200/503、未装配业务路由时的 404、优雅关闭、后台 Worker 非重叠退避重启 |
| `internal/store/sqlite/store_test.go` | 单写连接、读池逐连接 PRAGMA、读连接只读、文件/伴随文件权限、迁移前不就绪、重开数据库 |
| `internal/store/sqlite/tx_test.go` | 提交、回调错误、SQL 失败、取消、嵌套事务拒绝、panic 回滚 |
| `internal/store/sqlite/migrate_test.go` | 顺序迁移、重复启动、失败回滚与重试、未知版本、初始化契约拒绝、版本唯一键、来源标识非唯一 |
| `internal/artifact/*_test.go` | 聚合/画像/Skill/经验目标键无歧义编码/解析、空作用域拒绝、主题规范化与画像目标 |
| `internal/topic/taxonomy_test.go` | 中英文别名、原始标签、父主题继承、幂等规范化与扩展标签 |
| `internal/governance/p1_test.go` | 父主题聚合、新成员同步失效、Profile durability 门禁、Skill 缺失诊断、不可变来源审计、完整性检查与篡改检测 |
| `internal/governance/*_test.go` | 自动分类、已有主题不调用模型、输入变化后丢弃在途结果、恢复重排取消任务、重复对账不生成版本、协议/暂时失败调度、软限速窗口、模型超时延期 |
| `internal/governance/conflict_test.go` | 有新证据时自动关闭失效冲突、复杂冲突保持隔离且不重复排队、未决冲突依据不进入 Skill 召回 |
| `internal/governance/forget_test.go` | source/run/memory 联合永久清除、历史/渲染不可读、关联任务与结果清理、存储失败整体回滚 |
| `internal/governance/forget_closure_test.go` | 当前/历史 evidence 来源闭包、重复事实不动点闭包及 deleted 副本、跨 scope/主体/时间/否定保留与警告、完整 open/resolved 冲突清除、四类派生历史依赖与在途任务 revision 失效、扩展范围整体回滚 |
| `internal/governance/forget_rules_test.go` | 正文格式规范化、数量/小数/否定/主体差异不误删、fact group/scope/kind/phase 门禁、未知/重叠/相邻/相同点时间边界 |
| `internal/governance/aggregate_test.go` | 自动主题摘要/画像、版本与直接来源、原子修改/归档/删除立即失效、无输入停用、输入截断标记、清除后在途结果丢弃、失效对象对账修复与分组扫描 |
| `internal/governance/experience_test.go` | run_id 可选去重且不比较正文/不覆盖、经验案例与 Skill 自动生成、失败反馈失效重建、撤销同步停用并清除来源依赖、案例/ Skill 在途结果不能恢复已撤销运行 |
| `internal/skill/skill_test.go` | SKILL.md frontmatter 单行化、默认只提供 active 当前版本、历史版本显式读取并标明状态 |
| `internal/transport/http/experiences_test.go` | 结果反馈字段映射、202 回执、400/404 映射、撤销、Skill 内容版本与状态 |
| `internal/store/sqlite/search_test.go` | 外部内容 FTS5 插入/更新/删除触发器、归档保留、FTS 重建 |
| `internal/store/sqlite/search_query_test.go` | 中文词典分词、非相邻词项、过滤、BM25 权重、时间/ID 平局排序、Top-K、快照重建与失败回滚 |
| `internal/retrieval/*_test.go` | NFC/大小写/中英文词典分词、安全 FTS 编译、命中词项、可读摘要和文档构造器 |
| `internal/store/sqlite/sources_test.go` | 来源/任务原子插入与回滚、原始 JSON 形态、缺少可选标识、重复提交不同 ID、存储级重复清除 |
| `internal/store/sqlite/jobs_test.go` | 领取顺序、到期时间、尝试计数、终态不重复领取、结果/错误持久化、running/配置等待重排、来源取消 |
| `internal/store/sqlite/memories_test.go` | 快照/完整版本/证据往返、时间/断言/组字段往返、列表默认 50 条、过滤、稳定排序、墓碑读取 |
| `internal/store/sqlite/migrate_test.go` (补充) | 事实组、关系、冲突、冲突事件、目录 generation 表和索引 |
| `internal/memory/service_test.go` | 内部创建/更正/补证、版本递增与证据不继承、归档/删除/恢复、恢复冲突检查、永久清除与共享证据、删除/清除回滚、清除后证据不复活 |
| `internal/source/service_test.go` | 来源查询、原文与历史证据片段清除、queued/running 任务取消、终态错误摘要清理、重复清除稳定性、清除回滚 |
| `internal/source/locate_test.go` | 可选消息 ID 定位、中文 rune 偏移、顶层字符串、缺失/非法输入 |
| `internal/ingest/service_test.go` | 文本/对话/结构化/缺失内容接收、来源/任务同事务、回滚、重复提交、任务查询 |
| `internal/ingest/processor_test.go` | 两阶段提取/决策、候选默认值、按候选上下文、新增/补证/跳过、归档/清除目标、作用域/主题持久化与分类任务登记、事务回滚、模型阻塞不占写事务、非法协议不提交 |
| `internal/ingest/convergence_test.go` | 自动收敛门禁：明确更新证据、条件/否定、来源可定位性、单值属性、主体/谓词/scope、时间重叠与上下文完整性 |
| `internal/ingest/worker_test.go` | 单 Worker 顺序、暂时失败持续等待、连续协议失败隔离、配置等待、退避配置、启动恢复、终态保护、等待取消 |
| `internal/model/parse_test.go` | 提取候选、时间/事实组字段、零/部分候选、围栏、决策覆盖、未知动作和证据引用容错、经验案例与 Skill 指导文本容错 |
| `internal/runtime/services_test.go` | 生产组装注入同一规范化构造器；结果反馈→经验案例→Skill→分层召回→撤销的全自动闭环；模型/ingest/Worker 组装和拒绝多 Worker；`governance.enabled=false` 时原子服务仍可用 |
| `tests/quality` | 固定标注样例：分类规范化/作用域/最小模型调用、摘要否定/条件/作用域/截断/画像边界、确定性冲突 0 误裁决、Skill 边界与截断；可选真实模型评估默认跳过 |
| `tests/capacity` | 可选 10,000 条原子记忆、11,200 个治理任务、1,200 个派生对象、并发召回和单事务永久遗忘；默认跳过 |
| `internal/model/remote_test.go` | 两阶段 Chat Completions 请求/响应映射、决策上下文、凭据头、超时、连接失败、HTTP 错误分类和畸形响应 |
| `internal/transport/http/forget_test.go` | 联合遗忘请求映射、排序后响应、保留相关记忆警告与 ID、空范围 400、缺失目标 404、畸形 JSON |
| `internal/transport/http/handler_test.go` | JSON 400、404/409/500/503 映射、无认证/无额外头、列表与检索参数、PATCH 仅状态、恢复版本边界 |
| `internal/transport/http/conflicts_test.go` | as_of 严格校验、冲突列表/详情/裁决、400/404/409 与固定错误码 |
| `internal/testutil` | 临时数据库并行隔离、权限检查、故障注入点、假模型成功/失败/延迟/畸形/阻塞控制 |
| `tests/integration/transactions_test.go` | 真实 SQLite 跨表提交与分步失败回滚、FTS 一致性和任务结果字段 |
| `tests/integration/lifecycle_test.go` | 跨模块创建→更正→归档→删除→恢复→清除原文→永久清除，含任务取消与外键检查 |
| `internal/retrieval/service_test.go` | 原子层、派生层、hybrid 关键词分支、相关事实和冲突发现统一词项；无有效词不访问后端或模型；相关事实词项上限在过滤后应用 |
| `tests/integration/search_test.go` | 中文一/二字/长词、中英混合、标点、大小写、规范化等价、统一 any、自然语言与关键词等价、否定词保留、停用词空结果、覆盖率重排、低覆盖候选保留、scope/as_of/断言状态/归档过滤、摘要/证据/解释、生命周期、重建与相关上下文 |
| `tests/integration/ingest_pipeline_test.go` | 统一提交→模型→合并→检索、清除/结果提交两种竞态、接收/running/事务中断/成功后的重启、模型不可用与恢复、部分/零候选 |
| `tests/integration/readiness_test.go` | 真实数据库驱动 readyz、关闭后 503、模型地址不可达不影响就绪 |
| `tests/integration/http_contract_test.go` | 完整 HTTP 契约：无 Token/任意 Token、202 提交、任务成功/失败、列表/检索/版本/恢复/证据、归档/删除/清除、来源清除取消在途任务 |
| `tests/integration/temporal_conflict_test.go` | 有效区间变更与历史 as_of、supersedes 关系、pending 冲突发现与裁决、恢复冲突拒绝 |
| `tests/integration/conflict_convergence_test.go` | 换狗粮/血糖更正/租房要求/CKA 改期无决策模型自动收敛、完整唯一成员、检测/当前版本、resolved 审计与完整成员显式裁决 |
| `tests/integration/layered_recall_test.go` | 默认全层、显式 layers、原子与派生层统一 any、HTTP 自然语言问题与关键词召回、kinds 只过滤原子层、作用域隔离、摘要同步失效与后台重建、归档退出默认召回、历史时点仅原子层并提示 |
| `tests/e2e/process_test.go` | 构建并启动真实进程、同数据目录第二实例被拒绝、SIGTERM 优雅关闭、重启恢复 running 任务和持久化数据 |
| `tests/integration/sqlite_baseline_test.go` | SQLite 版本、WAL、外键、FTS5、CGO=0 兼容 |
| `tests/integration/unicode_baseline_test.go` | x/text NFC 基线与中文保持 |

测试覆盖统一提交、模型处理、记忆生命周期、关键词与混合检索、HTTP 契约、完整进程启动、
单实例锁、重启恢复、固定标注质量和一万条容量场景。容量场景默认跳过，需显式运行
`make test-capacity`。真实模型语义评估、十万条容量、备份恢复和发布准备仍未执行，不创建
占位通过结果。

## 5. CI

`.github/workflows/ci.yml` 在 GitHub Actions 的 `ubuntu-24.04` 运行器上通过
`actions/setup-go` 读取 `.go-version`，缓存以 `go.sum` 为依据，然后执行 `make check`。
`make check` 不运行容量场景，也不访问真实模型。CI 不保存凭据或用户数据库，也不配置生产
部署。远端运行结果需在实际触发后记录，不能把本地通过当作远端 CI 通过。

## 6. 测试边界

自动化测试不访问外网、真实模型或生产数据。一万条容量场景使用临时真实 SQLite 和本地假
模型/确定性派生提供器；它验证正确性和可运行性，不构成性能 SLA。真实模型语义质量、十万条
容量、备份恢复和发布准备尚未执行，不能宣称这些验收通过。

派生审计可单独执行 `dmemd --config <path> --artifact-integrity-check`；输出 JSON，
`issues` 非空或读取失败时返回非零。该命令需要服务停止并获得数据目录锁，不调用模型或修复数据。
