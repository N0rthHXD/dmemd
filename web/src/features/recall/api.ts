import { api, protocolError, unwrap } from '../../shared/api/client';
import type {
  NormalizedRecallRequest,
  SearchFilters,
  SearchRequest,
  SearchResponse,
} from '../../shared/api/types';

export async function recallMemories(
  request: NormalizedRecallRequest,
): Promise<SearchResponse> {
  const filters: SearchFilters = {};
  if (request.filters.kinds?.length) {
    filters.kinds = request.filters.kinds;
  }
  if (request.filters.states?.length) {
    filters.states = request.filters.states;
  }
  if (request.filters.asOf) {
    filters.as_of = request.filters.asOf;
  }
  if (request.filters.eventAt) {
    filters.event_at = request.filters.eventAt;
  }
  if (request.filters.createdByApp) {
    filters.created_by_app = request.filters.createdByApp;
  }
  if (request.filters.updatedAfter) {
    filters.updated_after = request.filters.updatedAfter;
  }
  if (request.filters.updatedBefore) {
    filters.updated_before = request.filters.updatedBefore;
  }

  const body: SearchRequest = {
    query: request.query,
    mode: request.mode,
    top_k: request.topK,
    explain: request.explain,
    filters,
  };
  if (request.scopeKey) {
    body.scope_key = request.scopeKey;
  }
  if (request.layers) {
    body.layers = request.layers;
  }
  if (request.maxContextTokens) {
    body.max_context_tokens = request.maxContextTokens;
  }

  const data = await unwrap(api.POST('/v1/memories/recall', { body }), '分层召回');
  for (const field of ['memories', 'summaries', 'experiences', 'skills', 'conflicts', 'warnings'] as const) {
    if (!Array.isArray(data[field])) {
      throw protocolError('分层召回', `${field} 不是数组`);
    }
  }
  for (const item of data.memories) {
    if (
      typeof item.memory?.id !== 'string' ||
      typeof item.memory.version !== 'number' ||
      typeof item.snippet !== 'string' ||
      !Array.isArray(item.matched_terms) ||
      !Array.isArray(item.evidence_refs) ||
      !Array.isArray(item.conflict_refs)
    ) {
      throw protocolError('分层召回', '原子结果缺少 memory/snippet/引用数组');
    }
  }
  for (const item of [...data.summaries, ...data.experiences, ...data.skills]) {
    if (
      typeof item.id !== 'string' ||
      typeof item.content !== 'string' ||
      typeof item.version !== 'number' ||
      !Array.isArray(item.memory_refs) ||
      !Array.isArray(item.run_refs)
    ) {
      throw protocolError('分层召回', '派生结果缺少 id/content/version/引用数组');
    }
  }
  for (const item of data.conflicts) {
    if (typeof item.id !== 'string' || typeof item.reason_code !== 'string') {
      throw protocolError('分层召回', '冲突摘要缺少 id/reason_code');
    }
  }
  return data;
}
