# API 决策记录

本文件补充《系统 AI 记忆服务架构设计.md》v1 未展开的接口语义，并与 `api/openapi.yaml`
共同构成 HTTP 契约。它是文档契约，不生成业务校验层。

## 1. 通用约定

| 项目 | 决策 |
| --- | --- |
| 基础路径 | `/v1`；健康检查位于根路径 `/healthz`、`/readyz` |
| 默认监听 | `127.0.0.1:18432`，端口可配置；首版只允许回环地址 |
| 传输 | HTTP/1.1，UTF-8 JSON；有 JSON 请求体时只需 `Content-Type: application/json` |
| 认证 | 无。`Authorization: Bearer ...` 可省略、可任意填写，服务不读取、不验证、不据此确定应用身份 |
| 其他请求头 | 不要求签名、应用标识、幂等键、预期版本或分页头 |
| 业务校验 | 不做身份、字段取值、证据、版本、重复提交、内容长度或请求大小校验 |
| ID | 服务生成的 UUID 字符串；来源、任务、记忆、证据分别有独立 ID |
| 时间 | UTC RFC 3339，例如 `2026-10-20T02:00:00Z`；不使用本地时区持久化 |
| 字段命名 | JSON 一律 `snake_case` |
| 响应头 | 不返回 `Location`、`ETag` 或版本控制头；有响应体时 `Content-Type: application/json` |
| 列表 | 不分页，不返回页码、游标或下一页；一次返回过滤后的本次结果 |
| 错误体 | `{"error":"...","code":"..."}`；`code` 只在冲突裁决等需要固定原因时出现 |
| 状态码 | 400 请求/裁决非法；404 资源不存在；409 冲突、成员版本或恢复前状态已变化；500 内部失败；503 服务暂不可用 |

未知 JSON 字段默认忽略。缺少可选辅助字段时使用本文或 `model-provider.md` 的默认值。
显式非法的 `filters.as_of` 返回 `400`，不会静默变成当前时间；冲突裁决版本变化返回 `409`。
模型调用失败不改变同步 HTTP 业务响应，通过任务查询体现。

## 2. 统一提交

### `POST /v1/ingestions`

请求体是 JSON 对象：

```json
{
  "app_id": "desktop-assistant",
  "external_id": "conversation-42-batch-7",
  "scope_key": "project:dmemd",
  "content": {"messages": [{"role": "user", "content": "以后请默认使用中文回答。"}]}
}
```

- `content` 可为字符串、对象、数组、数字、布尔或 `null`；不要求调用方说明写入模式。
- `app_id`、`external_id` 可选，仅作为来源信息；无唯一约束、无授权含义，不参与应用隔离。
- `scope_key` 可选，是平面作用域标签（例如 `user`、`project:dmemd`）；缺省表示作用域未知，
  不会自动提升为用户全局知识。模型不能自行修改该值。
- `content` 缺失按 JSON `null` 保存；服务正常接收并排队，提取不到记忆时任务成功且结果为空。
- 记忆保存后，同事务登记去抖的分类治理任务；分类在后台自动完成，调用方无需逐项触发。
- 同一请求重复提交按普通提交处理：每次生成新的来源和任务，不做请求级幂等；内容层面的
  重复由后台整理时尽量合并。
- 来源和初始 `queued` 任务在同一事务提交成功后返回 `202`：

```json
{"source_id":"source-uuid","job_id":"job-uuid","status":"queued"}
```

- 返回不等待模型调用，也不提供直接写入记忆或单独创建来源的接口。

## 3. 记忆管理

### `GET /v1/memories`

查询参数：

| 参数 | 语义 | 缺失/无效处理 |
| --- | --- | --- |
| `kinds` | 逗号分隔的 `fact,preference,event,procedure` | 缺失或空表示不按类型过滤；无法识别的值忽略 |
| `states` | 逗号分隔的 `active,archived,deleted` | 缺失或空默认为 `active,archived`（不含删除墓碑） |
| `assertion_status` | 逗号分隔的 `accepted,pending,rejected` | 缺失或空表示全部断言状态；全部不可识别时返回空列表 |
| `fact_group_id` | 事实组 ID | 缺失或空表示不过滤 |
| `created_by_app` | 创建该记忆的来源应用 | 缺失或空表示不过滤 |
| `updated_after` | RFC 3339，`updated_at` 下限 | 无法解析时忽略该边界 |
| `updated_before` | RFC 3339，`updated_at` 上限 | 无法解析时忽略该边界 |
| `limit` | 本次返回条数 | 缺失、非正整数或无法解析时使用 50 |

过滤列表存在但没有任何可识别值时，返回空 `items`，不扩大为默认集合。排序固定为
`updated_at DESC, id ASC`。响应：

```json
{"items":[{"id":"memory-uuid","kind":"preference","title":"回答语言","content":"用户希望默认使用中文回答。","state":"active","assertion_status":"accepted","version":3,"observed_at":"2026-10-20T02:00:00Z","valid_from":"2026-01-01T00:00:00Z","valid_to":null,"valid_from_basis":"explicit","valid_to_basis":"unknown","fact_group_id":"group-uuid","created_at":"2026-10-20T02:00:01Z","updated_at":"2026-10-20T03:00:00Z","created_by_app":"desktop-assistant","origin_source_id":"source-uuid"}]}
```

