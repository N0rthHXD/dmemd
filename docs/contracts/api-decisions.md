# API 决策记录

本文件补充 `docs/系统 AI 记忆服务架构设计.md` v2 的接口语义，并与
`api/openapi.yaml` 共同构成 HTTP 契约。它是文档契约，不生成业务校验层。

## 1. 通用约定

| 项目 | 决策 |
| --- | --- |
| 基础路径 | `/v1`；健康检查位于根路径 `/healthz`、`/readyz` |
| 默认监听 | `127.0.0.1:18432`，端口可配置；首版只允许回环地址 |
| 传输 | HTTP/1.1，UTF-8 JSON；有 JSON 请求体时只需 `Content-Type: application/json` |
| 认证 | 无。`Authorization: Bearer ...` 可省略、可任意填写，服务不读取、不验证、不据此确定应用身份 |
| 其他请求头 | 不要求签名、应用标识、幂等键或通用预期版本头；冲突裁决在 JSON 中携带版本 |
| 校验边界 | 无身份/准入校验或通用业务校验中间件；查询枚举、裁决、反馈和内部一致性按各能力检查 |
| ID | 服务生成的 UUID 字符串；来源、任务、记忆、证据分别有独立 ID |
| 时间 | UTC RFC 3339，例如 `2026-10-20T02:00:00Z`；不使用本地时区持久化 |
| 字段命名 | JSON 一律 `snake_case` |
| 响应头 | 不返回 `Location`、`ETag` 或版本控制头；有响应体时 `Content-Type: application/json` |
| 列表 | 普通列表只支持 limit，无总量/下一页；artifacts 与 artifact-targets 单独支持 cursor/next_cursor |
| 错误体 | `{"error":"...","code":"..."}`；`code` 只在冲突裁决等需要固定原因时出现 |
| 状态码 | 400 请求/裁决非法；404 资源不存在；409 冲突或成员版本已变化；500 内部失败；503 服务暂不可用 |

未知 JSON 字段默认忽略。缺少可选辅助字段时使用本文或 `model-provider.md` 的默认值。
当前不提供 `as_of/event_at` 时间过滤，旧字段不能作为查询条件；冲突裁决版本变化返回 `409`。
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
- 首次提取只保存 L0 Observation 和代码生成的 Evidence；observed 观察同事务登记
  govern_observation，Memory 由后续治理物化，聚合再逐层异步进行。
- 同一请求重复提交按普通提交处理：每次生成新的来源和任务，不做请求级幂等；内容层面的
  重复由后台整理时尽量合并。
- 来源和初始 `queued` 任务在同一事务提交成功后返回 `202`：

```json
{"source_id":"source-uuid","job_id":"job-uuid","status":"queued"}
```

- 返回不等待模型调用，也不提供直接写入记忆或单独创建来源的接口。

## 3. 记忆管理

### `GET /v1/memories`

`/v1/memories` 只返回治理后的当前规范视图，不返回首次提取结果。查询参数：

| 参数 | 语义 | 缺失/无效处理 |
| --- | --- | --- |
| `kinds` | 逗号分隔的 `fact,preference,event,procedure` | 缺失或空表示不按类型过滤；非法项忽略，全部非法时返回空列表 |
| `states` | 逗号分隔的 `active,archived,deleted` | 缺失或空默认为 `active,archived`（不含删除墓碑） |
| `assertion_status` | 逗号分隔的 `accepted,pending,rejected` | 缺失或空表示全部断言状态；全部不可识别时返回空列表 |
| `updated_after` | RFC 3339，`updated_at` 下限 | 无法解析时返回 400 |
| `updated_before` | RFC 3339，`updated_at` 上限 | 无法解析时返回 400 |
| `limit` | 本次返回条数 | 缺失、非正整数或无法解析时使用 50 |

响应：

```json
{"items":[{"id":"memory-uuid","kind":"preference","title":"回答语言","content":"用户希望默认使用中文回答。","state":"active","assertion_status":"accepted","version":3,"scope_key":"project:dmemd","origin_source_id":"source-uuid","created_at":"2026-10-20T02:00:01Z","updated_at":"2026-10-20T03:00:00Z"}]}
```

时间角色、事实组、procedure、qualifiers、topics 和 retention 已从当前记忆模型移除；它们不能
再通过本接口返回。原始提取内容通过 `/v1/observations` 查询。

