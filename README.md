# dmemd

`dmemd` 是面向同一操作系统用户的本地 AI 记忆服务。采用 Go 单进程、SQLite 与
HTTP/1.1 JSON API，支持关键词检索与基于 Embedding 的语义混合检索。

## 功能概览

- 统一异步接收文本、对话和结构化内容，模型提取与记忆合并由后台单 Worker 完成。
- 原子记忆保存平面 `scope_key`、带中英文别名和父主题继承的 canonical `topics` 与审计用 `raw_topics`；治理 Worker 自动补全分类并周期性对账，
  分类结果无变化时不生成新版本。
- 治理 Worker 自动生成有直接原子依据的主题摘要、作用域概览、作用域画像、经验案例和指导型
  Skill，保存不可变派生版本、直接来源和 FTS。原子依据或运行结果变化时同步失效；
  `POST /v1/memories/recall` 按 `layers` 统一返回原子、摘要、经验与 Skill 分区。
  `GET /v1/artifacts` 与 `GET /v1/artifact-targets` 分别发现派生对象和生成目标状态。
- 应用可提交完成结果反馈（success/failure/unknown/partial_success，均视为应用报告而非执行验证），
  可选 `run_id` 去重；失败反馈自动使相关 Skill 失效并后台重建，撤销结果同步阻断依赖使用。
  Skill 自动启用并保留来源，`GET /v1/skills/{id}/content` 返回带 frontmatter 的 SKILL.md。
- 记忆具有完整版本、证据和生命周期：创建、更正、补证、归档、删除、历史恢复与永久清除。
- 记录时间、陈述/观测时间、事件时间和事实有效时间分开建模；`filters.as_of` 只查询事实有效
  时点，`filters.event_at` 按事件发生时间查询。未知边界显式标记为 `temporal_status=unknown`，
  不伪造月份/日期精度，也不把接收时间回退当作新事实更晚的证据。
- 原子记忆保留必要的主体、对象、极性、数量/单位、条件和归属；结构化 procedure 随版本、
  内容 hash、失效和永久遗忘闭包保存；模型缺失 kind 时进行有诊断的推断，不再静默按 fact 处理。
- 两阶段模型整理：先提取独立事实，再按候选选择有限冲突上下文并按需调用决策模型；支持
  重复、补充、变化、更正、场景分离和未决冲突，非法目标不再降级为新增。
- 断言状态（accepted/pending/rejected）、事实组、版本关系和冲突审计独立持久化。普通召回
  优先返回 accepted，再展示明确标记的 pending；未决冲突通过 `conflict_refs`、`conflicts`
  摘要和 `fact_conflict` 警告提示，rejected 不参与召回。
- 事实身份由显式 `scope_key`、主体、谓词、场景和必要条件共同确定，并先按身份读取同组
  时间线，不依赖新值正文能否召回旧值。重复断言只补证；状态变化保留旧区间并生成后继；
  明确更正拒绝旧断言；改期/取消关联同一事件；独立观测不制造冲突。每次自动演化在同一事务
  写入 `supersedes`、不可变演化审计和必要时的 resolved 冲突；证据、时间、单值属性或
  作用域不明确时保持 pending，并在任务的 `convergence` 中返回固定原因。
- 冲突管理接口支持查看 open/resolved 冲突与成员检测版本，并通过
  `choose/timeline/separate_scope/reject_all` 显式裁决；版本变化返回 409。
- 关键词检索使用 NFC/大小写规范化与中文词典分词，基于 SQLite FTS5/BM25，支持过滤、
  可读摘要、证据引用和按需解释。
- 混合检索并行执行关键词与 Embedding 余弦召回，按 RRF 融合；语义模型不可用、限流、
  超时、响应非法或索引未就绪时自动降级为关键词结果，并通过 `warnings` 返回稳定代码。
  `GET /v1/embedding/status` 提供当前配置指纹、端点（无凭据）、文档/当前向量覆盖、待生成/
  失败任务与重建原因；`POST /v1/embedding/probe` 显式检查连接和响应形状。Embedding 默认关闭。