无分页信息；`limit` 没有业务上限。`created_by_app` 是创建时来源的 `app_id` 快照，
`scope_key` 为可选平面作用域，`topics` 为规范化平面标签数组（无匹配时 `[]`）。
`retention_reason` 是服务派生的保留理由（`explicit_intent`、`future_applicability`、
`durable_knowledge`、`episodic_event`、`temporary_context` 或 `unclassified`），只用于展示和
维护审阅，不删除记忆、不改变断言状态，也不自动生成 TTL。
后续更新和恢复保持不变；`origin_source_id` 是首次生成该记忆的来源。两者都不承担身份或
唯一性。

### `GET /v1/memories/{id}`

返回当前快照，包括 `state=deleted` 的墓碑，以及只读 `relations` 数组。不存在返回 `404`。
删除后仍可按 ID 读取墓碑，但任何全文检索都不返回删除记录。

### `PATCH /v1/memories/{id}/state`

只修改生命周期状态，请求体：

```json
{"state":"archived"}
```

- 只接受 `active`、`archived`。删除使用 `DELETE`；`deleted` 不作为 PATCH 值。
- 请求体中的 `content`、`title`、`kind`、`version` 等字段一律忽略，不形成内容直接写入捷径。
- `state` 缺失、未知或与当前状态相同：不产生新版本，返回当前记忆（`200`）。
- 内容更正仍只能通过 `POST /v1/ingestions` 提交。
- 响应为更新后的完整记忆对象。

### `DELETE /v1/memories/{id}`

- 逻辑删除：`state=deleted`，生成新版本，保留历史、证据和墓碑。
- 响应：`{"id":"memory-uuid","state":"deleted"}`。
- 已删除记忆再次逻辑删除按状态未变化处理。

### `POST /v1/memories/{id}/purge`

- 永久清除：删除当前记录、全部历史、版本证据关联、专属证据和索引；共享来源保留。
- 无请求体。永久清除不存在的 ID 返回 `404`。
- 响应：`{"id":"memory-uuid","purged":true}`。

### `POST /v1/forget`

显式范围的永久遗忘，一次请求可同时指定 `memory_ids`、`source_ids` 和 `experience_ids`，
至少提供一个：

```json
{"source_ids":["source-uuid"],"experience_ids":["experience-uuid"]}
```

- 来源会先展开为所有关联原子记忆和应用结果，再整体清除；结果会同时清除其专属报告来源。
- 来源关联包括当前/历史 `origin_source_id` 和任意版本 evidence 的 `source_id`，即便当前 origin
  指向其他来源也必须清除整条记忆。
- 再计算重复事实闭包：同非空 fact group（主体、谓词明确）、同 scope、kind、phase、正文
  NFC/大小写/空白/句尾标点规范化后一致，且有效时间相同或重叠。包括 archived/deleted 副本，
  不受普通列表上限影响。完全未知区间仅与同样完全未知的区间视为相同；相邻半开区间不重叠。
  不用编辑距离扩大删除范围，不跨主体/事实组猜测语义重复。
- 删除顺序固定为枚举依赖、清除派生对象全部版本/索引/依赖、清除原子/结果/来源叶子和引用
  任务；不先依赖外键级联再寻找受影响对象。
- 整个请求在一个事务内完成。任一 ID 不存在、外键冲突或存储失败都会回滚全部目标；不会
  部分清除后返回成功。
- 记忆历史、证据、派生历史、Skill 渲染和 FTS 在提交后均不可读；共享的其他来源不受影响。
- 依赖目标的整个 open/resolved 冲突及审计事件删除，review 任务取消；派生对象即便仅历史
  版本依赖目标也整体删除，其目标任务递增 revision 后从剩余输入重建，拒绝在途旧结果。
- 响应返回实际移除的排序后 ID 数组：

```json
{"memory_ids":["memory-uuid"],"source_ids":["source-uuid"],"experience_ids":["experience-uuid"]}
```

同组相关但未安全删除的记忆，以排序去重 ID 返回；没有保留项时省略以下两个字段：

```json
{"memory_ids":["memory-1"],"source_ids":[],"experience_ids":[],"warnings":["related_memories_retained"],"retained_related_memory_ids":["memory-2"]}
```

`DELETE /v1/sources/{id}` 仍是逻辑清除原文；`POST /v1/forget` 才是永久删除来源、关联记忆和
结果记录。首版不提供自动备份、外部副本管理或取证级物理擦除承诺。

## 4. 版本、恢复与证据

### `GET /v1/memories/{id}/history`

返回完整版本快照，按 `version DESC` 排序。可选 `limit`：缺失、非正整数或无法解析时使用
50；没有分页。响应：