### `GET /v1/observations` 与观察详情

| 路径 | 语义 |
| --- | --- |
| `GET /v1/observations` | 只读 L0 列表；支持 `source_id`、`scope_key`、`kind`、`status`、`limit` |
| `GET /v1/observations/{id}` | 单条 L0 原始观察 |
| `GET /v1/observations/{id}/evidence` | 代码从 source span 生成的 evidence |
| `GET /v1/observations/{id}/relations` | typed observation/memory 关系 |

观察内容不可通过 HTTP 修改；`content_mode=extractive` 表示正文直接来自 source span，
`normalized` 表示正文由模型表达。绑定失败但仍有可保存正文时保留 pending，可能没有 Evidence，
不自动登记治理；正常 observed 观察的 Evidence 必须由代码生成。

### `GET /v1/memories/{id}`

返回当前规范视图及其引用：`observation_ids`、typed `relations`、`conflict_ids`、
`derived_dependents`。`version` 是当前版本；`assertion_status=pending` 表示尚未采纳，
存在 open conflict 时同时返回冲突 ID。删除后仍可按 ID 读取墓碑，但任何全文检索都不返回
删除记录。

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
- 来源关联还包括所有版本的 Observation refs；删除来源时同时清理其 L0 观察。
  不再计算事实组/时间区间重复闭包，不按相似正文猜测删除未显式关联的 Memory。
- 删除顺序固定为枚举依赖、清除派生对象全部版本/索引/依赖、清除原子/结果/来源叶子和引用
  任务；不先依赖外键级联再寻找受影响对象。
- 整个请求在一个事务内完成。任一 ID 不存在、外键冲突或存储失败都会回滚全部目标；不会
  部分清除后返回成功。
- 已物理移除的记忆历史、专属证据及派生版本提交后不可读；仅递归失效的派生历史仍可审计，
  不进入默认召回。不要把 stale/disabled 等同于正文已擦除；共享的其他来源不受影响。
- 依赖目标的整个 open/resolved 冲突及审计事件删除，review 任务取消；派生对象即便仅历史
  版本直接依赖被清除 Memory/结果也会被枚举物理清理，目标任务递增 revision 后从剩余输入重建。
  Observation 的间接高层依赖走递归失效路径，保留历史，两类处理不能混为一谈。
- 响应返回实际移除的排序后 ID 数组：

```json
{"memory_ids":["memory-uuid"],"source_ids":["source-uuid"],"experience_ids":["experience-uuid"]}
```

响应只包含实际删除的三类排序去重 ID；没有删除项时数组为 `[]`。

`DELETE /v1/sources/{id}` 是逻辑清除原文并撤回观察/传播失效；`POST /v1/forget` 才永久删除来源、关联记忆和
结果记录。首版不提供自动备份、外部副本管理或取证级物理擦除承诺。

## 4. 版本、恢复与证据

### `GET /v1/memories/{id}/history`

返回完整版本快照，按 `version DESC` 排序。可选 `limit`：缺失、非正整数或无法解析时使用
50；没有分页。响应：

```json
{"items":[{"memory_id":"memory-uuid","version":3,"operation":"update","reason":"用户更正回答语言","snapshot":{"id":"memory-uuid","kind":"preference","title":"回答语言","content":"用户希望默认使用中文回答。","state":"active","assertion_status":"accepted","version":3,"created_at":"2026-10-20T02:00:01Z","updated_at":"2026-10-20T03:00:00Z","origin_source_id":"source-uuid","scope_key":"project:dmemd"},"evidence":[{"id":"evidence-uuid","source_id":"source-uuid","segment_id":"seg-0001","message_id":"msg-1","quote":"以后请默认使用中文回答。","location":{"field":"messages.0.content","start":0,"end":12},"redacted":false,"created_at":"2026-10-20T02:00:01Z"}],"created_at":"2026-10-20T03:00:00Z"}]}
```

记忆不存在返回 `404`。历史只读；恢复旧版本不会覆盖或删除旧版本。

### `GET /v1/memories/{id}/history/{version}`

返回指定版本的完整快照与证据集合。`version` 非整数、非正数或不存在时返回 `404`。
记忆不存在同样返回 `404`。

### `POST /v1/memories/{id}/history/{version}/restore`