- 召回先返回主命中，再只在剩余 `top_k` 名额内展开同事实组成员和未决冲突成员；关联项保留
  相同 scope/kind/state/时间过滤并带 `related` 标记。同一事实的完全重复表述折叠为
  `cluster`，合并成员证据但不合并否定、条件或时间不同的事实。`explain=true` 返回有界的
  候选、过滤、重排和预算阶段计数。
- 提供架构规定的全部本地 HTTP API、用户数据目录单实例锁和优雅关闭。
- 当前没有认证、分页、幂等键、条件请求或独立前端。

所有关键词查询统一使用 any 语义：按词典分词、过滤受控停用词并保留否定词，命中任一有效词
即可召回。原子层、派生层、相关事实选择及 hybrid 的关键词分支使用相同预处理；无有效词时
返回空结果，不执行无约束查询。关键词模式按 BM25 名次与词项覆盖率做有界重排，不设置最低
命中数过滤。语义不可用时 hybrid 降级为关键词结果，并返回 `semantic_unavailable` 等警告。
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
之后最长每 30 分钟重试，模型恢复后自动继续，无需重新提交。局部修复后仍连续失败达到
`worker.max_attempts`（默认 3）次解析/协议错误才进入
`failed`；凭据或请求配置错误保持等待并在重启后重评。协议失败任务在进程重启后也会按新的
模型/协议版本重新评估一次。失败原因通过任务查询返回。

`worker.process_budget` 默认 `600s`，覆盖一次完整处理尝试；`worker.extraction_timeout`
默认 `180s`，`worker.decision_timeout` 默认 `300s`，分别限制每次提取/决策及其修复调用。
`model.extraction`、`model.decision`、`model.governance` 可分别覆盖模型名、超时和
`reasoning_effort`（`none/minimal/low/medium/high`）；留空时回落到 `model.name`/`model.timeout`。
按任务拆分配置不等于用更快的模型修复协议或语义问题：规范化、证据校验和来源约束仍由服务
统一执行。`worker.extraction_chunk_runes` 默认 `12000`，超过预算的来源按稳定片段分块提取，
块间携带只读上下文用于指代和重复消解；单条超长字段本身也按固定片段切分，超大块会带
`extraction_chunk_oversized` 诊断。每来源仍最多接受 64 个候选，超出部分保留前 64 个并显式
记录 `candidate_limit_exceeded` 和 `partial`，不再静默截断或让整个来源不可用。

waiting/failed 任务返回稳定的 `stage` 和 `reason_code`，例如 `extraction` +
`process_budget_exceeded` 或 `decision` + `model_timeout`。`separate` 的非法目标字段直接移除；
`change` 的多目标结果仅定向修复一次，不猜测目标，也不重做已通过的同批决策。无相关事实、
无事实组且候选身份完整时，多候选批次可确定性新增而不调用决策模型；空关键词结果本身
不被当成“全库确无相关事实”。

分类、主题摘要、作用域概览、作用域画像和周期性对账由一个独立治理 Worker 执行；治理故障只降级后台
整理，不关闭 HTTP、原子查询或输入持久化。`governance.enabled=false` 只停止后台整理，原子
写入和查询继续可用。默认 `governance.debounce` 为 30 秒、对账间隔 15 分钟、单次派生输入
上限 64 条，治理模型调用受进程内软限速（默认最少间隔 2 分钟、每小时约 30 次）约束。配置
包含 `enabled`、`debounce`、`reconcile_interval`、`min_call_interval`、`max_inputs` 和
`max_wait`；`max_wait` 默认 30 分钟，表示连续摄取最多可推迟一个已到期治理目标多久，
超过后允许一次公平领取。示例见 `configs/dmemd.example.json`。

治理领取任务时让位于正在运行或已到期的摄取任务；摄取退避/等待配置期间不阻塞治理。
分类和冲突复查优先于 aggregate/Skill 批量生成。同一 scope/topic 只有一个待办目标，新的
原子版本在同一事务中替代旧待办 revision 并重新 debounce，旧 revision 不再执行或提交。
已发出的治理模型请求不抢占取消。日志按 `topic_summary`、`profile`、`skill` 独立记录调用数、
失败数、队列年龄和耗时，沿用共享治理限流预算，不记录原文或模型响应。