```json
{"items":[{"memory_id":"memory-uuid","version":3,"operation":"update","reason":"用户更正回答语言","snapshot":{"id":"memory-uuid","kind":"preference","title":"回答语言","content":"用户希望默认使用中文回答。","state":"active","version":3,"observed_at":"2026-10-20T02:00:00Z","created_at":"2026-10-20T02:00:01Z","updated_at":"2026-10-20T03:00:00Z","created_by_app":"desktop-assistant","origin_source_id":"source-uuid"},"evidence":[{"id":"evidence-uuid","source_id":"source-uuid","message_id":"msg-1","quote":"以后请默认使用中文回答。","location":{"field":"messages.0.content","start":0,"end":12},"model_name":"example-model","redacted":false,"created_at":"2026-10-20T02:00:01Z"}],"created_at":"2026-10-20T03:00:00Z"}]}
```

记忆不存在返回 `404`。历史只读；恢复旧版本不会覆盖或删除旧版本。

### `GET /v1/memories/{id}/history/{version}`

返回指定版本的完整快照与证据集合。`version` 非整数、非正数或不存在时返回 `404`。
记忆不存在同样返回 `404`。

### `POST /v1/memories/{id}/history/{version}/restore`

- `version` 位于路径中并指向历史版本；缺失、非正数、非整数或不存在时返回 `404`。
- 无请求体。
- 恢复复制该版本内容为新的当前版本，状态恢复为 `active`，版本号高于恢复前的当前版本；
  历史版本本身保持不变。断言状态不被提升：历史待确认/已否定仍保持；恢复为 accepted 时
  必须通过当前事实组冲突检查，否则整个恢复返回 `409`（`code=restore_conflict`）。
- 已清除原文的来源不会因恢复重新出现片段；证据只保留来源关联与 `redacted` 状态。
- 不要求调用方提供当前版本、预期版本或冲突令牌。
- 响应为恢复后的完整记忆对象。

### `GET /v1/memories/{id}/evidence`

可选查询参数 `version`：

- 缺失、`0`、非整数或非正数：返回当前版本完整证据集合。
- 正整数的记忆版本不存在：返回 `404`。
- 记忆不存在：返回 `404`。

证据按 `created_at ASC, id ASC` 返回，不分页。来源清除后，`quote`、`location` 和原文副本
不再返回，条目保留 `redacted=true` 与来源 ID。响应 `{"items":[...]}`。

## 5. 分层召回

### `POST /v1/memories/recall`

唯一召回入口，一次请求可返回原子记忆、摘要、经验和 Skill 四个分区。普通调用方只需提交
查询和必要上下文；仅查原子记忆时显式传 `layers: ["atomic"]`，不另设入口。

```json
{
  "query": "修改 dmemd 后应该如何验证",
  "scope_key": "project:dmemd",
  "layers": ["atomic", "summary"],
  "max_context_tokens": 3000,
  "filters": {"kinds": ["procedure"], "states": ["active"]},
  "top_k": 20,
  "explain": true
}
```

| 字段 | 默认/边界 |
| --- | --- |
| `query` | 关键词列表和自然语言问题统一分词、去重、过滤受控停用词并保留否定词；命中任一有效词即可召回，不暴露 FTS 语法；空白、纯标点或无有效词返回 `200` 与空分区且不执行检索；语义路保留原始问题，只做 NFC/首尾空白清理 |
| `scope_key` | 可选平面作用域；提供时原子与派生结果只接受该作用域或 `user` 级信息；缺失表示不做作用域过滤；未知作用域不会提升为全局结论 |
| `layers` | 缺失选择全部层；显式列表只检索对应分区；值只能是 `atomic/summary/experience/skill`，其他值返回 `400` |
| `mode` | 缺失或空为 `keyword`；支持 `keyword`、`hybrid`，其他值返回 `400`；只影响原子层，派生层固定使用派生 FTS |
| `filters.kinds/states` | 只过滤 `atomic` 分区；不会隐式排除摘要、经验或 Skill；`states` 缺失默认 `active`，`deleted` 永不返回 |
| `filters.as_of` | 事实有效时点，只支持原子层。所选 layers 含派生层时这些分区返回空数组并附 `derived_as_of_unsupported`；只选 `atomic` 不产生该提示。未知有效边界仍可返回但标记 `temporal_status=unknown` 并附 `temporal_uncertain` |
| `filters.event_at` | 事件发生时点；只匹配 `event_time` 精度窗口覆盖该时刻的事件，事件时间未知的事件默认不返回。单独提供时不把事实有效区间固定到当前时刻，历史事件和未来计划都可按发生时间查询 |
| `filters.created_by_app/updated_after/before` | 只过滤原子层；时间边界无法解析时忽略 |
| `top_k` | 缺失、非正整数时使用 20；大于 100 返回 `400`；上限按所选层分别执行 |
| `max_context_tokens` | 可选保守预算，按 rune 数估算而非模型精确 tokenizer；超过预算时截断后续条目并返回 `context_budget_truncated` |
| `explain` | 默认 `false`；控制原子条目的 `explanation` 和响应级 `diagnostics` 阶段计数。计数为有界诊断，不改变结果 |

原子层、派生层、相关事实选择、冲突发现及 hybrid 的关键词分支统一使用过滤后的词项和 any
语义，不提供匹配方式字段。原子层沿用加权 BM25（标题 4、正文 1）、覆盖率重排和 RRF 混合语义；`score` 只在当前查询
内比较。派生层按 `(kind,target_key)` 的当前版本检索，只有 `state=active` 的当前版本进入
默认召回；`stale/disabled` 历史可通过 `GET /v1/artifacts/{id}` 或历史接口读取，但不会出现
在默认召回。摘要、经验与 Skill 使用同一分词规则建立独立 FTS 索引，不与原子分数相加。

