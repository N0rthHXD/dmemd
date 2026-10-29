export type Tone =
  | 'success'
  | 'processing'
  | 'warning'
  | 'error'
  | 'default'
  | 'purple'
  | 'cyan'
  | 'blue';

export interface CodeDefinition {
  label: string;
  tone?: Tone;
  description?: string;
}

const define = (entries: Record<string, CodeDefinition>) => entries;

export const memoryKindLabels = define({
  fact: { label: '事实', tone: 'blue' },
  preference: { label: '偏好', tone: 'purple' },
  event: { label: '事件', tone: 'cyan' },
  procedure: { label: '流程', tone: 'processing' },
});

export const memoryStateLabels = define({
  active: { label: '使用中', tone: 'success' },
  archived: { label: '已归档', tone: 'warning' },
  deleted: { label: '已删除', tone: 'error' },
});

export const assertionLabels = define({
  accepted: {
    label: '已采纳',
    tone: 'success',
    description: '服务已采纳，但不代表真实性经过外部验证。',
  },
  pending: { label: '待确认', tone: 'warning' },
  rejected: { label: '已否定', tone: 'error' },
});

export const conflictStatusLabels = define({
  open: { label: '待核对', tone: 'warning' },
  resolved: { label: '已解决', tone: 'success' },
});

export const conflictReasonLabels = define({
  contradictory_value: { label: '内容说法不一致' },
  uncertain_time: { label: '适用时间不明确' },
  ambiguous_scope: { label: '适用情况不明确' },
  complex_timeline: { label: '需要核对时间先后' },
});

export const conflictActionLabels = define({
  choose: { label: '采用其中一条', tone: 'blue' },
  reject_all: { label: '全部不采用', tone: 'error' },
  timeline: { label: '按不同时间保留', tone: 'processing' },
  separate_scope: { label: '按适用情况重新分组', tone: 'purple' },
});

export const jobStatusLabels = define({
  queued: { label: '排队中', tone: 'default' },
  running: { label: '处理中', tone: 'processing' },
  waiting: { label: '等待处理', tone: 'warning' },
  succeeded: { label: '处理完成', tone: 'success' },
  failed: { label: '失败', tone: 'error' },
  cancelled: { label: '已取消', tone: 'default' },
});

export const waitReasonLabels = define({
  transient: { label: '暂时性问题', tone: 'warning' },
  configuration: { label: '配置问题', tone: 'error' },
  protocol: { label: '协议问题', tone: 'error' },
});

export const jobStageLabels = define({
  received: { label: '已接收', tone: 'default' },
  extracting: { label: '提取中', tone: 'processing' },
  observations_saved: { label: '原始观察已保存', tone: 'success' },
  governance_pending: { label: '等待治理', tone: 'warning' },
  memory_materialized: { label: '当前记忆已形成', tone: 'success' },
  aggregation_pending: { label: '等待聚合', tone: 'warning' },
  completed: { label: '已完成', tone: 'success' },
  failed: { label: '失败', tone: 'error' },
});

export const jobCategoryLabels = define({
  extraction: { label: '提取', tone: 'blue' },
  governance: { label: '治理', tone: 'purple' },
  aggregation: { label: '聚合', tone: 'cyan' },
});

export const observationStatusLabels = define({
  observed: { label: '已观察', tone: 'success' },
  pending: { label: '待处理', tone: 'warning' },
  retracted: { label: '已撤回', tone: 'default' },
  deleted: { label: '已删除', tone: 'error' },
});

export const observationContentModeLabels = define({
  extractive: { label: '原文摘取' },
  normalized: { label: '规范化表达' },
});

export const relationKindLabels = define({
  duplicate: { label: '重复' },
  supports: { label: '支持' },
  enriches: { label: '补充' },
  supersedes: { label: '替代' },
  contradicts: { label: '冲突' },
  separate: { label: '相互独立' },
  independent: { label: '独立新增' },
  unresolved: { label: '暂无法判断' },
});

export const artifactLayerLabels = define({
  l1: { label: 'L1 局部聚合', tone: 'blue' },
  l2: { label: 'L2 领域聚合', tone: 'purple' },
  l3: { label: 'L3 全局聚合', tone: 'cyan' },
  l4: { label: 'L4 指导', tone: 'success' },
});

