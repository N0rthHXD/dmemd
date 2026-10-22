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
make test-race        # 可选：CGO_ENABLED=1 go test -race ./...
make build            # CGO_ENABLED=0，输出 bin/dmemd
make check            # fmt-check + vet + test + build
```

失败时命令返回非零。`make build` 可通过 `VERSION=...` 注入构建版本。
README 中的开发入口与这些命令保持一致。

## 3. 隔离与依赖

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
| `internal/runtime/app_test.go` | `/healthz`、`/readyz` 的 200/503、未装配业务路由时的 404、优雅关闭 |
| `internal/store/sqlite/store_test.go` | 单写连接、读池逐连接 PRAGMA、读连接只读、文件/伴随文件权限、迁移前不就绪、重开数据库 |
| `internal/store/sqlite/tx_test.go` | 提交、回调错误、SQL 失败、取消、嵌套事务拒绝、panic 回滚 |
| `internal/store/sqlite/migrate_test.go` | 顺序迁移、重复启动、失败回滚与重试、未知版本、版本唯一键、来源标识非唯一 |
| `internal/store/sqlite/search_test.go` | 外部内容 FTS5 插入/更新/删除触发器、归档保留、FTS 重建 |
| `internal/store/sqlite/search_query_test.go` | 中文词典分词、非相邻词项、过滤、BM25 权重、时间/ID 平局排序、Top-K、快照重建与失败回滚 |
| `internal/retrieval/*_test.go` | NFC/大小写/中英文词典分词、安全 FTS 编译、命中词项、可读摘要和文档构造器 |
| `internal/store/sqlite/sources_test.go` | 来源/任务原子插入与回滚、原始 JSON 形态、缺少可选标识、重复提交不同 ID、存储级重复清除 |
| `internal/store/sqlite/jobs_test.go` | 领取顺序、到期时间、尝试计数、终态不重复领取、结果/错误持久化、running 重排、耗尽预算失败、来源取消 |
| `internal/store/sqlite/memories_test.go` | 快照/完整版本/证据往返、列表默认 50 条、过滤、稳定排序、墓碑读取 |
| `internal/memory/service_test.go` | 内部创建/更正/补证、版本递增与证据不继承、归档/删除/恢复、永久清除与共享证据、删除/清除回滚、清除后证据不复活 |
| `internal/source/service_test.go` | 来源查询、原文与历史证据片段清除、queued/running 任务取消、终态错误摘要清理、重复清除稳定性、清除回滚 |
| `internal/source/locate_test.go` | 可选消息 ID 定位、中文 rune 偏移、顶层字符串、缺失/非法输入 |
| `internal/ingest/service_test.go` | 文本/对话/结构化/缺失内容接收、来源/任务同事务、回滚、重复提交、任务查询 |
| `internal/ingest/processor_test.go` | 候选默认值、相关上下文、新增/更新/补证/跳过、旧 observed_at、归档/清除目标、事务回滚、模型阻塞不占写事务 |
| `internal/ingest/worker_test.go` | 单 Worker 顺序、三次尝试、1/5 秒退避配置、不可重试错误、启动恢复、终态保护、等待取消 |
| `internal/model/parse_test.go` | 零/部分候选、默认字段、围栏、整体不可解析和证据类型容错 |
| `internal/model/remote_test.go` | Chat Completions 请求/响应映射、凭据头、超时、连接失败、HTTP 错误分类和畸形响应 |
| `internal/runtime/services_test.go` | 生产组装注入同一规范化构造器并完成创建→检索；模型/ingest/Worker 组装和拒绝多 Worker |
| `internal/transport/http/handler_test.go` | JSON 400、404/500/503 映射、无认证/无额外头、列表与检索参数、PATCH 仅状态、恢复版本边界 |
| `internal/testutil` | 临时数据库并行隔离、权限检查、故障注入点、假模型成功/失败/延迟/畸形/阻塞控制 |
| `tests/integration/p2_transactions_test.go` | 真实 SQLite 跨表提交与分步失败回滚、FTS 一致性和任务结果字段 |
| `tests/integration/p3_lifecycle_test.go` | 跨模块创建→更正→归档→删除→恢复→清除原文→永久清除，含任务取消与外键检查 |
| `tests/integration/p4_search_test.go` | 中文一/二字/长词、中英混合、标点、大小写、规范化等价、all/any、词项选择、覆盖率重排、低覆盖候选保留、过滤、摘要/证据/解释、生命周期、重建与相关 active 上下文 |
| `tests/integration/p5_pipeline_test.go` | 统一提交→模型→合并→检索、清除/结果提交两种竞态、接收/running/事务中断/成功后的重启、模型不可用与恢复、部分/零候选 |
| `tests/integration/readiness_test.go` | 真实数据库驱动 readyz、关闭后 503、模型地址不可达不影响就绪 |
| `tests/integration/p6_http_test.go` | 完整 HTTP 契约：无 Token/任意 Token、202 提交、任务成功/失败、列表/检索/版本/恢复/证据、归档/删除/清除、来源清除取消在途任务 |
| `tests/e2e/p6_process_test.go` | 构建并启动真实进程、同数据目录第二实例被拒绝、SIGTERM 优雅关闭、重启恢复 running 任务和持久化数据 |
| `tests/integration/sqlite_baseline_test.go` | SQLite 版本、WAL、外键、FTS5、CGO=0 兼容 |
| `tests/integration/unicode_baseline_test.go` | x/text NFC 基线与中文保持 |

测试覆盖统一提交、模型处理、记忆生命周期、关键词与混合检索、HTTP 契约、完整进程启动、
单实例锁和重启恢复。十万条容量与性能实测、备份恢复和发布准备尚未执行，不创建占位通过结果。

## 5. CI

`.github/workflows/ci.yml` 在 GitHub Actions 的 `ubuntu-24.04` 运行器上通过
`actions/setup-go` 读取 `.go-version`，缓存以 `go.sum` 为依据，然后执行 `make check`。
CI 不保存凭据或用户数据库，也不配置生产部署。远端运行结果需在实际触发后记录，不能把
本地通过当作远端 CI 通过。

## 6. 测试边界

自动化测试不访问外网、真实模型或生产数据。十万条容量与性能实测、备份恢复和发布准备尚未
执行，不能宣称性能验收通过。
