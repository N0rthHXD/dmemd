import { api, protocolError, unwrap } from '../../shared/api/client';
import type { Memory } from '../../shared/api/types';

export async function getRecentMemories(
  limit: number,
  signal?: AbortSignal,
): Promise<Memory[]> {
  const data = await unwrap(
    api.GET('/v1/memories', { params: { query: { limit } }, signal }),
    '最近记忆',
  );
  if (!Array.isArray(data.items)) {
    throw protocolError('最近记忆', 'items 不是数组');
  }
  return data.items;
}
