# 远端模型协议

[文档索引](../README.md) · [系统架构](../系统%20AI%20记忆服务架构设计.md)

本文固定当前 `internal/model` 适配器的边界：Extraction 只形成 L0 草稿，Governance 只判断
有限关系，Aggregation 只生成派生内容。旧 Candidate/Decision、模型 Evidence 和时间/事实组
协议已移除，不提供兼容解析；未知字段忽略，但不会转成领域字段或数据库动作。

## 1. 配置与公共传输

| 配置 | 映射 |
| --- | --- |
| `model.base_url` | Chat Completions API 根地址，实际请求 `<base_url>/chat/completions` |
| `model.name`、`model.timeout` | 基础模型与超时，基础超时默认 60 秒 |
| `model.api_key` | 可选远端 `Authorization: Bearer ...`，仅来自本机 `0600` 配置文件 |
| `model.extraction` | 提取任务的 `name/timeout/reasoning_effort` |
| `model.governance` | 关系判断及分类适配器的任务配置 |
| `model.aggregation` | L1/L2/L3 聚合、经验案例和 Skill 的任务配置 |

三个任务配置共享基础端点/凭据；任务名缺省继承 `model.name`。运行组装的任务超时优先采用
显式 `model.<stage>.timeout`，否则采用对应 `worker.<stage>_timeout`（默认 180s/60s/60s）。
同时受 Worker 阶段 context 约束；提取尝试还有代码内固定 600 秒总预算，不再提供
`worker.process_budget` 或 decision 配置。`reasoning_effort` 可为 none/minimal/low/medium/high，
提供方不支持时留空；更换快模型不能代替协议和证据修复。

公共请求体使用 `model`、`temperature:0`、`response_format:{"type":"json_object"}`、可选
`reasoning_effort` 和 system/user 两条 messages。user content 为下述任务对象的 JSON 字符串。
`json_object` 不保证字段类型，仍需代码解析。服务不发送本地 API Token、数据库路径、任务 ID、
持久化 UUID 或完整记忆库；来源正文中的自然文本不保证不含用户自行写入的标识。

成功响应读取 `choices[0].message.content`；可选 `usage.prompt_tokens/completion_tokens` 累计
调用指标。缺少 usage 时不推测 token。原始响应、推理过程和远端自由文本错误不持久化、不落日志。

## 2. Extraction：`extraction-v6`

`ExtractionProvider.Extract` 只接收当前 Source 的有界片段，不读取相关 Memory。

```json
{
  "current_source": {
    "scope": "project:dmemd",
    "segments": [
      {"id":"seg-0001","role":"user","occurred_at":"2026-10-20T02:00:00Z","text":"以后请默认使用中文回答。"}
    ],
    "context_segments": []
  }
}
```

模型输出：

```json
{"items":[{"span_ids":["seg-0001"],"kind":"preference"}]}
```

| 字段 | 规则 |
| --- | --- |
| `items` | 必须是数组；合法空提取为 `{"items":[]}` |
| `span_ids` | 必须选择当前窗口 segments 的 ID；不能选只读 context；trim、去重 |
| `kind` | 可选 fact/preference/event/procedure，缺失由代码推断并以 fact 兜底 |
| `content` | 可选，仅在原文无法独立表达时提供简短 normalized 正文，不添加事实 |

- 默认省略 content，代码用选中原文构造 extractive Observation。
- 不解析 title/topics/durability/phase/time/fact_group/procedure/qualifiers/action/evidence，
  不采用模型 quote/offset、持久化 ID 或版本。
- segment ID 由代码稳定生成，role/occurred_at 为来源上下文，不代表身份或事实有效时间。
  field、offset 和客户端 message ID 留在本地，不发送给提取模型。
- 默认 12000 rune 窗口；前一窗口尾部只作为只读上下文。分块序号和任务元数据不发给模型。
  某窗口失败但其他窗口成功时可隔离失败窗口，并以固定 warning/计数说明部分处理。
- 代码只从当前 Source span 复制 quote/location 并生成 Evidence。绑定失败但有 normalized
  正文时可保存 pending Observation，不自动治理；没有可保存正文则不入库，不模糊恢复 span。
- 同 Source 相同 span 集合/正文去重；跨 Source 去重属于治理，不属于本协议。

### 2.1 当前解析边界

当前解析器允许外层一段 JSON Markdown 围栏，拒绝围栏外说明、多余 JSON、缺失/null/非数组
items。单项类型错误或没有非空 span_ids 时直接跳过，不执行旧版定向 repair。
这意味着非空 items 全部被解析器跳过时，后续可能呈现 empty；不能将 quality_outcome 当作
原文覆盖率或“模型确实没有发现信息”的证明。下游构造阶段可报告 pending/invalid/duplicate。

## 3. Governance：最小关系协议

先运行确定性规则；无候选、精确重复、重放和受保护对象等不需要语义调用。仅歧义时调用
`GovernanceProvider.Relate`，输入是任务临时引用而非数据库 ID：

```json
{
  "new_observation":{"ref":"new","type":"observation","kind":"preference","scope_key":"project:dmemd","content":"以后使用中文。"},
  "existing":[{"ref":"existing_1","type":"memory","kind":"preference","scope_key":"project:dmemd","content":"回答使用英文。"}]
}
```

可带有界 evidence_hint 和 known_relations；不发送完整 Source、版本号或 Evidence 对象。

```json
{"relations":[{"from_ref":"new","to_ref":"existing_1","kind":"supersedes"}]}
```

关系枚举为 duplicate/supports/enriches/supersedes/contradicts/separate/independent/unresolved。
前五种必须有目标引用；后三种可无目标。信息不足可返回：