主命中之后可追加有界关联项：同一事实组的成员和同事实未决冲突成员。关联项遵守与主查询
完全相同的 scope、kind、state、assertion 和有效/事件时间过滤，只使用剩余 `top_k` 名额，
不替换主命中，并以 `related.reason_codes`、`related.anchor_memory_ids` 明确标记。事实组
成员用于补齐偏好另一半、事件限定或流程条件，不扩展整个来源或会话。语义路不可用时，
`warnings` 区分 `semantic_unavailable`、`semantic_timeout`、`semantic_config_error`、
`semantic_rate_limited`、`semantic_invalid_response` 和 `semantic_index_not_ready`；
后两者分别表示响应不合法和存在文档但当前配置没有可用向量，空向量结果不会被解释为
“语义上不相关”。

同一事实的完全重复表述折叠为展示簇：要求相同事实组、scope、kind、phase、state、断言状态、
有效/事件时间和规范化标题/正文。折叠项通过 `cluster.member_ids` 暴露全部记忆 ID，并把
各成员证据合并到代表性的 `evidence_refs`；否定、条件或时间不同的独立事实不会被折叠。
`explain=true` 时返回可选 `diagnostics` 阶段计数：关键词/语义候选、词项候选、kind/state/
时间排除、参与重排数、关联展开/排除、返回数和预算裁剪数。过滤排除计数只做同 scope、保留
其他边界的单过滤放宽计数，不返回跨作用域正文，也不改变实际结果；`kind_filter_excluded`
只说明候选被过滤，不能据此断言模型分类错误。

响应按知识层分区，未请求、未命中或暂不可用的分区返回空数组：

```json
{
  "memories": [
    {
      "memory": {"id":"memory-uuid","kind":"procedure","title":"验证步骤","content":"修改后执行 go test。","state":"active","version":2,"observed_at":"2026-10-20T02:00:00Z","created_at":"2026-10-20T02:00:01Z","updated_at":"2026-10-20T03:00:00Z","scope_key":"project:dmemd","topics":["go","testing"]},
      "score": 0.03252247,
      "snippet": "修改后执行 go test。",
      "matched_terms": ["验证"],
      "evidence_refs": [{"id":"evidence-uuid","source_id":"source-uuid"}],
      "explanation": {"ranking":"rrf","keyword":{"rank":1,"bm25":-0.0000042},"semantic":{"rank":2,"cosine":0.93}},
      "temporal_status": "unknown",
      "conflict_refs": []
    }
  ],
  "summaries": [
    {
      "id":"artifact-uuid",
      "kind":"topic_summary",
      "version":1,
      "title":"验证流程",
      "content":"修改后应先运行单元测试，再执行构建检查。",
      "scope_key":"project:dmemd",
      "topic":"testing",
      "state":"active",
      "truncated":false,
      "memory_refs":[{"memory_id":"memory-uuid","version":2}],
      "updated_at":"2026-10-20T03:10:00Z"
    }
  ],
  "experiences": [],
  "skills": [],
  "conflicts": [],
  "warnings": []
}
```

`summaries` 同时返回当前 active 的 `topic_summary`、`scope_summary` 和 `profile`；三者
共用摘要分区和派生 FTS，但目标是不同派生类型。`experiences` 返回当前 active 的经验案例，
`skills` 返回当前 active 的指导型 Skill；未生成、失效或停用时为空数组。每个条目保留对象
ID、版本、状态、结构化 `payload`、直接原子依据 `memory_refs` 和应用结果依据 `run_refs`。经验结果一律是应用报告，Skill 只是可修正的指导
文本，响应不包含真实性或执行认证。派生索引故障只降级派生分区并返回
`derived_unavailable`，不阻止原子结果；召回不等待后台聚合、经验案例或 Skill 生成。
原子分区沿用既有 `snippet`、`matched_terms`、`evidence_refs` 和冲突说明语义。语义路关闭
或失败时回退关键词并返回稳定代码：`semantic_unavailable`、`semantic_timeout`、
`semantic_config_error`、`semantic_rate_limited`、`semantic_invalid_response` 或
`semantic_index_not_ready`。

### `POST /v1/experiences`

保存一次完成结果反馈：原文作为来源保存，结构化结果写入 `experience_runs` 并立即登记经验
案例任务。`outcome` 为 `success/failure/unknown`，缺失按 `unknown`；它是应用自报结果，服务
不验证、不转换为“已验证成功”，也不因未报错推断成功。`goal` 必填，`conditions`、`actions`
为字符串数组，`skill_id`/`skill_version` 可指出本次运行针对的精确 Skill。

`run_id` 可选：提供时以唯一约束去重，重复提交直接返回已有回执，不比较正文、不返回 409、
不覆盖原结果，也不恢复已撤销结果；未提供时每次创建新记录。响应 `202`：

```json
{"id":"experience-uuid","source_id":"source-uuid","run_id":"run-1","outcome":"success","state":"recorded","created_at":"2026-10-22T08:00:00Z","updated_at":"2026-10-22T08:00:00Z"}
```

