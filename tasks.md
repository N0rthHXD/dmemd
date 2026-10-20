# 可执行任务清单

## 执行说明

本清单依据《系统 AI 记忆服务架构设计.md》（v1）及 `tech-stack.md`、`project-structure.md`、`plan.md`。共保留 67 个任务，当前执行 P1–P6 的 57 项；P7（T7.1–T7.10）共 10 项暂缓。P1（T1.1–T1.9）、P2（T2.1–T2.9）与 P3（T3.1–T3.10）已于 2026-10-20 完成验收并记录；P4–P6 尚未开始，不能把规划文档、已完成阶段基础视为后续业务功能完成。

每个任务可以单独交给 Code Agent。执行时读取本任务、上述四份依据、适用的仓库指令和已完成依赖；只实现本任务范围，不提前引入后续功能。明确依赖是最低前置条件，阶段入口还要求上一阶段验收通过。当前在 T6.9 验收后结束，不自动进入 P7；用户明确恢复或指定执行后再开展暂缓任务。每项交付包含代码/文档、适当测试、运行命令和结果。测试不依赖真实模型或生产数据；CI 常规测试使用临时真实 SQLite 与可控假模型。P1–P6 原有的功能、事务、重试和恢复测试仍须完成。

状态使用 `[ ]`（未完成）、`[x]`（已验收）。执行完成后在任务下补充“执行记录：变更文件；验证命令与结果；剩余限制”。任务失败或尚有验收缺口不能勾选。未定义的版本和协议在 T1.2/T1.3 内确定并记录，不擅自更换核心技术。

## P1：基线、契约与脚手架

### [x] T1.1 建立 Go Module 与最小命令入口

- **依赖**：无。
- **描述**：按目标结构创建 Go Module、`cmd/dmemd/main.go` 和必要包；模块路径使用仓库真实地址，无法确认时记录本地模块路径约定。添加忽略规则，排除数据库、WAL/SHM、真实配置、凭据、备份和构建产物。更新 README 的开发入口。
- **验收标准**：最小命令可构建运行并退出；不创建空占位包、数据库或 HTTP 业务功能；现有架构和用户改动保持完整；忽略规则不排除迁移 SQL 或测试夹具。
- **执行记录**：模块路径使用仓库真实地址 `github.com/N0rthHXD/dmemd`；新增 `go.mod`、`go.sum`、`.go-version`、`.gitignore`、`cmd/dmemd/main.go`、`internal/idgen` 及领域类型包，README 增加构建/运行/验证入口。验证：`CGO_ENABLED=0 go build ./cmd/dmemd` 通过；`./bin/dmemd --version` 输出 `dmemd dev` 并退出；`go test ./...` 通过。限制：P1 只有启动骨架和健康检查，业务 API、数据库和 Worker 留给 P2 之后；`.gitignore` 只忽略数据库/真实配置/凭据/备份/构建产物，不排除迁移 SQL 或测试夹具。

### [x] T1.2 冻结工具链、依赖与 SQLite 能力基线

- **依赖**：T1.1。
- **描述**：选定兼容且仍受支持的 Go 精确补丁版本、`modernc.org/sqlite` 精确版本和 Unicode 规范化辅助依赖；固定 `.go-version`、`go.mod`、`go.sum`。通过最小真实数据库探针记录内置 SQLite 版本、FTS5、WAL、外键；记录供 P7 参考的一致性备份接口和候选实现路径，写入 `docs/dependency-baseline.md`。
- **验收标准**：文档无版本占位符；固定环境下 `CGO_ENABLED=0` 构建通过；FTS5 表可创建、插入和检索；探针记录实际 SQLite 版本，备份实现与演练明确留待 P7；版本声明与工具链一致，不使用浮动依赖。
- **执行记录**：固定 Go `1.27.3`、`modernc.org/sqlite v1.60.1`（内置 SQLite `3.53.4`）、`golang.org/x/text v0.43.0`，目标 `linux/amd64`；变更 `.go-version`、`go.mod`、`go.sum`、`docs/dependency-baseline.md`、`tests/integration/sqlite_baseline_test.go`、`tests/integration/unicode_baseline_test.go`。验证：`go test -v -count=1 ./tests/integration/...` 输出 `sqlite_version=3.53.4 journal_mode=wal foreign_keys=1 fts5=ok`；NFC 基线通过；`CGO_ENABLED=0 go build ./cmd/dmemd` 通过。限制：备份接口只记录 `sqlite.Backup`/`NewRestore` 候选路径，实现与演练留待 P7/T7.6、T7.7；当前只验证 `linux/amd64`。

### [x] T1.3 固定 API、模型协议与未展开语义

- **依赖**：T1.2。
- **描述**：创建 `api/openapi.yaml`、`docs/contracts/api-decisions.md`、`docs/contracts/model-provider.md`。覆盖架构全部路由和样例，明确列表默认状态与排序、创建应用归属、更新范围端点包含关系、状态 PATCH 请求体、恢复请求体、证据版本参数、查询 limit/top_k 边界默认行为、未知/缺失辅助业务字段的尽量处理策略。选定首个远端协议，列出模型/地址/凭据映射、结构化候选与处理动作、错误及超时语义。
- **验收标准**：POST content 接受字符串/对象/数组且不增加必填业务校验；PATCH 仅状态、恢复引用不存在版本返回 404；未知值处理不新增业务校验错误码；app/external 标识无唯一约束。全部路由、200/202/400/404/500/503、snake_case、UTC 时间有例子；不要求 Token/幂等键/预期版本/分页/额外响应头。远端协议有脱敏请求、成功/失败/部分可解析示例，适配器实现无需猜测字段。
- **执行记录**：新增 `api/openapi.yaml`（OpenAPI 3.0.3，覆盖架构全部 11 条路径，根级 `security: []` 表示无认证）、`docs/contracts/api-decisions.md`（列表默认 active+archived、排序、limit/top_k 默认、PATCH/恢复/证据参数、未知值处理）、`docs/contracts/model-provider.md`（OpenAI-compatible Chat Completions、候选字段/默认值、成功/失败/部分解析示例、超时与重试分类）。验证：`npx @redocly/cli@latest lint api/openapi.yaml` 返回 valid（3 条非阻断 `operation-4xx-response` 警告，来自集合列表与两个健康端点没有可定义的 4XX 响应）；PyYAML 解析成功并确认 11 条路径、无安全方案。限制：模型协议与 API 仅契约定稿，适配器、HTTP 路由和真实模型连通性分别在 P5/P6 实现；未调用真实模型。

### [x] T1.4 固定容量测试与性能验收规格

- **依赖**：T1.3。
- **描述**：在 `docs/performance.md` 定义十万条有效记忆的数据生成规则，覆盖中文/英文/混合文本、证据/版本、常见及稀有词、过滤组合、并发读写。明确测试硬件、运行器、预热、样本数、少量应用并发的具体值、P50/P95/P99 记录方式及项目验收预算。
- **验收标准**：数据种子、容量口径、并发数、延迟和资源预算均有明确值；区分本地接收/检索耗时与模型耗时；预算标明为项目补充目标，不冒称原架构 SLA；后续测试可按规格重复执行。本任务只编写规格，不执行暂缓的 P7 压测，也不将性能达标作为当前范围门禁。
- **执行记录**：新增 `docs/performance.md`，固定参考硬件（8 vCPU/16 GiB/NVMe）、种子 `20261020`、100,000 active 记忆及 archived/deleted/证据/版本分布、查询语料、4 读 + 1 写并发、冷/热/管理/提交/重建工作负载，以及 P50/P95/P99 与资源预算。验证：Python 静态检查含容量口径、种子、分位、并发和“项目补充目标/非原架构 SLA”声明，全部命中。限制：仅规格，P7/T7.5 压测与实测报告未执行；当前不宣称任何性能达标。

### [x] T1.5 建立统一开发与验证命令

