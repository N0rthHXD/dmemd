# dmemd

`dmemd` 是面向同一操作系统用户的本地 AI 记忆服务。采用 Go 单进程、SQLite 与
HTTP/1.1 JSON API，支持关键词检索与基于 Embedding 的语义混合检索。

## 功能概览

- 统一异步接收文本、对话和结构化内容，模型提取与记忆合并由后台单 Worker 完成。
- 原子记忆保存平面 `scope_key` 与规范化 `topics`；持久化治理 Worker 自动补全分类并周期性对账，
  分类结果无变化时不生成新版本。
- 治理 Worker 自动生成有直接原子依据的主题摘要、作用域画像、经验案例和指导型 Skill，
  保存不可变派生版本、直接来源和 FTS。原子依据或运行结果变化时同步失效；
  `POST /v1/memories/recall` 按 `layers` 统一返回原子、摘要、经验与 Skill 分区。
- 应用可提交完成结果反馈（success/failure/unknown，均视为应用报告而非执行验证），
  可选 `run_id` 去重；失败反馈自动使相关 Skill 失效并后台重建，撤销结果同步阻断依赖使用。
  Skill 自动启用并保留来源，`GET /v1/skills/{id}/content` 返回带 frontmatter 的 SKILL.md。
- 记忆具有完整版本、证据和生命周期：创建、更正、补证、归档、删除、历史恢复与永久清除。
- 事实有效区间与服务记录时间分离，支持 `filters.as_of` 指定历史时点召回；未知边界显式
  标记为 `temporal_status=unknown`，不伪造月份/日期精度。
- 两阶段模型整理：先提取独立事实，再按候选选择有限冲突上下文并按需调用决策模型；支持
  重复、补充、变化、更正、场景分离和未决冲突，非法目标不再降级为新增。
- 断言状态（accepted/pending/rejected）、事实组、版本关系和冲突审计独立持久化。普通召回
  只返回 accepted；未决冲突通过 `conflicts` 摘要和 `fact_conflict` 警告提示。
- 冲突管理接口支持查看 open/resolved 冲突与成员检测版本，并通过
  `choose/timeline/separate_scope/reject_all` 显式裁决；版本变化返回 409。
- 关键词检索使用 NFC/大小写规范化与中文词典分词，基于 SQLite FTS5/BM25，支持过滤、
  可读摘要、证据引用和按需解释。
- 混合检索并行执行关键词与 Embedding 余弦召回，按 RRF 融合；语义模型不可用或超时时
  自动降级为关键词结果，并通过 `warnings` 返回稳定代码。Embedding 默认关闭。
- 提供架构规定的全部本地 HTTP API、用户数据目录单实例锁和优雅关闭。
- 当前没有认证、分页、幂等键、条件请求或独立前端。

默认关键词检索会过滤受控停用词，并按 BM25 名次与词项覆盖率做有界重排，不设置最低命中
过滤；显式 `match=all` 保持严格合取。没有可用向量时仍按 hybrid 执行，只返回关键词候选。
关键词索引在记忆写入事务中同步维护，向量由独立后台 Worker 异步生成。

## 环境

- Go 精确版本见 `.go-version`（当前为 `1.27.3`）。
- 运行与发布构建不依赖 CGO：`CGO_ENABLED=0`。
- 依赖版本与 SQLite 能力基线见 `docs/dependency-baseline.md`。

## 构建与验证

```sh
make fmt-check   # 只检查格式，不改写源码
make vet
make test        # 单元、集成与进程级测试，使用临时真实 SQLite，不访问外网
make test-quality # 固定标注样例：分类、摘要约束、冲突误裁决、Skill 边界
make build       # CGO_ENABLED=0，输出 bin/dmemd
make check       # 上述检查、离线质量套件与构建
```

容量场景单独运行，默认测试不写入一万条数据：

```sh
make test-capacity # 10,000 条原子记忆、11,200 个治理任务、并发召回和单事务遗忘
```

