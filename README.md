# dmemd

`dmemd` 是面向同一操作系统用户的本地 AI 记忆服务。首版采用 Go 单进程、SQLite
与 HTTP/1.1 JSON API，当前实施范围为 P1–P6，P7 暂缓。

## 当前状态

P1 已建立 Go Module、精确依赖基线、API/模型契约、测试与 CI 入口、配置、
结构化日志和运行骨架。P2 已接入真实 SQLite：单写连接/小型读池、顺序迁移、
业务表、外部内容 FTS5 触发器、短事务与来源/任务存取，`/readyz` 使用真实数据库
探活。业务 API、记忆写入服务、检索和后台 Worker 从 P3 起实现；当前还没有业务
HTTP 路由。

## 环境

- Go 精确版本见 `.go-version`（当前为 `1.27.3`）。
- 运行与发布构建不依赖 CGO：`CGO_ENABLED=0`。
- 依赖版本与 SQLite 能力基线见 `docs/dependency-baseline.md`。

## 构建与验证

```sh
make fmt-check   # 只检查格式，不改写源码
make vet
make test        # 单元与集成测试，使用临时真实 SQLite，不访问外网
make build       # CGO_ENABLED=0，输出 bin/dmemd
make check       # 上述检查与构建
```

首次运行前会下载 `go.mod` 中锁定的依赖。测试不需要真实模型、全局数据库或凭据。
更多说明见 `docs/testing.md`。

## 运行

```sh
cp configs/dmemd.example.json ~/.config/dmemd/config.json
chmod 600 ~/.config/dmemd/config.json
make build
./bin/dmemd --check-config
./bin/dmemd
```

默认监听 `127.0.0.1:18432`，数据目录为
`${XDG_DATA_HOME:-$HOME/.local/share}/dmemd/`，配置目录为
`${XDG_CONFIG_HOME:-$HOME/.config}/dmemd/`。配置也可通过
`--config /path/to/config.json` 指定；显式指定的文件不存在时启动失败。

健康检查：

```sh
curl -i http://127.0.0.1:18432/healthz
curl -i http://127.0.0.1:18432/readyz
```

`--version` 输出构建版本后退出；`--check-config` 只加载并校验配置后退出。
启动时会打开并迁移 SQLite；数据库不可用时启动失败，运行中数据库失效时 `/readyz`
返回 `503`。当前不提供业务占位接口。

## 文档

- `系统 AI 记忆服务架构设计.md`：架构依据
- `tech-stack.md`、`project-structure.md`、`plan.md`、`tasks.md`：技术与实施规划
- `api/openapi.yaml`：HTTP 契约（仅文档，不生成校验层）
- `docs/contracts/api-decisions.md`：架构未展开的接口语义
- `docs/contracts/model-provider.md`：首个远端模型协议
- `docs/contracts/storage.md`：SQLite 连接、迁移、表与事务契约
- `docs/performance.md`：P7 容量测试规格（尚未执行）
- `docs/testing.md`：测试命令与隔离方式