export const qualityOutcomeLabels = define({
  empty: {
    label: '没有需要保存的内容',
    tone: 'default',
    description: '这次没有提取到需要保存的信息，不代表提交的所有内容都已保存。',
  },
  complete: {
    label: '内容已处理',
    tone: 'success',
    description: '本次选中的内容已处理；部分可能只是补充来源，不一定新增记忆，也不代表真实性已验证。',
  },
  partial: {
    label: '部分内容待核对',
    tone: 'warning',
    description: '部分内容暂未采纳，可在处理结果和提示中了解原因。',
  },
  unusable: { label: '不可用', tone: 'error' },
});

export const artifactKindLabels = define({
  local_summary: { label: '局部聚合', tone: 'blue' },
  topic_summary: { label: '主题摘要', tone: 'blue' },
  scope_summary: { label: '范围概览', tone: 'cyan' },
  profile: { label: '偏好与习惯', tone: 'purple' },
  experience_case: { label: '经验案例', tone: 'processing' },
  skill: { label: '操作指南', tone: 'success' },
  guidance: { label: '指导', tone: 'success' },
});

export const artifactStateLabels = define({
  active: { label: '可用', tone: 'success' },
  stale: { label: '需要更新', tone: 'warning', description: '依据已变化，目前不用于默认搜索；保留旧内容供追溯。' },
  disabled: { label: '已停用', tone: 'default' },
});

export const targetStatusLabels = define({
  queued: { label: '排队中', tone: 'default' },
  running: { label: '整理中', tone: 'processing' },
  waiting: { label: '等待整理', tone: 'warning' },
  active: { label: '可用', tone: 'success' },
  stale: { label: '需要更新', tone: 'warning' },
  disabled: { label: '已停用', tone: 'default' },
  no_eligible_input: { label: '暂无适合整理的内容', tone: 'default' },
  model_unavailable: { label: '模型不可用', tone: 'error' },
  generation_failed: { label: '生成失败', tone: 'error' },
  invalid_output: { label: '整理结果不符合要求', tone: 'error' },
});

export const revisionOperationLabels = define({
  create: { label: '创建' },
  update: { label: '更新' },
  add_evidence: { label: '补充证据' },
  archive: { label: '归档' },
  activate: { label: '激活' },
  delete: { label: '逻辑删除' },
  restore: { label: '历史恢复' },
  correct: { label: '更正' },
  change: { label: '变化' },
  resolve: { label: '冲突裁决' },
  classify: { label: '分类' },
});

export const experienceOutcomeLabels = define({
  success: { label: '成功（应用报告）', tone: 'success' },
  failure: { label: '失败（应用报告）', tone: 'error' },
  unknown: { label: '未知', tone: 'default' },
  partial_success: { label: '部分成功（应用报告）', tone: 'warning' },
});

export const experienceStateLabels = define({
  recorded: { label: '已记录', tone: 'success' },
  revoked: { label: '已撤销', tone: 'default' },
});

export const embeddingReasonLabels = define({
  ready: { label: '可以使用', tone: 'success' },
  disabled: { label: '未启用', tone: 'default' },
  no_documents: { label: '暂无可搜索内容', tone: 'default' },
  config_mismatch: { label: '配置不匹配', tone: 'error' },
  index_not_ready: { label: '搜索内容准备中', tone: 'warning' },
  backlog: { label: '部分内容仍在准备', tone: 'warning' },
  failed_jobs: { label: '存在失败任务', tone: 'error' },
});

export const probeCodeLabels = define({
  ok: { label: '成功', tone: 'success' },
  disabled: { label: '未启用', tone: 'default' },
  unauthorized: { label: '未授权', tone: 'error' },
  config_error: { label: '配置错误', tone: 'error' },
  rate_limited: { label: '限流', tone: 'warning' },
  timeout: { label: '超时', tone: 'error' },
  cancelled: { label: '已取消', tone: 'default' },
  invalid_response: { label: '响应无效', tone: 'error' },
  unavailable: { label: '不可用', tone: 'error' },
});

