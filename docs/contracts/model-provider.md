# 远端模型协议（首个适配器）

> 当前实现已把提取与判断分离为 `ExtractionProvider` 和 `DecisionProvider`。同一个远端
> 适配器实现两个接口，复用端点和凭据；模型名、超时和思考提示可按提取/决策/治理任务覆盖。
> 第 2～4 节描述请求、响应和错误语义；第 9 节给出当前提取/决策字段。旧动作和旧字段不提供兼容。

本文件固定远端模型协议，明确请求字段、候选/决策结构和错误语义。协议采用
OpenAI-compatible Chat Completions，但只使用下文列出的字段；Provider 不接收完整记忆库、
无关来源或本地 API Token。

## 1. 配置映射

| 配置项 | 映射 |
| --- | --- |
| `model.base_url` | API 根地址，例如 `https://model.example.invalid/v1`；实际地址为 `<base_url>/chat/completions` |
| `model.name` | 基础请求体 `model` 字段 |
| `model.api_key` | 请求头 `Authorization: Bearer <api_key>`；只从本机受限配置文件读取，不从 HTTP 请求头读取 |
| `model.timeout` | 基础单次调用超时，默认 60 秒；由 `context.WithTimeout` 实现并可被关闭/取消传播 |
| `model.extraction` / `model.decision` / `model.governance` | 可选的按任务覆盖：`name`、`timeout`、`reasoning_effort`（`none/minimal/low/medium/high`）；缺省回落基础配置。 |
| 本地 API Token | 与模型凭据无关，绝不转发 |

提取、决策和治理共享同一端点和凭据，但请求体使用各自任务的有效模型名、超时和
`reasoning_effort`。`reasoning_effort` 是 OpenAI-compatible 扩展提示；提供方不支持或会拒绝未知字段时应留空。
服务不把关闭思考或更换快模型当作协议、证据或语义问题的默认修复。

`api_key` 为空时省略 `Authorization`，便于连接本地假模型。凭据不得写入日志、任务错误、
响应或测试夹具。配置文件权限必须为 `0600`（服务启动时检查）。

## 2. 请求

### 2.1 HTTP

```http
POST /v1/chat/completions HTTP/1.1
Host: model.example.invalid
Content-Type: application/json
Authorization: Bearer sk-***

{
  "model": "example-model",
  "temperature": 0,
  "response_format": {"type": "json_object"},
  "reasoning_effort": "low",
  "messages": [
    {
      "role": "system",
      "content": "Extraction v5: Extract independently updateable assertions from current_source.segments. Return only one JSON object with a candidates array. Each evidence entry uses segment_id and a verbatim quote; never invent a segment ID. Fact_group and procedure are single objects, never arrays. Keep observed/event/valid times distinct and preserve qualifiers, uncertainty and attribution."
    },
    {
      "role": "user",
      "content": "{\"current_source\":{\"id\":\"source-uuid\",\"app_id\":\"desktop-assistant\",\"external_id\":\"conversation-42-batch-7\",\"received_at\":\"2026-10-20T02:00:01Z\",\"protocol_version\":\"extraction-v5\",\"segments\":[{\"segment_id\":\"seg-0001\",\"message_id\":\"m1\",\"role\":\"user\",\"field\":\"messages.0.content\",\"text\":\"以后请默认使用中文回答。\"}]}}"
    }
  ]
}
```

`messages[1].content` 是一个 JSON 字符串，解码后形如：

```json
{
  "current_source": {
    "id": "source-uuid",
    "app_id": "desktop-assistant",
    "external_id": "conversation-42-batch-7",
    "received_at": "2026-10-20T02:00:01Z",
    "protocol_version": "extraction-v5",
    "chunk_index": 2,
    "chunk_count": 3,
    "segments": [
      {
        "segment_id": "seg-0005",
        "message_id": "m3",
        "role": "user",
        "field": "messages.2.content",
        "text": "以后请默认使用中文回答。"
      }
    ],
    "context_segments": [
      {
        "segment_id": "seg-0004",
        "message_id": "m2",
        "role": "user",
        "field": "messages.1.content",
        "text": "上一窗口末尾的只读上下文。"
      }
    ]
  }
}
```

单窗口请求省略 `chunk_index`、`chunk_count` 和 `context_segments`。

约束：

- `current_source` 是服务端确定性片段视图，不是客户端 JSON 的改写。`segment_id` 在同一来源
  的重试、提取和 repair 中稳定；`message_id`/`role`/`occurred_at` 只是辅助上下文。单个超长
  字符串会按固定上限、优先在句末切成稳定子片段，证据位置仍映射回原始字段。
