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
- 删除顺序固定为枚举依赖、清除派生对象全部版本/索引/依赖、清除原子/结果/来源叶子和引用
  任务；不先依赖外键级联再寻找受影响对象。
- 整个请求在一个事务内完成。任一 ID 不存在、外键冲突或存储失败都会回滚全部目标；不会
  部分清除后返回成功。
- 记忆历史、证据、派生历史、Skill 渲染和 FTS 在提交后均不可读；共享的其他来源不受影响。
- 响应返回实际移除的排序后 ID 数组：

```json
{"memory_ids":["memory-uuid"],"source_ids":["source-uuid"],"experience_ids":["experience-uuid"]}
```

`DELETE /v1/sources/{id}` 仍是逻辑清除原文；`POST /v1/forget` 才是永久删除来源、关联记忆和
结果记录。首版不提供自动备份、外部副本管理或取证级物理擦除承诺。

## 4. 版本、恢复与证据

### `GET /v1/memories/{id}/history`

返回完整版本快照，按 `version DESC` 排序。可选 `limit`：缺失、非正整数或无法解析时使用
50；没有分页。响应：

```json
{"items":[{"memory_id":"memory-uuid","version":3,"operation":"update","reason":"用户更正回答语言","snapshot":{"id":"memory-uuid","kind":"preference","title":"回答语言","content":"用户希望默认使用中文回答。","state":"active","version":3,"observed_at":"2026-10-20T02:00:00Z","created_at":"2026-10-20T02:00:01Z","updated_at":"2026-10-20T03:00:00Z","created_by_app":"desktop-assistant","origin_source_id":"source-uuid"},"evidence":[{"id":"evidence-uuid","source_id":"source-uuid","message_id":"msg-1","quote":"以后请默认使用中文回答。","location":{"field":"messages.0.content","start":0,"end":13},"model_name":"example-model","redacted":false,"created_at":"2026-10-20T02:00:01Z"}],"created_at":"2026-10-20T03:00:00Z"}]}
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
| `query` | 缺失或空白返回 `200` 与空分区；按普通关键词处理，不暴露 FTS 语法；语义路只做 NFC/首尾空白清理 |
| `scope_key` | 可选平面作用域；提供时原子与派生结果只接受该作用域或 `user` 级信息；缺失表示不做作用域过滤；未知作用域不会提升为全局结论 |
| `layers` | 缺失选择全部层；显式列表只检索对应分区；值只能是 `atomic/summary/experience/skill`，其他值返回 `400` |
| `mode` | 缺失或空为 `keyword`；支持 `keyword`、`hybrid`，其他值返回 `400`；只影响原子层，派生层固定使用派生 FTS |
| `match` | 缺失、空值或未知值按 `any`；支持 `all`、`any`；keyword 模式的 `any` 先按受控停用词表选择有效词项再按覆盖率重排，`all` 保持严格 `AND` |
| `filters.kinds/states` | 只过滤 `atomic` 分区；不会隐式排除摘要、经验或 Skill；`states` 缺失默认 `active`，`deleted` 永不返回 |
| `filters.as_of` | 显式历史时点只支持原子层。所选 layers 含派生层时这些分区返回空数组并附 `derived_as_of_unsupported`；只选 `atomic` 不产生该提示 |
| `filters.created_by_app/updated_after/before` | 只过滤原子层；时间边界无法解析时忽略 |
| `top_k` | 缺失、非正整数时使用 20；大于 100 返回 `400`；上限按所选层分别执行 |
| `max_context_tokens` | 可选保守预算，按 rune 数估算而非模型精确 tokenizer；超过预算时截断后续条目并返回 `context_budget_truncated` |
| `explain` | 默认 `false`；只影响原子条目的 `explanation` |

原子层沿用加权 BM25（标题 4、正文 1）、覆盖率重排和 RRF 混合语义；`score` 只在当前查询
内比较。派生层按 `(kind,target_key)` 的当前版本检索，只有 `state=active` 的当前版本进入
默认召回；`stale/disabled` 历史可通过 `GET /v1/artifacts/{id}` 或历史接口读取，但不会出现
在默认召回。摘要、经验与 Skill 使用同一分词规则建立独立 FTS 索引，不与原子分数相加。

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

`experiences` 返回当前 active 的经验案例，`skills` 返回当前 active 的指导型 Skill；未生成、
失效或停用时为空数组。每个条目保留对象 ID、版本、状态、结构化 `payload`、直接原子依据
`memory_refs` 和应用结果依据 `run_refs`。经验结果一律是应用报告，Skill 只是可修正的指导
文本，响应不包含真实性或执行认证。派生索引故障只降级派生分区并返回
`derived_unavailable`，不阻止原子结果；召回不等待后台聚合、经验案例或 Skill 生成。
原子分区沿用既有 `snippet`、`matched_terms`、`evidence_refs` 和冲突说明语义。语义路关闭
或失败时回退关键词并返回 `semantic_unavailable`/`semantic_timeout`。

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

返回任务状态、尝试次数、脱敏错误摘要、生成记忆 ID 与时间：

```json
{"id":"job-uuid","source_id":"source-uuid","status":"succeeded","attempts":1,"protocol_failures":0,"memory_ids":["memory-uuid"],"conflict_ids":["conflict-uuid"],"pending_memory_ids":["memory-uuid"],"warnings":["fact_conflict"],"created_at":"2026-10-20T02:00:01Z","updated_at":"2026-10-20T02:00:03Z","started_at":"2026-10-20T02:00:02Z","finished_at":"2026-10-20T02:00:03Z"}
```

`status` 为 `queued`、`running`、`waiting`、`succeeded`、`failed`、`cancelled`。`waiting`
表示任务已持久化但暂不执行：`wait_reason=transient` 会在 5 秒、30 秒、2 分钟后继续，之后
最长每 30 分钟重试；`configuration` 等待重启后重评；`protocol` 连续三次后进入 `failed`。
`succeeded` 表示处理完成，不保证全部断言被采纳。`memory_ids` 是本次创建或修订的全部记忆；
`conflict_ids` 与 `pending_memory_ids` 分别列出未决冲突和待裁决断言；`warnings` 为固定代码
数组。失败原因不包含完整模型响应或原始输入。任务不存在返回 `404`。无结果时数组字段为 `[]`。

## 7. 健康检查

| 路径 | 成功 | 失败 |
| --- | --- | --- |
| `GET /healthz` | `200 {"status":"ok"}` | 进程不存活则无响应 |
| `GET /readyz` | `200 {"status":"ready"}` | 数据库未就绪/不可用：`503 {"status":"unavailable"}` |

模型未配置或不可达不影响 `/readyz`；服务仍可提交、查询和管理已有记忆。

## 8. 路由与端点边界

- `/v1/memories/recall` 是集合级动作，不是记忆 ID；路由实现必须先匹配该静态路径。
- 历史、证据是记忆的只读子资源；恢复是新增版本的动作，不是内容写入端点。
- `/v1/artifacts/*` 与 `/v1/skills/*/content` 只提供派生对象当前/历史只读视图；聚合、经验
  案例、Skill 生成、失效和重算由治理 Worker 自动执行，调用方不需要也不存在逐项触发端点。
- `/v1/experiences` 保存应用报告并触发经验案例；`/v1/experiences/{id}/retract` 是显式撤销，
  不是原子事实写入端点。
- `PATCH .../state` 仅状态；`DELETE /v1/memories/{id}` 是逻辑删除，`POST .../purge` 是单个记忆
  永久清除，`POST /v1/forget` 是显式范围的原子、来源和结果联合遗忘；内容新增或更正只走
  `POST /v1/ingestions`。
- `/v1/memory-conflicts` 是只读列表/详情与显式裁决能力；普通召回不返回 pending 正文。
- 不存在 `POST /v1/sources`、模型提取提交端点、直接记忆写入端点或分页端点。
- 所有本地应用按相同规则访问全部记忆；`app_id` 不产生隔离或授权。

## 9. 时间、断言与冲突

### 9.1 记忆字段

当前快照和管理列表新增：

| 字段 | 含义 |
| --- | --- |
| `assertion_status` | `accepted` / `pending` / `rejected`；普通召回只返回 accepted |
| `valid_from` / `valid_to` | 左闭右开事实有效区间；`null` 表示未知，不表示无限 |
| `valid_from_basis` / `valid_to_basis` | `explicit` / `inferred` / `unknown`；空边界必须为 unknown |
| `fact_group_id` | 同主体、属性、场景的事实组；可为空 |

`observed_at` 仍表示发生或观测时间。缺失时可用来源接收时间作为排序辅助，但不会自动填入
有效区间。`temporal_status` 在召回 item 上为 `within_bounds` 或 `unknown`，只描述记录约束。
`GET /v1/memories/{id}` 还返回 `relations`；当前只记录 `supersedes` 关系，含决策时两端版本和
`from_version_changed` / `to_version_changed`。未决矛盾通过冲突成员表达，不重复写入关系。

### 9.2 召回与冲突提示

- 缺失 `as_of` 时请求开始固定一次 now；`as_of` 只选择区间匹配的 accepted 记忆。
- 归档不等于过期：过期记录可以保持 active，仍可按历史时点召回。
- 每个原子条目返回 `temporal_status` 与 `conflict_refs`；响应顶层返回相关 `conflicts` 摘要。
- 与查询时点相关的未决冲突返回 `warnings: ["fact_conflict"]`；摘要超过 20 项时追加
  `conflict_results_truncated`。
- 冲突发现是独立路径：即使原子 `memories` 分区为空，只要 accepted 或 pending 成员命中查询且
  存在相关 open 冲突，仍返回 `conflicts` 与警告。
- 已知只涉及其他历史时期的冲突不告警；时间未知的潜在冲突保守告警。

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

`governance` 只有五个首版配置项：`enabled`（缺省 true）、`debounce`、
`reconcile_interval`、`min_call_interval`、`max_inputs`。`max_inputs` 同时限制聚合、经验案例
和 Skill 单次选择的原子/运行依据数量；模型调用仍有单次超时，进程内软限速（每小时约 30 次）
是不写入配置的固定运行边界。`enabled=false` 只停止后台整理，HTTP、原子写入和查询保持可用，
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
