# dmemd

`dmemd` 是面向同一操作系统用户的本地 AI 记忆服务。首版采用 Go 单进程、SQLite
与 HTTP/1.1 JSON API。P1–P6 已于 2026-10-20 完成验收；语义与关键词混合检索已按
`docs/hybrid-retrieval-design.md` 实现。P7 的质量、容量、备份和发布准备暂缓，不阻塞
当前本地服务使用。

## 当前状态

P1–P5 已建立精确依赖基线、API/模型契约、配置与日志、真实 SQLite 单写连接/读池、
顺序迁移、跨表事务、完整记忆版本与证据、来源清除、中文关键词检索、远端模型适配、
统一异步接收、自动合并、单 Worker、三次尝试与崩溃恢复。P6 已提供架构规定的全部
本地 HTTP API、完整进程组装、用户数据目录单实例锁、优雅关闭、调用端示例和 HTTP
契约回归测试。当前没有认证、分页、幂等键、条件请求或独立前端。

检索支持 `keyword` 与 `hybrid` 两种模式，默认仍为关键词检索。混合模式并行执行
FTS5/BM25 与 Embedding 余弦召回，按 RRF 融合；语义模型不可用或超时时自动降级为
关键词结果，并通过 `warnings` 返回稳定代码。没有可用向量时仍按 hybrid 执行，只返回
关键词候选。Embedding 默认关闭，启用后由独立后台 Worker 生成并校验向量，关键词索引
仍在记忆写入事务中同步维护。

## 环境

- Go 精确版本见 `.go-version`（当前为 `1.27.3`）。
- 运行与发布构建不依赖 CGO：`CGO_ENABLED=0`。
- 依赖版本与 SQLite 能力基线见 `docs/dependency-baseline.md`。

## 构建与验证

```sh
make fmt-check   # 只检查格式，不改写源码
make vet
make test        # 单元、集成与进程级测试，使用临时真实 SQLite，不访问外网
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

同一数据目录同时只允许一个进程运行。单实例保护使用数据目录内的进程锁，不依赖
HTTP 端口冲突；进程崩溃后锁由操作系统释放，可直接重新启动。启动顺序为
配置 → 目录/单实例锁 → 数据库/迁移 → 恢复遗留任务 → HTTP/Worker；`SIGINT`/
`SIGTERM` 会停止接收请求、取消模型等待、结束事务并关闭连接。

模型地址不可用时服务仍可启动，已有记忆的查询、检索和管理保持可用；新提交仍返回
`202`，提取任务按三次总尝试重试，失败原因通过任务查询返回。

`embedding.enabled=true` 时需配置端点、模型版本、维数和超时；`min_similarity` 应按
模型用相关/不相关样例校准。配置只包含这些必要项，不提供输入前缀、重试或查询预算的
细粒度开关。启用后启动时会重建派生搜索文档，为缺失或配置不匹配的向量建立持久化任务，
并由独立 Worker 在后台生成。Embedding 故障不影响 `/readyz`，
`hybrid` 检索会降级并返回 `warnings`。配置示例见 `configs/dmemd.example.json`。

## HTTP API 调用示例

API 不验证 Token。`Authorization: Bearer ...` 可以省略，也可以填写任意值；`app_id`
和 `external_id` 只是可选来源信息，不承担认证、隔离或唯一性。只有带 JSON 请求体时
需要 `Content-Type: application/json`，没有其他自定义请求头要求。

以下示例假设服务监听 `http://127.0.0.1:18432`。

### 提交文本、对话和结构化内容

```sh
# 简单文本
curl -sS -X POST http://127.0.0.1:18432/v1/memories \
  -H 'Content-Type: application/json' \
  -d '{"content":"以后请默认使用中文回答。"}'

# 对话对象
curl -sS -X POST http://127.0.0.1:18432/v1/memories \
  -H 'Content-Type: application/json' \
  -d '{
    "app_id":"desktop-assistant",
    "external_id":"conversation-42-batch-7",
    "content":{"messages":[
      {"role":"user","content":"以后请默认使用中文回答。","occurred_at":"2026-10-20T02:00:00Z"}
    ]}
  }'

# 已有结构化内容
curl -sS -X POST http://127.0.0.1:18432/v1/memories \
  -H 'Content-Type: application/json' \
  -d '{"content":{"preferences":{"answer_language":"zh-CN"}}}'
```

