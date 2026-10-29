# dmemd 技术与使用指南

[返回项目首页](../README.md) · [文档索引](README.md)

本文集中说明当前实现的功能语义、构建与运行方式、完整 HTTP 调用示例和使用边界。
架构设计与接口契约见[文档索引](README.md)；以下命令均从仓库根目录执行。

## 目录

- [功能与实现语义](#功能与实现语义)
- [环境](#环境)
- [构建与验证](#构建与验证)
- [前端管理台](#前端管理台)
- [运行](#运行)
  - [派生质量与来源审计](#派生质量与来源审计)
- [HTTP API 调用示例](#http-api-调用示例)
  - [提交文本、对话和结构化内容](#提交文本对话和结构化内容)
  - [查询任务](#查询任务)
  - [关系与冲突](#关系与冲突)
  - [列表、召回与当前记忆](#列表召回与当前记忆)
  - [结果反馈、经验与 Skill](#结果反馈经验与-skill)
  - [归档、版本恢复与删除](#归档版本恢复与删除)
  - [来源查询与原文清除](#来源查询与原文清除)
  - [永久遗忘](#永久遗忘)
  - [健康检查](#健康检查)
- [首版限制](#首版限制)

## 功能与实现语义

- 统一异步接收文本、对话和结构化内容；Source 保存原始内容，Extraction Worker 只生成 L0
  Observation 并登记治理任务，不同步合并 Memory。
- 首次提取模型只选择当前 Source 的 `span_ids`，可选返回 `kind` 和必要的 `content`。Evidence
  的 quote、定位、来源关联和 ID 全部由代码根据 Source segment 生成，模型不能提交证据。
- Observation 治理优先执行确定性规则，规则无法判断时才调用最小关系模型。模型只返回
  `duplicate/supports/enriches/supersedes/contradicts/separate/independent/unresolved` 和
  临时引用；当前 Memory 的创建、补证、修订或 pending 状态由代码物化。
- 派生记忆按 L0→L1→L2→L3 逐层生成，L4 单独使用稳定 Memory、相关派生和应用报告。
  普通写入只登记目标并去抖，不在写路径调用聚合模型；
  `derived_dependencies` 保存精确输入，失效沿依赖图递归传播，stale 内容退出默认召回。
- 应用可提交完成结果反馈（success/failure/unknown/partial_success，均视为应用报告而非执行验证），
  可选 `run_id` 去重；失败反馈自动使相关 Skill 失效并后台重建，撤销结果同步阻断依赖使用。
  Skill 自动启用并保留来源，`GET /v1/skills/{id}/content` 返回带 frontmatter 的 SKILL.md。
- 记忆具有完整版本、证据和生命周期：创建、更正、补证、归档、删除、历史恢复与永久清除。
- 当前规范视图保留 created_at/updated_at 记录时间，不保存结构化观测/事件/有效时间、事实组、procedure、qualifiers、topics 或
  durability/phase 字段。Observation 正文和 Evidence 保留原文中的时间表达；后续时间能力由
  独立 annotation 任务实现，目前不支持 as_of/event_at。
- 断言状态（accepted/pending/rejected）、关系、版本和冲突审计独立持久化。普通召回优先返回
  accepted，再展示明确标记的 pending；未决冲突通过 `conflict_refs`、`conflicts` 摘要提示，
  rejected 不参与召回。删除和失效不按事实组猜测副本，只沿显式 Observation、Memory 和派生依赖传播。
- 当前 `POST /v1/memories/recall` 按 `layers` 返回 Observation、当前 Memory 和 L1-L4 分区；
  `GET /v1/artifacts` 与 `GET /v1/artifact-targets` 分别发现派生对象和生成目标状态。

Source 是原始依据，Observation 是首次记忆形成，Memory 是治理物化视图，Derived Memory 是
可失效的聚合；不能把四者都称为“已确认原子事实”。当前 classify 任务仅完成调度边界，独立
分类结果流水线尚未接通。supersedes 当前创建新视图并记录关系，不自动删除旧对象或关闭时间线。

## 环境

- Go 使用 [`.go-version`](../.go-version) 固定的 `1.27.3`；当前验证平台为 linux/amd64。
- 生产构建使用纯 Go SQLite 驱动，不需要 CGO 或系统 SQLite；首次下载模块需要网络。
- 管理台构建使用 [`web/package.json`](../web/package.json) 固定的 Node `24.15.0`、npm `11.12.1`。
- 默认测试用本地假模型与临时数据库，不需要生产凭据。竞争检测才需要 CGO 工具链。

## 构建与验证

```sh
make build            # 纯 Go，输出 bin/dmemd
make check            # 格式检查、vet、默认测试、质量入口和构建
make test-integration # 当前启用的集成测试
make test-race        # 可选，需 CGO
```

当前质量/容量旧套件带 `legacy` 标签；`make test-quality` 会报告跳过，`make test-capacity`
不能运行现有旧版容量套件，不代表新架构通过。一万条旧架构数据不得外推 v2 性能；真实模型质量
与新分层容量需要独立验证。完整命令及覆盖范围见[测试文档](testing.md)。

## 前端管理台

```sh
make web-install
make web-check
make build-ui
./bin/dmemd
```

访问 `http://127.0.0.1:18432/ui/`。带 UI 的二进制内嵌静态资源，运行时不需要 Node；
`make build` 会覆盖同一路径且不带 UI，需要界面时重新 `make build-ui`。

开发时后端单独启动，再执行：

```sh
DMEMD_DEV_API_URL=http://127.0.0.1:18432 make web-dev
```

Vite 默认 `http://127.0.0.1:5173/ui/`，同源代理 `/v1` 和健康检查到后端。界面区分 L0 观察、
当前规范记忆、L1–L4、提取/治理/聚合状态；不提供直接编辑正文、执行 Skill 或修改模型配置。
部署、页面与缓存边界见[前端架构](frontend-architecture.md)。

## 运行

```sh
config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/dmemd"
mkdir -p "$config_dir"
cp configs/dmemd.example.json "$config_dir/config.json"
chmod 600 "$config_dir/config.json"
```

已有配置时请先核对，不要直接覆盖。启动前编辑配置中的 `model.base_url`、`model.name` 和
按需填写的 `model.api_key`。示例端点和模型名都是占位值；`model.extraction.name`、
`model.governance.name`、`model.aggregation.name` 也需要替换，或移除这些任务级 `name`
字段以继承 `model.name`。模型协议见[远端模型协议](contracts/model-provider.md)。

```sh
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
配置 → 目录/单实例锁 → 数据库/迁移 → 恢复遗留任务 → Extraction → Governance →
Aggregation → Embedding Worker → HTTP；`SIGINT`/`SIGTERM` 会停止接收请求、取消模型等待、
结束事务并关闭连接。

模型地址不可用时服务仍可启动，已有记忆的查询、检索和管理保持可用；新提交仍返回
`202`。暂时性网络、超时和限流错误进入 `waiting` 并按 5 秒、30 秒、2 分钟递增延期，
之后最长每 30 分钟重试，模型恢复后自动继续，无需重新提交。连续失败达到
`worker.max_attempts`（默认 3）次解析/协议错误才进入
`failed`；凭据或请求配置错误保持等待并在重启后重评。协议失败任务在进程重启后也会按新的
模型/协议版本重新评估一次。失败原因通过任务查询返回。

`worker.extraction_timeout` 默认 `180s`，`worker.governance_timeout` 和
`worker.aggregation_timeout` 默认 `60s`，分别限制提取、治理与聚合阶段的单次调用。
`model.extraction`、`model.governance`、`model.aggregation` 可分别覆盖模型名、超时和
`reasoning_effort`（`none/minimal/low/medium/high`）；模型名留空继承 `model.name`，超时未指定则
使用对应 worker 阶段超时。提取还有代码内固定 600 秒尝试预算，没有旧 process_budget/decision 配置。
按阶段拆分配置不等于用更快的模型修复协议或语义问题：来源、证据、身份、版本和依赖仍由服务
统一生成。`worker.extraction_chunk_runes` 默认 `12000`，超过预算的来源按稳定片段分块提取，
块间携带只读上下文用于指代和重复消解；单条超长字段本身也按固定片段切分。

waiting/failed 提取任务返回 `diagnostic_stage` 和 `reason_code`，例如 `extraction` +
`model_timeout`。提取任务只保存 L0 Observation；治理和聚合状态通过各自任务目标查询，
不会在 Extraction Job 中伪装成同步完成。

Observation 关系治理、冲突复查和周期性对账由一个独立治理 Worker 执行；L1/L2/L3 聚合、经验案例和 Skill
由独立聚合 Worker 执行。提取、治理、聚合和 Embedding Worker 共享 SQLite，但每个 Worker
只领取自己的任务类型，因此单个阶段的模型故障不会阻塞其他阶段或 HTTP 读写。
`governance.enabled=false` 只停止治理后台整理，`aggregation.enabled=false` 只停止派生记忆
生成，Source/L0 接收和已有记忆查询继续可用；停止治理时新 Memory 不会自动物化。
默认 `governance.debounce` 为 30 秒、对账间隔 15 分钟、关系候选上限 64 条，
治理模型调用受进程内软限速（默认最少间隔 2 分钟、每小时约 30 次）
约束；配置包含 `enabled`、`debounce`、`reconcile_interval`、`min_call_interval`、
`max_inputs` 和 `max_wait`。聚合配置包含 `enabled`、`debounce`、`reconcile_interval`、
`max_inputs` 和 `l1_bucket_size`，默认分别为 64 与 24h，聚合 debounce 默认 10 秒；
L1 时间桶按记录时间由代码生成，不是事实有效区间，不由模型选择。示例见
[配置模板](../configs/dmemd.example.json)。

治理与聚合领取任务时让位于正在运行或已到期的摄取任务；摄取退避/等待配置期间不阻塞治理。
各 Worker 只领取自己的任务类型。同一 kind/target_key 只有一个待办目标，新的
输入在同一事务中替代旧待办 revision 并重新 debounce，旧 revision 不再执行或提交。
已发出的治理模型请求不抢占取消。日志按 extraction/relation/aggregation/experience/skill
操作记录调用数、token 和耗时，任务查询提供排队年龄/错误，不记录原文或模型响应。

已有冲突复查目标只在成员集合本身足以证明结果时自动裁决；没有旧版时间线或事实组自动收敛，
contradicts/unresolved 保留 pending，也不保证自动创建冲突行。普通查询保持明确的断言状态和
冲突说明。`POST /v1/forget` 可按 Memory、Source 或应用结果显式永久遗忘；直接清理与间接
失效边界见后文。来源清除会同时撤回观察并传播失效，不再仅仅清空一个原文字段。

`POST /v1/memories/recall` 是唯一召回入口，默认返回 observations、memories、l1、l2、l3、l4；
`query_mode` 选择层级组合，显式 `layers` 优先。派生对象由独立 Aggregation Worker
生成并保存 typed dependencies；依据修改、归档、删除或结果撤销时旧内容同步退出默认召回，
随后后台重算。历史可通过 `GET /v1/artifacts/{id}` 和 `/history/{version}` 查看；
`GET /v1/artifacts` 按 scope/kind/state/reason 发现对象，`GET /v1/artifact-targets` 同时返回
尚无对象的目标、排队/运行/等待任务、无合格输入、生成失败、已有可用版本和 stale 状态。
经验结果来自应用报告，Skill 只提供可修正的指导文本，不提供真实性、执行成功或安全认证。

### 派生质量与来源审计

当前派生记忆按 L1 局部聚合、L2 领域聚合、L3 全局聚合、L4 经验案例/指导 Skill 分层生成。
分类结果应独立于 Memory；当前分类流水线尚未落地。规范 Memory 不再内嵌 `topics`、`raw_topics`、时间角色、事实组、procedure
或 qualifiers 字段。普通聚合与经验模型只返回正文，标题、依赖、版本、层级、target 和截断标记
由代码生成；Skill 保留专用 name/description/content/可选步骤协议，系统元数据仍由代码管理。
当前自动 L2 使用代码默认领域 general；不能宣称已按独立分类结果生成细粒度主题画像。

- L1 只读取同 scope/bucket 的 Observation；L2 只读取 active L1；L3 只读取 active L2；L4
  只读取稳定 Memory、相关派生对象和应用结果。任何一层都不能直接读取全部 L0 或写回 Observation。
- `derived_dependencies` 保存每个版本的精确输入（observation/memory/derived/experience）。
  下层变化沿依赖图递归标记上层 stale；stale 内容退出默认召回，上层可以回退到下层或等待重建。
- 摘要输入受条数、单条字数和总字数限制。超过上限或下层存在 stale 时，revision 标记
  `truncated=true`，不声称覆盖完整范围。输入签名相同的活跃目标不再重复调用模型。
- 经验案例正文始终附带“这是应用报告，不代表独立验证”的等效声明（按来源语言渲染），结果状态
  由服务保留，不能被模型升级。
- Skill 先检查稳定 procedure 资格；没有输入的目标只记录 `no_eligible_input` 评估，不再创建空
  artifact。已有 Skill 失去来源时仍立即 `stale/disabled` 并保留历史。当前输入选择按 scope、
  稳定 procedure 与低层/报告类型约束，不提供完整工作流主题相关性或执行认证保证。
- 每个 revision 及其依赖表保存精确输入类型/ID/版本、generator/protocol version、截断标记及内容 hash；
  正文、kind、scope、断言或生命周期变化立即使依赖对象 stale，补证但断言不变时保留已有引用。

服务停止后可对同一配置的数据库执行只读审计（持有相同单实例锁，不启动 HTTP 或模型）：

```sh
./bin/dmemd --config ~/.config/dmemd/config.json --artifact-integrity-check
# {"checked":0,"issues":[]}
```

当前审计只检查 active/current_version 指针是否有对应 revision，发现问题返回
`missing_current_revision` 并非零退出，不自动修复。它尚不完整检查依赖图、hash、来源有效性或
语义覆盖，空 issues 不能视为全量来源审计通过。
项目尚未发布，schema 直接更新；本次实现只面向全新数据库，不提供旧库迁移。

`embedding.enabled=true` 时需配置端点、模型版本、维数和超时；`min_similarity` 应按
模型用相关/不相关样例校准。配置只包含这些必要项，不提供输入前缀、重试或查询预算的
细粒度开关。启用后启动时会重建派生搜索文档，为缺失或配置不匹配的向量建立持久化任务，
并由独立 Worker 在后台生成。模型、维数或文档构造方式变化会登记重建原因，旧向量退出当前
配置召回，只有 profile、维数和内容 hash 全部匹配的向量参与语义分支。Embedding 故障不影响
`/readyz`，`hybrid` 检索会降级并返回 `warnings`；`GET /v1/embedding/status` 区分配置
不匹配、索引未就绪、积压和失败任务，存在文档但当前向量为零时返回
`semantic_index_not_ready`，不会把空向量结果解释为语义不相关。配置示例见
[配置模板](../configs/dmemd.example.json)。

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
curl -sS http://127.0.0.1:18432/v1/jobs/33333333-3333-4333-8333-333333333333
# 最近提取任务与独立的治理/聚合任务
curl -sS 'http://127.0.0.1:18432/v1/jobs?limit=20'
curl -sS 'http://127.0.0.1:18432/v1/governance-jobs?category=governance&limit=20'
curl -sS 'http://127.0.0.1:18432/v1/governance-jobs?category=aggregation&limit=20'
```

任务成功后可读取 `observation_ids` 和固定 `warnings`；业务 stage 到 observations_saved 即止，
合法空提取为 completed。`quality_outcome` 当前主要为 empty/complete/partial，枚举保留的
unusable 不代表当前路径会检出所有无效输出。解析器跳过的 item 不会完整计入后续统计，
不能用 empty 证明来源没有可记忆信息。`extraction_stats` 给出已解析 draft、Observation、待定、无效、去重和分块计数，
`diagnostics` 只含字段、固定代码、阶段和最终处置。`timings` 给出排队年龄、提取、规范化、
提交和总耗时，`model_usage` 给出调用数和 token；`queue_age_seconds`、`stale_seconds`、
`retry_reason` 用于发现长期等待和重试原因。失败时 `error` 是脱敏摘要，不包含完整模型响应或
原始输入。`succeeded` 只表示 Extraction 完成；治理和聚合进度通过各自任务目标查询。

`/v1/ingest-jobs/{id}` 是同一提取任务的别名；新客户端使用 `/v1/jobs/{id}`。
公共 `stage` 是业务展示阶段，失败位置由 `diagnostic_stage/reason_code` 说明，两者不要混淆。

```sh
# 尚未治理也可以读取 L0；将占位 ID 替换为实际值
curl -sS 'http://127.0.0.1:18432/v1/observations?source_id=<source-id>&limit=20'
curl -sS 'http://127.0.0.1:18432/v1/observations/<observation-id>'
curl -sS 'http://127.0.0.1:18432/v1/observations/<observation-id>/evidence'
curl -sS 'http://127.0.0.1:18432/v1/observations/<observation-id>/relations'
```

### 关系与冲突

```sh
# 普通 Memory 召回先 accepted 再明确标记 pending；相关冲突附带摘要与 warning
curl -sS 'http://127.0.0.1:18432/v1/memory-conflicts?status=open&limit=50'
curl -sS 'http://127.0.0.1:18432/v1/memory-conflicts/<conflict-id>'
```

当前视图不提供 `as_of` 或 `event_at` 过滤，也不保存 `event_time`、`valid_from`、`valid_to`
或时间 basis。相关能力将由独立的时间标注任务实现，而不是重新进入首次提取链路。

裁决请求必须携带 `revision` 与详情中的全部 `member_versions`：

```sh
curl -sS -X POST 'http://127.0.0.1:18432/v1/memory-conflicts/<conflict-id>/resolve' \
  -H 'Content-Type: application/json' \
  -d '{"revision":1,"action":"choose","member_ids":["<memory-id>"],"member_versions":[{"memory_id":"<memory-id>","version":2},{"memory_id":"<other-id>","version":1}]}'
```

### 列表、召回与当前记忆

```sh
# 默认返回最近更新的 50 条 active/archived 记忆，无分页
curl -sS 'http://127.0.0.1:18432/v1/memories?limit=20'

# 关键词召回；统一 any 语义，默认 top_k=20、仅 active
curl -sS -X POST http://127.0.0.1:18432/v1/memories/recall \
  -H 'Content-Type: application/json' \
  -d '{"query":"回答 语言","filters":{"kinds":["preference"]},"explain":true}'

# 自然语言问题与关键词列表使用同一预处理和 any 匹配
curl -sS -X POST http://127.0.0.1:18432/v1/memories/recall \
  -H 'Content-Type: application/json' \
  -d '{"query":"女儿什么时候上小学？"}'

# 查询会过滤受控停用词，但不设置最低命中数；
# 结果按 BM25 名次与词项覆盖率做有界重排

# 混合召回；语义不可用、限流、超时、响应非法或索引未就绪时自动降级为关键词并返回 warnings
curl -sS -X POST http://127.0.0.1:18432/v1/memories/recall \
  -H 'Content-Type: application/json' \
  -d '{"query":"吃饭有什么忌口","mode":"hybrid","top_k":10}'

# explain=true 返回候选/过滤/重排/预算阶段计数；关系与下钻引用由服务生成
curl -sS -X POST http://127.0.0.1:18432/v1/memories/recall \
  -H 'Content-Type: application/json' \
  -d '{"query":"回答 语言","query_mode":"detail","layers":["observations","memories"],"explain":true}'

# 概览选择 L1/L2/L3，指导选择 L4；不是时间点历史查询
curl -sS -X POST http://127.0.0.1:18432/v1/memories/recall \
  -H 'Content-Type: application/json' \
  -d '{"query":"修改后验证","scope_key":"project:dmemd","query_mode":"guidance"}'

# Embedding 配置指纹、向量覆盖率、待生成/失败任务和重建原因
curl -sS http://127.0.0.1:18432/v1/embedding/status

# 显式检查端点和响应形状；结果不持久化，不影响 readyz 或普通召回
curl -sS -X POST http://127.0.0.1:18432/v1/embedding/probe

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
curl -sS -X POST 'http://127.0.0.1:18432/v1/experiences/<experience-id>/retract'

# 默认只读取 active 当前版本；显式 version 读取不可变历史并返回其状态
curl -sS 'http://127.0.0.1:18432/v1/skills/<skill-id>/content'
curl -sS 'http://127.0.0.1:18432/v1/skills/<skill-id>/content?version=1'

# 同一召回入口在 L4 分区返回经验、指导与 Skill
curl -sS -X POST http://127.0.0.1:18432/v1/memories/recall \
  -H 'Content-Type: application/json' \
  -d '{"query":"修改后验证","scope_key":"project:dmemd","layers":["l4"]}'
```

成功反馈返回 `202` 和回执；`run_id` 重复时返回同一回执。Skill 内容响应包含渲染后的
`SKILL.md`、精确版本与状态；精确来源通过 `/v1/artifacts/<skill-id>` 的 typed dependencies 读取。
反馈不会绕过 ingest 直接写成原子
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

# 清除原文/证据并撤回关联 Observation、传播失效；无其他支持的 Memory 逻辑删除
curl -sS -X DELETE http://127.0.0.1:18432/v1/sources/22222222-2222-4222-8222-222222222222
```

清除响应：

```json
{"id":"22222222-2222-4222-8222-222222222222","redacted":true,"cancelled_jobs":1,"retracted_observations":1,"invalidated_derived_ids":[]}
```

### 永久遗忘

```sh
# 显式永久遗忘来源、原子记忆或应用报告；来源会展开为其关联记忆和结果
curl -sS -X POST http://127.0.0.1:18432/v1/forget \
  -H 'Content-Type: application/json' \
  -d '{"source_ids":["22222222-2222-4222-8222-222222222222"]}'
```

响应返回实际移除的排序后 ID 数组。请求在一个事务内完成：任一目标不存在或存储失败时
全部回滚。需物理移除的派生历史、FTS、依赖和在途任务引用先枚举后清除；其余依赖沿图递归
失效并重建/停用，不能将 stale/disabled 解释为历史正文已物理删除。

遗忘闭包包含：来源关联的任意历史版本 evidence 引用、Observation、显式记忆 ID，以及通过
`derived_dependencies` 直接或间接依赖这些对象的派生结果。删除只沿显式依赖边传播，不再按事实组
或时间区间猜测重复事实，也不会把高层摘要当作恢复已删除内容的来源。

没有其他合法输入的派生目标会被置为 `disabled`；仍有其他输入的重新登记构建任务。仅被失效的
间接派生历史仍可显式审计，这不同于物理移除对象。响应返回
实际移除的 ID 数组，不自动扩展未显式关联的记忆。

### 健康检查

```sh
curl -sS http://127.0.0.1:18432/healthz
curl -sS http://127.0.0.1:18432/readyz
```

`/healthz` 只反映进程存活；`/readyz` 检查数据库迁移和连接。模型不可达不影响
`/readyz`。同步错误使用简单 JSON：`400` 请求/裁决非法、`404` 资源不存在、`409` 冲突或成员
版本变化、`500` 执行失败、`503` 服务暂不可用。业务 API 不返回 `Location`、`ETag` 或版本控制头；
`/ui` 静态资源重定向/缓存是独立边界。

`--version` 输出构建版本后退出；`--check-config` 只加载并校验配置后退出。

## 首版限制

- 每用户单实例、回环 HTTP，无认证和多应用 ACL；同 UID 应用共享全部记忆，`app_id`、
  `scope_key` 和对话 role 都不是身份或权限凭据。
- 真实模型不可用时仍接收输入，已有 L0/Memory 可查；新提取、歧义关系和聚合会等待；服务不保证模型输出
  的语义真实性。
- 所有经验结果都是应用报告，Skill 只是可修正的指导文本，不提供真实性、执行成功、安全或
  工具授权认证；服务不生成或执行脚本。
- 旧质量/容量套件目前为 legacy，新架构的真实模型质量、分层容量、十万条、长期高并发、
  费用上限和 OS 应用隔离尚未验证。
- 不提供自动备份/恢复，不管理外部副本，也不承诺取证级物理擦除；永久遗忘只覆盖本服务正在
  保存的数据。
- 不提供 as_of/event_at 时间过滤、时间线裁决或时间 annotation Worker；旧 atomic/summary/
  experience/skill layers 不再接受，请使用 observations/memories/l1/l2/l3/l4。
- 自动冲突复查只处理成员集合本身足以证明的确定性规则；复杂边界保持 `open`，等待新证据或
  显式裁决。

更多架构依据、接口契约和开发文档见[文档索引](README.md)。
