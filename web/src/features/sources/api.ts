import { api, protocolError, unwrap } from '../../shared/api/client';
import type { ForgetResponse, RedactResponse, Source } from '../../shared/api/types';

export async function getSource(id: string, signal?: AbortSignal): Promise<Source> {
  const data = await unwrap(
    api.GET('/v1/sources/{id}', { params: { path: { id } }, signal }),
    '来源详情',
  );
  if (
    typeof data.id !== 'string' ||
    typeof data.redacted !== 'boolean' ||
    typeof data.created_at !== 'string'
  ) {
    throw protocolError('来源详情', '缺少 id/redacted/created_at');
  }
  return data;
}

export async function redactSource(id: string): Promise<RedactResponse> {
  const data = await unwrap(
    api.DELETE('/v1/sources/{id}', { params: { path: { id } } }),
    '清除来源原文',
  );
  if (data.id !== id || data.redacted !== true || typeof data.cancelled_jobs !== 'number') {
    throw protocolError('清除来源原文', '响应未确认 redacted=true');
  }
  return data;
}

export async function forgetSource(id: string): Promise<ForgetResponse> {
  const data = await unwrap(
    api.POST('/v1/forget', { body: { source_ids: [id] } }),
    '永久遗忘来源',
  );
  if (
    !Array.isArray(data.memory_ids) ||
    !Array.isArray(data.source_ids) ||
    !Array.isArray(data.experience_ids)
  ) {
    throw protocolError('永久遗忘来源', '响应缺少实际删除 ID');
  }
  return data;
}