- **依赖**：T1.2。
- **描述**：提供 Makefile 或薄脚本，统一格式检查、`go vet`、单元/集成测试和无 CGO 发布构建；写入 `docs/testing.md`。脚本只引用当前已有测试，后续按阶段补充。
- **验收标准**：干净检出后可按 README 执行；格式检查不静默改写源码；发布构建固定 `CGO_ENABLED=0`；默认不依赖真实模型、全局数据库或凭据；失败返回非零。
- **执行记录**：新增 `Makefile`（`fmt-check`、`vet`、`test`、`test-integration`、`test-race`、`build`、`check`、`clean`）和 `docs/testing.md`。验证：`make check` 通过，依次执行 `gofmt -l` 检查（非改写）、`go vet ./...`、`go test ./...`、`CGO_ENABLED=0 go build -trimpath ... ./cmd/dmemd`；测试使用临时目录和真实临时 SQLite，不访问真实模型或凭据。限制：P1 尚无业务/Worker 测试，集成测试目前只覆盖 SQLite 与 Unicode 基线；`test-race` 未纳入默认检查且本次未执行。

### [x] T1.6 配置持续集成

- **依赖**：T1.5。
- **描述**：按仓库实际托管平台新增 CI，锁定 Go 与目标运行器，调用统一检查和构建命令；缓存以依赖锁定文件为依据，不保存凭据或用户数据库。
- **验收标准**：本地等价命令通过；CI 配置可解析且工具链与基线一致；若当前环境不能触发远端 CI，明确记录“配置已验证、远端未运行”，不得宣称远端通过；不配置自动生产部署。
- **执行记录**：新增 `.github/workflows/ci.yml`，运行器固定 `ubuntu-24.04`，通过 `actions/setup-go` 的 `go-version-file: .go-version` 使用 `1.27.3`，缓存依赖以 `go.sum` 为依据，执行 `go mod download` 与 `make check`，无生产部署步骤。验证：PyYAML 解析成功并断言运行器、工具链文件和 `make check` 步骤；本地 `make check` 通过。限制：**配置已验证、远端 GitHub Actions 未运行**；未推送/触发远端工作流，不能宣称远端 CI 通过。

### [x] T1.7 定义领域类型、错误和能力接口

- **依赖**：T1.3、T1.5。
- **描述**：定义记忆/版本/证据/来源/任务/搜索类型，以及 `IngestService`、`MemoryService`、`SearchService`、`ExtractionProvider` 和事务能力。确定生成 UUID、UTC 时间、完整证据集合及创建应用归属的统一表示。为写入路径定义可注入的搜索文档构造能力，P4 提供生产规范化实现；先用测试实现验证原子性。
- **验收标准**：包依赖无循环；HTTP DTO 与领域类型分离；业务接口不暴露 `*sql.Tx`；事务能力可同时覆盖记忆、来源、证据、索引和任务结果；无逐表业务服务、版本冲突或业务校验层。
- **执行记录**：新增 `internal/memory/{types,ports}.go`、`internal/source/{types,ports}.go`、`internal/ingest/{types,ports}.go`、`internal/retrieval/{types,ports}.go`、`internal/idgen/idgen.go`。定义 UUID、UTC 时间、完整版本/证据集合、`CreatedByApp`/`OriginSourceID` 表示，`MemoryService`、`SourceService`、`SearchService`、`IngestService`、`ExtractionProvider`、各模块 `Store.WithTx`，以及 `ingest.Tx` 对记忆、来源、证据、搜索索引和任务结果的组合能力；`memory.DocumentBuilder` 为 P4 生产规范化留出注入点，HTTP DTO 尚未引入。验证：`go test ./...` 与 `go vet ./...` 通过（编译即验证无循环、接口不泄露 `*sql.Tx`）；`go list ./...` 列出预期包。限制：P1 只有类型/接口，真实事务原子性由 T2.2/T2.9 用 SQLite 验证；生产搜索文档构造器在 P4 接入，当前未实现逐表服务或版本冲突协议。

### [x] T1.8 实现配置、用户目录与结构化日志

- **依赖**：T1.7。
- **描述**：实现 JSON 配置、默认用户数据/配置路径、端口、模型参数、超时、数据库等待与读池参数；提供无凭据示例配置。统一创建/检查目录及文件权限；实现结构化日志基础与敏感字段不记录策略。
- **验收标准**：默认端口 18432、模型超时 60 秒、单 Worker 和三次总尝试符合规范；目录 0700、数据库/凭据 0600；配置解析失败清楚退出；日志不输出原始内容、Authorization 或模型凭据；测试使用独立临时目录。
- **执行记录**：新增 `internal/runtime/config.go`、`internal/runtime/logging.go` 及测试，`configs/dmemd.example.json`。实现 XDG 默认路径、显式配置优先、JSON 严格解析、数据/配置目录 0700、配置文件与数据库/凭据文件 0600 检查、默认 `127.0.0.1:18432`、模型 60s、单 Worker、三次总尝试、1s/5s 退避、读池 4、`busy_timeout=5s`；结构化 JSON 日志对 Authorization/API key/token/content/request/response body 做省略或脱敏并统一 UTC。验证：`go test ./internal/runtime` 通过（默认值、权限、未知字段、非回环监听、日志脱敏、UTC 时间）；`./bin/dmemd --config <0600临时配置> --check-config` 输出 `configuration valid`；二进制冒烟确认 data 目录为 0700。限制：真实数据库由 P2 创建并接入；示例配置不含真实凭据，模型凭据只做配置/日志路径验证，未调用模型。

### [x] T1.9 建立运行骨架与健康状态接口

- **依赖**：T1.8、T1.6。
- **描述**：建立 runtime 组装入口、context/信号取消、HTTP 生命周期及健康状态抽象；注册 `/healthz`、`/readyz`，数据库就绪能力先由测试替身提供，P2 连接真实数据库。
- **验收标准**：健康返回存活状态；就绪能按数据库能力返回 200/503；模型状态不影响数据库就绪；启动/关闭错误可观察，无泄漏后台循环；不提供其他占位成功的业务接口。
- **执行记录**：新增 `internal/runtime/app.go` 及测试，完善 `cmd/dmemd/main.go`：组装配置/日志/HTTP 生命周期，`signal.NotifyContext` 响应 SIGINT/SIGTERM，`http.Server.Shutdown` 有界关闭，注册 `GET /healthz`、`GET /readyz`；就绪通过可注入 `Readiness`，模型状态不参与。验证：`go test ./internal/runtime` 通过（ready 返回 200/503、health 始终 200、业务路径返回 404、Serve 取消后无泄漏退出）；二进制冒烟 `/healthz` 返回 `200 {"status":"ok"}`，`/readyz` 在 P1 返回 `503 {"status":"unavailable"}`，SIGTERM 后日志出现 `http server stopped` 且进程退出。限制：P1 尚未接入真实数据库，实际进程 readyz 固定 503；P2 用真实数据库探活替换该测试替身。未启动 Worker、业务 HTTP 路由或其他占位成功接口。

## P2：SQLite 与一致性基础

### [x] T2.1 实现 SQLite 连接与全部连接初始化

- **依赖**：T1.9。
- **描述**：实现单写连接、小型读池和关闭，统一设置 WAL、外键、FULL 同步及有界等待。把真实数据库探活接入 runtime 就绪能力；确保新建/替换连接同样初始化。
- **验收标准**：真实文件库运行；逐连接读回并验证配置；读池连接更新不丢 PRAGMA；写连接上限为一；数据库不可用时 readyz 为 503，未配置/不可达模型不使其失败；权限检查覆盖伴随文件。
- **执行记录**：新增 `internal/store/sqlite/store.go`、`internal/store/sqlite/codec.go`、`internal/store/sqlite/store_test.go`、`tests/integration/readiness_test.go`，更新 `cmd/dmemd/main.go`、`internal/runtime/app.go`、`docs/contracts/storage.md`。实现单写连接（`MaxOpenConns=1`）、默认 4 连接只读读池、DSN 逐连接设置 `journal_mode=WAL`/`foreign_keys=1`/`synchronous=FULL`/`busy_timeout`，读连接额外 `query_only=1`；数据库与目录分别以 0600/0700 创建，启动检查并收窄 WAL/SHM/journal 权限；迁移完成后 `Store.Ready` 探活写、读连接并接入 `/readyz`。验证：`go test -v -count=1 ./internal/store/sqlite` 通过 `TestOpenConfiguresWriterAndEveryReadConnection`（含 4 条读连接逐条 PRAGMA、替换连接和读连接拒写）、`TestDatabaseAndSidecarPermissions`、`TestReadyRequiresMigrationAndWorkingPools`、`TestReopenUsesExistingMigrationsAndData`、`TestOpenAcceptsURISpecialCharactersInPath`；`go test -v -count=1 ./tests/integration` 通过 `TestRealDatabaseDrivesReadinessOnly`（真实库 readyz=200、关闭后 503、模型地址不可达不影响）。二进制冒烟：临时配置启动 `./bin/dmemd`，`/healthz` 返回 `{"status":"ok"}`、`/readyz` 返回 `{"status":"ready"}`，SIGTERM 后日志出现 `http server stopped`，数据目录 0700、数据库 0600；`make check` 通过；CGO 竞争检测通过。限制：尚未实现备份（P7/T7.6）；当前只验证 `linux/amd64`；业务路由和 Worker 尚未接入。

