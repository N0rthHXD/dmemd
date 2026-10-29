import { api, apiLong, isRecord, protocolError, rawJSON, unwrap } from '../../shared/api/client';
import type { EmbeddingProbe, EmbeddingStatus, Health } from '../../shared/api/types';

function checkedHealth(value: Health, label: string): Health {
  if (!isRecord(value) || typeof value.status !== 'string') {
    throw protocolError(label, '缺少 status 字符串');
  }
  return value;
}

export async function getHealth(signal?: AbortSignal): Promise<Health> {
  const data = await unwrap(api.GET('/healthz', { signal }), '存活检查');
  return checkedHealth(data, '存活检查');
}

export interface ReadinessState extends Health {
  httpStatus: number;
}

export async function getReadiness(signal?: AbortSignal): Promise<ReadinessState> {
  const { status, body } = await rawJSON('/readyz', undefined, signal);
  if (!isRecord(body) || typeof body.status !== 'string') {
    throw protocolError('就绪检查', '缺少 status 字符串');
  }
  if (status !== 200 && status !== 503) {
    throw protocolError('就绪检查', `未预期的 HTTP ${status}`);
  }
  return { status: body.status as Health['status'], httpStatus: status };
}

function checkedEmbedding(value: EmbeddingStatus): EmbeddingStatus {
  if (!isRecord(value)) {
    throw protocolError('Embedding 状态', '响应不是对象');
  }
  for (const field of [
    'enabled',
    'config_fingerprint_match',
    'rebuild_required',
    'ready',
  ] as const) {
    if (typeof value[field] !== 'boolean') {
      throw protocolError('Embedding 状态', `字段 ${field} 不是布尔值`);
    }
  }
  for (const field of [
    'documents',
    'current_vectors',
    'stale_vectors',
    'missing_vectors',
    'pending_jobs',
    'failed_jobs',
  ] as const) {
    if (typeof value[field] !== 'number') {
      throw protocolError('Embedding 状态', `字段 ${field} 不是数字`);
    }
  }
  if (!Array.isArray(value.failed_codes) || !Array.isArray(value.indexed_profiles)) {
    throw protocolError('Embedding 状态', '失败原因或索引 Profile 不是数组');
  }
  if (typeof value.coverage !== 'number' || typeof value.reason_code !== 'string') {
    throw protocolError('Embedding 状态', '缺少覆盖率或原因码');
  }
  return value;
}

export async function getEmbeddingStatus(signal?: AbortSignal): Promise<EmbeddingStatus> {
  return checkedEmbedding(
    await unwrap(api.GET('/v1/embedding/status', { signal }), 'Embedding 状态'),
  );
}

export async function probeEmbedding(): Promise<EmbeddingProbe> {
  const data = await unwrap(apiLong.POST('/v1/embedding/probe'), 'Embedding 探测');
  if (
    !isRecord(data) ||
    typeof data.attempted !== 'boolean' ||
    typeof data.success !== 'boolean' ||
    typeof data.code !== 'string' ||
    typeof data.duration_ms !== 'number'
  ) {
    throw protocolError('Embedding 探测', '缺少 attempted/success/code/duration_ms');
  }
  return data;
}
