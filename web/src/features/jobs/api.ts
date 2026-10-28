import { api, protocolError, unwrap } from '../../shared/api/client';
import type { Job } from '../../shared/api/types';

export async function getJob(id: string, signal?: AbortSignal): Promise<Job> {
  const data = await unwrap(
    api.GET('/v1/ingest-jobs/{id}', { params: { path: { id } }, signal }),
    '处理任务',
  );
  if (
    typeof data.id !== 'string' ||
    typeof data.source_id !== 'string' ||
    typeof data.status !== 'string' ||
    typeof data.attempts !== 'number'
  ) {
    throw protocolError('处理任务', '缺少 id/source_id/status/attempts');
  }
  for (const field of [
    'memory_ids',
    'conflict_ids',
    'pending_memory_ids',
    'warnings',
    'diagnostics',
    'convergence',
  ] as const) {
    if (!Array.isArray(data[field])) {
      throw protocolError('处理任务', `${field} 不是数组`);
    }
  }
  if (
    typeof data.extraction_stats !== 'object' ||
    typeof data.retention !== 'object' ||
    typeof data.timings !== 'object' ||
    typeof data.model_usage !== 'object' ||
    typeof data.efficiency !== 'object'
  ) {
    throw protocolError('处理任务', '缺少统计对象');
  }
  return data;
}

export function isTerminalStatus(status: string): boolean {
  return status === 'succeeded' || status === 'failed' || status === 'cancelled';
}