提交失败反馈不会直接写入原子事实；需要形成原子事件时仍走 `POST /v1/ingestions`。

### `POST /v1/experiences/{id}/retract`

显式撤销错误结果。同一事务内将结果置为 `revoked`、取消该案例的未完成任务、停用案例，
并让当前版本依赖该结果的 Skill 立即变为 `stale`、退出默认召回并登记后台重建。操作可重复
调用；已撤销结果不会被重复提交的相同 `run_id` 恢复。结果不存在返回 `404`。

### `GET /v1/skills/{id}/content`

默认返回 `state=active` 的当前版本；当前对象失效或停用时返回 `404`，不会把 `stale/disabled`
正文当作当前建议提供。显式 `?version=N` 读取该不可变历史版本，并在响应中返回对象当前状态，
便于区分历史内容。响应包含渲染后的完整 `SKILL.md`（frontmatter 使用 `name`、`description`）、
精确版本、截断标记和 `memory_refs`/`run_refs`。永久清除后返回 `404`。

### `GET /v1/artifacts/{id}` 与历史

`GET /v1/artifacts/{id}` 返回当前对象状态和当前版本；`current_version=0` 表示尚无可用版本。
`GET /v1/artifacts/{id}/history/{version}` 读取显式历史版本。版本包含标题、正文、结构化
`payload`、固定生成器/协议版本、内容哈希、可选原因码、截断标记、直接原子依据
`memory_refs` 和应用结果依据 `run_refs`。`stale/disabled` 对象仍可显式读取历史；永久清除后
对象和版本均为 `404`。

`GET /v1/artifacts` 按 `scope_key/kind/state/reason_code` 过滤当前对象，使用有界
`cursor/limit` 游标，不依赖正文关键词。`GET /v1/artifact-targets` 额外列出尚未形成对象的
生成目标，按 `scope_key/kind/status/reason_code` 过滤，并区分 `queued/running/waiting`、
`no_eligible_input`、`model_unavailable`、`generation_failed`、`invalid_output`、
`active/stale/disabled`；`artifact`、`job`、`evaluated_at` 和 `has_usable_version` 分别展示
对象、任务、最近一次无结果评估及是否已有不可变版本。`job` 额外返回尝试次数、连续失败、
`retry_reason`、目标年龄和 queued/running/waiting 的 stale 秒数，用于发现连续摄取造成的等待；
没有合格 procedure 的 Skill 目标只记录
目标评估，不创建空 artifact；已有 Skill 失去来源时仍立即 `stale`，随后 `disabled` 并保留历史。
摘要版本的 `payload` 分开记录 `input_selection`（合格/选入/实际包含/省略数量与原因）、
`citation_integrity`（引用是否存在且版本匹配）、`semantic_coverage`（无法确定时为
`unknown`，服务已知省略时为 `subset`）、`citations` 与 `omitted_inputs`；引用合法不等于语义完整。
`scope_summary` 是按作用域和派生类型确定目标的通用概览，不要求模型输出项目 topic；
`topic_summary` 保留为细分视图，父主题直接读取叶子原子。

## 6. 来源与任务

### `GET /v1/sources/{id}`

返回来源标识、可选来源信息、原始 `content`、`redacted` 状态与接收时间。来源不存在返回
`404`。清除后仍返回 ID、`redacted=true` 与 `redacted_at`，但不再返回原文。

### `DELETE /v1/sources/{id}`

同一事务清空来源原文、全部历史证据中的原文副本与定位，保留来源标识和 `redacted`，并把
关联的 `queued`/`running` 任务置为 `cancelled`。该来源终态任务中非空、可能残留原文的错误
摘要也会替换为固定脱敏说明，任务状态、尝试次数和 `memory_ids` 不变。已形成的记忆正文保留。

响应：

```json
{"id":"source-uuid","redacted":true,"cancelled_jobs":1}
```

重复清除保持 `redacted=true`；不再取消任务时 `cancelled_jobs=0`。清除会取消该来源的
`queued`、`running` 和 `waiting` 任务。该操作是逻辑清除，不承诺存储介质或远端模型侧的擦除。

### `GET /v1/ingest-jobs/{id}`

返回任务状态、尝试次数、脱敏错误摘要、业务质量结果、分阶段统计、候选级诊断、收敛诊断、
生成记忆 ID 与时间：

