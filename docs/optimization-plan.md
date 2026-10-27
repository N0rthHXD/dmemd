# dmemd 优化方案

> 依据：`/home/ut005426@uos/data/dmemd-eval/reports/2026-10-23/eval-2026-10-23.md`
>
> 方案版本：v1.1
>
> 本版本根据评估结论及检索策略调整要求修订：**移除 `all` 检索方式，系统统一使用 `any` 语义，不再区分检索方式。**

## 1. 评估结论与优化目标

### 1.1 当前主要问题

| 领域 | 当前结果 | 主要问题 |
| --- | ---: | --- |
| 关键词检索 | 28/49（`any`）；另有 5 条使用 `all` | 事实未提取、未映射、pending 阻断和空召回是主要损失 |
| 事实提取 | 37/153（24.2%） | 日期、专有名词、条件、流程步骤被模型改写或丢失 |
| 内容锚点 | 49.0% | 一个记忆合并多个原子事实，或正文遗漏关键限定词 |
| 有效时间 | 69.2% | 日期出现在正文中，但没有写入 `valid_from/valid_to` |
| 记忆冲突 | 严格命中 0/12 | 清晰更新长期处于 `pending`，最新事实无法正常回答 |
| 派生对象 | 17/20 找到，严格通过 0/20 | topic 碎片化、来源覆盖不足、profile 混入一次性事件 |
| 默认处理预算 | 120 秒不适配 | 长会话提取和决策耗时超过默认预算 |
| 模型协议 | 2 次协议失败 | `separate`、`change` 输出不符合服务约束 |

### 1.2 优化目标

第一阶段优先保证用户得到**可用且可解释的答案**，再提高摘要、画像和 Skill 的覆盖质量。

| 指标 | 第一阶段目标 | 第二阶段目标 |
| --- | ---: | ---: |
| 统一 any 检索非空查询 0 结果率 | < 8% | < 5% |
| 统一 any 检索严格负例违规 | ≤ 1/54 | 0/54 |
| 内容锚点全命中 | ≥ 75% | ≥ 85% |
| kind 正确率 | ≥ 85% | ≥ 92% |
| 有效时间匹配 | ≥ 82% | ≥ 90% |
| 严格事实通过率 | ≥ 55% | ≥ 70% |
| procedure 通过率 | ≥ 60% | ≥ 80% |
| 明确更新自动收敛率 | ≥ 80% | ≥ 90% |
| 派生来源引用结构正确率 | 100% | 100% |
| 可执行生命周期操作 | 18/18 | 18/18 |
| 模型协议失败率 | < 1% | < 0.5% |

## 2. 总体设计原则

1. **模型负责理解，服务负责约束。** 日期、证据、状态、事实组和生命周期状态由服务校验。
2. **一个记忆只表示一个可独立更新的原子断言。** 计划、完成、取消、改期和结果不能无条件合并。
3. **检索采用单一 any 语义。** 不再向用户暴露严格合取，也不再维护两套匹配逻辑。
4. **冲突不应阻断所有答案。** 已确认事实优先返回，未决候选通过明确状态和冲突引用展示。
5. **派生对象必须可追溯、可失效、可重建。** 每个对象都需要记录来源 memory/version 和生成版本。
6. **能由确定性规则完成的工作不调用模型。** 模型调用集中在事实理解、复杂决策和文本生成。

### 2.1 项目阶段约束

项目尚未发布，本方案按“直接修改当前实现”执行：

- 直接删除 `match` 字段、`MatchAll` 枚举和相关内部接口，不保留旧字段解析；
- 直接调整数据库结构和内部数据模型，不设计旧 schema 兼容层；
- 不编写数据迁移、历史数据回填、旧数据兼容读取或双写逻辑；
- 开发和评测使用全新数据库，测试夹具、OpenAPI、README 和示例同步更新；
- 现有历史评测数据只作为问题分析依据，不作为发布前兼容对象；
- 若实现过程中需要改变表结构，直接修改初始化 schema 和测试数据库构造逻辑。

目标流程：

