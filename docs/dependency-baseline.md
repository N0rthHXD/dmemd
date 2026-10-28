# 依赖与 SQLite 能力基线

本文件记录当前固定的工具链、依赖与 SQLite 能力基线。所有开发、CI 和发布构建必须使用
这里的精确版本；不能以 `latest` 或浮动版本替代。架构依据为
`docs/系统 AI 记忆服务架构设计.md` v1。

## 1. 固定版本

| 组件 | 固定版本 | 说明 |
| --- | --- | --- |
| Go | `1.27.3` | `.go-version`、`go.mod` 的 `go` 指令与 CI 使用同一版本 |
| 目标构建平台 | `linux/amd64` | 当前验证平台；其他平台未声明支持 |
| SQLite 驱动 | `modernc.org/sqlite v1.60.1` | 纯 Go，生产构建不依赖 CGO |
| 驱动内置 SQLite | `3.53.4` | 由驱动决定，不另外安装 SQLite 作为运行依赖 |
| Unicode 规范化 | `golang.org/x/text v0.43.0` | 用于 NFC 规范化 |
| libc 适配 | `modernc.org/libc v1.77.1` | 由驱动 `go.mod` 固定；升级驱动时不得单独漂移 |

直接依赖和完整校验和见 `go.mod`、`go.sum`。`go.sum` 是依赖锁定的依据，CI 缓存也以它为依据。
`modernc.org/libc` 的版本必须与 `modernc.org/sqlite` 自身 `go.mod` 一致，避免驱动与 libc 不匹配。

## 2. 验证记录

执行日期：2026-10-20。工具链来自 `go.dev` 官方 `go1.27.3.linux-amd64.tar.gz`，SHA-256 为
`65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40`。

```text
$ go version
go version go1.27.3 linux/amd64

$ go test -v -count=1 ./tests/integration/...
--- PASS: TestUnicodeNormalizationBaseline
--- PASS: TestSQLiteDependencyBaseline
    sqlite_baseline_test.go: sqlite_version=3.53.4 journal_mode=wal foreign_keys=1 fts5=ok

$ CGO_ENABLED=0 go build -trimpath -o bin/dmemd ./cmd/dmemd
（成功）
```

探针 `tests/integration/sqlite_baseline_test.go` 使用真实临时文件数据库，验证：

| 能力 | 结果 | 说明 |
| --- | --- | --- |
| SQLite 版本查询 | 通过 | `SELECT sqlite_version()` 返回 `3.53.4` |
| WAL | 通过 | `PRAGMA journal_mode=WAL` 返回 `wal` |
| 外键 | 通过 | 每连接 `_pragma=foreign_keys(1)`，违反外键的插入被拒绝 |
| FTS5 | 通过 | 可创建 FTS5 表、插入并执行 `MATCH` 查询 |
| CGO 无关构建 | 通过 | `CGO_ENABLED=0` 构建成功 |

`golang.org/x/text v0.43.0` 的 NFC 行为由 `TestUnicodeNormalizationBaseline` 验证：组合字符
`e` + `U+0301` 规范化为 `é`，中文文本保持不变。

## 3. 一致性备份能力（预留）

当前驱动提供导出的 `sqlite.Backup` 类型，以及驱动连接上的在线备份入口：

- `NewBackup(dstURI) (*sqlite.Backup, error)`：从源连接创建在线备份；
- `Backup.Step(n)`、`Backup.PageCount()`、`Backup.Remaining()`：分步复制与进度；
- `Backup.Commit()`、`Backup.Finish()`：完成或清理；
- `NewRestore(srcURI)`：从备份恢复。

通过 `database/sql` 使用时可取得 `*sql.Conn`，在 `Conn.Raw` 中把底层驱动连接断言为
包含 `NewBackup` / `NewRestore` 的接口，再调用上述方法。服务当前未实现、未演练备份或
恢复命令；实现时应使用该一致性备份机制。运行中的数据库不能只复制主文件充当备份。

## 4. 升级规则

1. 升级 Go、驱动或 Unicode 依赖必须修改 `.go-version`、`go.mod`、`go.sum` 并更新本文件。
2. 驱动升级后重跑 FTS5、WAL、外键、事务回滚与索引重建测试；实现备份后还须重跑备份恢复测试。
3. 不以命令行 SQLite 替换驱动内置版本作为生产运行依赖；命令行工具只用于可选排障。
4. `go build` 的发布构建固定 `CGO_ENABLED=0`，并使用 `.go-version` 指定的工具链。
5. 当前只声明 `linux/amd64` 已验证；其他操作系统或架构需要补充验证后才能声明支持。