```json
{"id":"job-uuid","source_id":"source-uuid","status":"succeeded","attempts":1,"protocol_failures":0,"memory_ids":["memory-uuid"],"conflict_ids":["conflict-uuid"],"pending_memory_ids":["memory-uuid"],"warnings":["fact_conflict"],"quality_outcome":"complete","extraction_stats":{"raw_candidates":1,"parsed_candidates":1,"normalized_candidates":0,"normalizations":0,"repair_attempts":0,"repaired_candidates":0,"repair_succeeded":0,"repair_failed":0,"repair_timed_out":0,"repair_invalid_output":0,"excluded_candidates":0,"isolated_candidates":0,"accepted_candidates":1,"skipped_candidates":0,"memory_writes":1,"extraction_chunks":1,"extraction_chunk_failures":0,"dropped_candidates":0,"cross_chunk_duplicates":0,"context_only_candidates":0},"retention":{"retained":1,"not_retained":0,"by_reason":{"explicit_intent":1},"not_retained_by_reason":{}},"timings":{"queue_age_ms":1000,"extraction_ms":900,"normalization_ms":3,"repair_ms":0,"context_ms":20,"decision_ms":0,"commit_ms":15,"total_ms":938},"model_usage":{"calls":1,"input_tokens":120,"output_tokens":40,"total_tokens":160},"efficiency":{"memories":1,"model_calls":1,"total_tokens":160,"calls_per_memory":1,"tokens_per_memory":160},"queue_age_seconds":1,"stale_seconds":0,"diagnostics":[],"convergence":[],"created_at":"2026-10-20T02:00:01Z","updated_at":"2026-10-20T02:00:03Z","started_at":"2026-10-20T02:00:02Z","finished_at":"2026-10-20T02:00:03Z"}
```

`status` 为 `queued`、`running`、`waiting`、`succeeded`、`failed`、`cancelled`。`waiting`
表示任务已持久化但暂不执行：`wait_reason=transient` 会在 5 秒、30 秒、2 分钟后继续，之后
最长每 30 分钟重试；`configuration` 等待重启后重评；`protocol` 在局部修复后连续失败达到
`worker.max_attempts`（默认 3）后进入 `failed`。`stage` 返回 source/extraction/context/decision/
decision_repair/commit 等阶段，`reason_code` 返回稳定原因（如 `model_timeout`、
`process_budget_exceeded`、`decision_repair_failed`），不依赖展示用 `error` 文本。

`quality_outcome` 与调度状态分开：`empty` 表示合法空提取；`complete` 表示候选全部安全
处理（包括补证、重复和明确 skip）；`partial` 表示保留部分候选并隔离其余候选；`unusable`
表示非空候选全部因协议/证据错误不可用，任务会按协议重试或失败，不能被解释成正常空结果。
全量隐私排除属于正常完成并记录 `excluded_candidates`，不会通过重试重新提取。

`extraction_stats` 区分原始候选、解析/规范化、repair、拆分、隔离、排除、接受、写入、分块和
超限丢弃数量；`retention` 按固定原因记录保留与未保留候选，只用于观察噪声写入和有用信息
漏记，不会自动删除记忆；`timings` 记录一次尝试的排队年龄和各阶段耗时，`model_usage` 与
`efficiency` 记录调用数、token 和每个有效原子的成本；`queue_age_seconds`、`stale_seconds` 和
`retry_reason` 用于发现长期等待与重试原因。`diagnostics` 仅包含 candidate ID、field、固定
code、stage 和最终处置；`convergence` 仅包含
candidate ID、事实组 ID 和 `identity_ambiguous`、`missing_update_evidence`、`time_unknown`、
`cardinality_unknown`、`context_truncated` 固定原因，不含原文或完整模型响应。`succeeded`
表示处理完成，不保证全部断言被采纳。`memory_ids` 是本次创建或修订的
全部记忆；`conflict_ids` 与 `pending_memory_ids` 分别列出未决冲突和待裁决断言；`warnings`
为固定代码数组。失败原因不包含完整模型响应或原始输入。任务不存在返回 `404`。无结果时
数组字段为 `[]`。

## 7. 健康检查

| 路径 | 成功 | 失败 |
| --- | --- | --- |
| `GET /healthz` | `200 {"status":"ok"}` | 进程不存活则无响应 |
| `GET /readyz` | `200 {"status":"ready"}` | 数据库未就绪/不可用：`503 {"status":"unavailable"}` |

模型未配置或不可达不影响 `/readyz`；服务仍可提交、查询和管理已有记忆。

### `GET /v1/embedding/status`

只读返回当前 Embedding 配置指纹、无凭据端点和向量库存。`current_vectors` 只统计
profile、维数和当前文档内容 hash 全部匹配的向量；`stale_vectors` 与 `missing_vectors`
不参与召回，并驱动 `rebuild_required`。`config_fingerprint_match` 比较当前 profile/
文档契约与持久化登记值；`indexed_profiles` 暴露历史 profile 的残留向量数。
`reason_code` 为 `ready`、`disabled`、`no_documents`、`config_mismatch`、
`index_not_ready`、`backlog` 或 `failed_jobs`；存在文档但当前向量为零时必须是
`index_not_ready`，不能把空语义结果解释为不相关。响应不包含 API key、来源正文、查询、
向量值或原始模型响应。

### `POST /v1/embedding/probe`

显式发起一次有界 Embedding 调用，使用固定探针文本和与召回相同的编码器，返回
`attempted/success/code/dimensions/duration_ms/checked_at`。`code` 区分 `ok`、
`disabled`、`unauthorized`、`config_error`、`rate_limited`、`timeout`、
`invalid_response` 和 `unavailable`。探测结果不持久化，不影响 `/readyz`，也不参与普通召回。

模型、维数或文档构造方式变化时，启动阶段把当前 profile 与新重建原因写入索引元数据；
旧向量退出当前配置召回，后台任务按 profile + content hash 重新生成。失败任务保留
固定错误码和计数，供 status 读取。