- `version` 位于路径中并指向历史版本；缺失、非正数、非整数或不存在时返回 `404`。
- 无请求体。
- 恢复复制该版本内容为新的当前版本，状态恢复为 `active`，版本号高于恢复前的当前版本；
  历史版本本身保持不变。断言状态不被提升：历史待确认/已否定仍保持；当前不执行旧版事实组
  时间线检查，也不因恢复自动裁决冲突。
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

唯一召回入口，返回六个独立分区：`observations`、`memories`、`l1`、`l2`、`l3`、`l4`。

```json
{
  "query": "修改 dmemd 后应该如何验证",
  "scope_key": "project:dmemd",
  "query_mode": "detail",
  "layers": ["observations", "memories", "l1"],
  "derived_states": ["active"],
  "max_context_tokens": 3000,
  "filters": {"kinds": ["procedure"], "states": ["active"]},
  "top_k": 20,
  "explain": true
}
```

| 字段 | 默认/边界 |
| --- | --- |
| `query` | 关键词和自然语言问题使用统一分词、去重和受控停用词，保留否定词；空白或无有效词返回空分区 |
| `scope_key` | 可选平面作用域；提供时结果只接受该作用域或 `user` 级信息 |
| `query_mode` | overview 选 L1/L2/L3；detail 选 L0/Memory；history/conflict 选 L0/Memory 并加载关系；guidance 选 L4；缺失选全部层 |
| `layers` | 显式层列表优先于 query_mode；只能是 observations/memories/l1/l2/l3/l4；显式空数组不选任何层 |
| `mode` | `keyword` 或 `hybrid`；只改变当前 Memory 的检索策略，L0/派生层固定关键词 |
| `filters.kinds` | 过滤 Observation 与当前 Memory 的类型，不过滤派生层 |
| `filters.states` | 只过滤当前 Memory 生命周期；不表示 Observation status |
| `filters.updated_after/before` | 当前 Memory 更新时间边界；当前召回解析器无法解析时忽略，记忆列表接口则严格返回 `400` |
| `derived_states` | 默认 `active`；显式加入 `stale` 才返回已失效派生内容 |
| `top_k` | 缺失、非正整数时使用 20；大于 100 返回 `400`；按每个选中层分别执行 |
| `max_context_tokens` | 可选保守预算；超过时截断后续条目并返回 `context_budget_truncated` |
| `explain` | 默认 `false`；返回条目解释和有界诊断计数 |

派生层按 `(kind,target_key)` 的当前版本检索，只有 `state=active` 的当前版本进入默认召回；
stale/disabled 历史可通过 `GET /v1/artifacts/{id}` 读取，但不会作为当前结论返回。高层
stale 时可通过下层分区继续查询，HTTP 请求不等待聚合模型。`warnings` 区分语义降级、派生
不可用和 stale 回退等固定代码。`explain=true` 时 `diagnostics` 返回关键词/语义/观察/记忆/
派生候选数、过滤排除数、预算截断数等有界计数。

响应按知识层分区，未请求、未命中或暂不可用的分区返回空数组：

```json
{
  "query_mode": "detail",
  "observations": [
    {
      "observation": {"id":"observation-uuid","source_id":"source-uuid","kind":"procedure","content":"修改后执行 go test。","content_mode":"extractive","status":"observed","created_at":"2026-10-20T02:00:00Z","updated_at":"2026-10-20T02:00:00Z"},
      "score": 0.7,
      "snippet": "修改后执行 go test。",
      "matched_terms": ["测试"],
      "evidence_refs": [{"id":"evidence-uuid","source_id":"source-uuid"}]
    }
  ],
  "memories": [
    {
      "memory": {"id":"memory-uuid","kind":"procedure","title":"验证步骤","content":"修改后执行 go test。","state":"active","assertion_status":"accepted","version":2,"created_at":"2026-10-20T02:00:01Z","updated_at":"2026-10-20T03:00:00Z","scope_key":"project:dmemd"},
      "score": 0.03252247,
      "snippet": "修改后执行 go test。",
      "matched_terms": ["验证"],
      "evidence_refs": [{"id":"evidence-uuid","source_id":"source-uuid"}],
      "explanation": {"ranking":"rrf","keyword":{"rank":1,"bm25":-0.0000042},"semantic":{"rank":2,"cosine":0.93}},
      "conflict_refs": []
    }
  ],
  "l1": [
    {
      "artifact": {"id":"artifact-uuid","layer":"l1","kind":"local_summary","target_key":"l1:...","current_version":1,"state":"active","updated_at":"2026-10-20T03:10:00Z"},
      "revision": {"artifact_id":"artifact-uuid","version":1,"title":"验证流程","content":"修改后应先运行单元测试，再执行构建检查。","content_hash":"...","generator":"aggregation:layer","protocol_version":"aggregate-v1","truncated":false,"created_at":"2026-10-20T03:10:00Z"},
      "score": 0.5,
      "snippet": "修改后应先运行单元测试",
      "matched_terms": ["测试"],
      "dependencies": [{"type":"observation","id":"observation-uuid","version":0,"required":true}]
    }
  ],
  "l2": [],
  "l3": [],
  "l4": [],
  "relations": [],
  "conflicts": [],
  "warnings": []
}
```