- 提取请求不包含相关记忆或数据库目标。决策阶段的输入见第 9.2 节，包含有界相关事实。
- `related_memories` 只包含服务按关键词选出的 `active` 记忆，不含无关来源、全库、归档或
  删除记录。选择规则：对当前输入使用与搜索相同的 NFC/大小写/词典分词规范化，去重后
  取前 32 个分词词项按 `any` 组合；最多返回 10 条，排序与公共搜索一致（加权 BM25、`updated_at`
  降序、`id` 升序）。没有匹配时为空数组；不因输入过长而拒绝当前提交。
- 模型只负责提出候选，版本号、来源证据 ID、最终写入事务由服务决定。
- 不发送本地 HTTP 请求头、Token、数据库路径或其他应用数据。

## 3. 成功响应

HTTP 状态 `200`：

```json
{
  "id": "chatcmpl-***",
  "object": "chat.completion",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "{\"candidates\":[{\"content\":\"用户希望默认使用中文回答。\",\"kind\":\"preference\",\"title\":\"回答语言\",\"observed_at\":\"2026-10-20T02:00:00Z\"}]}"
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {"prompt_tokens": 120, "completion_tokens": 40}
}
```

`usage` 可选；适配器只读取标准 `prompt_tokens`/`completion_tokens` 并累计到任务
`model_usage`，不保存原始响应。

`choices[0].message.content` 必须是 JSON 对象（不是自然语言说明），解码后：

```json
{
  "candidates": [
    {
      "content": "用户希望默认使用中文回答。",
      "kind": "preference",
      "title": "回答语言",
      "observed_at": "2026-10-20T02:00:00Z"
    }
  ]
}
```

默认提示词要求 `content` 和可定位证据，并允许省略有默认值的字段。候选不包含
`action`、目标记忆或数据库状态；`reason` 只能作为决策协议中的诊断字段，不作为事实依据。
解析器对单个证据对象、单元素 `fact_group`/`procedure` 数组和单字符串列表执行白名单
规范化，其余字段类型错误会保留为字段级诊断并进入一次定向 repair。

### 3.1 候选字段

提取协议版本为 `extraction-v5`。候选只描述断言，不包含数据库动作或目标记忆。

| 字段 | 必填 | 语义与默认值 |
| --- | --- | --- |
| `candidate_id` | 否 | repair 时用于关联；服务在首次提取时重新分配稳定 ID |
| `kind` | 否 | `fact`、`preference`、`event`、`procedure`；缺失时按流程结构、phase 和通用偏好线索推断，并记录 `kind_inferred` 诊断，不再静默按 fact 处理 |
| `title` | 否 | 简短标题；缺失或空时由服务取正文前 40 个 Unicode 字符（去首尾空白） |
| `content` | 是 | 记忆正文；缺失、类型错误或为空时进入定位 repair |
| `observed_at` | 否 | 来源作出陈述或观测的时间；缺失时回退到来源接收时间并以 `observed_at_basis=recorded` 标记。该回退不作为“新事实更晚”的收敛证据 |
| `observed_at_basis` | 否 | `explicit`、`inferred`、`unknown` |
| `event_time` | 否 | 事件发生、计划或取消的时间；与事实有效时间分开 |
| `event_time_basis` | 否 | `explicit`、`inferred`、`unknown` |
| `valid_from`/`valid_to` | 否 | 事实有效区间边界，RFC 3339 或完整日期；必须与证据中的日期语义匹配，不能把正文所有日期都填入 |
| `valid_from_basis`/`valid_to_basis` | 否 | `explicit`、`inferred`、`unknown` |
| `phase` | 否 | `unknown`、`scheduled`、`in_progress`、`completed`、`cancelled`、`postponed` |
| `durability` | 否 | `unknown`、`durable`、`episodic`、`temporary` |
| `topics` | 否 | 字符串数组；服务 trim、转小写并去重 |
| `fact_group` | 否 | 单个对象：`subject`、`predicate`、`scope`、`conditions`、`cardinality`；服务还会加入来源 `scope_key`，模型不能代填 |
| `procedure` | 否 | 单个对象：`goal`、`steps`、`preconditions`、`completion_conditions`、`failure_branches`；持久化到原子修订，不再只是候选临时字段 |
| `qualifiers` | 否 | 通用限定：`entities`、`quantity{value,unit}`、`conditions`、`polarity`、`certainty`、`attribution`；用于检查实体、值/单位、条件、极性和归属是否丢失 |
| `evidence` | 否 | 证据对象数组：`segment_id`、`quote`，可选 `start`/`end` |