## 8. 路由与端点边界

- `/v1/memories/recall` 是集合级动作，不是记忆 ID；路由实现必须先匹配该静态路径。
- `/v1/embedding/status` 与 `/v1/embedding/probe` 只提供语义配置/覆盖/降级诊断；没有修改
  配置、强制重建或绕过 Worker 的端点。
- 历史、证据是记忆的只读子资源；恢复是新增版本的动作，不是内容写入端点。
- `/v1/artifacts`、`/v1/artifacts/*`、`/v1/artifact-targets` 与 `/v1/skills/*/content` 只提供
  派生对象和生成目标的只读视图；聚合、经验案例、Skill 生成、失效和重算由治理 Worker 自动执行，
  调用方不需要也不存在逐项触发端点。
- `/v1/experiences` 保存应用报告并触发经验案例；`/v1/experiences/{id}/retract` 是显式撤销，
  不是原子事实写入端点。
- `PATCH .../state` 仅状态；`DELETE /v1/memories/{id}` 是逻辑删除，`POST .../purge` 是单个记忆
  永久清除，`POST /v1/forget` 是显式范围的原子、来源和结果联合遗忘；内容新增或更正只走
  `POST /v1/ingestions`。
- `/v1/memory-conflicts` 是只读列表/详情与显式裁决能力；普通召回按 accepted 优先、pending 随后展示。
- 不存在 `POST /v1/sources`、模型提取提交端点、直接记忆写入端点或分页端点。
- 所有本地应用按相同规则访问全部记忆；`app_id` 不产生隔离或授权。

## 9. 时间、断言与冲突

### 9.1 记忆字段

当前快照和管理列表新增：

| 字段 | 含义 |
| --- | --- |
| `assertion_status` | `accepted` / `pending` / `rejected`；普通召回先 accepted 再 pending，永不返回 rejected |
| `observed_at` / `observed_at_basis` | 来源作出陈述或观测的时间及其依据；缺失时回退到接收时间并标记 `recorded`，不作为新事实更晚的证据 |
| `event_time` / `event_time_basis` / `event_time_precision` | 事件发生或计划时间；计划事件不被未来发生时间隐藏。精度可为 day/hour/minute/second 等 |
| `valid_from` / `valid_to` | 左闭右开事实有效区间；`null` 表示未知，不表示无限 |
| `valid_from_basis` / `valid_to_basis` | `explicit` / `inferred` / `unknown`；空边界必须为 unknown |
| `temporal` | 每个时间锚点的原文、精度和是否有明确时区；无时区的日期不会被宣称为明确 UTC 零点 |
| `procedure` | 可选结构化流程：目标、有序步骤、前置条件、完成检查和失败分支；随修订版本、hash、失效和清除闭包保存 |
| `qualifiers` | 通用限定：实体、值/单位、条件、极性、确定性和归属；正文仍是权威表达 |
| `fact_group_id` | 同主体、属性、场景的事实组；可为空 |

`temporal_status` 在召回 item 上为 `within_bounds` 或 `unknown`，只描述记录约束，不宣称现实真值。
`filters.as_of` 只表示事实有效时点，不是“系统当时知道什么”的知识截面；时间未知且仍返回的
结果保留 `unknown` 并附 `temporal_uncertain`。
`GET /v1/memories/{id}` 还返回 `relations` 和 `evolution`；`relations` 只记录 `supersedes`
关系并含 `from_version_changed` / `to_version_changed`，`evolution` 返回
`change/correction/reschedule/cancel/supersede` 确定性演化审计及双方决策版本。未决矛盾通过
冲突成员表达，不重复写入关系。

### 9.2 召回与冲突提示

- 缺失 `as_of` 时请求开始固定一次 now；`as_of` 选择区间匹配的 accepted 和 pending 记忆。
- 归档不等于过期：过期记录可以保持 active，仍可按历史时点召回。
- 每个原子条目返回 `temporal_status` 与 `conflict_refs`；响应顶层返回相关 `conflicts` 摘要。
- 与查询时点相关的未决冲突返回 `warnings: ["fact_conflict"]`；摘要超过 20 项时追加
  `conflict_results_truncated`。
- 冲突发现是独立路径：即使原子 `memories` 分区为空，只要 accepted 或 pending 成员命中查询且
  存在相关 open 冲突，仍返回 `conflicts` 与警告。
- 已知只涉及其他历史时期的冲突不告警；时间未知的潜在冲突保守告警。
- pending 正文保留 `assertion_status=pending`，不得作为已确认事实；即使没有可关联的 open
  冲突，返回 pending 时也提示 `fact_conflict`。同状态内保持原有 BM25/覆盖率或 RRF 排序，
  accepted 优先于 pending，Top-K 和上下文预算限制不变。

### 9.3 冲突管理

`GET /v1/memory-conflicts?status=open&limit=50`

返回 `{"items":[...]}`。`status` 缺失默认为 `open`，`limit` 缺失/非法使用 50，固定按
`updated_at DESC, id ASC` 排序。

`GET /v1/memory-conflicts/{id}` 返回冲突、检测时成员版本（`current_version` 为当前版本）和不可变审计事件。不存在返回 404。