`observations`、`memories`、`l1`、`l2`、`l3`、`l4` 是六个独立分区。`query_mode` 选择默认
层级，显式 `layers` 优先；`top_k` 按每个选中层分别执行。派生条目携带代码保存的
`dependencies`，不是模型返回的引用。stale 派生内容不会作为当前结论返回；显式请求
`derived_states=["stale"]` 时才作为历史结果出现。派生索引故障只降级派生分区并返回
`derived_unavailable`，不阻止原始观察和当前记忆查询；召回不等待后台治理或聚合。

### `POST /v1/experiences`

保存一次完成结果反馈：原文作为来源保存，结构化结果写入 `experience_runs` 并立即登记经验
案例任务。`outcome` 为 `success/failure/unknown/partial_success`，缺失按 `unknown`；它是应用自报结果，服务
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
精确版本和截断标记。精确来源通过 artifacts 当前/历史详情的 `dependencies` 下钻，
Skill 内容响应仍保留 `memory_refs/run_refs` 数组字段，但当前渲染服务不填充，通常为空；不能
用空数组推断没有来源，应读取 typed dependencies。已永久清除的对象返回 `404`。

### `GET /v1/artifacts/{id}` 与历史

`GET /v1/artifacts/{id}` 返回当前对象状态和当前版本；`current_version=0` 表示尚无可用版本。
`GET /v1/artifacts/{id}/history/{version}` 读取显式历史版本。版本包含标题、正文、结构化
`payload`、固定生成器/协议版本、内容哈希和截断标记。详情单独返回 `dependencies`，每项含
`type/id/version/required`，支持 observation/memory/derived/experience。原因码在对象元数据上。
`stale/disabled` 对象仍可显式读取历史；永久清除后
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
派生层以 layer 和依赖类型确定输入：L1 读取本 scope/bucket 的 Observation，L2 读 active L1，
L3 读 active L2，L4 读稳定 Memory、相关派生与应用报告。模型只生成摘要正文，输入选择/
truncated/版本/依赖由代码生成；不再接收模型 citations/omitted_inputs。
依赖合法和输入数量不证明语义覆盖完整，高层也不能绕过下一层直接读取全部原子。

## 6. 来源与任务

### `GET /v1/sources/{id}`

返回来源标识、可选来源信息、原始 `content`、代码重建的 `segments`、redacted 和接收时间。
来源不存在返回 `404`。清除后保留 ID/redacted/redacted_at，不返回原文且 segments 为空。

### `DELETE /v1/sources/{id}`

同一事务清空来源原文、全部历史证据中的原文副本与定位，保留来源标识和 `redacted`，并把
关联的 `queued`/`running`/`waiting` 提取任务置为 `cancelled`。该来源终态任务中非空、可能残留
原文的错误摘要替换为固定说明，已完成任务保留其 `observation_ids`。当前 runtime 同事务撤回
关联 Observation、取消治理任务并递归失效派生；Memory 没有其他合法观察支持时逻辑删除，
仍有其他支持时保留正文。历史仍可审计，不等于永久遗忘。

响应：

```json
{"id":"source-uuid","redacted":true,"cancelled_jobs":1,"retracted_observations":1,"invalidated_derived_ids":["artifact-uuid"]}
```