### [x] T2.2 实现短事务能力与回滚测试设施

- **依赖**：T2.1、T1.7。
- **描述**：实现 `Store.WithTx`，提供事务作用域的组合读写能力、context 取消与执行错误传播；增加测试用可控中断点，不把故障开关暴露为生产 API。
- **验收标准**：成功提交、回调报错、SQL 失败与取消均有测试；失败后跨表没有部分结果；业务不自行嵌套/提交事务；事务作用域不执行网络请求或长计算。
- **执行记录**：新增 `internal/store/sqlite/tx.go`、`internal/store/sqlite/tx_test.go`，并在 `docs/contracts/storage.md` 固定事务契约。`Store.WithTx` 在单写连接上开启事务，`Tx` 不暴露 `Commit`/`Rollback`；`MemoryStore`、`SourceStore`、`IngestStore` 适配器把同一事务转换为各业务能力接口。验证：`go test -v -count=1 ./internal/store/sqlite` 通过 `TestWithTxCommitsAndRollsBack` 的 commit、callback error、SQL failure、cancel before commit、pre-cancelled context、nested transaction、panic rollback 子用例，以及 `TestWithTxPropagatesDeadline`；`CGO_ENABLED=1 go test -race -count=1 ./internal/store/sqlite ./internal/testutil ./tests/integration` 通过。限制：事务回调是调用约定，代码无法强制拒绝网络调用或长计算；P5 将通过阻塞假模型验证模型调用不持有写事务。嵌套检测只覆盖通过回调 context 再进入 `WithTx` 的路径，不提供跨 goroutine 的事务对象。

### [x] T2.3 创建业务表与顺序迁移机制

- **依赖**：T2.2。
- **描述**：内嵌迁移 SQL，建立 memories、memory_revisions、sources、evidence、revision_evidence、ingest_jobs、schema_migrations。包含创建应用归属、完整版本快照、redacted 状态、证据可选定位、任务尝试次数/时间/结果等契约字段，设计必要外键及索引。
- **验收标准**：空库启动和重复启动迁移可重复；迁移失败不标记成功且服务不就绪；`(memory_id, version)` 唯一；app_id/external_id 不承担身份或唯一性；不引入过期、租约、幂等键、预期版本字段和业务约束校验流程。
- **执行记录**：新增 `internal/store/sqlite/migrate.go`、`internal/store/sqlite/migrations/0001_core.sql`、`internal/store/sqlite/migrate_test.go`，更新 `docs/contracts/storage.md`。实现内嵌顺序迁移、`schema_migrations` 记录、逐文件事务、重复启动跳过、未知/改名版本拒绝；建立 sources、memories、memory_revisions、evidence、revision_evidence、ingest_jobs 及外键/索引，版本快照保存完整字段，任务保存尝试次数、时间、脱敏错误和 `memory_ids`，未增加租约/幂等键/预期版本字段。验证：`go test -v -count=1 ./internal/store/sqlite` 通过 `TestMigrationsCreateCoreAndSearchSchema`、`TestMigrationFailureRollsBackAndCanBeRetried`（失败迁移的表和触发器均回滚，修复后可重试）、`TestMigrationDetectsUnknownOrRenamedVersion`（失败后 Ready 不就绪）、`TestRevisionVersionIsUnique`、`TestSourceIdentifiersAreNotUnique`；`make check` 通过。限制：迁移只创建结构、不导入数据；已应用文件不得修改，只能追加新版本；备份与恢复属于 P7。

### [x] T2.4 创建搜索文档、FTS5 表与触发器

- **依赖**：T2.3。
- **描述**：建立 `memory_search_docs` 与外部内容 FTS5 表及同步触发器；固定记忆 ID、rowid、原始字段与规范化索引字段的映射。此任务使用明确的已规范化测试文档，生产文本构造在 T4.1 接入。
- **验收标准**：搜索文档插入/修改/删除后 FTS 同步；归档可保留索引、逻辑删除可移除；首次索引构建可运行；迁移失败回滚无半成品触发器；不依赖外部搜索服务。
- **执行记录**：新增 `internal/store/sqlite/migrations/0002_search.sql`、`internal/store/sqlite/search.go`、`internal/store/sqlite/search_test.go`；在 `internal/store/sqlite/memories.go` 实现 `UpsertSearchDocument`/`DeleteSearchDocument`。`memory_search_docs` 以 `row_id` 对应 FTS rowid、`memory_id` 唯一，保留原始标题/正文和规范化词项；`memory_fts` 为外部内容 FTS5（`unicode61`），插入/更新/删除触发器同步；`RebuildSearchIndex` 从派生文档重建索引。验证：`go test -v -count=1 ./internal/store/sqlite` 通过 `TestSearchDocumentTriggersStayInSync`（插入、更新词项、归档保留、删除移除）和 `TestRebuildSearchIndexRestoresDerivedIndex`（delete-all 后重建恢复）；`TestMigrationFailureRollsBackAndCanBeRetried` 验证失败迁移不留下表或触发器；`make check` 通过。限制：P2 使用测试构造器生成已规范化文档，生产 NFC/中文分词构造器在 T4.1；关键词查询、摘要和排序在 P4；当前重建只重建 FTS，不重新生成派生文档。

### [x] T2.5 实现来源与任务原子保存方法

- **依赖**：T2.4。
- **描述**：实现来源与 queued job 在同一事务插入，原样保存合法 JSON 内容及可选来源信息；提供来源/任务按 ID 读取方法。
- **验收标准**：两次相同提交可产生不同来源/任务；缺少 app_id/external_id 可保存；对象/数组不被强制改成字符串；任一步失败不留下孤立来源或任务；读取缺失 ID 返回统一不存在错误。
- **执行记录**：新增 `internal/store/sqlite/sources.go`、`internal/store/sqlite/jobs.go`、`internal/store/sqlite/sources_test.go`。来源内容以 BLOB 原样保存，`app_id`/`external_id` 无唯一约束；`InsertSource` 与 `InsertJob` 可在同一 `ingest.Tx` 组合；`GetSource`/`GetJob` 在事务内和存储只读入口均可按 ID 读取，缺失返回 `source.ErrNotFound`/`ingest.ErrNotFound`。验证：`go test -v -count=1 ./internal/store/sqlite` 通过 `TestSourceAndJobInsertAtomically`、`TestSourceAndJobRollbackTogether`（after_source/after_job 均无孤立行）、`TestSourceStoresRawJSONShapesAndOptionalIdentifiers`（字符串/对象/数组/null/数字/布尔逐字节保留，缺少可选标识可保存）、`TestRepeatedSubmissionsCreateDistinctSourcesAndJobs`、`TestMissingSourceAndJobUseUnifiedNotFoundErrors`；`make check` 通过。限制：统一提交服务和 HTTP 路由分别在 P5/P6；本任务只提供存储方法，不执行模型或任务状态推进。

### [x] T2.6 实现任务状态与尝试次数持久化

