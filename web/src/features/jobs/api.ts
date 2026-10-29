import { api, protocolError, unwrap } from '../../shared/api/client';
import type { GovernanceJob, GovernorJobFilters, Job } from '../../shared/api/types';

export async function getJob(id: string, signal?: AbortSignal): Promise<Job> {
  const data = await unwrap(
    api.GET('/v1/jobs/{id}', { params: { path: { id } }, signal }),
    '处理任务',
  );
  if (
    typeof data.id !== 'string' ||
    typeof data.source_id !== 'string' ||
    typeof data.status !== 'string' ||
    typeof data.stage !== 'string' ||
    typeof data.attempts !== 'number'
  ) {
    throw protocolError('处理任务', '缺少 id/source_id/status/stage/attempts');
  }
  for (const field of ['observation_ids', 'warnings', 'diagnostics'] as const) {
    if (!Array.isArray(data[field])) {
      throw protocolError('处理任务', `${field} 不是数组`);
    }
  }
  if (
    typeof data.extraction_stats !== 'object' ||
    typeof data.timings !== 'object' ||
    typeof data.model_usage !== 'object'
  ) {
    throw protocolError('处理任务', '缺少统计对象');
  }
  return data;
}

export async function listJobs(
  status: string,
  limit: number,
  signal?: AbortSignal,
): Promise<Job[]> {
  const data = await unwrap(
    api.GET('/v1/jobs', {
      params: { query: { status: (status || undefined) as Job['status'] | undefined, limit } },
      signal,
    }),
    '提取任务列表',
  );
  if (!Array.isArray(data.items)) {
    throw protocolError('提取任务列表', 'items 不是数组');
  }
  return data.items;
}

export async function listGovernanceJobs(
  filters: GovernorJobFilters,
  signal?: AbortSignal,
): Promise<GovernanceJob[]> {
  const data = await unwrap(
    api.GET('/v1/governance-jobs', {
      params: {
        query: {
          category: filters.category,
          kind: filters.kind,
          status: filters.status,
          target_key: filters.targetKey || undefined,
          limit: filters.limit,
        },
      },
      signal,
    }),
    '治理任务列表',
  );
  if (!Array.isArray(data.items)) {
    throw protocolError('治理任务列表', 'items 不是数组');
  }
  for (const item of data.items) {
    if (
      typeof item.id !== 'string' ||
      typeof item.category !== 'string' ||
      typeof item.stage !== 'string' ||
      typeof item.kind !== 'string' ||
      typeof item.target_key !== 'string'
    ) {
      throw protocolError('治理任务列表', '记录缺少 id/category/stage/kind/target_key');
    }
  }
  return data.items;
}

export function isTerminalStatus(status: string): boolean {
  return status === 'succeeded' || status === 'failed' || status === 'cancelled';
}