模型不输出 `action`、`target_memory_id`、记忆 ID、版本、证据 ID、断言状态、组 ID 或
`scope_key`。`evidence[].segment_id` 只能引用 `current_source.segments` 中可见的片段；
未知 ID 不会被模糊匹配。quote 必须逐字出现；同一片段内重复出现时必须给出相对片段文本的
rune 偏移。证据缺失时只保留来源关联，不能据此自动更正或收敛。

## 4. 容错与部分可解析

### 4.1 部分可解析示例

模型返回 `200`，`message.content` 为：

```json
{
  "candidates": [
    {
      "kind": "preference",
      "title": "回答语言",
      "content": "用户希望默认使用中文回答。",
      "observed_at": "2026-10-20T02:00:00Z",
      "evidence": [{"segment_id": "seg-0001", "quote": "用户希望默认使用中文回答。"}]
    },
    {
      "kind": "not-a-kind",
      "content": ""
    },
    {
      "kind": "preference",
      "title": "回答语言",
      "content": "用户希望默认使用中文回答。",
      "evidence": {"segment_id": "seg-0002", "quote": "以后请默认使用中文回答。"}
    }
  ]
}
```

处理结果：第一个候选通过；第二个候选进入一次定向 repair，repair 仍不能提供合法正文和
`kind` 时被隔离；第三个候选的单个证据对象按白名单规范化为单元素数组并通过。整次提取仍是
成功结果，`quality_outcome=partial`，隔离数量与固定诊断可从任务查询。若无任何候选可保留，
质量结果为 `unusable`，任务按既有协议重试预算进入 `waiting/failed`，不会伪装成合法空提取。

### 4.2 失败示例

HTTP `400`/`401`/`403`/`404`（不可重试）：

```json
{"error":{"message":"invalid model name","type":"invalid_request_error"}}
```

HTTP `500`（可重试）：

```json
{"error":{"message":"upstream unavailable","type":"server_error"}}
```

HTTP `200` 但 `message.content` 为自然语言而非 JSON（可重试，最终失败）：

```json
{
  "choices": [
    {
      "index": 0,
      "message": {"role": "assistant", "content": "I could not determine any memories."},
      "finish_reason": "stop"
    }
  ]
}
```

以上失败只记录脱敏类别、HTTP 状态和任务错误摘要，不保存原始模型响应或 `error.message`，
也不把凭据写入任何持久化字段。

### 4.3 解析规则

- 响应 `content` 可被一层 Markdown 代码围栏包裹（例如 ```` ```json ... ``` ````）；适配器
  去除围栏后解析。围栏之外的自然语言视为不可解析。
- 顶层缺少 `candidates` 或为 `null`：视为不可解析；合法的空提取必须返回
  `{"candidates":[]}`。
- 单个候选不是对象、`content` 缺失/类型错误/为空：保留候选并记录固定字段诊断，进入一次
  定向 repair；不会静默跳过或补默认值。
- `observed_at`/`event_time`/`valid_from`/`valid_to` 类型错误或无法解析时记录时间诊断，不因此丢弃
  其余可用字段，但该候选不能直接入库。完整日期允许无时区；只出现月份、节假日或模糊时间时
  不生成精确日期。
- 安全形状规范化白名单：`evidence` 单个合法对象 → 单元素数组；`fact_group`/`procedure`
  单元素对象数组 → 对象；`topics` 与 procedure 字符串列表的单个非空字符串 → 单元素数组。
  规范化记录随任务诊断返回。多个 `fact_group`、任意字符串 → 证据、列表按标点拆分等均不
  允许恢复。
- 证据数组逐项解析。非法项被剔除并记录 `field`/`code`；剩余证据仍不能消除协议问题时，
  候选进入定向 repair，repair 后仍不安全则隔离。
- 顶层 JSON 无法解析、`choices` 缺失/为空、`message.content` 为空或不是 JSON 对象：
  整个任务调用失败，记录脱敏错误摘要，不保存该次候选；非空候选全部不可用时质量结果为
  `unusable`，按既有协议重试预算进入 `waiting/failed`。
- 不把模型返回的整段原始文本写入任务错误、日志或数据库；只保留错误类别、HTTP 状态、
  固定诊断码和截断后的安全描述。

## 5. HTTP 错误与超时

