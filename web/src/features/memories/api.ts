import { api, protocolError, unwrap } from '../../shared/api/client';
import type {
  Evidence,
  ForgetResponse,
  Memory,
  MemoryDetail,
  MemoryListFilters,
  MemoryState,
  Revision,
} from '../../shared/api/types';

function listQuery(filters: MemoryListFilters) {
  return {
    kinds: filters.kinds.length > 0 ? filters.kinds.join(',') : undefined,
    states: filters.states.length > 0 ? filters.states.join(',') : undefined,
    assertion_status:
      filters.assertionStatuses.length > 0 ? filters.assertionStatuses.join(',') : undefined,
    fact_group_id: filters.factGroupId.trim() || undefined,
    created_by_app: filters.createdByApp.trim() || undefined,
    updated_after: filters.updatedAfter || undefined,
    updated_before: filters.updatedBefore || undefined,
    limit: filters.limit,
  };
}

function checkedMemory(value: unknown): MemoryDetail {
  if (
    typeof value !== 'object' ||
    value === null ||
    typeof (value as { id?: unknown }).id !== 'string' ||
    typeof (value as { version?: unknown }).version !== 'number'
  ) {
    throw protocolError('记忆详情', '缺少 id 或 version');
  }
  const detail = value as MemoryDetail;
  if (!Array.isArray(detail.relations) || !Array.isArray(detail.evolution)) {
    throw protocolError('记忆详情', 'relations 或 evolution 不是数组');
  }
  return detail;
}

export async function listMemories(
  filters: MemoryListFilters,
  signal?: AbortSignal,
): Promise<Memory[]> {
  const data = await unwrap(
    api.GET('/v1/memories', { params: { query: listQuery(filters) }, signal }),
    '记忆列表',
  );
  if (!Array.isArray(data.items)) {
    throw protocolError('记忆列表', 'items 不是数组');
  }
  for (const item of data.items) {
    if (
      typeof item.id !== 'string' ||
      typeof item.version !== 'number' ||
      typeof item.content !== 'string'
    ) {
      throw protocolError('记忆列表', '记录缺少 id/version/content');
    }
  }
  return data.items;
}

export async function getMemory(id: string, signal?: AbortSignal): Promise<MemoryDetail> {
  return checkedMemory(
    await unwrap(
      api.GET('/v1/memories/{id}', { params: { path: { id } }, signal }),
      '记忆详情',
    ),
  );
}

export async function setMemoryState(id: string, state: Extract<MemoryState, 'active' | 'archived'>) {
  const data = await unwrap(
    api.PATCH('/v1/memories/{id}/state', {
      params: { path: { id } },
      body: { state },
    }),
    '记忆状态更新',
  );
  if (typeof data.version !== 'number') {
    throw protocolError('记忆状态更新', '响应缺少 version');
  }
  return data;
}

export async function deleteMemory(id: string) {
  const data = await unwrap(
    api.DELETE('/v1/memories/{id}', { params: { path: { id } } }),
    '逻辑删除记忆',
  );
  if (data.id !== id || data.state !== 'deleted') {
    throw protocolError('逻辑删除记忆', '响应 ID 或状态不符');
  }
  return data;
}

export async function purgeMemory(id: string) {
  const data = await unwrap(
    api.POST('/v1/memories/{id}/purge', { params: { path: { id } } }),
    '永久清除记忆',
  );
  if (data.id !== id || data.purged !== true) {
    throw protocolError('永久清除记忆', '响应未确认 purged=true');
  }
  return data;
}

export async function listMemoryHistory(
  id: string,
  limit: number,
  signal?: AbortSignal,
): Promise<Revision[]> {
  const data = await unwrap(
    api.GET('/v1/memories/{id}/history', {
      params: { path: { id }, query: { limit } },
      signal,
    }),
    '记忆历史',
  );
  if (!Array.isArray(data.items)) {
    throw protocolError('记忆历史', 'items 不是数组');
  }
  return data.items;
}

export async function getMemoryRevision(
  id: string,
  version: number,
  signal?: AbortSignal,
): Promise<Revision> {
  const data = await unwrap(
    api.GET('/v1/memories/{id}/history/{version}', {
      params: { path: { id, version } },
      signal,
    }),
    '历史版本',
  );
  if (
    typeof data.memory_id !== 'string' ||
    typeof data.version !== 'number' ||
    typeof data.snapshot !== 'object' ||
    !Array.isArray(data.evidence)
  ) {
    throw protocolError('历史版本', '缺少 memory_id/version/snapshot/evidence');
  }
  return data;
}

export async function restoreMemoryVersion(id: string, version: number) {
  const data = await unwrap(
    api.POST('/v1/memories/{id}/history/{version}/restore', {
      params: { path: { id, version } },
    }),
    '恢复历史版本',
  );
  if (data.id !== id || typeof data.version !== 'number' || data.state !== 'active') {
    throw protocolError('恢复历史版本', '响应未返回新的 active 版本');
  }
  return data;
}

export async function getMemoryEvidence(
  id: string,
  version: number | null,
  signal?: AbortSignal,
): Promise<Evidence[]> {
  const data = await unwrap(
    api.GET('/v1/memories/{id}/evidence', {
      params: {
        path: { id },
        query: version === null ? {} : { version },
      },
      signal,
    }),
    '记忆证据',
  );
  if (!Array.isArray(data.items)) {
    throw protocolError('记忆证据', 'items 不是数组');
  }
  return data.items;
}

export async function forgetMemory(id: string): Promise<ForgetResponse> {
  const data = await unwrap(
    api.POST('/v1/forget', { body: { memory_ids: [id] } }),
    '永久遗忘记忆',
  );
  if (!Array.isArray(data.memory_ids) || !Array.isArray(data.source_ids) || !Array.isArray(data.experience_ids)) {
    throw protocolError('永久遗忘记忆', '响应缺少实际删除 ID');
  }
  return data;
}
