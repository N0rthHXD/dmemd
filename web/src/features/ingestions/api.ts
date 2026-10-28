import { api, protocolError, unwrap } from '../../shared/api/client';
import type { SubmitResponse } from '../../shared/api/types';

export interface IngestionInput {
  content: unknown;
  appId?: string;
  externalId?: string;
  scopeKey?: string;
}

export async function submitIngestion(input: IngestionInput): Promise<SubmitResponse> {
  const body: {
    content: unknown;
    app_id?: string;
    external_id?: string;
    scope_key?: string;
  } = { content: input.content };
  if (input.appId?.trim()) {
    body.app_id = input.appId.trim();
  }
  if (input.externalId?.trim()) {
    body.external_id = input.externalId.trim();
  }
  if (input.scopeKey?.trim()) {
    body.scope_key = input.scopeKey.trim();
  }
  const data = await unwrap(api.POST('/v1/ingestions', { body }), '提交内容');
  if (
    typeof data.source_id !== 'string' ||
    typeof data.job_id !== 'string' ||
    typeof data.status !== 'string'
  ) {
    throw protocolError('提交内容', '响应缺少 source_id/job_id/status');
  }
  return data;
}