| 情况 | Provider 返回 | Worker 处理 |
| --- | --- | --- |
| 连接失败、DNS 失败、超时（默认 60s） | `ProviderError{Retryable:true}` | 进入 `waiting`，按 5 秒/30 秒/2 分钟、之后最长 30 分钟继续同一任务 |
| `429 Too Many Requests` | 可重试错误 | 进入 `waiting` 并退避；记录脱敏状态码 |
| `500`/`502`/`503`/`504` | 可重试错误 | 进入 `waiting` 并退避 |
| `400`/`401`/`403`/`404` | 不可重试错误 | `waiting`（配置/凭据原因），重启后重评 |
| `200` 但整体不可解析 | 协议错误 | 连续三次后 `failed`，重启后重新评估一次 |
| `200` 且 `candidates:[]` | 成功 | 任务 `succeeded`，`quality_outcome=empty`，`memory_ids=[]` |
| 部分候选安全、部分隔离 | 成功 | 任务 `succeeded`，`quality_outcome=partial`，隔离候选只保存固定诊断 |
| 非空候选全部不可用 | 协议错误 | 质量结果 `unusable`，按协议重试进入 `waiting`；达到上限后 `failed` |

重试调度、`queued → running → waiting → succeeded/failed/cancelled` 状态机和崩溃恢复
由 ingest Worker 负责；Provider 内部不自行重试，也不创建第二套状态。已发出的远端请求
无法撤回；来源清除后的取消只保证本地不提交结果，不承诺远端擦除。

## 6. 假模型与测试要求

契约测试使用本地 `httptest` 假服务，覆盖：

- 成功单候选、多候选、零候选；
- 部分候选缺失辅助字段（默认值）；
- 整体不可解析、部分可解析；
- 超时、连接失败、429、5xx、4xx；
- 阻塞响应开始后来源被清除的取消竞态；
- 校验请求只包含当前来源和有界相关上下文，且不包含真实凭据或完整记忆库。

测试不得访问外网或使用真实模型凭据；真实连通性检查只能作为显式可选操作，不作为默认测试
前提。

## 7. 处理编排与合并边界

`ingest.Processor` 在模型调用前读取来源，先调用一次提取；随后为每个候选选择关键词
（启用 Embedding 时补充语义）上下文、事实组时间线和 open 冲突。无相关事实且可确定时直接
构建新增/补证计划，否则按最多 8 个候选一批调用决策模型。所有模型与 Embedding 请求都在写
事务外完成。

提交时重新读取任务、来源、目录 generation、目标版本和组 generation：

- 非法目标、缺失决策、未知动作和非法区间属于协议错误，整个尝试不提交并进入任务重试；
  不再把非法目标降级为 `add`。
- 上下文不完整时禁止 `change/correct/enrich/merge_evidence`，规范化为 pending/unresolved。
- `add` 只在同组 single 冲突检查通过时 accepted；同结论重复合并证据，不扩大区间。
- `change` 关闭一个已确认前驱、创建后继并写 `supersedes`；乱序、未知边界、多段拆分或
  越过后继时转 pending/冲突。
- `correct` 仅在有当前来源可定位引文时自动执行；局部时间纠错、无依据更正转 pending。
- `conflict`/`unresolved` 生成 pending 记录和固定原因码；同组同成员版本同原因复用 open
  冲突，不堆叠重复记录。
- 目录 generation、目标版本或组 generation 在模型期间变化时返回内部可重试错误；现有
  Worker 以暂时失败退避重跑整个来源。不允许先提交部分来源结果。
- 多候选、版本、证据、关系、冲突、索引和任务 `succeeded`/结果摘要在同一短事务提交；
  cancelled 优先，已取消或已成功任务不重复写入。零候选成功提交空结果。
- 提取阶段维护 `quality_outcome`（`empty/complete/partial/unusable`）和分阶段
  `extraction_stats`；候选级诊断只含 candidate ID、field、固定 code、stage 和处置，
  不保存原文或模型响应。协议/证据错误导致的全部不可用进入 `unusable` 并在既有协议重试
  预算内等待或失败；隐私排除正常完成且不重试。
- 暂时失败进入 `waiting`，按 5 秒、30 秒、2 分钟后最长每 30 分钟继续同一任务；连续三次
  协议/解析错误隔离为 `failed`。配置/凭据错误等待重启后重评；启动恢复把 running 重排 queued、
  重评配置等待，并让协议隔离任务按新的模型/协议版本重新评估一次。
- 已发出的远端请求不能撤回。来源清除只保证本地取消后不提交结果，不宣称远端副本被擦除。

## 8. Embedding 协议

Embedding 使用与提取适配器分开的配置和 HTTP 适配器（`embedding.*`），采用
OpenAI-compatible Embeddings：`POST <base_url>/embeddings`，请求体包含 `model`、
`input` 字符串数组和 `encoding_format: "float"`。配置只包含 enabled、端点、凭据、
模型、维数、调用超时和相似度阈值。