```text
提交来源
  ↓
Extraction v2：提取原子事实、时间和证据
  ↓
确定性校验与有限修复
  ↓
确定性决策预处理
  ↓
仅对歧义事实调用 Decision Model
  ↓
事务性写入记忆、事实组、关系和冲突
  ↓
主题规范化与派生任务调度
  ↓
按影响范围重建 summary/profile/skill
```

## 3. P0：统一 any 检索

### 3.1 目标行为

所有关键词查询和派生对象查询都使用 `any` 语义：查询词经过统一分词和停用词过滤后，命中任一有效词即可进入候选集，再由 BM25、词项覆盖率和时间/状态过滤排序。

例如：

```text
“我对什么过敏？”
→ 过滤“我、对、什么”
→ 使用“过敏”进行 any 检索
```

查询词全部被过滤时返回空结果，不能生成无约束查询。

### 3.2 API 和内部模型调整

当前 API 直接删除检索请求中的 `match` 字段，不再允许调用方选择 `all` 或 `any`。响应中不增加
`requested_match`、`effective_match` 或 `match_relaxed`。项目尚未发布，不保留旧字段解析、兼容分支或
过渡版本；代码、OpenAPI、示例和测试一次性切换到统一 any 契约。

涉及位置：

- `internal/retrieval/types.go`
- `internal/retrieval/query.go`
- `internal/retrieval/normalize.go`
- `internal/retrieval/service.go`
- `internal/retrieval/recall.go`
- `internal/transport/http/dto.go`
- `internal/transport/http/search.go`
- `api/openapi.yaml`
- `README.md`

实现调整：

1. 移除 `MatchAll`、严格 `AND` 编译路径和相关分支；
2. `Compile` 固定生成 `OR` 表达式，或重命名为 `CompileAny`；
3. `selectQueryTerms` 成为所有文本查询的统一预处理；
4. 原子层、派生层、相关事实选择和冲突发现使用同一组过滤后词项；
5. hybrid 检索中的关键词分支也固定使用 any；
6. 保留 BM25、覆盖率重排、scope、state、kind、as_of 和 conflict 过滤能力。

### 3.3 检索验收

- 所有请求不再能够触发严格合取；
- “女儿什么时候上小学？”等自然语言查询不因疑问词缺失而返回 0；
- 关键词列表和自然语言问题没有两套匹配行为；
- 空查询、纯标点和停用词查询仍安全返回空结果；
- 否定词（如“不、没、无、not、never”）不会被停用词过滤误删；
- scope 隔离、as_of、archive、pending 和负例约束不回归；
- 评测数据中的所有查询统一按 any 执行，不再统计 `all` 通过率。

## 4. P0：事实提取保真度和原子性

### 4.1 Extraction Prompt v2

调整 `internal/model/protocol.go` 的提取协议：

1. 一个 candidate 只表示一个独立断言；
2. 保留日期、专有名词、条件、否定、数量和项目作用域；
3. 计划、已完成、取消、改期分别建模；
4. 原文出现明确日期时必须写入 `valid_from` 或 `valid_to`，并保留正文锚点；
5. 月份、节日或模糊时间不能伪造精确日期；
6. procedure 必须保留步骤顺序、前置条件、完成条件和失败分支；
7. “不要记”“别记录”等明确排除内容不得生成候选；
8. 优先使用来源语言，中文来源不得无故改写为泛化英文标签。

推荐候选结构：

```json
{
  "candidate_id": "c1",
  "kind": "event",
  "content": "2026年10月8日在广州有广告片拍摄",
  "valid_from": "2026-10-08T00:00:00Z",
  "valid_from_basis": "explicit",
  "phase": "scheduled",
  "durability": "episodic",
  "fact_group": {
    "subject": "用户",
    "predicate": "广告片拍摄",
    "scope": "广州",
    "cardinality": "multiple"
  },
  "evidence": [
    {"message_id": "m1", "quote": "2026年10月8日在广州有广告片拍摄"}
  ]
}
```

`phase`、`durability` 可以先作为协议字段和派生过滤字段；是否进入持久化模型由实现阶段决定，但不能丢失其语义。

### 4.2 确定性校验

新增提取结果校验器，至少检查：

