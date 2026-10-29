# 测试与统一验证命令

[文档索引](README.md) · [技术与使用指南](technical-guide.md#构建与验证)

本文说明 v2 当前可执行的验证入口与覆盖边界，不沿用旧版测试数量或容量记录作为新架构验收。

## 1. 环境与隔离

- Go 使用 `.go-version` 固定的 `1.27.3`；首次下载锁定模块需要网络，默认测试不访问真实模型。
- 生产构建与默认测试不需要 CGO；race 检测需要 CGO 工具链。
- 测试用 `testing.T.TempDir()`、临时真实 SQLite、本地 httptest/FakeModel，不访问生产数据或凭据。
- 配置文件权限 `0600`；默认 XDG 目录通过环境变量指向临时目录，不改写用户数据。
- `internal/testutil` 提供数据库、只读检查连接、确定性文档构造器、故障注入点与可控假模型。

## 2. 当前命令

```sh
make fmt-check        # 只报告 gofmt 差异，不改源码
make vet              # go vet，排除 web/node_modules 中无关 Go 包
make test             # 当前启用的单元/集成/进程测试，不包含 legacy
make test-integration # 当前未标 legacy 的 tests/integration
make test-quality     # 当前报告无启用质量包并跳过；不是质量通过
make test-race        # 可选，CGO_ENABLED=1
make build            # CGO_ENABLED=0，输出 bin/dmemd
make check            # fmt-check + vet + test + test-quality + build
```

`make build` 可传 `VERSION=...`。前端单独执行：

```sh
make web-install
make web-check        # OpenAPI 生成类型一致性、tsc、lint
make build-ui         # 构建前端并嵌入 Go 二进制
```

不手工修改 `web/src/shared/api/schema.gen.ts`；OpenAPI 类型变更由前端生成脚本检查。
默认 Go 验证不隐式安装 Node，也不运行真实模型或前端构建。

## 3. legacy 套件与未验证项

下列文件仍在仓库，但带 `//go:build legacy`，不参与默认构建和测试：

- `tests/quality/*.go`：旧分类/摘要/冲突/Skill 标注回放与 live 评估。
- `tests/capacity/capacity_test.go`：旧一万条原子、11,200 治理任务、1,200 派生容量场景。
- `tests/integration` 中旧 ingest pipeline、search/hybrid、生命周期、时间线、事实组自动收敛、
  association/layered recall、transactions 和完整 HTTP 契约等文件。

当前 `make test-capacity` 虽仍存在，但没有可启用的普通 Go 测试文件，不能执行 v2 容量验证。
旧 `DMEMD_CAPACITY_*` 和 `DMEMD_EVAL_*` 命令不应作为当前验收入口推荐；也不要简单加
`-tags legacy` 当作受支持测试，旧 API/类型可能已移除，需要先重构套件。

`make check` 成功不意味着质量/容量套件实际运行。新架构仍需独立的 L0 锚定覆盖率、错误覆盖率、
治理关系准确率、摘要忠实性、stale/删除传播、各阶段 token 成本与容量评估。旧版记录不能证明
v2 一万条或十万条、长期高并发、备份恢复、真实模型语义质量已通过。

## 4. 当前阶段测试覆盖

| 位置 | 当前关注点 |
| --- | --- |
| `internal/source/{span,locate,service}_test.go` | 稳定句子/子句片段、rune 定位、context-only 拒绝、代码 Evidence、清除事务 |
| `internal/observation/*_test.go` | extractive/normalized 区分、原文不被模型正文替代、绑定失败 pending、来源/类型默认值 |
| `internal/extraction/*_test.go` | 有界窗口、只读上下文、同 Source span/正文去重，不读取当前 Memory |
| `internal/ingest/processor_observation_test.go` | 只保存 L0、Evidence/治理任务同事务、无关 Memory 不进入提取、清除竞态 |
| `internal/ingest/{service,worker}_test.go` | 接收事务、任务状态、暂时失败等待、协议隔离、配置等待、单 Worker、启动恢复 |
| `internal/governance/{rule_match,observation_handler,model,relation}_test.go` | 规则优先、临时引用、重复/补证不改正文版本、enrich 修订、supersedes 保留旧对象、pending、重放和 revision 保护 |
| `internal/governance/invalidation_test.go` | 依赖图跨层失效、观察撤回、Source 清除传播、仅删除无支持 Memory、遗忘后目标停用 |
| `internal/aggregation/{service,guidance}_test.go` | L1→L2→L3、去抖目标、下层 typed inputs、L4 procedure 资格、经验报告与通用依赖 |
| `internal/artifact/*_test.go` | target/bucket 编码、层级、typed dependencies 和无歧义目标 |
| `internal/memory/*_test.go` | 当前视图、完整历史/证据、版本、归档/删除/恢复、purge、共享来源、详情引用与事务回滚 |
| `internal/retrieval/*_test.go` | NFC/分词/停用词与安全 OR 编译、空查询、六层选择、每层独立结果、stale 回退、预算和降级 |
| `internal/model/{parse,remote}_test.go` | extraction-v6、关系临时引用、聚合正文、L4 专用指导、旧字段忽略、ID/版本不外发、超时/错误/token |
| `internal/embedding/*_test.go`、`internal/model/embedding_test.go` | profile/hash、归一化与向量协议、后台任务、诊断和查询降级 |
| `internal/store/sqlite/*_test.go` | 连接/迁移/短事务、L0/Memory FTS、Observation refs、层级输入、依赖、向量任务和列表 |
| `internal/jobs/*_test.go` | 提取只到 observations_saved，治理/聚合 category/stage 分开 |
| `internal/transport/http/*_test.go` | L0/当前视图 DTO、六分区召回、任务列表与阶段、反馈、冲突 400/404/409、清除与遗忘映射 |
| `internal/runtime/*_test.go` | 配置/私有目录/日志、四阶段组装、各 Worker 只领取自己的任务、开关与健康检查 |
| `internal/webui/*_test.go`、HTTP UI 测试 | 内嵌资源、SPA 限定路径、缓存、缺失文件 404、不依赖数据库就绪 |
| `tests/integration/{readiness,sqlite_baseline,unicode_baseline}_test.go` | 当前启用的真实数据库 readiness、SQLite/FTS5/Unicode 基线 |
| `tests/e2e/process_test.go` | 真实进程、同数据目录单实例锁、关闭与持久化/恢复 |

表格是测试职责索引，不是本次执行通过记录。模块测试不能代替被 legacy 隔离的完整跨阶段集成
和质量/容量验证；关系语义正确也不能由纯 JSON 形状测试保证。

## 5. CI 与审计边界

`.github/workflows/ci.yml` 在 ubuntu-24.04 读取 `.go-version`、下载 go.mod/go.sum 锁定依赖，
执行 `make check`。它不运行 legacy、真实模型、容量或前端检查；本地通过不代表远端 CI 已执行。

```sh
# 需要服务停止并获得同一数据目录锁；不调用模型或自动修复
./bin/dmemd --config <配置路径> --artifact-integrity-check
```

当前命令只检查 active/current_version 是否存在对应 revision，返回 missing_current_revision。
它尚不完整检查依赖闭包、hash、已删除来源、索引或语义覆盖；issues 为空不能视为全量来源审计。