- **依赖**：T2.5。
- **描述**：实现按确定顺序读取 queued、切换 running 并增加尝试计数、回 queued、failed/cancelled、读取错误/结果的 SQL 能力；持久化下次执行时间，供后续 Worker 使用。
- **验收标准**：状态转换和尝试计数真实落库；succeeded/cancelled/failed 不被普通取任务重复领取；不增加租约或分布式锁；running 恢复所需字段足够，结果不存原始模型响应副本。
- **执行记录**：在 `internal/store/sqlite/jobs.go` 实现 `InsertJob`、`GetJob`、`UpdateJob`、`ClaimNextJob`、`RequeueRunningJobs`，在 `sources.go` 实现 `CancelJobsBySource`；新增 `internal/store/sqlite/jobs_test.go`。`ClaimNextJob` 只领取到期的 queued，按 `created_at ASC, id ASC` 顺序，领取时尝试次数加一并设置 started_at；终态不再领取；`RequeueRunningJobs` 保留尝试次数和 started_at；`memory_ids` 以 JSON 数组持久化，错误字段只存摘要，表中没有原始模型响应列。验证：`go test -v -count=1 ./internal/store/sqlite` 通过 `TestClaimNextJobOrdersDueJobsAndCountsAttempts`、`TestTerminalJobsAreNeverClaimedAgain`、`TestUpdateJobPersistsTerminalResultAndError`、`TestUpdateJobPersistsFailedErrorSummary`、`TestRequeueRunningJobsPreservesAttempts`、`TestCancelJobsBySourceOnlyCancelsPendingWork`；`tests/integration` 的 `TestCrossTableJobResultContainsOnlyMemoryIDs` 验证结果结构与无响应副本字段；`make check` 通过。限制：重试退避、三次总尝试上限、单 Worker 和 running 启动重排的编排属于 P5；本层不使用租约或分布式锁。

### [x] T2.7 实现快照、版本与证据存取方法

- **依赖**：T2.6。
- **描述**：实现当前快照、完整版本、来源证据、版本证据关联的 SQL 方法及列表查询，全部可在同一事务中组合。列表过滤、排序和 limit 按 T1.3 契约实现。
- **验收标准**：版本与完整证据集合可往返读出；创建应用过滤有确定存储来源；记忆默认最近 50 条且稳定排序；无游标/页码；方法只负责存取、不决定业务合并策略。
- **执行记录**：在 `internal/store/sqlite/memories.go` 实现 `InsertMemory`/`UpdateMemory`/`GetMemory`、`InsertRevision`/`GetRevision`/`ListRevisions`、`LinkRevisionEvidence`/`ListRevisionEvidence`、`UpsertSearchDocument`/`DeleteSearchDocument`；新增只读 `ListMemories` 与 `memory.Reader` 能力，全部可在 `memory.Tx` 内组合。新增 `internal/store/sqlite/memories_test.go`。列表固定 `updated_at DESC, id ASC`，零 limit 使用 50，支持 kind/state/created_by_app/updated_after/updated_before 过滤且无分页。验证：`go test -v -count=1 ./internal/store/sqlite` 通过 `TestMemoryRevisionAndEvidenceRoundTrip`（两版完整快照与各自证据集合、定位/消息/模型字段、历史顺序、缺失版本错误）和 `TestListMemoriesDefaultsFiltersAndOrdering`（默认 50 条、id 平局、过滤、墓碑读取）；`make check` 通过。限制：存储层零状态表示不过滤，HTTP 默认 `active,archived` 由 P3 服务按契约设置；版本递增、证据语义和合并策略属于 P3/P5，存储方法不决定这些规则。

### [x] T2.8 建立可复用真实数据库与假模型测试环境

- **依赖**：T2.7。
- **描述**：提供每测试独立的临时文件数据库、完整迁移和权限检查辅助；提供可控成功/失败/延迟/阻塞/畸形响应的假模型 HTTP 服务。
- **验收标准**：测试可以并行且不共享数据；可精确控制模型开始和完成以验证竞态；不依赖外网或真实凭据；失败后资源可关闭，故障中断点可定位到事务步骤。
- **执行记录**：新增 `internal/testutil/sqlite.go`、`internal/testutil/model.go`、`internal/testutil/testutil_test.go`，更新 `docs/testing.md`。`NewStore`/`NewStoreAt` 创建独立临时真实文件数据库、完整迁移并检查权限；`OpenInspector` 提供独立只读核对连接；`DocumentBuilder` 提供确定性测试搜索文档；`FaultInjector` 通过命名 checkpoint 精确定位故障步骤；`FakeModel` 为本地 `httptest` Chat Completions 服务，可脚本化成功、HTTP 失败、原始/畸形响应、延迟和阻塞，`WaitStarted`/`Release` 控制请求开始与完成，请求体原样捕获。验证：`go test -v -count=1 ./internal/testutil` 通过 `TestNewStoreCreatesIsolatedMigratedDatabases`、`TestNewStoresCanRunInParallel`（4 个并行独立库）、`TestFaultInjectorMarksPreciseStep`、`TestDocumentBuilderKeepsOriginalTextAndLowercasesTerms`、`TestFakeModelScriptsResponsesAndCapturesRequests`、`TestFakeModelDelayAndMalformedContent`、`TestFakeModelBlocksUntilReleased`；`CGO_ENABLED=1 go test -race -count=1 ./internal/store/sqlite ./internal/testutil ./tests/integration` 通过。限制：假模型只模拟已冻结的 Chat Completions 外层协议，真实模型适配器在 P5；测试设施仅被测试导入，不进入生产依赖图；假模型不访问外网或保存真实凭据。

### [x] T2.9 验证跨表事务与索引同步基础

- **依赖**：T2.8。
- **描述**：用真实 SQLite 和测试搜索文档构造器，覆盖快照/版本/证据/索引组合提交与分步故障回滚，并覆盖来源/任务接收回滚。
- **验收标准**：在写快照后、写证据后、写搜索文档后触发失败均无部分业务结果；成功时所有表一致；测试证明实际事务边界，不能只检查 mock 调用顺序；P2 必要命令全部通过。
- **执行记录**：新增 `tests/integration/p2_transactions_test.go`，使用 `testutil.NewStore`/`OpenInspector`/`FaultInjector`/`DocumentBuilder` 和真实文件 SQLite。覆盖来源+任务、记忆快照、版本、证据、版本证据关联、搜索文档和任务成功状态在同一 `ingest.Tx` 的组合提交；在 source_job、memory、revision、evidence、link、search_doc、job_result 七个检查点分别注入失败，均通过独立只读连接确认 sources、ingest_jobs、memories、memory_revisions、evidence、revision_evidence、memory_search_docs 全为 0，FTS 无匹配；提交场景确认各表各 1 行、FTS 命中 1、任务状态 succeeded 且 memory_ids 正确，并验证任务表没有原始模型响应列。验证：`go test -v -count=1 ./tests/integration` 通过 `TestCrossTableTransactionAndIndexBoundary`（commit + 7 个 rollback 子用例）与 `TestCrossTableJobResultContainsOnlyMemoryIDs`；`go test -count=1 ./...`、`make check`、`CGO_ENABLED=1 go test -race -count=1 ./internal/store/sqlite ./internal/testutil ./tests/integration` 均通过。限制：验证的是存储事务边界，不包含模型调用、Worker 调度、进程崩溃恢复或备份；这些分别留在 P5/P7。

## P3：记忆、版本与来源生命周期

### [x] T3.1 实现内部记忆创建与统一写入单元

- **依赖**：T2.9。
- **描述**：在 memory 服务实现内部创建与统一变更单元，生成 UUID、active 状态、version=1、UTC 时间；在调用方事务中写快照、版本、完整证据和派生索引。使用 T1.7 的搜索文档能力注入，生产构造器在 P4 接入。
- **验收标准**：创建后完整快照/版本/证据/索引原子可读；事务可由 Worker 外层持有且不嵌套；失败全部回滚；仅提供内部能力，不新增公共直接写入 API。
- **执行记录**：新增 `internal/memory/service.go`、`internal/memory/evidence.go`，扩展 `internal/memory/ports.go`，在 `internal/store/sqlite/memories.go` 增加读池版本/证据读取。`Create` 只接收调用方已开启的 `memory.Tx`，生成 UUID/active/version=1/UTC 时间，在同一事务写入快照、完整版本、证据关联和搜索文档；缺少证据 ID/时间时补齐，已存在证据只复用。验证：`go test -count=1 ./internal/memory` 通过 `TestCreateWritesSnapshotRevisionEvidenceAndIndex`（快照/版本/证据/搜索文档可读）与 `TestCreateRollsBackEveryTableOnCallerFailure`（回调失败后五张表均为 0）。限制：生产 NFC/分词搜索文档构造器仍由 T4.1 接入，P3 测试注入 `testutil.DocumentBuilder`；未新增 HTTP 或公共直接写入端点。