- candidate 内容非空；
- kind、时间 basis、phase 和 durability 属于合法枚举；
- evidence quote 能在当前 source 中定位；
- `valid_from <= valid_to`；
- `explicit` 时间边界必须有日期证据；
- 否定内容没有被改写为肯定；
- procedure 至少包含动作或步骤；
- 明显的多个并列动作不得无提示地合并为一个事实；
- 过滤掉显式禁止记忆内容。

处理策略：

```text
Extraction
  ↓
Validator
  ├─ 全部通过 → 继续
  ├─ 少量字段错误 → 一次局部 repair
  └─ 大量结构错误 → 一次带错误说明的重试
```

单个候选错误不应导致整个 source 重新处理；repair 失败后只隔离问题候选，并保留可安全入库的候选。

### 4.3 原子性规则

一个 memory 应满足：

```text
一个主体 + 一个谓词 + 一个独立值/状态 + 一个时间范围
```

以下内容必须拆分：

- 项目计划、当前进度和完成结果；
- 偏好、偏好原因和使用条件；
- 事件发生、事件取消和后续安排；
- 流程目标、流程步骤和执行结果。

保留 `supersedes` 等关系表达事实演化，不通过把多个状态拼到一条正文中表达更新。

## 5. P0：冲突收敛和可用召回

### 5.1 自动收敛规则

以下条件同时满足时自动裁决：

- 同一 fact group；
- 同一主体和谓词；
- 新事实有明确更正、改期或替换证据；
- 新旧时间区间不重叠，或新事实明确晚于旧事实；
- 属性为单值事实；
- 当前 source 有有效证据。

处理结果：

```text
旧事实 → rejected 或设置 valid_to
新事实 → active + accepted
写入 supersedes 关系
冲突 → resolved
写入 resolved 审计事件
```

典型场景包括换狗粮、血糖更正、租房要求变更和 CKA 改期。

以下情况保持 pending：

- 时间区间重叠且值不同；
- 没有明确更正证据；
- 主体、scope 或 fact group 不明确；
- 单值/多值属性无法确定。

### 5.2 召回策略

不再新增多种检索方式，但可以保留断言状态的展示策略。推荐普通助手召回采用：

```text
accepted 优先 + pending 候选附带冲突说明
```

pending 不能伪装成已确认事实，但也不应因为冲突而让用户完全看不到最新候选。返回结果必须携带：

- `assertion_status`；
- `conflict_refs`；
- `fact_conflict` warning；
- accepted 和 pending 的明确排序。

### 5.3 冲突成员一致性

冲突写入时必须保证：

- 新记忆及所有相关旧记忆进入同一冲突集合；
- 成员 ID 唯一；
- 保存检测版本和当前版本；
- `resolve_conflict` 能使用完整成员集合定位 open 冲突；
- resolved 冲突保留完整成员和不可变审计事件。

涉及：

- `internal/ingest/merge.go`
- `internal/governance/conflict.go`
- `internal/memory/conflicts.go`
- `internal/store/sqlite/conflicts.go`
- `internal/retrieval/service.go`

## 6. P1：主题分类、分层聚合和自进化

### 6.1 受控 topic 词表

建立中英文别名和层级 taxonomy，示例：

```text
containers
├── kubernetes
├── docker
├── oom
└── deployment

work
├── studio
├── course
├── training
└── project

travel
├── hong-kong
├── wuhan
├── guangzhou
└── itinerary
```

保存 canonical topic，同时保留原始标签用于审计：

```json
{
  "topics": ["containers", "kubernetes"],
  "raw_topics": ["kubernetes", "节点内存耗尽"]
}
```

子主题自动继承父主题；父主题聚合包含所有子主题和别名主题。

### 6.2 聚合和质量门禁

#### Profile

只允许稳定偏好、稳定事实、长期程序和长期约束；排除一次性事件、临时进度、短期疼痛和单次行程。

#### Topic summary

生成前检查输入是否完整；超出输入上限必须设置 `truncated=true`；正文必须覆盖来源原子事实，或明确声明只覆盖子集。

#### Experience case

统一使用“成功/失败/未知/部分成功”等状态，并明确“这是应用报告，不代表独立验证”。

#### Skill