- `api_key` 独立于提取模型，只从本机配置文件读取；为空时省略 `Authorization`。
- 成功响应必须包含与输入数量一致的 `data`，每项按 `index` 排序且不重复；维数必须与
  `embedding.dimensions` 一致，分量必须有限且范数非零。调用方在 L2 归一化后以
  little-endian float32 保存。
- 输入不静默截断；长度由模型端点拒绝。查询失败时保留关键词检索，文档任务记录脱敏
  错误码。
- `429` 可重试并尊重有上限的 `Retry-After`；连接失败、超时、`408`/`425` 和 `5xx`
  可重试；`401`/`403`、`400`、维数、数量和协议错误不可重试。错误只保存稳定代码。
- 在线查询不自动重试，使用固定 500 ms 子预算；后台索引最多三次，指数退避从 1 秒起、
  上限 60 秒并加抖动。
- 适配器不记录或持久化查询、正文、向量、完整响应或凭据。测试使用本地假 Embeddings
  服务，不访问外网。


## 9. 两阶段协议（当前实现）

### 9.1 提取

`ExtractionProvider.Extract` 的输入只有 `current_source`，不包含数据库目标、相关记忆或
动作。`current_source` 是服务端构建的确定性片段视图，不是原始 JSON 的改写：

```json
{
  "current_source": {
    "id": "source-uuid",
    "scope_key": "project:dmemd",
    "received_at": "2026-10-20T02:00:00Z",
    "protocol_version": "extraction-v5",
    "segments": [
      {"segment_id": "seg-0001", "message_id": "m1", "role": "user",
       "field": "messages.0.content", "text": "用户常住上海"}
    ]
  }
}
```

系统提示要求返回一个 JSON 对象：

```json
{
  "candidates": [
    {
      "candidate_id": "c1",
      "kind": "fact",
      "title": "常住城市",
      "content": "用户常住上海。",
      "observed_at": "2026-01-10T02:00:00Z",
      "valid_from": "2026-01-10T02:00:00Z",
      "valid_to": null,
      "valid_from_basis": "explicit",
      "valid_to_basis": "unknown",
      "topics": ["housing", "shanghai"],
      "fact_group": {"subject": "用户", "predicate": "常住城市", "scope": "", "cardinality": "single"},
      "evidence": [{"segment_id": "seg-0001", "quote": "用户常住上海", "start": 0, "end": 6}]
    }
  ]
}
```

- 提取协议版本为 `extraction-v5`，决策协议版本为 `decision-v4`。当前 OpenAI-compatible
  适配器发送 `response_format: {"type":"json_object"}`；字段类型和必需结构由服务端完整示例、
  白名单规范化和本地校验保证，不能把 `json_object` 本身视为字段类型约束。
- `segment_id` 由服务按确定性遍历分配；同一来源重试、提取和 repair 使用同一视图。客户端
  消息 ID 只作为辅助信息，缺失、重复或冲突不影响片段 ID 的唯一性。单个超长字符串会按固定
  上限、优先在句末切成稳定子片段，`field` 不变，证据位置仍映射回原始字段 rune 范围。
- 超过 `worker.extraction_chunk_runes`（默认 12000）的来源按稳定片段分成有界窗口。除第一个
  窗口外，`context_segments` 携带上一窗口末尾的只读上下文；模型只能引用/提取
  `segments`，上下文只用于指代、代词和重复消解。服务在合并结果时丢弃仅由上下文支撑的候选，
  合并跨窗口完全相同的表示，并保留所有有效证据。若单个稳定片段仍大于配置窗口，它作为
  oversized 窗口继续处理并产生固定诊断，不会静默截断。
- 模型不得输出记忆 ID、版本、证据 ID、断言状态、组 ID、作用域或动作。`topics` 只能是
  平面短标签，由服务 trim、转小写并去重；不是层级路径。
- `current_source.scope_key` 由调用方显式提供并原样交给模型作为上下文；模型无权提升为
  用户全局作用域。`role` 用于区分用户陈述、助手建议、引用和假设，不是身份认证。
- 证据只能引用可见片段。未知 `segment_id` 不会被模糊修正；缺失 `segment_id` 仅在 quote
  全来源精确唯一时恢复并记录 `unique_quote`；同一片段内重复出现且没有 `start`/`end` 时
  视为歧义。
- `valid_from/to` 接受 RFC 3339 或完整日期；只有月份、节假日或模糊日期时留空，不伪造某一天。
  正文日期必须按语义角色分配到 `observed_at`、`event_time` 或有效区间，不能因为有日期就填入
  `valid_from`。非空边界必须带 `explicit` 或 `inferred` 依据；空边界对应 `unknown`。
