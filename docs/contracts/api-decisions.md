# API 决策记录

本文件补充《系统 AI 记忆服务架构设计.md》v1 未展开的接口语义，并与 `api/openapi.yaml`
共同构成 T1.3 固定的 HTTP 契约。它是文档契约，不生成业务校验层。

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
| 错误体 | `{"error":"..."}`；仅 400/404/500/503，不设置认证失败、业务校验失败或版本冲突专用错误 |

未知 JSON 字段默认忽略。缺少可选辅助字段时使用本文或 `model-provider.md` 的默认值；只有
JSON 解码失败才返回 `400`，资源不存在返回 `404`，执行失败返回 `500`，服务暂不可用
（例如数据库未就绪）返回 `503`。模型调用失败不改变 HTTP 业务响应，通过任务查询体现。

## 2. 统一提交

### `POST /v1/memories`

请求体是 JSON 对象：

```json
{
  "app_id": "desktop-assistant",
  "external_id": "conversation-42-batch-7",
  "content": {"messages": [{"role": "user", "content": "以后请默认使用中文回答。"}]}
}
```

- `content` 可为字符串、对象、数组、数字、布尔或 `null`；不要求调用方说明写入模式。
- `app_id`、`external_id` 可选，仅作为来源信息；无唯一约束、无授权含义，不参与应用隔离。
- `content` 缺失按 JSON `null` 保存；服务正常接收并排队，提取不到记忆时任务成功且结果为空。
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
| `created_by_app` | 创建该记忆的来源应用 | 缺失或空表示不过滤 |
| `updated_after` | RFC 3339，`updated_at` 下限 | 无法解析时忽略该边界 |
| `updated_before` | RFC 3339，`updated_at` 上限 | 无法解析时忽略该边界 |
| `limit` | 本次返回条数 | 缺失、非正整数或无法解析时使用 50 |

过滤列表存在但没有任何可识别值时，返回空 `items`，不扩大为默认集合。排序固定为
`updated_at DESC, id ASC`。响应：

```json
{"items":[{"id":"memory-uuid","kind":"preference","title":"回答语言","content":"用户希望默认使用中文回答。","state":"active","version":3,"observed_at":"2026-10-20T02:00:00Z","created_at":"2026-10-20T02:00:01Z","updated_at":"2026-10-20T03:00:00Z","created_by_app":"desktop-assistant","origin_source_id":"source-uuid"}]}
```

无分页信息；`limit` 没有业务上限。`created_by_app` 是创建时来源的 `app_id` 快照，
后续更新和恢复保持不变；`origin_source_id` 是首次生成该记忆的来源。两者都不承担身份或
唯一性。

### `GET /v1/memories/{id}`

返回当前快照，包括 `state=deleted` 的墓碑。不存在返回 `404`。删除后仍可按 ID 读取墓碑，
但任何全文检索都不返回删除记录。

### `PATCH /v1/memories/{id}`

只修改生命周期状态，请求体：

```json
{"state":"archived"}
```

- 只接受 `active`、`archived`。删除使用 `DELETE`；`deleted` 不作为 PATCH 值。
- 请求体中的 `content`、`title`、`kind`、`version` 等字段一律忽略，不形成内容直接写入捷径。
- `state` 缺失、未知或与当前状态相同：不产生新版本，返回当前记忆（`200`）。
- 内容更正仍只能通过 `POST /v1/memories` 提交。
- 响应为更新后的完整记忆对象。

### `DELETE /v1/memories/{id}`

- 默认逻辑删除：`state=deleted`，生成新版本，保留历史、证据和墓碑。
- `?purge=true`（大小写不敏感，或 `1`）执行永久清除：删除当前记录、全部历史、版本证据
  关联、专属证据和索引；共享来源保留。其他取值按逻辑删除处理。
- 逻辑删除响应：`{"id":"memory-uuid","state":"deleted"}`。
- 永久清除响应：`{"id":"memory-uuid","purged":true}`。
- 已删除记忆再次逻辑删除按状态未变化处理；永久清除不存在的 ID 返回 `404`。

## 4. 版本、恢复与证据

### `GET /v1/memories/{id}/versions`

返回完整版本快照，按 `version DESC` 排序。可选 `limit`：缺失、非正整数或无法解析时使用
50；没有分页。响应：

```json
{"items":[{"memory_id":"memory-uuid","version":3,"operation":"update","reason":"用户更正回答语言","snapshot":{"id":"memory-uuid","kind":"preference","title":"回答语言","content":"用户希望默认使用中文回答。","state":"active","version":3,"observed_at":"2026-10-20T02:00:00Z","created_at":"2026-10-20T02:00:01Z","updated_at":"2026-10-20T03:00:00Z","created_by_app":"desktop-assistant","origin_source_id":"source-uuid"},"evidence":[{"id":"evidence-uuid","source_id":"source-uuid","message_id":"msg-1","quote":"以后请默认使用中文回答。","location":{"field":"messages.0.content","start":0,"end":13},"model_name":"example-model","redacted":false,"created_at":"2026-10-20T02:00:01Z"}],"created_at":"2026-10-20T03:00:00Z"}]}
```

记忆不存在返回 `404`。历史只读；恢复旧版本不会覆盖或删除旧版本。

### `GET /v1/memories/{id}/versions/{version}`

返回指定版本的完整快照与证据集合。`version` 非整数、非正数或不存在时返回 `404`。
记忆不存在同样返回 `404`。

