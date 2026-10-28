import { api, protocolError, unwrap } from '../../shared/api/client';
import type {
  ArtifactDetail,
  ArtifactList,
  ArtifactListFilters,
  ArtifactTargetFilters,
  ArtifactTargetList,
  ExperienceReceipt,
  ExperienceSubmit,
  ForgetResponse,
  SkillContent,
} from '../../shared/api/types';

function artifactQuery(filters: ArtifactListFilters, cursor: string | undefined) {
  return {
    scope_key: filters.scopeKey || undefined,
    kind: filters.kind,
    state: filters.state,
    reason_code: filters.reasonCode || undefined,
    cursor: cursor || undefined,
    limit: filters.limit,
  };
}

function targetQuery(filters: ArtifactTargetFilters, cursor: string | undefined) {
  return {
    scope_key: filters.scopeKey || undefined,
    kind: filters.kind,
    status: filters.status,
    reason_code: filters.reasonCode || undefined,
    cursor: cursor || undefined,
    limit: filters.limit,
  };
}

export async function listArtifacts(
  filters: ArtifactListFilters,
  cursor: string | undefined,
  signal?: AbortSignal,
): Promise<ArtifactList> {
  const data = await unwrap(
    api.GET('/v1/artifacts', { params: { query: artifactQuery(filters, cursor) }, signal }),
    '派生对象列表',
  );
  if (!Array.isArray(data.items)) {
    throw protocolError('派生对象列表', 'items 不是数组');
  }
  for (const item of data.items) {
    if (
      typeof item.id !== 'string' ||
      typeof item.kind !== 'string' ||
      typeof item.current_version !== 'number' ||
      typeof item.state !== 'string'
    ) {
      throw protocolError('派生对象列表', '记录缺少 id/kind/current_version/state');
    }
  }
  return data;
}

export async function listArtifactTargets(
  filters: ArtifactTargetFilters,
  cursor: string | undefined,
  signal?: AbortSignal,
): Promise<ArtifactTargetList> {
  const data = await unwrap(
    api.GET('/v1/artifact-targets', { params: { query: targetQuery(filters, cursor) }, signal }),
    '生成目标列表',
  );
  if (!Array.isArray(data.items)) {
    throw protocolError('生成目标列表', 'items 不是数组');
  }
  for (const item of data.items) {
    if (
      typeof item.kind !== 'string' ||
      typeof item.target_key !== 'string' ||
      typeof item.status !== 'string' ||
      typeof item.has_usable_version !== 'boolean'
    ) {
      throw protocolError('生成目标列表', '记录缺少 kind/target_key/status');
    }
  }
  return data;
}

export async function getArtifactDetail(
  id: string,
  version: number | null,
  signal?: AbortSignal,
): Promise<ArtifactDetail> {
  const data =
    version === null
      ? await unwrap(
          api.GET('/v1/artifacts/{id}', { params: { path: { id } }, signal }),
          '派生对象详情',
        )
      : await unwrap(
          api.GET('/v1/artifacts/{id}/history/{version}', {
            params: { path: { id, version } },
            signal,
          }),
          '派生对象历史版本',
        );
  if (
    typeof data.artifact !== 'object' ||
    typeof data.artifact.id !== 'string' ||
    typeof data.artifact.current_version !== 'number'
  ) {
    throw protocolError('派生对象详情', '缺少 artifact.id 或 current_version');
  }
  return data;
}

export async function getSkillContent(
  id: string,
  version: number | null,
  signal?: AbortSignal,
): Promise<SkillContent> {
  const data = await unwrap(
    api.GET('/v1/skills/{id}/content', {
      params: {
        path: { id },
        query: version === null ? {} : { version },
      },
      signal,
    }),
    'Skill 内容',
  );
  if (typeof data.content !== 'string' || typeof data.version !== 'number') {
    throw protocolError('Skill 内容', '缺少 content 或 version');
  }
  return data;
}

export async function submitExperience(body: ExperienceSubmit): Promise<ExperienceReceipt> {
  const data = await unwrap(api.POST('/v1/experiences', { body }), '提交应用结果');
  if (
    typeof data.id !== 'string' ||
    typeof data.source_id !== 'string' ||
    typeof data.outcome !== 'string' ||
    typeof data.state !== 'string'
  ) {
    throw protocolError('提交应用结果', '回执缺少 id/source_id/outcome/state');
  }
  return data;
}

export async function retractExperience(id: string): Promise<ExperienceReceipt> {
  const data = await unwrap(
    api.POST('/v1/experiences/{id}/retract', { params: { path: { id } } }),
    '撤销应用报告',
  );
  if (data.id !== id || data.state !== 'revoked') {
    throw protocolError('撤销应用报告', '回执未返回 state=revoked');
  }
  return data;
}

export async function forgetExperience(id: string): Promise<ForgetResponse> {
  const data = await unwrap(
    api.POST('/v1/forget', { body: { experience_ids: [id] } }),
    '永久遗忘应用报告',
  );
  if (
    !Array.isArray(data.memory_ids) ||
    !Array.isArray(data.source_ids) ||
    !Array.isArray(data.experience_ids)
  ) {
    throw protocolError('永久遗忘应用报告', '响应缺少实际删除 ID');
  }
  return data;
}