`POST /v1/memory-conflicts/{id}/resolve` 请求：

```json
{
  "revision": 1,
  "action": "choose",
  "member_ids": ["memory-uuid"],
  "member_versions": [
    {"memory_id": "memory-uuid", "version": 2},
    {"memory_id": "memory-uuid-2", "version": 1}
  ],
  "separate_group": {"subject": "用户", "predicate": "回答语言", "scope": "工作", "cardinality": "single"},
  "timeline": [
    {"memory_id": "memory-uuid", "valid_from": "2026-01-01T00:00:00Z", "valid_to": "2026-06-01T00:00:00Z"},
    {"content": "用户常住北京。", "valid_from": "2026-06-01T00:00:00Z", "valid_to": "2027-01-01T00:00:00Z"}
  ],
  "reason": "用户确认"
}
```

| action | 行为 |
| --- | --- |
| `choose` | 选择一个成员为 accepted，其余互斥成员 rejected；时间段可证明不相交时拒绝粗暴否定并要求 timeline |
| `timeline` | 提交明确且不重叠的分段；未覆盖成员 rejected，新分段生成可追溯记录 |
| `separate_scope` | 用 `separate_group` 重新归组并检查新组冲突 |
| `reject_all` | 全部成员 rejected |

`revision` 与完整 `member_versions`（成员当前版本）是必要输入。冲突/成员已变化、或与其他 accepted 事实
冲突返回 `409`；不得隐式重开已 resolved 冲突。相同裁决重复提交返回现有结果，不同裁决返回
`409`。

### 9.4 有限自动复查

入库前先执行确定性自动收敛：显式 `scope_key`、主体、谓词、场景、必要条件和单值属性必须
一致；先按规范化身份读取同组时间线，再补充关键词/语义上下文。当前来源的可定位 quote 必须
明确表达更正、改期、取消或替换，不使用模型 reason_code、单纯接收顺序或候选 ID 顺序作为
证据。状态变化保留旧值历史段并在推断的转移时点关闭；明确更正拒绝旧断言但不伪造其有效
区间；改期/取消拒绝旧计划并把新阶段作为当前事件；独立观测保留各自观测时间，数值不同不自动
构成冲突。完整已知且重叠的互斥值、条件性更新、未知单值属性不自动裁决。满足条件时创建
active+accepted 后继，记录 supersedes、`memory_evolution_events`、完整冲突成员和必要的
resolved 事件；检测版本不被后续版本覆盖，current_version 始终反映当前快照。未自动收敛时
任务 `convergence` 返回固定原因。

冲突创建、成员扩展或任一相关成员产生新版本（包括仅补证和生命周期变化）时，同一事务会
更新 `(review_conflict, conflict:<id>)` 治理目标。Worker 只使用当前冲突成员及有限原子快照，
不调用模型、不搜索其他记忆，也不按接收顺序、出现次数或应用来源裁决。首版仅在成员集合本身
足以确定结果时自动解决：

- 只有一个 active+accepted 成员且没有待确认成员，其余成员已归档/删除/否定且时间段重叠；
- 全部成员 active+accepted 且正文收敛为同一断言；
- 全部成员 active+accepted、起止均已知且两两不重叠，按既有区间提交 timeline。

复杂重叠、未知边界、多成员重新归组、来源不足或任何规则无法证明的情况保持 `open`。未变化
的成员集合不会重复刷新任务；没有新证据就不产生新的模型调用。未决冲突成员继续从摘要、
经验案例和 Skill 输入中排除，普通原子召回仍带冲突说明。

## 10. 治理配置

`governance` 配置项为 `enabled`（缺省 true）、`debounce`、`reconcile_interval`、
`min_call_interval`、`max_inputs` 和 `max_wait`。`max_inputs` 同时限制聚合、经验案例和 Skill
单次选择的原子/运行依据数量；`max_wait` 默认 30 分钟，限制连续摄取推迟一个已到期治理目标的
最长时间，超过后执行一次公平领取。模型调用仍有单次超时，进程内软限速（每小时约 30 次）是
不写入配置的固定运行边界。`enabled=false` 只停止后台整理，HTTP、原子写入和查询保持可用，
已登记任务保留到重新启用。

## 11. 首版验证与明确限制

- 固定标注质量、离线评估和一万条容量实测记录在 `docs/quality.md`；它们不构成真实模型语义
  质量、十万条或性能 SLA 的通过声明。
- 本 API 不提供认证、应用隔离、分页、自动备份/恢复、外部副本撤销或执行认证。`app_id`、
  `scope_key` 和对话 role 都不是身份或权限凭据。
- `filters.as_of` 只支持原子层历史召回；含派生层的请求返回空派生分区和
  `derived_as_of_unsupported`，不会伪造历史摘要、经验或 Skill。
- 经验结果是应用报告，Skill 是可修正的指导文本；服务不生成或执行脚本，不授予工具权限。
- 自动冲突复查只处理成员集合本身足以证明的确定性规则；复杂冲突保持 `open`，不以接收顺序、
  出现次数或模型自报置信度裁决。
- 永久遗忘只覆盖本服务持久化的数据；已返回客户端、发送模型或手工复制到服务外的数据不在
  召回或擦除承诺内。