- 计划事件使用 `event_time` 和 phase；未来事件日期不得写成未来 `valid_from`，否则当前查询会把
  已知计划隐藏。相对时间只在同一消息存在可解析 `occurred_at` 时接受，否则保持未知。
- 正文、否定、条件、主体和时间必须保留；“去北京出差”不能提取成“常住北京”。
- 每来源最多接受 64 个候选（含 repair 拆分后的候选）。超出部分不再让整个来源不可用：
  安全的前 64 个候选继续处理，超出数量写入 `dropped_candidates`、固定
  `candidate_limit_exceeded` 诊断和 `partial` 质量结果；不会静默截断或无限提高上限。
- 每次成功响应可带 `usage.prompt_tokens`/`usage.completion_tokens`；适配器按任务累计调用数
  和 token 到任务 `model_usage`。提供方不返回 usage 时仍计调用数，token 为 0，不推测。
- 安全形状规范化和候选级修复规则见第 4.3 节；已通过候选不进入 repair。

### 9.2 决策

`DecisionProvider.Decide` 接收最多 8 个候选、有限相关事实、事实组和 open 冲突。相关事实
已包含事实组时间线，不再单独重复发送时间线。决策请求的 `current_source.segments` 只包含
本批候选证据实际引用的稳定片段；无法定位引用时才回落到完整来源视图，避免长来源在每个
决策批次重复传输。输出一个对象，所有候选必须恰有一项决策：

```json
{
  "decisions": [
    {
      "candidate_id": "c1",
      "action": "change",
      "target_memory_ids": ["m1"],
      "target_group_id": "g1",
      "content": "用户常住北京。",
      "valid_from": "2026-06-15T02:00:00Z",
      "valid_to": null,
      "valid_from_basis": "explicit",
      "valid_to_basis": "unknown",
      "reason_code": "explicit_transition",
      "evidence_refs": [{"candidate_id": "c1", "evidence_index": 0}],
      "fact_group": {"subject": "用户", "predicate": "常住城市", "conditions": [], "cardinality": "single"}
    }
  ]
}
```

决策输入还会返回 `incomplete_candidate_ids` 和 `incomplete_fact_group_ids`，标识被截断的
候选或事实组；服务只对相应候选降级破坏性写入，不因无关组截断阻塞其他安全更新。

允许动作：`add`、`merge_evidence`、`enrich`、`change`、`correct`、`separate`、`conflict`、
`unresolved`、`skip`。`enrich/correct/change/merge_evidence` 首版只允许一个目标。目标记忆
和组 ID 必须出现在本次上下文中；证据引用只能来自本批候选。未知动作、缺失决策、重复
candidate_id、伪造目标和非法区间先进入一次定向 repair，仍不合法才使整个尝试不提交并进入任务重试；业务上证据
不足的动作规范化为 pending/unresolved，不降级为 add。

### 9.3 上下文与预算

- 每个候选按主体/属性/场景/正文做关键词召回，默认最多 20 条初始相似记忆；同组时间线
  额外最多 50 条。任一上限截断时 `context_complete=false`，破坏性动作转 pending。
- 一致性上下文可包含历史段、pending 和归档背景，但不含删除墓碑；归档记录不能被自动激活。
- 每次任务尝试共用 `worker.process_budget`（默认 600 秒）总预算；提取和提取 repair 默认使用
  `worker.extraction_timeout`（180 秒），决策和决策 repair 默认使用
  `worker.decision_timeout`（300 秒），可分别由 `model.extraction.timeout` 和
  `model.decision.timeout` 覆盖。治理调用默认使用 `model.timeout`，可由
  `model.governance.timeout` 覆盖。每次调用还受剩余总预算约束。
- `separate` 的非法 `target_memory_ids` 由服务删除，不增加调用；`change` 多目标不能猜测，
  仅将无效候选、原决策和固定错误码发送一次 repair。同批有效决策保留。repair 仍失败才记协议
  失败，网络/超时仍记暂时失败。协议隔离上限由 `worker.max_attempts` 配置，默认 3。
- 模型调用日志包含 `stage`、`batch_size`、`context_size`（字节）、`duration_ms`、调用数、
  input/output token 和错误类型；任务查询持久化 `timings`、`model_usage` 和 `efficiency`。
  协议校验单独记录 `protocol_error_type`，不记录原文、完整响应或自由文本原因。