### [x] T3.2 实现内容更正与完整版本记录

- **依赖**：T3.1。
- **描述**：实现内部内容更新，保留 ID、创建时间及创建应用归属，递增版本，记录 observed_at、操作类型与原因，并同步当前快照和搜索文档。
- **验收标准**：每次有效内容变更产生完整新版本；旧版本不被覆盖；版本从当前持久化值生成，不要求调用方预期版本；证据更正不自动继承支持旧结论的证据。
- **执行记录**：在 `internal/memory/service.go` 实现 `Revise`：从持久化当前快照读取版本并加一，保留 ID、`created_at`、`created_by_app`、`origin_source_id`，带入 kind/title/content/observed_at，写完整新版本并同步搜索文档。内容更正只使用本次显式证据，不自动复制旧版本证据。验证：`go test -count=1 ./internal/memory` 通过 `TestReviseKeepsHistoryAndDoesNotInheritOldEvidence`（v1 证据保留、v2 无旧结论证据、创建元数据不变）。限制：调用方仍须在 ingest 层完成候选合并决策；P3 不提供预期版本条件或冲突协议。

### [x] T3.3 实现证据定位与纯证据变更版本

- **依赖**：T3.2。
- **描述**：根据来源和可选消息标识定位原文片段，记录找到的位置；无法定位时保留来源关联。实现补充证据生成新版本，并区分完整新证据集合、重复关联和旧结论证据。
- **验收标准**：定位失败不拒绝整批；消息标识/片段/模型名均可选；只增加证据也生成版本；每版本可独立读出完整集合；完全重复、未改变集合的处理不制造无意义版本；不要求调用方提供证据。
- **执行记录**：新增 `internal/source/locate.go`（`LocateQuote`、`NewEvidence`），在 `internal/memory/evidence.go` 实现证据补齐、去重、复用与集合比较，在 `Revise` 中区分完整更正集合与 `add_evidence` 合并。定位按可选消息 ID 搜索解码后的 JSON，返回点分字段路径与 rune 偏移；无法定位仍保留来源关联。验证：`go test -count=1 ./internal/source ./internal/memory` 通过 `TestLocateQuoteFindsMessageAndTopLevelText`、`TestLocateQuoteHandlesRootStringAndInvalidJSON`、`TestAddEvidenceCreatesVersionAndIgnoresDuplicates`（补证生成 v2、重复补证不增版本、同集合更新不增版本）。限制：定位为尽力而为，不解析外部消息 ID 协议；P5 负责从来源构造候选证据。

### [x] T3.4 实现记忆列表与当前快照查询

- **依赖**：T3.3。
- **描述**：提供 memory 服务当前快照和管理列表，按契约支持类型、状态、创建来源应用等过滤以及可选 limit，区分读取墓碑与全文搜索规则。
- **验收标准**：不存在 ID 返回不存在错误；已删除快照可按契约作为墓碑读取；列表默认最近更新 50 条；无下一页信息；不将 app_id 当作隔离或授权条件。
- **执行记录**：在 `internal/memory/service.go` 实现 `Get`/`List`：`Get` 直接返回当前快照（含 deleted 墓碑）；`List` 空状态默认 `active,archived`，其余过滤和默认 50 条、`updated_at DESC, id ASC` 由 `internal/store/sqlite/memories.go` 执行，无分页字段。验证：`go test -count=1 ./internal/memory ./internal/store/sqlite` 通过 `TestLifecycleArchiveDeleteRestoreAndEvidenceQueries`（墓碑读取、默认列表排除 deleted）、`TestListMemoriesDefaultsFiltersAndOrdering`（50 条、过滤、稳定排序）。限制：列表是管理读取能力，不代表 P4 关键词检索；`app_id` 仅过滤字段，不做授权隔离。

### [x] T3.5 实现历史版本与版本证据查询

- **依赖**：T3.4。
- **描述**：提供版本列表、指定版本、当前/指定版本证据查询；按契约处理 limit 与证据版本参数。
- **验收标准**：可按版本追溯原始来源；历史内容/证据与当时快照一致；不存在记忆或版本返回不存在错误；证据引用不依赖调用方额外字段；查询不修改历史。
- **执行记录**：扩展 `internal/memory/ports.go` 的 `Reader`，在 `internal/store/sqlite/memories.go` 增加读池 `GetRevision`/`ListRevisions`/`ListRevisionEvidence`，并复用同一查询逻辑供事务内使用；`internal/memory/service.go` 实现 `ListVersions`、`GetVersion`、`GetEvidence`，版本为 0/负数时取当前版本，显式版本不存在返回 `memory.ErrNotFound`。验证：`go test -count=1 ./internal/memory ./internal/store/sqlite` 通过 `TestLifecycleArchiveDeleteRestoreAndEvidenceQueries`（当前/历史证据、缺失版本）、`TestMemoryRevisionAndEvidenceRoundTrip`（完整快照与证据集合、历史顺序）。限制：版本列表默认/显式 limit 无业务上限；P3 未提供 HTTP 参数解析。

### [x] T3.6 实现归档与状态恢复

- **依赖**：T3.5。
- **描述**：实现状态修改，归档和重新激活都使用统一变更单元记录版本；同步派生搜索文档状态，不实现自动到期任务。
- **验收标准**：状态变化产生新版本；归档条目仍可供显式检索，默认查询不返回；重新 active 后可以默认检索；无状态变化按契约处理且不无端增加版本。
- **执行记录**：在 `internal/memory/lifecycle.go` 实现 `UpdateState`/`ChangeState`：未知或与当前相同的状态不产生版本；归档与重新激活复制当前完整证据集合并生成 `archive`/`activate` 版本，归档同步保留搜索文档并更新状态。验证：`go test -count=1 ./internal/memory` 通过 `TestLifecycleArchiveDeleteRestoreAndEvidenceQueries`（归档/激活版本与证据、同状态不增版本、默认管理列表行为）。限制：不运行自动到期任务；默认关键词检索排除归档由 P4 实现。

### [x] T3.7 实现逻辑删除与墓碑

- **依赖**：T3.6。
- **描述**：将删除实现为 state=deleted 的新版本，保留当前墓碑、历史和证据，事务内移除派生搜索条目。
- **验收标准**：历史仍可读取；索引中没有 deleted 条目；任何全文检索不返回删除记录；失败时状态、版本和索引全部回滚。
- **执行记录**：在 `internal/memory/lifecycle.go` 实现 `Delete`/`deleteInTx`：以 `state=deleted` 生成新版本，保留墓碑、历史和证据，并在同一事务删除派生搜索文档；重复删除不增版本。验证：`go test -count=1 ./internal/memory` 通过 `TestLifecycleArchiveDeleteRestoreAndEvidenceQueries`（墓碑可读、索引移除、历史保留）与 `TestDeleteRollsBackStateRevisionAndIndexTogether`（索引删除失败后状态、版本和索引全部回滚）。限制：任何全文检索必须排除 deleted 的规则由 P4 查询实现；P3 只保证索引中不再有条目。

### [x] T3.8 实现从历史版本恢复