优先从 procedure 原子生成；procedure 缺失时生成可诊断状态，例如：

```json
{"state": "disabled", "reason_code": "no_procedure_input"}
```

不能将上游 procedure 未提取、topic 不匹配和模型生成失败都表现为静默缺失。

### 6.3 新鲜度和来源审计

每个 artifact revision 必须记录：

- memory ID/version；
- run ID；
- taxonomy version；
- generator version；
- protocol version；
- truncated 状态；
- content hash。

当 memory 的正文、kind、时间、断言状态、fact group、canonical topic 或生命周期状态变化时，相关 artifact 立即标记 stale 并重建。

新增 `artifact-integrity-check`，检查来源版本、删除引用、截断标记、序列化内容和数据库依赖是否一致。

## 7. P1：forget 语义闭包

永久遗忘按以下层级展开：

### Level 1：来源闭包

清除所有 evidence 指向目标 source 的 memory，不能只检查 memory 当前的 `origin_source_id`。

### Level 2：事实重复闭包

仅在以下条件同时满足时清除相关副本：

- 同一 scope；
- 同一 fact group；
- 正文规范化后相同或高度相似；
- 时间范围相同或重叠；
- 不是不同主体的独立事实。

### Level 3：派生闭包

失效或删除所有依赖目标 memory 的 summary、profile、experience、Skill、冲突和治理任务。

如果发现相关但未删除的记忆，在响应中明确返回：

```json
{
  "warnings": ["related_memories_retained"],
  "retained_related_memory_ids": ["memory-2"]
}
```

## 8. P2：模型调用和运行稳定性

### 8.1 配置化预算

将 `internal/ingest/processor.go` 中的固定 `processBudget` 改为配置项：

```json
{
  "worker": {
    "process_budget": "600s",
    "extraction_timeout": "180s",
    "decision_timeout": "300s",
    "max_attempts": 3
  }
}
```

处理预算和单次模型 timeout 分开配置，默认值应与实际模型延迟匹配。任务进入 waiting 时返回稳定的阶段和原因码。

### 8.2 协议错误局部修复

- `separate` 带非法 `target_memory_ids`：可以删除非法字段后继续；
- `change` 有多个目标：不能猜目标，使用一次带错误说明的定向 repair；
- repair 仍失败才进入 protocol failure；
- 记录 stage、batch size、context size、模型耗时和协议错误类型。

### 8.3 治理限流

- ingest 优先级高于 governance；
- 相同 scope/topic 的聚合任务合并；
- 新原子版本写入时取消旧的未执行聚合任务；
- ingestion 空闲后再执行批量 aggregate；
- summary、profile、Skill 使用独立调用统计。

## 9. Embedding 和 Hybrid 检索

当前评估未启用 Embedding，不能据此判断向量召回、RRF 和 `min_similarity` 的效果。

本方案不在 P0 阶段调整 hybrid 排序。Embedding 可用后单独比较：

- 统一 any 的关键词召回；
- vector 召回；
- RRF 融合；
- semantic unavailable 时的关键词 fallback。

无 Embedding 时继续返回 `semantic_unavailable`，但补充记录 endpoint 错误、timeout 和 fallback 命中率。

## 10. 评测调整

### 10.1 检索评测

- 移除 `all` 维度和 `match=all` 通过率；
- 全部 keyword 查询统一按 any 执行；
- 区分“事实未提取/未映射”和“检索未命中”；
- 保留 Recall@k、MRR、scope、as_of、负例和空结果指标；
- 不因严格合取造成的 0 结果再计入失败。

### 10.2 提取评测

增加日期、专有名词、条件、否定、procedure、原子性和 evidence quote 指标。

### 10.3 冲突评测

将样本拆分为：真实冲突、明确不冲突、gold 缺失、无法判断。只有人工确认的真实冲突进入自动裁决门禁。

### 10.4 派生评测

分别评估 artifact 是否生成、来源是否完整、内容是否覆盖、profile 是否混入事件、更新后是否新鲜、遗忘后是否退出召回。

每个真实模型场景至少重复 3 次，记录平均值、方差和错误类型，避免单次模型随机输出决定结论。