- 调用次数至少为每个提取窗口一次，加每 8 个候选一批决策；无相关事实且可确定性新增/补证时
  不调用决策。多候选批次只有在每个候选都有完整作用域身份、且没有相关事实/组/冲突时才走
  确定性新增；空关键词召回本身不构成“全库无相关事实”的证明。长来源可能产生多个提取窗口，
  任务 `timings`、`model_usage` 和 `efficiency` 分别记录阶段耗时、token 与每个有效原子的成本。
- 修复预算仍是每次尝试最多一次模型 repair；长来源某窗口的协议失败会消耗该预算，其余已通过
  窗口的候选保持不变，失败窗口以固定诊断隔离。
- 网络、限流、超时进入 `waiting` 并按 5 秒、30 秒、2 分钟递增退避，之后最长 30 分钟；
  恢复后继续同一任务。连续三次解析/协议错误进入 `failed`；认证/配置错误保持等待并在重启后
  重评。`generation`/version 变化按任务 revision 丢弃旧结果。
- 模型返回 `unresolved` 是业务成功；持久化 pending 记录和固定原因码，不保存推理过程、
  自由文本理由或完整响应。

### 9.4 分类

当原子记忆没有可用主题时，治理分类处理器可调用同一个适配器。分类、摘要、经验和 Skill
共享 `model.governance` 的有效模型名、超时和思考提示：

```json
{
  "title": "项目数据库",
  "content": "项目使用 SQLite 保存记忆。",
  "scope_key": "project:dmemd"
}
```

系统提示要求只返回：

```json
{"topics": ["go", "sqlite"]}
```

- 输入只包含选中的一条原子快照，正文按固定上限截断；不发送来源原文、全库或凭据。
- 缺少 `topics` 数组按空结果处理；数组类型错误属于协议错误。服务再次规范化后保存，
  标签完全相同时不生成新版本。
- 分类调用受进程内软限速约束；限速只推迟任务，不影响输入持久化或原子查询。

### 9.5 聚合摘要、作用域概览与画像

治理 Worker 在选出的原子记忆有界集合上调用同一适配器。`kind` 为 `topic_summary`、
`scope_summary` 或 `profile`；作用域概览和画像的目标只由 scope 与派生类型确定，不要求模型
输出项目名称。

```json
{
  "kind": "scope_summary",
  "scope_key": "project:dmemd",
  "topic": "",
  "truncated": false,
  "eligible": 3,
  "memories": [
    {
      "id": "memory-uuid",
      "version": 3,
      "kind": "fact",
      "title": "项目数据库",
      "content": "项目使用 SQLite 保存记忆。",
      "scope_key": "project:dmemd",
      "topics": ["go", "sqlite"],
      "procedure": {"goal": "...", "steps": ["..."]},
      "qualifiers": {"conditions": ["..."]},
      "observed_at": "2026-10-20T02:00:00Z",
      "updated_at": "2026-10-20T03:00:00Z"
    }
  ]
}
```

系统提示要求只返回：

```json
{
  "title": "项目概览",
  "content": "项目使用 SQLite 保存记忆，并受项目作用域限制。",
  "citations": [
    {"section": "现状", "point": "项目使用 SQLite", "memory_id": "memory-uuid", "version": 3}
  ],
  "omitted_inputs": [
    {"memory_id": "memory-uuid", "version": 3, "reason": "与本次概览无关"}
  ]
}
```

- 输入只来自 `active`、`accepted`、时间适用且没有未决冲突的原子记忆。`topic_summary`
  按 canonical topic 读取叶子原子；`scope_summary` 读取整个 scope 的合格原子；
  `profile` 只接收 `durability=durable`、无事件 phase、无结束边界的 fact/preference/procedure。
- 每个目标最多 64 条，正文按单条和总量上限截断。服务单独记录输入选择完整性
  （`eligible/selected/included/omitted` 与原因）、引用完整性、语义覆盖
  （`unknown/subset`）、合法 `citations` 和 `omitted_inputs`；无法确定语义覆盖时使用
  `unknown`，服务已知省略时使用 `subset`，不用子串或模型自报直接认定完整。
- 模型返回的引用必须引用本轮实际提供的 `memory_id` 和精确 `version`；不存在或版本不匹配的
  引用被服务丢弃。引用合法不等于引用足以支持正文。摘要输入签名相同的活跃目标不会重复调用
  模型；输入变化、归档、删除或冲突裁决仍使当前版本失效。
- Profile 的生成正文另做固定进展/时间标记检查；检测到一次性进度内容时不激活该版本。
- 正文为空或 JSON 不可解析属于协议错误；标题缺失使用固定默认值，不因辅助字段缺失丢弃
  可用正文。
- 返回正文不会成为原子事实、配置或可执行动作；服务只保存来源引用和派生版本。输入变化、
  归档、删除或冲突裁决使当前版本失效，迟到结果因任务 revision 变化被丢弃。

