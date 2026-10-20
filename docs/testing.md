# 测试与统一验证命令

## 1. 前提

- 使用 `.go-version` 固定的 Go `1.27.3`（或同一精确补丁版本）。
- 首次构建需要能下载 `go.mod` 锁定的模块；测试本身不访问外网、不调用真实模型。
- 生产构建和默认测试不要求 CGO。竞争检测需要 CGO，属于可选检查，不是当前 P1 门禁。

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
- 不需要真实模型、凭据、生产数据库或用户内容；P1 还没有模型或业务 HTTP 测试。
- 配置文件测试会显式设置权限为 `0600`；服务拒绝组/其他用户可读的配置文件。
- 测试不修改仓库外的用户数据目录。涉及默认目录的测试通过 `t.Setenv` 指向临时目录。

## 4. 当前测试覆盖（P1）

| 测试 | 覆盖内容 |
| --- | --- |
| `cmd/dmemd/main_test.go` | `--version`、`--check-config`、显式配置缺失时报错 |
| `internal/idgen` | UUID v4 格式、变体位与唯一性 |
| `internal/runtime/config_test.go` | 默认值、配置优先级、未知字段、权限、目录创建、架构参数校验 |
| `internal/runtime/logging_test.go` | 结构化日志、凭据脱敏、正文/模型响应不落日志 |
| `internal/runtime/app_test.go` | `/healthz`、`/readyz` 的 200/503、无业务占位路由、优雅关闭 |
| `tests/integration/sqlite_baseline_test.go` | SQLite 版本、WAL、外键、FTS5、CGO=0 兼容 |
| `tests/integration/unicode_baseline_test.go` | x/text NFC 基线与中文保持 |

后续阶段按任务增加事务回滚、检索、Worker、HTTP 契约和进程恢复测试；本阶段不提前创建占位测试。

## 5. CI

`.github/workflows/ci.yml` 在 GitHub Actions 的 `ubuntu-24.04` 运行器上通过
`actions/setup-go` 读取 `.go-version`，缓存以 `go.sum` 为依据，然后执行 `make check`。
CI 不保存凭据或用户数据库，也不配置生产部署。

当前环境无法触发远端 GitHub Actions；本地已用同一组命令验证配置与检查通过。远端运行结果
必须在实际触发后另行记录，不能把本地通过当作远端 CI 通过。

## 6. 故障与性能测试边界

模型超时/失败、事务回滚、取消竞态、进程崩溃恢复等按 P2–P6 任务实现并在对应阶段验证。
十万条容量与性能实测属于暂缓的 P7；其规格见 `docs/performance.md`，当前未执行，不能宣称
性能验收通过。