有新的相关版本或证据时，治理 Worker 会自动复查未决冲突；只有当前成员集合本身足以证明结果
时才自动裁决，复杂重叠和未知边界保持隔离。未决冲突成员不会进入摘要、经验案例或 Skill
输入，普通原子查询仍返回冲突说明。`POST /v1/forget` 可按原子、来源或结果显式永久遗忘，
在同一事务内先枚举并清除派生历史、索引和依赖，再清除叶子数据；`DELETE /v1/sources/{id}`
仍只是清除原文的逻辑操作。

`POST /v1/memories/recall` 是唯一召回入口，默认返回原子记忆、摘要、经验和 Skill 分区；
`layers` 可只选部分层。主题摘要、作用域概览、作用域画像、经验案例与 Skill 由治理 Worker
自动生成并保存直接原子/运行依据；依据修改、归档、删除或结果撤销时旧内容同步退出默认召回，
随后后台重算。历史可通过 `GET /v1/artifacts/{id}` 和 `/history/{version}` 查看；
`GET /v1/artifacts` 按 scope/kind/state/reason 发现对象，`GET /v1/artifact-targets` 同时返回
尚无对象的目标、排队/运行/等待任务、无合格输入、生成失败、已有可用版本和 stale 状态。
经验结果来自应用报告，Skill 只提供可修正的指导文本，不提供真实性、执行成功或安全认证。

### 派生质量与来源审计

内置 `taxonomy-v1` 只保留通用概念；`K8s`、`节点内存耗尽` 会保存为
`topics=[containers,kubernetes,oom]`，原始拼写保留在 `raw_topics`。未知业务实体保留为
作用域内扩展标签，不按 `/` 机械拆成层级；只有每一段都是已知概念时才展开已知父子关系。
父主题摘要直接聚合子主题的原子事实，不再聚合子摘要。`scope_summary` 是仅由 scope 和派生
类型确定目标的通用概览，综合现状、约束、关键历史和计划；Profile 不再承担项目进度摘要。

- Profile 只接收 `durability=durable`、无事件 phase、无结束边界的 fact/preference/procedure，
  未知持久性、一次性事件、临时进度和单次行程不进入画像；生成文本另记录 durability/引用检查。
- 原子记忆返回服务派生的 `retention_reason`（明确意图、未来适用、持久知识、一次性事件、
  临时上下文或未分类）。服务不会仅凭通用信号自动丢弃低价值候选；低价值判断只进入
  `retention` 诊断，便于同时观察噪声写入和有用信息漏记。临时性只影响展示和后续维护审阅，
  不触发自动 TTL 或不可逆删除；显式“不要记/不要保存”仍由摄取硬边界排除，不能被 repair
  或后续重试复活。
- 摘要输入受条数、单条字数和总字数限制。payload 分开记录 `input_selection`（合格、选入、
  实际包含、省略数量和原因）、`citation_integrity`、`semantic_coverage` 及合法 `citations`/
  `omitted_inputs`；无法确定正文是否覆盖时使用 `unknown`，服务已知省略时使用 `subset`，
  不靠正文追加子集声明冒充质量。
  输入签名相同的活跃目标不再重复调用模型；旧/新主题和父主题变化仍会使目标失效。
- 经验案例正文始终附带“这是应用报告，不代表独立验证”的等效声明（按来源语言渲染），结果状态
  由服务保留，不能被模型升级。
- Skill 先检查 procedure 资格；没有输入的目标只记录 `no_eligible_input` 评估，不再创建空
  artifact。已有 Skill 失去来源时仍立即 `stale/disabled` 并保留历史。同 scope 的经验不会
  自动进入 Skill；只有显式 Skill 引用或经验案例中同一工作流主题才相关。模型未配置、生成失败、
  输出不合格分别记录 `model_unavailable`、`model_generation_failed`、`invalid_model_output`，
  激活要求可用 revision、直接来源和结构化内容检查。
- 每个 revision 保存精确 memory/version、应用结果引用、生成 `run_id`、taxonomy/generator/protocol
  version、截断标记及内容 hash；独立的不可变 `provenance` 清单可发现数据库依赖丢失。
  正文、kind、时间、断言、fact group、主题或生命周期变化立即使依赖及旧/新主题目标 stale，
  新增事实也会使对应父摘要失效；补证但断言不变时保留已有历史版本引用。