重复清除保持 `redacted=true`；不再取消任务时 `cancelled_jobs=0`。清除会取消该来源的
`queued`、`running` 和 `waiting` 任务。该操作是逻辑清除，不承诺存储介质或远端模型侧的擦除。

### `GET /v1/jobs/{id}` 与任务阶段

提取任务只报告来源、已保存的 `observation_ids`、提取调度状态、模型调用和错误摘要。任务
`stage` 使用固定阶段：`received`、`extracting`、`observations_saved`、`governance_pending`、
`memory_materialized`、`aggregation_pending`、`completed`、`failed`。提取任务的阶段最多到
`observations_saved`（有观察）或 `completed`（合法空提取）；治理和聚合状态通过
`/v1/governance-jobs` 按任务目标查询，不在提取任务中伪装成同步完成。

```json
{"id":"job-uuid","source_id":"source-uuid","status":"succeeded","stage":"observations_saved","attempts":1,"observation_ids":["observation-uuid"],"warnings":[],"quality_outcome":"complete","extraction_stats":{"items":1,"observations":1,"normalized_observations":0,"pending_observations":0,"invalid_items":0,"duplicate_observations":0,"chunks":1,"chunk_failures":0},"timings":{"queue_age_ms":1000,"extraction_ms":900,"normalization_ms":3,"commit_ms":15,"total_ms":938},"model_usage":{"calls":1,"input_tokens":120,"output_tokens":40,"total_tokens":160},"queue_age_seconds":1,"stale_seconds":0,"diagnostics":[],"created_at":"2026-10-20T02:00:01Z","updated_at":"2026-10-20T02:00:03Z"}
```

`GET /v1/ingest-jobs/{id}` 保留为同一提取任务的兼容读取路径；新客户端应使用
`/v1/jobs/{id}`。`/v1/jobs` 返回最近的提取任务列表。

`GET /v1/governance-jobs` 和 `/v1/governance-jobs/{id}` 返回治理与聚合任务，字段包括
`category`（`governance` 或 `aggregation`）、`stage`、`kind`、`target_key`、`revision`、
调度状态、尝试次数、固定错误码和等待原因。stage 由任务类型/status 计算：普通治理成功显示
memory_materialized，reconcile/聚合成功为 completed；该展示标签不是独立分类或实际创建
Memory 的证明。提取失败位置另见 diagnostic_stage/reason_code，业务 stage 不复用错误阶段名。
quality_outcome 是已解析 draft 的处理摘要，不证明提取覆盖完整，解析器跳过项的限制见模型协议。

`GET /v1/jobs` 支持 status/limit；`GET /v1/governance-jobs` 支持 category/status/kind/target_key/
limit。两者均返回最近的有界 items，不提供总量或游标；未知枚举返回 400。

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
  派生对象和生成目标的只读视图；聚合、经验案例和 Skill 由独立 Aggregation Worker 执行，
  调用方不需要也不存在逐项触发端点。
- `/v1/experiences` 保存应用报告并触发经验案例；`/v1/experiences/{id}/retract` 是显式撤销，
  不是原子事实写入端点。
- `PATCH .../state` 仅状态；`DELETE /v1/memories/{id}` 是逻辑删除，`POST .../purge` 是单个记忆
  永久清除，`POST /v1/forget` 是显式范围的原子、来源和结果联合遗忘；内容新增或更正只走
  `POST /v1/ingestions`。
- `/v1/memory-conflicts` 是只读列表/详情与显式裁决能力；普通召回按 accepted 优先、pending 随后展示。
- 不存在 `POST /v1/sources`、模型提取提交端点或直接记忆写入端点；只在派生发现列表提供游标。
- 所有本地应用按相同规则访问全部记忆；`app_id` 不产生隔离或授权。

## 9. 时间、断言与冲突

### 9.1 记忆字段

当前规范记忆只保留编排当前视图所需的字段：

| 字段 | 含义 |
| --- | --- |
| `assertion_status` | `accepted` / `pending` / `rejected`；普通召回先 accepted 再 pending，永不返回 rejected |
| `version` | 当前规范视图的版本；历史版本只读 |
| `scope_key` | 平面作用域标签 |
| `origin_source_id` | 首次形成该记忆的来源；不承担身份或唯一性 |
| `observation_ids` | 仅详情返回：当前版本引用的 L0 观察 ID |
| `relations` | 仅详情返回：typed observation/memory 关系 |
| `conflict_ids` | 仅详情返回：相关未决冲突 |
| `derived_dependents` | 仅详情返回：直接依赖该记忆的派生对象 |