- **依赖**：T3.7。
- **描述**：读取指定旧版本并复制内容为当前新版本，按契约恢复到 active，维护完整证据与索引；已清除原文不从历史恢复。
- **验收标准**：新版本号高于恢复前当前版本；被恢复的历史版本保留；归档/删除记忆可重新进入默认检索；不存在版本返回不存在错误；已 redacted 来源及证据片段不会重新出现原文。
- **执行记录**：在 `internal/memory/lifecycle.go` 实现 `Restore`/`RestoreVersion`：读取指定历史版本，复制内容、`observed_at`、创建应用和证据集合为新的 `active` 版本，版本号高于恢复前当前值，旧版本不被改写；已清除证据只保留 `redacted` 关联。验证：`go test -count=1 ./internal/memory ./tests/integration/...` 通过 `TestLifecycleArchiveDeleteRestoreAndEvidenceQueries`（删除后恢复、版本号递增、证据复制）与 `TestSourceRedactionKeepsBodyAndPreventsEvidenceRestoration`（清除后恢复不重现片段）。限制：恢复不绕过来源清除；已发出的远端模型请求不在本任务撤回。

### [x] T3.9 实现永久清除记忆

- **依赖**：T3.8。
- **描述**：事务内删除指定记忆的当前、历史、版本关联、专属证据和索引；共享证据仅在无其他引用时清除，来源记录按架构保留。
- **验收标准**：清除后记忆及各版本不存在、搜索无结果；共享来源和其他记忆/证据关联完整；外键检查通过；中途失败全部回滚，不误删原始来源。
- **执行记录**：在 `internal/store/sqlite/memories.go` 实现 `PurgeMemory`，在 `internal/memory/lifecycle.go` 实现 `Purge`/`PurgeInTx`：同一事务删除搜索文档、仅被该记忆引用的证据、当前快照及级联历史/版本证据关联；共享证据和来源记录保留。验证：`go test -count=1 ./internal/memory ./tests/integration/...` 通过 `TestPurgeRemovesDedicatedDataAndKeepsSharedEvidence`（专属证据删除、共享证据/来源保留、外键干净、清除回滚）与集成 `TestP3LifecycleVersionEvidenceRedactionAndPurge`。限制：永久清除是数据库行删除，不承诺文件块取证级擦除；备份副本不在 P3 处理。

### [x] T3.10 实现来源查询、原文清除与任务取消

- **依赖**：T3.9、T2.6。
- **描述**：实现来源查询和清除服务；同一事务清空来源原文、全部关联证据片段及定位中的原文副本，保留标识和 redacted；把 queued/running 相关任务设为 cancelled，清理会暴露原文的任务错误字段。记忆正文继续保留。
- **验收标准**：当前/历史证据查询不再泄露片段；来源仍返回 ID 和 redacted；queued/running 不再可正常执行；succeeded 不重跑；事务失败不出现“原文清了但任务未取消”；清除重复调用的行为按契约稳定，不宣称文件块取证级擦除。
- **执行记录**：新增 `internal/source/service.go`，在 `internal/source/ports.go` 定义读能力与任务错误清理，在 `internal/store/sqlite/sources.go` 实现 `ScrubJobErrorsBySource`。`Redact` 同一事务清空来源原文、全部历史证据片段/定位并保留 `redacted`，取消 queued/running 任务并把终态非空错误摘要替换为固定脱敏说明；重复清除稳定。验证：`go test -count=1 ./internal/source ./tests/integration/...` 通过 `TestRedactClearsSourceEvidenceAndCancelsOnlyPendingJobs`（只取消未完成任务、终态错误清理、重复清除不改时间）、`TestRedactRollsBackWhenAnyStepFails`（原文、证据和任务状态全部回滚）、`TestP3LifecycleVersionEvidenceRedactionAndPurge`。限制：清除不撤回已发送的远端请求，也不宣称存储介质擦除。

## P4：中文关键词检索

### [ ] T4.1 实现文档/查询共用规范化并接入写入

- **依赖**：T3.10。
- **描述**：实现 NFC、英文大小写归一、英文数字按词、连续汉字逐字 token、标点边界；提供搜索文档构造器并接入 T3.1 的写入能力，替换测试注入用于生产的装配。
- **验收标准**：文档与查询共享相同规则；规范化等价文本结果一致；一字/两字/长中文及中英文混合测试通过；所有创建/内容更正/恢复路径均写入同一规则的标题和正文文档；保留原正文用于摘要。

### [ ] T4.2 实现普通关键词到 FTS 的安全编译

- **依赖**：T4.1。
- **描述**：将连续汉字编译为相邻短语，按空白分组并用 all/any 组合，处理标点边界；正确转义 FTS 字面量并参数绑定，不把用户文本当作 FTS 表达式。
- **验收标准**：“记忆”只匹配相邻“记”“忆”；一字查询工作；引号、括号、星号、OR 等输入不会成为用户可控运算符；空查询按契约引导管理列表语义；不增加同义词或语义推断。

### [ ] T4.3 实现搜索过滤与固定排序

- **依赖**：T4.2。
- **描述**：实现 FTS 与类型、状态、创建应用、更新时间范围过滤；标题/正文 BM25 权重 4/1，按 bm25 升序、updated_at 降序、id 升序，输出 score=-bm25 和默认 top_k=20。
- **验收标准**：相同查询结果稳定；分数越大越相关但不解释为可信度；默认 active，显式 archived 可查，deleted 绝不返回；all/any 和组合过滤可验证；无跨请求快照或分页。

### [ ] T4.4 实现可读摘要、证据引用与按需解释

- **依赖**：T4.3。
- **描述**：从原始记忆正文生成 snippet、命中词与当前版本 evidence_refs；仅 explain=true 时包含 ranking 与 field_weights 等解释。
- **验收标准**：中文摘要保持连续原文而非拆字索引；混合 Unicode 不截断非法字符；不嵌入 HTML；explanation 默认省略；证据引用对应当前版本且已清除来源不返回片段副本。

### [ ] T4.5 验证检索与记忆生命周期一致性

- **依赖**：T4.4。
- **描述**：通过真实服务/SQLite 场景验证新增、内容更正、纯证据变更、归档、删除、恢复及永久清除后的检索表现。
- **验收标准**：旧内容被更正后不继续以旧正文命中；归档仅显式查出；删除/永久清除始终不可查；恢复重新可查；事务故障不会留下幽灵索引；证据变化后引用及时更新。

### [ ] T4.6 实现索引重建能力

- **依赖**：T4.5。
- **描述**：从业务快照按当前规则重建全部未删除记忆的搜索文档和 FTS，提供可由后续维护命令调用的能力；明确迁移/规则变化时重建时机与失败处理，记录结果日志。
- **验收标准**：清空/破坏派生索引后可重建；归档保留、deleted 排除；重建前后命中/过滤/排序一致；中断不会被记录为成功；日志不包含记忆原文。

### [ ] T4.7 完成中文与排序边界回归

- **依赖**：T4.6。
- **描述**：建立固定输入语料，覆盖一字、两字、长中文、中英文、英文数字、大小写、规范化等价输入、标点、FTS 特殊字符、标题加权、时间与 ID 平局规则。
- **验收标准**：预期行为直接来自契约且有正反例；摘要可读、分数方向正确；不依赖随机模型输出；全部检索回归通过。

### [ ] T4.8 提供后台相关 active 记忆检索能力

- **依赖**：T4.7。
- **描述**：向 ingest 暴露基于当前输入关键词的相关 active 记忆查询，用于候选提取/合并上下文；按 T1.3 固定选择与上下文组装方式，不修改公共搜索协议。
- **验收标准**：上下文只含当前来源及相关 active 记忆，不载入无关来源/全库；archived/deleted 不作为自动合并目标；无匹配可返回空上下文；不强制截断或拒绝用户提交内容。

## P5：模型与持久化处理链

### [ ] T5.1 实现统一接收和任务查询服务

- **依赖**：T4.8。
- **描述**：实现 ingest 服务接收合法 JSON 内容，调用来源/任务原子保存并返回 source_id、job_id、queued；实现任务状态、错误摘要与生成 memory_ids 查询。
- **验收标准**：文本/对话/结构化内容相同路径；缺少来源标识可接收；返回前已持久化且不等待模型；重复提交创建普通新任务；无直接结构化写入捷径。

### [ ] T5.2 实现首个远端模型协议适配器

- **依赖**：T5.1、T1.3。
- **描述**：按固定协议实现 ExtractionProvider，传递模型标识、当前输入及相关记忆上下文；使用模型服务凭据和可取消 context，单次调用默认超时 60 秒。
- **验收标准**：假模型核验请求和响应映射；超时、连接失败、服务错误可返回任务处理错误；不读取本地 API Token 作为模型凭据；不发送无关来源/全库；日志不输出凭据或完整请求/响应。