### `POST /v1/memories/{id}/restorations`

请求体：

```json
{"version":2}
```

- `version` 指向历史版本；缺失、非正数、非整数或不存在时返回 `404`。
- 恢复复制该版本内容为新的当前版本，状态恢复为 `active`，版本号高于恢复前的当前版本；
  历史版本本身保持不变。
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

## 5. 关键词检索

### `POST /v1/memories/search`

```json
{
  "query": "回答 语言",
  "match": "all",
  "filters": {
    "kinds": ["preference"],
    "states": ["active"],
    "created_by_app": "desktop-assistant",
    "updated_after": "2026-01-01T00:00:00Z",
    "updated_before": "2026-12-31T23:59:59Z"
  },
  "top_k": 20,
  "explain": true
}
```

| 字段 | 默认/边界 |
| --- | --- |
| `query` | 缺失或空白返回 `200` 与空 `items`；按普通关键词处理，不暴露 FTS 语法 |
| `match` | 缺失或未知值按 `all`；支持 `all`、`any` |
| `filters.kinds` | 缺失/空不过滤；无法识别的值忽略；全部不可识别时结果为空 |
| `filters.states` | 缺失/空默认 `active`；显式包含 `archived` 才查归档；`deleted` 永不返回 |
| `filters.created_by_app` | 缺失/空不过滤 |
| `filters.updated_after/before` | RFC 3339；无法解析时忽略该边界 |
| `top_k` | 缺失、非正整数或无法解析时使用 20；无业务上限 |
| `explain` | 默认 `false`；只有 `true` 返回 `explanation` |

排序固定为加权 BM25（标题 4、正文 1）升序、`updated_at` 降序、`id` 升序。对外
`score = -bm25`，越大越相关；分数只用于当前查询内比较。响应：

```json
{
  "items": [
    {
      "memory": {"id":"memory-uuid","kind":"preference","title":"回答语言","content":"用户希望默认使用中文回答。","state":"active","version":3,"observed_at":"2026-10-20T02:00:00Z","created_at":"2026-10-20T02:00:01Z","updated_at":"2026-10-20T03:00:00Z","created_by_app":"desktop-assistant","origin_source_id":"source-uuid"},
      "score": 0.0000042,
      "snippet": "用户希望默认使用中文回答。",
      "matched_terms": ["回答", "语言"],
      "evidence_refs": [{"id":"evidence-uuid","source_id":"source-uuid"}],
      "explanation": {"ranking":"bm25","field_weights":[4,1]}
    }
  ]
}
```

`snippet` 来自原始正文，不返回拆字索引文本、不嵌入 HTML；`evidence_refs` 对应当前版本。
`explanation` 仅在 `explain=true` 时出现。空查询不是错误，调用方应改用管理列表接口获取
全量列表。搜索没有跨请求快照或分页。

## 6. 来源与任务

### `GET /v1/sources/{id}`

返回来源标识、可选来源信息、原始 `content`、`redacted` 状态与接收时间。来源不存在返回
`404`。清除后仍返回 ID、`redacted=true` 与 `redacted_at`，但不再返回原文。

### `DELETE /v1/sources/{id}`

同一事务清空来源原文、全部历史证据中的原文副本与定位，保留来源标识和 `redacted`，并把
关联的 `queued`/`running` 任务置为 `cancelled`。已形成的记忆正文保留。

响应：

```json
{"id":"source-uuid","redacted":true,"cancelled_jobs":1}
```

重复清除保持 `redacted=true`，不再取消任务时 `cancelled_jobs=0`。该操作是逻辑清除，不承诺
存储介质或远端模型侧的擦除。

### `GET /v1/ingest-jobs/{id}`

返回任务状态、尝试次数、脱敏错误摘要、生成记忆 ID 与时间：

```json
{"id":"job-uuid","source_id":"source-uuid","status":"succeeded","attempts":1,"memory_ids":["memory-uuid"],"created_at":"2026-10-20T02:00:01Z","updated_at":"2026-10-20T02:00:03Z","started_at":"2026-10-20T02:00:02Z","finished_at":"2026-10-20T02:00:03Z"}
```

`status` 为 `queued`、`running`、`succeeded`、`failed`、`cancelled`。失败原因不包含完整
模型响应或原始输入。任务不存在返回 `404`。无生成记忆时 `memory_ids` 为 `[]`。

## 7. 健康检查

| 路径 | 成功 | 失败 |
| --- | --- | --- |
| `GET /healthz` | `200 {"status":"ok"}` | 进程不存活则无响应 |
| `GET /readyz` | `200 {"status":"ready"}` | 数据库未就绪/不可用：`503 {"status":"unavailable"}` |

模型未配置或不可达不影响 `/readyz`；服务仍可提交、查询和管理已有记忆。

## 8. 路由与端点边界

- `/v1/memories/search` 是集合级动作，不是记忆 ID；路由实现必须先匹配该静态路径。
- 版本、证据是记忆的只读子资源；恢复是新增版本的动作，不是内容写入端点。
- `PATCH` 仅状态；`DELETE` 仅生命周期/清除；内容新增或更正只走 `POST /v1/memories`。
- 不存在 `POST /v1/sources`、模型提取提交端点、直接记忆写入端点或分页端点。
- 所有本地应用按相同规则访问全部记忆；`app_id` 不产生隔离或授权。