首次提取阶段不再产生 `observed_at`、`event_time`、`valid_from`、`valid_to`、`temporal`、
`procedure`、`qualifiers`、`fact_group_id`、`topics` 或 `retention_reason`。后续时间能力以独立
注释任务实现，不进入当前主链路。`GET /v1/memories/{id}` 不再返回 `evolution` 或旧版
`relations`；关系统一使用 typed endpoint。

### 9.2 召回与冲突提示

- 当前不提供历史时点/事件时间过滤，也不返回 `temporal_status`。history/conflict 查询模式
  表示返回 L0、当前视图及关系，不表示重建某个历史时点。
- Memory 条目返回 `conflict_refs`；响应顶层返回相关 `conflicts` 摘要。
- 相关未决冲突返回 `warnings: ["fact_conflict"]`；摘要超过 20 项时追加
  `conflict_results_truncated`。
- 冲突发现是独立路径：即使原子 `memories` 分区为空，只要 accepted 或 pending 成员命中查询且
  存在相关 open 冲突，仍返回 `conflicts` 与警告。
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
  "reason": "用户确认"
}
```

| action | 行为 |
| --- | --- |
| `choose` | 采用指定成员；其余成员按现有冲突裁决规则处理 |
| `reject_all` | 全部成员 rejected |

`revision` 与完整 `member_versions`（成员当前版本）是必要输入。冲突/成员已变化返回 `409`；
不得隐式重开已 resolved 冲突。相同裁决重复提交返回现有结果，不同裁决返回
`409`。

### 9.4 有限自动复查

首次提取不执行旧版自动收敛、事实组时间线或改期/取消推断。Observation 治理先做精确重复、
scope、重放及对象保护，再按需判断最小关系；contradicts/unresolved 物化 pending，不保证
自动创建对应 conflict 行。

已有 `review_conflict` 目标只使用当前成员，不调用模型或搜索全库，仅在一个 active+accepted
且无 active pending 成员，或全部成员 active+accepted 且规范正文相同时自动选择。
其余保持 open，不存在 timeline/separate_scope 裁决或按接收时间关闭区间的能力。
普通 Memory 新版本不会自动重新裁决或推断冲突已经解决。

## 10. 治理配置

`governance` 配置项为 `enabled`（缺省 true）、`debounce`、`reconcile_interval`、
`min_call_interval`、`max_inputs` 和 `max_wait`。治理的 `max_inputs` 限制关系候选，
聚合/经验/Skill 的输入上限独立由 `aggregation.max_inputs` 控制；`max_wait` 默认 30 分钟，限制连续摄取推迟一个已到期治理目标的
最长时间，超过后执行一次公平领取。模型调用仍有单次超时，进程内软限速（每小时约 30 次）是
不写入配置的固定运行边界。`enabled=false` 只停止后台整理，HTTP、原子写入和查询保持可用，
已登记任务保留到重新启用。关闭治理不会自动物化新 Memory，已保存 L0 仍可查询。
`aggregation.enabled=false` 只停止派生生成；聚合配置还包括 debounce/reconcile_interval/
max_inputs/l1_bucket_size，bucket 由代码生成，不代表事实有效时间。

## 11. 首版验证与明确限制

- 当前质量与容量旧套件带 legacy 标签，不是 v2 验收；新分层容量、真实模型语义质量、十万条
  或性能 SLA 均需独立验证，见测试文档。
- 本 API 不提供认证、应用隔离、通用分页、自动备份/恢复、外部副本撤销或执行认证。`app_id`、
  `scope_key` 和对话 role 都不是身份或权限凭据。
- 不提供 `filters.as_of/event_at`，未来时间能力将作为独立 annotation 实现。
- 经验结果是应用报告，Skill 是可修正的指导文本；服务不生成或执行脚本，不授予工具权限。
- 自动冲突复查只处理成员集合本身足以证明的确定性规则；复杂冲突保持 `open`，不以接收顺序、
  出现次数或模型自报置信度裁决。
- 永久遗忘只覆盖本服务持久化的数据；已返回客户端、发送模型或手工复制到服务外的数据不在
  召回或擦除承诺内。