提交成功返回 `202`。响应中的 `source_id`、`job_id` 用于后续查询：

```json
{"source_id":"22222222-2222-4222-8222-222222222222","job_id":"33333333-3333-4333-8333-333333333333","status":"queued"}
```

### 查询任务

```sh
curl -sS http://127.0.0.1:18432/v1/ingest-jobs/33333333-3333-4333-8333-333333333333
```

任务成功后可读取 `memory_ids`；失败时 `error` 是脱敏摘要，不包含完整模型响应或
原始输入。

### 列表、检索与当前记忆

```sh
# 默认返回最近更新的 50 条 active/archived 记忆，无分页
curl -sS 'http://127.0.0.1:18432/v1/memories?limit=20'

# 关键词检索；按词典分词，默认 match=all、top_k=20、仅 active
curl -sS -X POST http://127.0.0.1:18432/v1/memories/search \
  -H 'Content-Type: application/json' \
  -d '{"query":"回答 语言","filters":{"kinds":["preference"]},"explain":true}'

# 混合检索；语义不可用时自动降级为关键词并返回 warnings
curl -sS -X POST http://127.0.0.1:18432/v1/memories/search \
  -H 'Content-Type: application/json' \
  -d '{"query":"吃饭有什么忌口","mode":"hybrid","top_k":10}'

curl -sS http://127.0.0.1:18432/v1/memories/11111111-1111-4111-8111-111111111111
```

### 归档、版本恢复与删除

```sh
# PATCH 只修改状态；active/archived 之外的 state 不产生变更
curl -sS -X PATCH http://127.0.0.1:18432/v1/memories/11111111-1111-4111-8111-111111111111 \
  -H 'Content-Type: application/json' \
  -d '{"state":"archived"}'

# 版本历史和指定版本
curl -sS 'http://127.0.0.1:18432/v1/memories/11111111-1111-4111-8111-111111111111/versions?limit=10'
curl -sS http://127.0.0.1:18432/v1/memories/11111111-1111-4111-8111-111111111111/versions/1

# 从历史版本恢复为新的 active 版本
curl -sS -X POST http://127.0.0.1:18432/v1/memories/11111111-1111-4111-8111-111111111111/restorations \
  -H 'Content-Type: application/json' \
  -d '{"version":1}'

# 逻辑删除；加 ?purge=true 永久清除记忆及其历史
curl -sS -X DELETE http://127.0.0.1:18432/v1/memories/11111111-1111-4111-8111-111111111111
curl -sS -X DELETE 'http://127.0.0.1:18432/v1/memories/11111111-1111-4111-8111-111111111111?purge=true'
```

### 来源查询与原文清除

```sh
curl -sS http://127.0.0.1:18432/v1/sources/22222222-2222-4222-8222-222222222222

# 清除原文、历史证据片段并取消未完成任务；已形成的记忆正文保留
curl -sS -X DELETE http://127.0.0.1:18432/v1/sources/22222222-2222-4222-8222-222222222222
```

清除响应：

```json
{"id":"22222222-2222-4222-8222-222222222222","redacted":true,"cancelled_jobs":1}
```

### 健康检查

```sh
curl -sS http://127.0.0.1:18432/healthz
curl -sS http://127.0.0.1:18432/readyz
```

`/healthz` 只反映进程存活；`/readyz` 检查数据库迁移和连接。模型不可达不影响
`/readyz`。同步错误使用简单 JSON：`400` 解码失败、`404` 资源不存在、`500` 执行
失败、`503` 服务暂不可用。响应不返回 `Location`、`ETag` 或版本控制头。

`--version` 输出构建版本后退出；`--check-config` 只加载并校验配置后退出。

## 文档

- `系统 AI 记忆服务架构设计.md`：架构依据
- `tech-stack.md`、`project-structure.md`、`plan.md`、`tasks.md`：技术与实施规划
- `api/openapi.yaml`：HTTP 契约（仅文档，不生成校验层）
- `docs/hybrid-retrieval-design.md`：关键词与语义混合检索实现方案
- `docs/contracts/api-decisions.md`：架构未展开的接口语义
- `docs/contracts/model-provider.md`：首个远端模型协议
- `docs/contracts/storage.md`：SQLite 连接、迁移、表与事务契约
- `docs/performance.md`：P7 容量测试规格（尚未执行）
- `docs/testing.md`：测试命令与隔离方式