实测环境、数据规模和结果记录在 `docs/quality.md`。真实模型语义评估需要显式提供
`DMEMD_EVAL_BASE_URL` 等环境变量，命令见该文件；默认测试和 CI 不访问真实模型，未执行的
语义评估不记为通过。首次运行前会下载 `go.mod` 中锁定的依赖。测试不需要真实模型、全局
数据库或凭据。更多说明见 `docs/testing.md`。

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
`--config <配置文件路径>` 指定；显式指定的文件不存在时启动失败。

同一数据目录同时只允许一个进程运行。单实例保护使用数据目录内的进程锁，不依赖
HTTP 端口冲突；进程崩溃后锁由操作系统释放，可直接重新启动。启动顺序为
配置 → 目录/单实例锁 → 数据库/迁移 → 恢复遗留任务 → HTTP/Worker；`SIGINT`/
`SIGTERM` 会停止接收请求、取消模型等待、结束事务并关闭连接。

模型地址不可用时服务仍可启动，已有记忆的查询、检索和管理保持可用；新提交仍返回
`202`。暂时性网络、超时和限流错误进入 `waiting` 并按 5 秒、30 秒、2 分钟递增延期，
之后最长每 30 分钟重试，模型恢复后自动继续，无需重新提交。连续三次解析/协议错误才进入
`failed`；凭据或请求配置错误保持等待并在重启后重评。协议失败任务在进程重启后也会按新的
模型/协议版本重新评估一次。失败原因通过任务查询返回。

分类、主题摘要、作用域画像和周期性对账由一个独立治理 Worker 执行；治理故障只降级后台
整理，不关闭 HTTP、原子查询或输入持久化。`governance.enabled=false` 只停止后台整理，原子
写入和查询继续可用。默认 `governance.debounce` 为 30 秒、对账间隔 15 分钟、单次派生输入
上限 64 条，治理模型调用受进程内软限速（默认最少间隔 2 分钟、每小时约 30 次）约束。配置
只有 `enabled`、`debounce`、`reconcile_interval`、`min_call_interval`、`max_inputs` 五个
治理项，示例见 `configs/dmemd.example.json`。

有新的相关版本或证据时，治理 Worker 会自动复查未决冲突；只有当前成员集合本身足以证明结果
时才自动裁决，复杂重叠和未知边界保持隔离。未决冲突成员不会进入摘要、经验案例或 Skill
输入，普通原子查询仍返回冲突说明。`POST /v1/forget` 可按原子、来源或结果显式永久遗忘，
在同一事务内先枚举并清除派生历史、索引和依赖，再清除叶子数据；`DELETE /v1/sources/{id}`
仍只是清除原文的逻辑操作。

`POST /v1/memories/recall` 是唯一召回入口，默认返回原子记忆、摘要、经验和 Skill 分区；
`layers` 可只选部分层。主题摘要、作用域画像、经验案例与 Skill 由治理 Worker 自动生成并
保存直接原子/运行依据；依据修改、归档、删除或结果撤销时旧内容同步退出默认召回，随后
后台重算。历史可通过 `GET /v1/artifacts/{id}` 和 `/history/{version}` 查看。经验结果来自
应用报告，Skill 只提供可修正的指导文本，不提供真实性、执行成功或安全认证。

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
curl -sS -X POST http://127.0.0.1:18432/v1/ingestions \
  -H 'Content-Type: application/json' \
  -d '{"content":"以后请默认使用中文回答。"}'

# 对话对象
curl -sS -X POST http://127.0.0.1:18432/v1/ingestions \
  -H 'Content-Type: application/json' \
  -d '{
    "app_id":"desktop-assistant",
    "external_id":"conversation-42-batch-7",
    "content":{"messages":[
      {"role":"user","content":"以后请默认使用中文回答。","occurred_at":"2026-10-20T02:00:00Z"}
    ]}
  }'

# 已有结构化内容
curl -sS -X POST http://127.0.0.1:18432/v1/ingestions \
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

任务成功后可读取 `memory_ids`、`conflict_ids`、`pending_memory_ids` 和固定
`warnings`；失败时 `error` 是脱敏摘要，不包含完整模型响应或原始输入。`succeeded` 表示
处理完成，不保证所有断言都被采纳。