服务停止后可对同一配置的数据库执行只读审计（持有相同单实例锁，不启动 HTTP 或模型）：

```sh
./bin/dmemd --config ~/.config/dmemd/config.json --artifact-integrity-check
# {"checked":12,"issues":[]}
```

检查来源版本与删除引用、序列化清单/依赖表、截断标记、生成元数据、内容 hash 和搜索文档版本；
发现问题输出 artifact/version/固定错误码并以非零状态退出，不自动修复或回填。
项目尚未发布，schema 直接更新；本次实现只面向全新数据库，不提供旧库迁移。

`embedding.enabled=true` 时需配置端点、模型版本、维数和超时；`min_similarity` 应按
模型用相关/不相关样例校准。配置只包含这些必要项，不提供输入前缀、重试或查询预算的
细粒度开关。启用后启动时会重建派生搜索文档，为缺失或配置不匹配的向量建立持久化任务，
并由独立 Worker 在后台生成。模型、维数或文档构造方式变化会登记重建原因，旧向量退出当前
配置召回，只有 profile、维数和内容 hash 全部匹配的向量参与语义分支。Embedding 故障不影响
`/readyz`，`hybrid` 检索会降级并返回 `warnings`；`GET /v1/embedding/status` 区分配置
不匹配、索引未就绪、积压和失败任务，存在文档但当前向量为零时返回
`semantic_index_not_ready`，不会把空向量结果解释为语义不相关。配置示例见
`configs/dmemd.example.json`。

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
`warnings`；`quality_outcome` 区分合法空提取（`empty`）、全部安全处理（`complete`）、
部分候选隔离（`partial`）和候选全部不可用（`unusable`），`extraction_stats` 给出原始/
规范化/repair/拆分/隔离/接受/分块/丢弃等阶段计数，`diagnostics` 只含候选 ID、字段、固定
代码和最终处置。`retention` 按固定原因分别记录保留与未保留候选，便于同时观察噪声写入和
有用信息漏记；它只用于诊断，不会自动删除记忆。`timings` 给出排队年龄、提取、规范化、
repair、上下文、决策和提交耗时，`model_usage`/`efficiency` 给出调用数、token 和每个有效
原子的成本；`queue_age_seconds`、`stale_seconds`、`retry_reason` 用于发现长期等待和重试
原因。失败时 `error` 是脱敏摘要，不包含完整模型响应或原始输入。`succeeded` 表示处理完成，
不保证所有断言都被采纳。

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

按事件发生时间查询历史或未来安排：

```sh
curl -sS -X POST http://127.0.0.1:18432/v1/memories/recall \
  -H 'Content-Type: application/json' \
  -d '{"query":"复诊","filters":{"event_at":"2026-11-20T12:00:00Z"},"layers":["atomic"]}'
```

`event_at` 只匹配 `event_time` 精度窗口覆盖该时刻的 event；事件时间未知的记录不会作为确定发生结果返回。

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

# explain=true 返回候选/过滤/重排/预算阶段计数；关联项带 related，重复事实折叠为 cluster
curl -sS -X POST http://127.0.0.1:18432/v1/memories/recall \
  -H 'Content-Type: application/json' \
  -d '{"query":"回答 语言","layers":["atomic"],"explain":true}'

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

遗忘按三层闭包执行：来源关联包含任意历史版本的 evidence 引用；重复事实仅在同非空事实组、
同 scope、kind 和 phase、正文规范化后一致且有效时间相同或重叠时一并删除（包括已逻辑删除的
副本）；依赖这些记忆的摘要、画像、经验、Skill 全部历史和冲突一并清除，相关治理任务取消或
递增 revision 后使用剩余输入重建。正文规范化只处理 NFC、大小写、空白和句尾标点，不用模糊
编辑距离猜测语义，避免误删否定、数量或不同主体的独立事实。没有事实组不自动扩展；时间完全
未知与有界区间不视为已证明重叠。

同组相关记忆不满足安全删除条件时保留，并在响应中明确提示，例如：

```json
{"memory_ids":["memory-1"],"source_ids":[],"experience_ids":[],"warnings":["related_memories_retained"],"retained_related_memory_ids":["memory-2"]}
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