### [ ] T5.3 实现模型结果容错解析

- **依赖**：T5.2。
- **描述**：解析候选与处理建议；缺失辅助字段使用契约默认值，可用结果继续处理；对空候选、部分不可用、整体不可解析分别处理，不引入业务校验层。
- **验收标准**：零候选为成功空列表；部分候选可用时不会整批无理由失败；整体不可解析可记录脱敏原因；默认字段确定且可测试；不存储完整模型响应作为错误文本。

### [ ] T5.4 实现模型处理编排与相关上下文

- **依赖**：T5.3、T4.8。
- **描述**：编排来源加载、关键词相关记忆选择、模型调用和候选整理；固定 observed_at 缺失时的接收时间回退规则，全部网络和长计算在写事务外完成。
- **验收标准**：假模型检查只收到当前输入与相关上下文；blocked 模型不持有数据库写事务；调用失败不改已有记忆；任意 content 形态不依赖独立提取模式或调用方证据。

### [ ] T5.5 实现新增与同事实更新决策

- **依赖**：T5.4。
- **描述**：实现同一事实明确更新关系时更新原记录、独立时间事件分别新增、无法确认关联时新增的决策；结合 observed_at 和接收时间处理新旧关系，记录操作原因。
- **验收标准**：同事实更正保留 ID 并产生新版本；相似但独立事件不错误覆盖；时间较旧内容不无理由覆盖明确新事实；不确定关联按新增处理；不创建调用方逻辑唯一键或版本前置条件。

### [ ] T5.6 实现补证、跳过与 active 合并边界

- **依赖**：T5.5。
- **描述**：重复内容尽量合并到现有 active 记忆并补证；已具备相同完整证据时可跳过。固定在候选计算后目标被归档、删除或永久清除时的处理，提交时按实际当前状态应用 active 合并规则，不增加冲突重算协议。
- **验收标准**：重复提交不强制形成重复记忆；新来源补证产生版本；完全无变更可跳过；归档/删除不被后台自动恢复或更新为 active；目标已不存在按契约新增或跳过且不返回版本冲突错误；更正不继承旧结论支持证据。

### [ ] T5.7 实现处理结果与任务成功的原子提交

- **依赖**：T5.6。
- **描述**：一个短事务内检查任务取消状态，读取当前持久化状态，执行候选的快照/版本/证据/索引变化，并写入 succeeded 与 memory_ids；复用 memory 统一变更单元，禁止内层提交。
- **验收标准**：多候选任务要么变更与成功状态全部提交，要么全部回滚；记忆写入后、任务成功前故障不会留下已落库记忆；cancelled 不提交任何候选；零候选同样原子成功；事务内不调用模型。

### [ ] T5.8 实现单 Worker 调度

- **依赖**：T5.7。
- **描述**：实现一个顺序 Worker，按持久化顺序及可执行时间取 queued 并处理，空队列等待可取消；配置和 runtime 不允许首版无意启动多个 Worker。
- **验收标准**：阻塞第一个任务时第二个不并行调用模型；HTTP/读取不被阻塞；停止能退出等待；已成功、失败终态、取消任务不重复领取；无内存队列替代持久化事实来源。

### [ ] T5.9 实现三次总尝试与失败退避

- **依赖**：T5.8。
- **描述**：使用持久化尝试计数，暂时错误后按初始化间隔回 queued，到第三次仍失败标记 failed；实现脱敏错误摘要与下次执行时间，重试等待可被关闭取消。
- **验收标准**：首次也计入三次；失败/超时/整体不可解析按协议处理且不会无限重试；到上限无第四次调用；重启不重置预算；现有查询/管理继续可用；日志不包含输入原文或服务密钥。

### [ ] T5.10 实现启动恢复与已成功任务保护

- **依赖**：T5.9。
- **描述**：Worker 启动前把遗留 running 重排 queued，保留尝试计数；已经耗尽预算的任务不再调模型并落为 failed；成功或取消任务不恢复执行。
- **验收标准**：关闭后重开同一真实数据库可继续 queued/running；计数不归零；succeeded 不生成第二批版本；最大次数边界明确；不引入租约、心跳或分布式协调。

### [ ] T5.11 验证清除原文与在途模型结果竞态

- **依赖**：T5.10、T3.10。
- **描述**：用阻塞假模型控制“已开始请求 → 清除来源 → 模型返回”和“成功提交 → 清除来源”两个顺序，检查取消状态与结果提交事务的协作。
- **验收标准**：清除先提交时模型结果不写任何记忆/证据/索引，任务保持 cancelled；结果先提交时正文保留但原文/历史片段清除；之后查询不重新出现片段；明确已发出的远端请求不等于可以撤回，不把本地取消宣传成远端擦除。

### [ ] T5.12 完成崩溃恢复与模型不可用集成验证

- **依赖**：T5.11。
- **描述**：进程级覆盖接收后、running 后、模型返回后、结果事务中断及结果提交后崩溃；测试模型持续不可用、失败后恢复、无候选和部分可解析输入。
- **验收标准**：重启后无半份记忆、无多余版本、无丢失 queued 来源；成功任务不重跑；在途尝试计数保留；模型不可用期间可提交/查询/管理；所有结果和状态可通过服务接口检查。

## P6：HTTP 完整接口与调用端联调

### [ ] T6.1 实现 HTTP JSON 基础与错误映射

- **依赖**：T5.12。
- **描述**：实现 net/http 路由基础、DTO、UTF-8 JSON 响应及简单错误对象；JSON 解码失败映射 400，不存在映射 404，执行错误映射 500，暂不可用映射 503。
- **验收标准**：无认证、权限、业务字段、额外请求大小中间件；不存在版本冲突/业务校验专用错误；有响应体设置 application/json；不增加 Location、ETag 或版本控制头；未知/缺失业务辅助值按 T1.3 处理。

### [ ] T6.2 接入统一提交与任务查询路由

- **依赖**：T6.1。
- **描述**：接入 POST /v1/memories 和 GET /v1/ingest-jobs/{id}，复用业务服务，不在 HTTP 内执行模型调用。
- **验收标准**：提交 202 且 JSON 含 source_id/job_id/status；缺少 Token、任意 Token、缺少 app_id 均成功接收；无幂等键/预期版本/证据字段要求；阻塞模型时 POST 已返回；任务完成可读 memory_ids，失败原因从任务查询返回。

### [ ] T6.3 接入记忆列表与当前快照路由

- **依赖**：T6.2。
- **描述**：接入 GET /v1/memories、GET /v1/memories/{id}，映射过滤和可选 limit。
- **验收标准**：默认最近更新 50 条；一次返回 items，无页码/游标/下一页；跨应用可读相同记忆；时间和字段命名符合契约；缺失 ID 返回简单 404。

### [ ] T6.4 接入状态修改、删除与永久清除路由

- **依赖**：T6.3。
- **描述**：接入 PATCH /v1/memories/{id} 与 DELETE 同路径，按 purge=true 区分永久清除；PATCH 只调用状态变更能力。
- **验收标准**：归档/激活、逻辑删除及永久清除响应符合架构；PATCH 中附带 content 不形成内容直接写入捷径，未知辅助字段按契约处理；不要求版本头；共享来源保留。

### [ ] T6.5 接入版本、恢复与证据路由

- **依赖**：T6.4。
- **描述**：接入版本列表、指定版本、POST restorations、GET evidence，准确使用契约的请求体、limit 和证据版本参数。
- **验收标准**：旧版本可读且恢复生成新版本；当前/历史证据均可追溯；恢复不存在版本为 404；清除后不经接口泄露原文副本；无分页和版本匹配协议。

### [ ] T6.6 接入关键词检索路由

- **依赖**：T6.5。
- **描述**：接入 POST /v1/memories/search，映射 query、match、filters、top_k、explain，并返回契约中的 items。
- **验收标准**：默认 all/20/active；返回 memory/score/snippet/matched_terms/evidence_refs，explanation 按需出现；静态 search 路由不会被当作记忆 ID；无搜索分页；中文与特殊字符测试复用真实 SQLite。

