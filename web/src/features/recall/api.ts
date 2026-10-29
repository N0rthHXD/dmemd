import { api, protocolError, unwrap } from '../../shared/api/client';
import type {
  NormalizedRecallRequest,
  RecallDerivedItem,
  RecallObservationItem,
  RecallRequest,
  RecallResponse,
  SearchFilters,
  SearchItem,
} from '../../shared/api/types';

function checkedObservation(item: RecallObservationItem) {
  if (
    typeof item.observation?.id !== 'string' ||
    typeof item.observation.content !== 'string' ||
    typeof item.snippet !== 'string' ||
    !Array.isArray(item.matched_terms) ||
    !Array.isArray(item.evidence_refs)
  ) {
    throw protocolError('分层召回', '观察结果缺少字段');
  }
}

function checkedMemory(item: SearchItem) {
  if (
    typeof item.memory?.id !== 'string' ||
    typeof item.memory.version !== 'number' ||
    typeof item.snippet !== 'string' ||
    !Array.isArray(item.matched_terms) ||
    !Array.isArray(item.evidence_refs) ||
    !Array.isArray(item.conflict_refs)
  ) {
    throw protocolError('分层召回', '记忆结果缺少字段');
  }
}

function checkedDerived(item: RecallDerivedItem) {
  if (
    typeof item.artifact?.id !== 'string' ||
    typeof item.artifact.layer !== 'string' ||
    typeof item.revision?.version !== 'number' ||
    typeof item.revision.content !== 'string' ||
    !Array.isArray(item.dependencies)
  ) {
    throw protocolError('分层召回', '派生结果缺少字段');
  }
}

export async function recallMemories(
  request: NormalizedRecallRequest,
): Promise<RecallResponse> {
  const filters: SearchFilters = {};
  if (request.filters.kinds?.length) {
    filters.kinds = request.filters.kinds;
  }
  if (request.filters.states?.length) {
    filters.states = request.filters.states;
  }
  if (request.filters.updatedAfter) {
    filters.updated_after = request.filters.updatedAfter;
  }
  if (request.filters.updatedBefore) {
    filters.updated_before = request.filters.updatedBefore;
  }

  const body: RecallRequest = {
    query: request.query,
    mode: request.mode,
    top_k: request.topK,
    explain: request.explain,
    filters,
  };
  if (request.scopeKey) {
    body.scope_key = request.scopeKey;
  }
  if (request.queryMode) {
    body.query_mode = request.queryMode;
  }
  if (request.layers) {
    body.layers = request.layers;
  }
  if (request.derivedStates) {
    body.derived_states = request.derivedStates;
  }
  if (request.maxContextTokens) {
    body.max_context_tokens = request.maxContextTokens;
  }

  const data = await unwrap(api.POST('/v1/memories/recall', { body }), '分层召回');
  for (const field of ['observations', 'memories', 'l1', 'l2', 'l3', 'l4', 'relations', 'conflicts', 'warnings'] as const) {
    if (!Array.isArray(data[field])) {
      throw protocolError('分层召回', `${field} 不是数组`);
    }
  }
  data.observations.forEach(checkedObservation);
  data.memories.forEach(checkedMemory);
  [...data.l1, ...data.l2, ...data.l3, ...data.l4].forEach(checkedDerived);
  return data;
}