```json
{"relations":[{"from_ref":"new","to_ref":"","kind":"unresolved"}]}
```

不返回正文改写、时间、证据或 add/change/correct 等数据库动作。服务校验枚举、临时引用、
对象状态和任务 revision，创建 typed relation 后由代码物化 Memory。无可信目标的破坏性关系
不能降级为确定新增。contradicts/unresolved 保持 pending；业务不确定性不是网络重试错误。
supersedes 当前只记录替代关系并创建新视图，不隐含时间线关闭或旧对象自动删除。

## 4. Aggregation：逐层正文协议

`AggregationProvider.Aggregate` 只接收服务选择的下一层正文和 target 的语义范围：

```json
{
  "target":{"layer":"l2","kind":"topic_summary","scope_key":"project:dmemd","topic":"testing"},
  "inputs":[{"content":"修改后运行单元测试。"},{"content":"构建用于检查编译问题。"}],
  "input_count":2,
  "truncated":false
}
```

```json
{"content":"修改后先运行单元测试，再执行构建检查。"}
```

只接受非空 content，标题、真实输入 ID/版本、依赖、target key、hash、状态、生成器/协议版本和
truncated 都由代码生成。模型不返回 citations、omitted_inputs 或“完整覆盖”认证。
L1 读取本 scope/bucket 的 Observation，L2 读 active L1，L3 读 active L2。输入有界且不完整性
向上传递，不能重新读取全库 L0 绕过下层。提交时重查输入及任务 revision，迟到结果丢弃。

## 5. L4 与分类适配器

### 5.1 经验案例

`ComposeExperience` 使用 aggregation 任务配置，只发送 run 的 goal/conditions/actions/outcome、
scope、有界 `inputs[{type,content}]` 和 truncated；不发送 run_id/source_id/skill_id 或版本。
输出同样只有 `{"content":"案例正文"}`。真实结果、声明、引用和元数据由代码附加；结果始终是
应用报告，模型不能升级为验证成功，不能从“未报错”推断成功。

### 5.2 Skill

`ComposeSkill` 接收 scope/topic、稳定程序及相关低层派生/报告正文、truncated，不接收真实 ID。
当前 Skill 保留专用指导文本协议，**并非**所有 L4 输出都只有 content：

```json
{
  "name":"修改后验证",
  "description":"修改后的验证步骤",
  "content":"## 步骤\n1. 运行单元测试\n2. 执行构建检查",
  "steps":["运行单元测试","执行构建检查"],
  "stop_conditions":["测试失败"]
}
```

可选 preconditions/completion_checks/counterexamples。必须有可用步骤或指导正文，辅助字段
错误不应丢掉可用内容；最终生成仍检查 procedure 资格与指导结构。模型不能输出依赖/ID/版本、
授权或执行认证。没有合格输入只记录目标评估，不创建空 Skill。

### 5.3 分类

`SuggestTopics` 适配器可接收选定快照的 title/content/scope_key，返回 `{"topics":["go","sqlite"]}`。
规范 Memory 不再内嵌 topics。当前 `ClassifyHandler` 仅完成任务，不调用该适配器或持久化分类
结果；独立分类流水线是待完善能力，不能声称所有观察已自动打标。

## 6. 错误、重试与观测

| 情况 | 当前处理 |
| --- | --- |
| 连接/DNS/超时、408/425、429、5xx | 暂时失败，Worker waiting 并退避 |
| 401/403 | credentials 等待，重启后重评 |
| 其他非成功 4xx | configuration 等待，重启后重评 |
| 200 但空 choices、空 content 或整体非法 JSON | 协议失败，默认连续 3 次后 failed |
| 合法空 items、合法 unresolved | 业务成功，不作为远端故障 |

Provider 不内部重试、不维护任务状态。Worker 默认 5s/30s/2m 后最长 30m 延期，协议隔离及
配置等待在重启时获得重评机会。每次响应读取上限为 8 MiB；仅保存安全类别、HTTP 状态和固定
错误码，不保存远端错误正文。日志/指标按 extraction/relation/aggregation/experience/skill 等
操作分别统计耗时、调用数与 token。已发出的请求不能撤回，本地取消不承诺远端擦除。

## 7. Embedding 协议

独立使用 `embedding.*` 端点/凭据，`POST <base_url>/embeddings`，发送 model、input 字符串
数组及 `encoding_format:"float"`。只用于当前 Memory 文档与 hybrid 查询，不为 L0/L1–L4
分别创建向量队列。

- data 数量/index 必须匹配输入；维数匹配配置，分量有限且范数非零。L2 归一化后以
  little-endian float32 存储。
- 查询不自动重试，固定 500 ms 子预算；失败返回关键词结果和稳定语义降级 warning。
- 后台任务最多三次，指数退避；429 尊重有界 Retry-After。凭据、数量/维数和协议错误用固定
  错误码表示；配置/profile/内容 hash 不匹配的向量不参与当前召回。
- 不持久化查询、完整响应或凭据；不静默截断输入。

## 8. 验证边界

默认模型测试使用本地 `httptest`/FakeModel，不访问外网。覆盖最小字段、ID/版本不外发、未知
旧字段忽略、span 选择、临时引用、正文输出、超时/错误映射和使用量统计。真实模型语义、提取
覆盖率、治理准确率、聚合忠实性和 token 成本需单独评估，不能由协议通过推导。
旧 `tests/quality` 目前全部带 legacy 标签，不构成 v2 实际评估；入口状态见[测试文档](../testing.md)。