### 9.6 经验案例

`ExperienceProvider.ComposeExperience` 把一条应用报告的运行结果和有限的原子程序/事件
组织为结构化案例：

```json
{
  "run": {
    "id": "experience-uuid",
    "run_id": "run-1",
    "scope_key": "project:dmemd",
    "goal": "验证修改",
    "conditions": ["依赖已安装"],
    "actions": ["运行 go test"],
    "outcome": "success",
    "skill_id": "skill-uuid",
    "skill_version": 2,
    "created_at": "2026-10-22T08:00:00Z"
  },
  "skill": {"id": "skill-uuid", "version": 2, "title": "验证工作流", "content": "..."},
  "memories": [
    {"id": "memory-uuid", "version": 2, "kind": "procedure", "title": "验证步骤",
     "content": "修改后运行 go test。", "scope_key": "project:dmemd", "topics": ["testing"]}
  ],
  "truncated": false
}
```

系统提示要求只返回一个 JSON 对象：

```json
{
  "problem": "验证 dmemd 修改",
  "preconditions": ["依赖已安装"],
  "actions": ["运行 go test"],
  "lessons": ["先执行单元测试"],
  "failure_modes": ["跳过构建会遗漏编译错误"],
  "content": "问题、条件、行动和结果的示例正文"
}
```

- 结果保留调用方提交的 `outcome`，模型输出中的任何结果声明都不采用；服务不将“没有报错”
  转为成功，也不写执行认证。
- 输入只含结构化运行摘要和有界原子记忆，不读取来源原文；超过上限时标记截断。经验案例的
  `payload.topics` 是服务从实际选入的程序/事件派生的工作流关联；Skill 只接受显式 Skill 引用
  或单一同主题经验，同一 scope 本身不构成相关性证据。
- 服务自行附上真实 `memory_refs`、`experience_refs` 和选中的 Skill 上下文；模型伪造的
  来源引用不建立关联。

### 9.7 指导型 Skill

`SkillProvider.ComposeSkill` 使用同一适配器生成单个指导型工作流：

```json
{
  "scope_key": "project:dmemd",
  "topic": "testing",
  "procedures": [{"id": "memory-uuid", "version": 2, "kind": "procedure",
    "title": "验证步骤", "content": "修改后运行 go test。", "topics": ["testing"]}],
  "experiences": [{"id": "experience-uuid", "goal": "验证修改",
    "actions": ["运行 go test"], "outcome": "failure"}],
  "truncated": false
}
```

系统提示要求返回：

```json
{
  "name": "验证工作流",
  "description": "修改 dmemd 后的验证步骤",
  "content": "## 前置条件\n- 依赖已安装\n\n## 步骤\n1. 运行 go test\n2. 执行构建检查",
  "preconditions": ["依赖已安装"],
  "steps": ["运行 go test", "执行构建检查"],
  "stop_conditions": ["测试失败"],
  "completion_checks": ["全部命令通过"],
  "counterexamples": ["不要跳过测试"]
}
```

- 没有合格 procedure 时只记录目标评估 `no_eligible_input/no_procedure_input`，不创建空
  artifact；已有 Skill 失去来源时立即 stale，随后 disabled 并保留历史。生成必须至少包含
  结构化步骤或带标题/编号/列表的指导正文；只有名字、描述或散文段落属于 `invalid_output`。
- 模型文本不修改配置、不授予权限、不触发工具执行。生成失败保持停用并沿用任务重试；
  依赖变化或失败反馈使旧版本退出默认召回并在后台重生成，不自动回滚旧版本。
- 经验只补充已报告的适用条件和反例；显式 Skill 引用或单一同主题经验才相关，同一 scope
  本身不足以成为 Skill 输入。

### 9.8 固定样例与真实模型评估

分类、摘要、冲突和 Skill 的固定标注样例位于 `tests/quality/testdata/labeled.json`。默认
`make test-quality` 使用样例中的回放输出验证协议、作用域、来源引用、截断和确定性冲突规则，
不访问真实模型。真实模型评估是显式入口：

```sh
DMEMD_EVAL_BASE_URL=http://127.0.0.1:8000/v1 \
DMEMD_EVAL_MODEL=<model-name> \
DMEMD_EVAL_API_KEY=<optional-key> \
go test -count=1 -run TestLiveModelQuality -v ./tests/quality
```

真实模型评估和容量实测分开记录在 `docs/quality.md`。未执行时不得把离线回放结果当作真实
语义质量通过；模型版本、提示词版本和 token/耗时需要随实际报告记录。
