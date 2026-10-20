# 远端模型协议（首个适配器）

本文件固定 T1.3 的首个 `ExtractionProvider` 协议。P5 的 T5.2/T5.3 按此实现，不需要再猜测
请求字段、候选结构或错误语义。协议采用 OpenAI-compatible Chat Completions，但只使用下文
列出的字段；Provider 不接收完整记忆库、无关来源或本地 API Token。

## 1. 配置映射

| 配置项 | 映射 |
| --- | --- |
| `model.base_url` | API 根地址，例如 `https://model.example.invalid/v1`；实际地址为 `<base_url>/chat/completions` |
| `model.name` | 请求体 `model` 字段 |
| `model.api_key` | 请求头 `Authorization: Bearer <api_key>`；只从本机受限配置文件读取，不从 HTTP 请求头读取 |
| `model.timeout` | 单次调用超时，默认 60 秒；由 `context.WithTimeout` 实现并可被关闭/取消传播 |
| 本地 API Token | 与模型凭据无关，绝不转发 |

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
  "messages": [
    {
      "role": "system",
      "content": "Extract durable memories from the current source. Return only a JSON object with a candidates array. Each candidate must contain content (the memory text). Optional fields are kind (fact, preference, event, or procedure), title (short), and observed_at (RFC3339). If the source corrects a memory in related_memories, set action to \"update\" and target_memory_id to that memory id; otherwise omit action. Never invent a target_memory_id. Return an empty candidates array when nothing durable should be stored. Prefer Chinese memory text when the source is Chinese."
    },
    {
      "role": "user",
      "content": "{\"current_source\":{\"id\":\"source-uuid\",\"app_id\":\"desktop-assistant\",\"external_id\":\"conversation-42-batch-7\",\"received_at\":\"2026-10-20T02:00:01Z\",\"content\":{\"messages\":[{\"role\":\"user\",\"content\":\"以后请默认使用中文回答。\",\"occurred_at\":\"2026-10-20T02:00:00Z\"}]}},\"related_memories\":[{\"id\":\"memory-uuid\",\"kind\":\"preference\",\"title\":\"回答语言\",\"content\":\"用户希望使用中文回答。\",\"version\":2,\"observed_at\":\"2026-10-20T02:00:00Z\",\"updated_at\":\"2026-10-20T02:00:01Z\"}]}"
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
    "content": {"messages": [{"role": "user", "content": "以后请默认使用中文回答。", "occurred_at": "2026-10-20T02:00:00Z"}]}
  },
  "related_memories": [
    {
      "id": "memory-uuid",
      "kind": "preference",
      "title": "回答语言",
      "content": "用户希望使用中文回答。",
      "version": 2,
      "observed_at": "2026-10-20T02:00:00Z",
      "updated_at": "2026-10-20T02:00:01Z"
    }
  ]
}
```

约束：

- `current_source.content` 原样保留客户端提交的 JSON 形态（字符串/对象/数组均可）。
- `related_memories` 只包含服务按关键词选出的 `active` 记忆，不含无关来源、全库、归档或
  删除记录。T4.8 固定选择规则：对当前输入使用与搜索相同的 NFC/大小写/分词规范化，去重后取
  前 32 个查询组按 `any` 组合；最多返回 10 条，排序与公共搜索一致（加权 BM25、`updated_at`
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
  ]
}
```

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

默认提示词只要求 `content`，并允许省略有默认值的字段。除非需要更正
`related_memories` 中的记忆，否则不需要输出 `action`；`reason` 和 `evidence` 不由默认
提示词请求，但解析器仍接受模型主动返回的这些字段。

### 3.1 候选字段

| 字段 | 必填 | 语义与默认值 |
| --- | --- | --- |
| `action` | 否 | `add`、`update`、`merge_evidence`、`skip`；缺失/未知按 `add` |
| `target_memory_id` | 条件 | `update`/`merge_evidence` 必须指向 `related_memories` 中的 ID；缺失或不在上下文中时降级为 `add` |
| `kind` | 否 | `fact`、`preference`、`event`、`procedure`；缺失/未知按 `fact` |
| `title` | 否 | 简短标题；缺失或空时由服务取正文前 40 个 Unicode 字符（去首尾空白） |
| `content` | 条件 | 记忆正文；`add`/`update`/`merge_evidence` 缺失或空时该候选跳过；`skip` 可为空 |
| `observed_at` | 否 | 事实/事件发生时间；缺失、无法解析或为零值时回退到来源接收时间，统一转 UTC |
| `reason` | 否 | 操作原因，可空；不写入原始模型响应 |
| `evidence` | 否 | 证据数组；缺失表示只生成记忆，不补证据 |
| `evidence[].message_id` | 否 | 来源内的消息标识 |
| `evidence[].quote` | 否 | 原文片段；缺失时仅保留来源关联 |
| `evidence[].field` | 否 | 点分路径，例如 `messages.0.content` |
| `evidence[].start`/`end` | 否 | 字段文本中的 rune 偏移；缺失或越界时忽略定位，不影响整批 |

`action=skip` 表示模型判断无需记忆；服务应记录处理结果但不生成记忆。`add` 表示新增；
`update` 表示同一事实的明确更正；`merge_evidence` 表示内容不变、只补证据。服务最终仍按
当前持久化状态重新判断，不能仅凭模型声明覆盖归档/删除记录。

