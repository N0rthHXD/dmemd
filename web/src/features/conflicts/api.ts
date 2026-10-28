import { api, protocolError, unwrap } from '../../shared/api/client';
import type {
  Conflict,
  ConflictListFilters,
  ConflictResolveRequest,
  Evidence,
  Memory,
} from '../../shared/api/types';

function checkedConflict(value: unknown): Conflict {
  if (
    typeof value !== 'object' ||
    value === null ||
    typeof (value as { id?: unknown }).id !== 'string' ||
    typeof (value as { revision?: unknown }).revision !== 'number'
  ) {
    throw protocolError('冲突', '缺少 id 或 revision');
  }
  const conflict = value as Conflict;
  if (!Array.isArray(conflict.members)) {
    throw protocolError('冲突', 'members 不是数组');
  }
  return conflict;
}

export async function listConflicts(
  filters: ConflictListFilters,
  signal?: AbortSignal,
): Promise<Conflict[]> {
  const data = await unwrap(
    api.GET('/v1/memory-conflicts', {
      params: { query: { status: filters.status, limit: filters.limit } },
      signal,
    }),
    '冲突列表',
  );
  if (!Array.isArray(data.items)) {
    throw protocolError('冲突列表', 'items 不是数组');
  }
  return data.items.map((item) => checkedConflict(item));
}

export async function getConflict(id: string, signal?: AbortSignal): Promise<Conflict> {
  return checkedConflict(
    await unwrap(
      api.GET('/v1/memory-conflicts/{id}', { params: { path: { id } }, signal }),
      '冲突详情',
    ),
  );
}

export async function resolveConflict(
  id: string,
  body: ConflictResolveRequest,
): Promise<Conflict> {
  const data = await unwrap(
    api.POST('/v1/memory-conflicts/{id}/resolve', {
      params: { path: { id } },
      body,
    }),
    '冲突裁决',
  );
  return checkedConflict(data);
}

export interface MemberSnapshot {
  memory: Memory;
  evidence: Evidence[];
}

export async function getMemberSnapshot(
  memoryId: string,
  signal?: AbortSignal,
): Promise<MemberSnapshot> {
  const memory = await unwrap(
    api.GET('/v1/memories/{id}', { params: { path: { id: memoryId } }, signal }),
    '冲突成员快照',
  );
  if (
    typeof memory.id !== 'string' ||
    typeof memory.version !== 'number' ||
    typeof memory.content !== 'string'
  ) {
    throw protocolError('冲突成员快照', '缺少 id/version/content');
  }
  const evidence = await unwrap(
    api.GET('/v1/memories/{id}/evidence', { params: { path: { id: memoryId } }, signal }),
    '冲突成员证据',
  );
  if (!Array.isArray(evidence.items)) {
    throw protocolError('冲突成员证据', 'items 不是数组');
  }
  return { memory, evidence: evidence.items };
}

/** Fetch member snapshots with at most four concurrent member requests. */
export async function getMemberSnapshots(
  memoryIds: string[],
  signal?: AbortSignal,
): Promise<MemberSnapshot[]> {
  const result: MemberSnapshot[] = new Array(memoryIds.length);
  for (let start = 0; start < memoryIds.length; start += 4) {
    const batch = memoryIds.slice(start, start + 4);
    const values = await Promise.all(
      batch.map((memoryId) => getMemberSnapshot(memoryId, signal)),
    );
    values.forEach((value, index) => {
      result[start + index] = value;
    });
  }
  return result;
}