### [ ] T6.7 接入来源查询与原文清除路由

- **依赖**：T6.6。
- **描述**：接入 GET /v1/sources/{id} 和 DELETE 同路径，分别返回原文/已清除状态及清除结果。
- **验收标准**：未清除来源返回原始 content 形态；清除后返回 redacted 且保留标识；未完成任务取消且不能落库；不新增来源创建或提取提交接口；任意本地应用按相同规则访问。

### [ ] T6.8 完整组装启动、单实例与优雅关闭

- **依赖**：T6.7、T1.9。
- **描述**：组装配置→数据库/迁移→恢复任务→HTTP/Worker 的启动顺序；以用户数据目录为单位实现本地单实例保护，异常退出后可重新启动；关闭时停止接收/领取、取消模型等待、结束事务并关闭连接。
- **验收标准**：相同用户数据目录第二实例不能同时运行，且不依赖单纯端口冲突判断；异常退出不遗留不可恢复锁；数据库未就绪 readyz=503，模型不可用仍就绪；SIGTERM 后无半事务、重启可恢复任务；无分布式租约。

### [ ] T6.9 完成调用端示例与 HTTP 契约回归

- **依赖**：T6.8。
- **描述**：更新 OpenAPI 与 README，提供 curl 提交文本/对话/结构化内容、查询任务、检索、归档、历史恢复、清除来源的示例；建立完整 API 契约集成测试。
- **验收标准**：架构 API 清单所有方法/路径均覆盖；无需业务自定义请求头，只有带 JSON 请求体需 Content-Type；无 Token 和任意 Token 都可全流程使用；列表、错误、状态码、时间格式符合契约；示例不含真实凭据和用户内容。

## P7：质量、容量、备份与发布准备（暂缓）

T7.1–T7.10 全部保留原编号及任务定义，当前不执行、不计入 P1–P6 完成门禁，也不能标记为已完成。以下验收标准仅在用户恢复或指定执行相关任务后适用。

### [ ] T7.1 建立架构验收追踪矩阵

- **依赖**：T6.9。
- **描述**：在 `docs/testing.md` 把架构“验收与运行保障”每条映射到任务、具体测试、执行命令及结果，识别覆盖缺口并在本阶段对应任务补齐。
- **验收标准**：统一提交、事务、版本来源、重试/崩溃、检索、生命周期、HTTP、容量恢复均有可定位证据；未运行明确标注，不能用任务勾选代替测试结果；不引入无关重构。

### [ ] T7.2 验证权限、日志与原文清除隐私边界

- **依赖**：T7.1。
- **描述**：覆盖目录/数据库/凭据/WAL/SHM 权限，捕获请求、模型错误、重试、索引重建日志，验证原始内容与凭据默认不记录；复查来源及当前/历史证据清除。
- **验收标准**：0700/0600 要求满足且伴随文件未放宽；测试标记原文/密钥不出现在日志或接口清除结果；不把应用间共享误判为漏洞而增加认证；不把逻辑清除宣称为存储介质或远端副本擦除。

### [ ] T7.3 验证生命周期、恢复与取消的跨模块竞态

- **依赖**：T7.2。
- **描述**：使用可控同步覆盖后台更新与状态管理/永久清除、旧版本恢复与来源清除、队列领取与来源取消等交错顺序，并在支持的平台执行竞争检测。
- **验收标准**：按实际写入顺序生效；不恢复归档/删除记录、不从旧版本恢复已清除片段；cancelled 不落模型结果；无数据竞争、死锁或版本唯一键冲突泄露；不添加预期版本与冲突重算协议。

### [ ] T7.4 验证存储故障与短事务降级行为

- **依赖**：T7.3。
- **描述**：注入连接不可用、写入失败、有界锁等待、模型长时间阻塞及关闭中断，验证接收事务、业务结果事务、错误响应和就绪状态。
- **验收标准**：失败无部分来源/任务或记忆/版本/索引；锁等待有界；阻塞模型时读取和独立管理写入仍工作；数据库故障不虚报接收成功；恢复后状态和索引一致，无凭据泄露。

### [ ] T7.5 执行十万条容量与性能验收

- **依赖**：T7.4、T1.4。
- **描述**：按固定种子构造十万条有效记忆及约定历史/证据，运行冷热查询、过滤、Top-K、少量并发提交/读取，记录延迟分位数、CPU/内存、数据库/WAL/索引体积、索引重建耗时。
- **验收标准**：报告含硬件/版本/数据分布/并发/命令/原始结果位置和预算对照；模型延迟单独统计；满足既定预算才能标记通过；不通过时定位并仅优化相关瓶颈，复测，不事后静默放宽预算。

### [ ] T7.6 实现并验证 SQLite 一致性备份

- **依赖**：T7.5、T1.2。
- **描述**：按已冻结驱动能力实现 SQLite 一致性备份及本地维护命令，限定备份目标权限，覆盖业务写入和 WAL 活跃期间的备份。
- **验收标准**：备份可独立打开并通过结构/完整性检查；快照/版本/证据/任务一致；不把运行时复制主文件当备份；备份不暴露为新业务 API；失败不留下被误认成完整备份的产物。

### [ ] T7.7 实现离线恢复与索引重建维护命令

- **依赖**：T7.6、T4.6。
- **描述**：提供停止服务后恢复备份、检查结构/完整性、重建并校验索引的本地维护命令；复用单实例保护防止在线覆盖数据库，验证恢复后的任务重排语义。
- **验收标准**：源备份校验失败不覆盖现有库；恢复前后的记忆/版本/来源/任务可对照；FTS 重建可恢复同样检索结果；恢复后 running 按预算重新排队、succeeded 不重跑；权限正确；过程可在隔离目录重复演练。

### [ ] T7.8 编写用户级部署与运维手册

- **依赖**：T7.7。
- **描述**：完善 `docs/operations.md` 与 Linux 用户级 systemd 服务模板，说明二进制安装、受限配置、远端模型参数、数据路径、启动停止、健康检查、故障排查、备份恢复和版本升级前备份。
- **验收标准**：普通用户可按文档在独立目录完成安装/启动/停止/恢复；默认回环监听，无 root 服务或认证平台依赖；服务模板正确处理退出信号与单实例；凭据不写入示例、命令历史示例或仓库；数据库迁移不承诺未经测试的自动降级。

### [ ] T7.9 准备可复现发布包与部署冒烟

- **依赖**：T7.8、T1.6。
- **描述**：按基线平台生成无 CGO 二进制、版本/构建元信息、示例配置、服务模板、使用说明和校验和；在干净临时用户目录执行提交→任务完成→检索→归档/恢复→重启冒烟。
- **验收标准**：发布包不含真实数据/凭据；构建命令与工具链记录完整；目标环境无需 CGO 工具链或外部数据库/队列；冒烟和恢复步骤通过；仅准备本地可审阅产物，不把未执行的对外发布记为完成。

### [ ] T7.10 完成全量验收与交接

- **依赖**：T7.9。
- **描述**：执行统一格式/静态检查/测试/发布构建，汇总容量、故障恢复、隐私、API 契约与部署证据；更新任务完成记录、README 和运行手册，记录剩余非阻断限制。
- **验收标准**：67 项状态均有对应证据，全部架构验收无阻断缺口；固定版本、配置、接口和发布产物一致；后续开发者无需会话背景即可构建、运行、测试、备份和恢复；未执行的远端 CI 或真实模型试运行明确列出，不虚报结果。

## 单任务 Prompt 模板

```text
执行 tasks.md 中的 <任务 ID>。
先阅读《系统 AI 记忆服务架构设计.md》、tech-stack.md、
project-structure.md、plan.md，以及适用的仓库指令。
确认任务属于当前执行范围；P7 暂缓，除非用户明确恢复或指定执行。
确认该任务依赖已有验收记录；按本任务描述实现，保持架构约束。
运行任务验收所需的最小有效验证，更新相关契约和文档。
交付变更文件、验证命令及结果、限制，并更新本任务状态和执行记录。
不要把未实现、未运行或依赖不满足的内容标记为完成。
```