### 历史时点召回与冲突

```sh
# 返回在指定时点有效的 accepted 事实
curl -sS -X POST http://127.0.0.1:18432/v1/memories/recall \
  -H 'Content-Type: application/json' \
  -d '{"query":"用户住在哪里","filters":{"as_of":"2026-07-01T00:00:00Z"},"top_k":10}'

# 普通召回未决冲突；items 不会混入 pending 正文
curl -sS http://127.0.0.1:18432/v1/memory-conflicts?status=open&limit=50
curl -sS http://127.0.0.1:18432/v1/memory-conflicts/<conflict-id>
```

裁决请求必须携带 `revision` 与详情中的全部 `member_versions`：

```sh
curl -sS -X POST http://127.0.0.1:18432/v1/memory-conflicts/<conflict-id>/resolve \
  -H 'Content-Type: application/json' \
  -d '{"revision":1,"action":"choose","member_ids":["<memory-id>"],"member_versions":[{"memory_id":"<memory-id>","version":2},{"memory_id":"<other-id>","version":1}]}'
```

### 列表、召回与当前记忆

```sh
# 默认返回最近更新的 50 条 active/archived 记忆，无分页
curl -sS 'http://127.0.0.1:18432/v1/memories?limit=20'

# 关键词召回；按词典分词，默认 match=any、top_k=20、仅 active
curl -sS -X POST http://127.0.0.1:18432/v1/memories/recall \
  -H 'Content-Type: application/json' \
  -d '{"query":"回答 语言","filters":{"kinds":["preference"]},"explain":true}'

# 需要所有原始分词同时命中时显式指定 match=all
curl -sS -X POST http://127.0.0.1:18432/v1/memories/recall \
  -H 'Content-Type: application/json' \
  -d '{"query":"回答 语言","match":"all"}'

# 默认 any 会过滤受控停用词，但不设置最低命中数；
# 结果按 BM25 名次与词项覆盖率做有界重排

# 混合召回；语义不可用时自动降级为关键词并返回 warnings
curl -sS -X POST http://127.0.0.1:18432/v1/memories/recall \
  -H 'Content-Type: application/json' \
  -d '{"query":"吃饭有什么忌口","mode":"hybrid","top_k":10}'

curl -sS http://127.0.0.1:18432/v1/memories/11111111-1111-4111-8111-111111111111
```

### 结果反馈、经验与 Skill

```sh
# 完成结果反馈；outcome 是应用报告，缺失按 unknown 保存。
# run_id 可选，重复提交直接返回已有回执，不比较正文、不覆盖、不恢复已撤销结果。
curl -sS -X POST http://127.0.0.1:18432/v1/experiences \
  -H 'Content-Type: application/json' \
  -d '{
    "run_id":"run-2026-10-22-1",
    "scope_key":"project:dmemd",
    "goal":"修改 dmemd 后验证",
    "conditions":["依赖已安装"],
    "actions":["运行 go test","执行构建检查"],
    "outcome":"success"
  }'

# 撤销错误结果；案例停用，相关 Skill 立即退出默认召回并后台重建
curl -sS -X POST http://127.0.0.1:18432/v1/experiences/<experience-id>/retract

# 默认只读取 active 当前版本；显式 version 读取不可变历史并返回其状态
curl -sS http://127.0.0.1:18432/v1/skills/<skill-id>/content
curl -sS 'http://127.0.0.1:18432/v1/skills/<skill-id>/content?version=1'

# 同一召回入口返回经验与 Skill 分区
curl -sS -X POST http://127.0.0.1:18432/v1/memories/recall \
  -H 'Content-Type: application/json' \
  -d '{"query":"修改后验证","scope_key":"project:dmemd","layers":["experience","skill"]}'
```

成功反馈返回 `202` 和回执；`run_id` 重复时返回同一回执。Skill 内容响应包含渲染后的
`SKILL.md`、精确版本、状态和 `memory_refs`/`run_refs`。反馈不会绕过 ingest 直接写成原子
事实：需要形成原子事件时仍通过 `POST /v1/ingestions`。