## 4. 容错与部分可解析

### 4.1 部分可解析示例

模型返回 `200`，`message.content` 为：

```json
{
  "candidates": [
    {
      "action": "add",
      "kind": "preference",
      "title": "回答语言",
      "content": "用户希望默认使用中文回答。",
      "observed_at": "2026-10-20T02:00:00Z"
    },
    {
      "action": "update",
      "kind": "not-a-kind",
      "content": ""
    },
    {
      "action": "merge_evidence",
      "target_memory_id": "11111111-1111-4111-8111-111111111111",
      "kind": "preference",
      "title": "回答语言",
      "content": "用户希望默认使用中文回答。",
      "evidence": [{"quote": "以后请默认使用中文回答。"}]
    }
  ]
}
```

处理结果：第一个候选新增；第二个候选因正文为空跳过，未知 `kind` 不影响其余候选；第三个
候选补证到上下文中的目标记忆。整次调用仍按成功处理。

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
- 顶层缺少 `candidates` 或为 `null`：视为成功的空候选。
- 单个候选不是对象、`content` 缺失/类型错误/为空（非 skip）：跳过该候选，继续处理其余
  候选；部分可用不导致整批失败。`observed_at` 缺失、类型错误或无法解析时按 3.1 回退到
  来源接收时间，不因此丢弃可用候选。
- 证据项字段类型错误：忽略该证据项，保留其余候选内容。
- 顶层 JSON 无法解析、`choices` 缺失/为空、`message.content` 为空或不是 JSON 对象：
  整个任务调用失败，记录脱敏错误摘要，不保存该次候选。
- 不把模型返回的整段原始文本写入任务错误、日志或数据库；只保留错误类别、HTTP 状态和
  截断后的安全描述。

## 5. HTTP 错误与超时

| 情况 | Provider 返回 | Worker 处理 |
| --- | --- | --- |
| 连接失败、DNS 失败、超时（默认 60s） | `ProviderError{Retryable:true}` | 按持久化尝试计数重试，最多三次 |
| `429 Too Many Requests` | 可重试错误 | 重试；记录脱敏状态码 |
| `500`/`502`/`503`/`504` | 可重试错误 | 重试 |
| `400`/`401`/`403`/`404` | 不可重试错误 | 直接标记任务失败，不继续调用 |
| `200` 但整体不可解析 | 可重试错误 | 最多三次后失败 |
| `200` 且零候选 | 成功 | 任务 `succeeded`，`memory_ids=[]` |
| 部分候选可解析 | 成功 | 处理可用候选，失败的候选跳过 |

重试次数、1 秒/5 秒退避、`queued → running → succeeded/failed/cancelled` 状态机和崩溃恢复
由 ingest Worker 负责；Provider 内部不自行重试，也不创建第二套状态。已发出的远端请求
无法撤回；来源清除后的取消只保证本地不提交结果，不承诺远端擦除。

## 6. 假模型与测试要求

P5 的契约测试使用本地 `httptest` 假服务，覆盖：

- 成功单候选、多候选、零候选；
- 部分候选缺失辅助字段（默认值）；
- 整体不可解析、部分可解析；
- 超时、连接失败、429、5xx、4xx；
- 阻塞响应开始后来源被清除的取消竞态；
- 校验请求只包含当前来源和相关 active 记忆，且不包含真实凭据或完整记忆库。

测试不得访问外网或使用真实模型凭据；真实连通性检查只能作为显式可选操作，不作为默认测试
前提。

## 7. P5 处理编排与合并边界

P5 的 `ingest.Processor` 在模型调用前读取当前来源并选择相关 active 记忆，模型调用和
候选整理全部在写事务外完成。结果提交时重新读取任务、来源和记忆的实际持久化状态：

- `update`/`merge_evidence` 的 `target_memory_id` 不在本次相关上下文中时按 `add` 处理；
  服务从不使用模型返回的证据 ID、来源 ID 或版本号。
- 目标仍为 active 且 `update` 的候选 `observed_at` 早于当前值时不覆盖当前事实，改用新增；
  正文未变化时把 `update` 视为补证，保留现有证据并合并新证据。
- 目标在候选计算后被归档或逻辑删除时不自动恢复/更新，也不为同一目标新建 active 记忆；
  目标被永久清除时，`update` 按新增处理，`merge_evidence` 跳过。
- `add` 与现有同 kind、规范化正文相同的 active 记忆合并；已有完整证据时跳过，新来源证据
  生成 `add_evidence` 版本。内容更正只使用本次候选证据，不继承旧结论证据。
- 多候选、版本、证据、索引和任务 `succeeded`/`memory_ids` 在同一短事务提交；事务先检查
  cancelled，已取消任务不写任何候选。零候选同样提交成功空结果。
- 单 Worker 首次尝试即计入三次预算；暂时失败按 1 秒、5 秒退避，不可重试的 4xx 立即失败。
  启动恢复把 running 重排 queued 并保留尝试次数，已耗尽预算的 queued 直接 failed；成功和
  取消任务不重跑。错误摘要只保留类别、HTTP 状态或截断的安全文本。
- 已发出的远端请求不能撤回。来源清除只保证本地取消后不提交结果，不宣称远端副本被擦除。