export const reasonCodeLabels = define({
  configuration: { label: '配置问题' },
  transient: { label: '暂时性问题' },
  protocol: { label: '协议问题' },
  model_timeout: { label: '模型超时' },
  process_budget_exceeded: { label: '处理预算超时' },
  state_changed: { label: '状态已变化' },
  no_procedure_input: { label: '尚无可用于编写指南的操作步骤' },
  topic_mismatch: { label: '主题不匹配' },
  model_generation_failed: { label: '模型生成失败' },
  invalid_model_output: { label: '模型输出无效' },
  invalid_profile_content: { label: '画像内容无效' },
  model_unavailable: { label: '模型不可用' },
  no_eligible_input: { label: '暂无适合整理的内容' },
  input_truncated: { label: '输入已截断' },
  profile_changed: { label: 'Embedding Profile 已变化' },
  profile_registered: { label: 'Profile 已登记' },
  index_version_changed: { label: '索引版本已变化' },
  vectors_stale: { label: '存在过期向量' },
  vectors_missing: { label: '缺少向量' },
  jobs_pending: { label: '存在待处理任务' },
  related_memories_retained: { label: '相关记忆被保留' },
  explicit_exclusion: { label: '明确排除' },
  empty_extraction: { label: '提取为空' },
  chunk_failure: { label: '分块失败' },
  extraction_chunk_oversized: { label: '提取分块过大' },
  conflict_results_truncated: { label: '冲突结果已截断' },
});

export const warningLabels = define({
  semantic_unavailable: {
    label: '本次已使用关键词搜索，部分语义匹配结果可能未包含。',
    tone: 'warning',
  },
  semantic_timeout: { label: '语义检索超时，已保留关键词结果', tone: 'warning' },
  semantic_config_error: { label: '语义检索配置错误，已保留关键词结果', tone: 'warning' },
  semantic_rate_limited: { label: '语义检索被限流，已保留关键词结果', tone: 'warning' },
  semantic_invalid_response: { label: '语义服务响应无效，已保留关键词结果', tone: 'warning' },
  semantic_index_not_ready: { label: '语义索引尚未就绪', tone: 'warning' },
  fact_conflict: { label: '部分记忆的说法不一致，需要进一步核对。', tone: 'warning' },
  conflict_results_truncated: { label: '冲突结果已达到上限', tone: 'warning' },
  derived_unavailable: { label: '暂时无法搜索自动整理内容，已保留记忆搜索结果。', tone: 'warning' },
  context_budget_truncated: { label: '上下文预算导致后续结果被截断', tone: 'warning' },
});

const maps: Record<string, Record<string, CodeDefinition>> = {
  memoryKind: memoryKindLabels,
  memoryState: memoryStateLabels,
  assertion: assertionLabels,
  conflictStatus: conflictStatusLabels,
  conflictReason: conflictReasonLabels,
  conflictAction: conflictActionLabels,
  jobStatus: jobStatusLabels,
  waitReason: waitReasonLabels,
  jobStage: jobStageLabels,
  jobCategory: jobCategoryLabels,
  observationStatus: observationStatusLabels,
  observationContentMode: observationContentModeLabels,
  relationKind: relationKindLabels,
  artifactLayer: artifactLayerLabels,
  qualityOutcome: qualityOutcomeLabels,
  artifactKind: artifactKindLabels,
  artifactState: artifactStateLabels,
  targetStatus: targetStatusLabels,
  revisionOperation: revisionOperationLabels,
  experienceOutcome: experienceOutcomeLabels,
  experienceState: experienceStateLabels,
  embeddingReason: embeddingReasonLabels,
  probeCode: probeCodeLabels,
  reasonCode: reasonCodeLabels,
  warning: warningLabels,
};

export function codeDefinition(category: string, value: string | undefined): CodeDefinition {
  if (!value) {
    return { label: '未知', tone: 'default' };
  }
  return maps[category]?.[value] ?? { label: '暂无法识别', tone: 'default', description: '可在技术详情中查看原始状态。' };
}

export function codeDescription(category: string, value: string | undefined): string | undefined {
  return value ? maps[category]?.[value]?.description : undefined;
}

export function warningText(code: string): string {
  return warningLabels[code]?.label ?? '服务返回了补充提示，可展开技术详情查看。';
}

export function reasonText(code: string | undefined): string | undefined {
  if (!code) {
    return undefined;
  }
  return reasonCodeLabels[code]?.label ?? '暂无法确认原因，可查看技术详情。';
}