### 归档、版本恢复与删除

```sh
# PATCH 只修改状态；active/archived 之外的 state 不产生变更
curl -sS -X PATCH http://127.0.0.1:18432/v1/memories/11111111-1111-4111-8111-111111111111/state \
  -H 'Content-Type: application/json' \
  -d '{"state":"archived"}'

# 版本历史和指定版本
curl -sS 'http://127.0.0.1:18432/v1/memories/11111111-1111-4111-8111-111111111111/history?limit=10'
curl -sS http://127.0.0.1:18432/v1/memories/11111111-1111-4111-8111-111111111111/history/1

# 从历史版本恢复为新的 active 版本
curl -sS -X POST http://127.0.0.1:18432/v1/memories/11111111-1111-4111-8111-111111111111/history/1/restore

# 逻辑删除；永久清除记忆及其历史使用独立的 purge 动作
curl -sS -X DELETE http://127.0.0.1:18432/v1/memories/11111111-1111-4111-8111-111111111111
curl -sS -X POST http://127.0.0.1:18432/v1/memories/11111111-1111-4111-8111-111111111111/purge
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

### 永久遗忘

```sh
# 显式永久遗忘来源、原子记忆或应用报告；来源会展开为其关联记忆和结果
curl -sS -X POST http://127.0.0.1:18432/v1/forget \
  -H 'Content-Type: application/json' \
  -d '{"source_ids":["22222222-2222-4222-8222-222222222222"]}'
```

响应返回实际移除的排序后 ID 数组。请求在一个事务内完成：任一目标不存在或存储失败时
全部回滚。派生历史、FTS、依赖和在途任务引用于同一事务内先枚举后清除，迟到结果不能恢复
已遗忘内容。

### 健康检查

```sh
curl -sS http://127.0.0.1:18432/healthz
curl -sS http://127.0.0.1:18432/readyz
```

`/healthz` 只反映进程存活；`/readyz` 检查数据库迁移和连接。模型不可达不影响
`/readyz`。同步错误使用简单 JSON：`400` 解码失败、`404` 资源不存在、`500` 执行
失败、`503` 服务暂不可用。响应不返回 `Location`、`ETag` 或版本控制头。

`--version` 输出构建版本后退出；`--check-config` 只加载并校验配置后退出。

## 首版限制

- 每用户单实例、回环 HTTP，无认证和多应用 ACL；同 UID 应用共享全部记忆，`app_id`、
  `scope_key` 和对话 role 都不是身份或权限凭据。
- 真实模型不可用时仍接收输入，但分类、摘要、经验和 Skill 生成会等待；服务不保证模型输出
  的语义真实性。
- 所有经验结果都是应用报告，Skill 只是可修正的指导文本，不提供真实性、执行成功、安全或
  工具授权认证；服务不生成或执行脚本。
- 容量实测覆盖一万条原子记忆及对应派生量；十万条、长期高并发、费用上限和 OS 应用隔离
  尚未验证。详见 `docs/quality.md`。
- 不提供自动备份/恢复，不管理外部副本，也不承诺取证级物理擦除；永久遗忘只覆盖本服务正在
  保存的数据。
- 历史时点召回只支持 `atomic` 层；包含派生层的请求会返回空派生分区和
  `derived_as_of_unsupported`。
- 自动冲突复查只处理成员集合本身足以证明的确定性规则；复杂边界保持 `open`，等待新证据或
  显式裁决。

## 文档

- `系统 AI 记忆服务架构设计.md`：架构依据
- `api/openapi.yaml`：HTTP 契约（仅文档，不生成校验层）
- `docs/contracts/api-decisions.md`：架构未展开的接口语义
- `docs/contracts/model-provider.md`：远端模型协议
- `docs/contracts/storage.md`：SQLite 连接、迁移、表与事务契约
- `docs/quality.md`：固定标注质量样例、离线评估和一万条容量实测记录
- `docs/dependency-baseline.md`：依赖版本与 SQLite 能力基线
- `docs/testing.md`：测试命令与隔离方式
