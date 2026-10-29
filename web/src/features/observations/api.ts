import { api, protocolError, unwrap } from '../../shared/api/client';
import type {
  Evidence,
  Observation,
  ObservationListFilters,
  Relation,
} from '../../shared/api/types';

function listQuery(filters: ObservationListFilters) {
  return {
    source_id: filters.sourceId || undefined,
    scope_key: filters.scopeKey || undefined,
    kind: filters.kind,
    status: filters.status,
    limit: filters.limit,
  };
}

function checkedObservation(value: unknown): Observation {
  if (
    typeof value !== 'object' ||
    value === null ||
    typeof (value as { id?: unknown }).id !== 'string' ||
    typeof (value as { content?: unknown }).content !== 'string' ||
    typeof (value as { source_id?: unknown }).source_id !== 'string'
  ) {
    throw protocolError('观察记录', '缺少 id/source_id/content');
  }
  return value as Observation;
}

export async function listObservations(
  filters: ObservationListFilters,
  signal?: AbortSignal,
): Promise<Observation[]> {
  const data = await unwrap(
    api.GET('/v1/observations', { params: { query: listQuery(filters) }, signal }),
    '观察列表',
  );
  if (!Array.isArray(data.items)) {
    throw protocolError('观察列表', 'items 不是数组');
  }
  return data.items.map(checkedObservation);
}

export async function getObservation(id: string, signal?: AbortSignal): Promise<Observation> {
  return checkedObservation(
    await unwrap(api.GET('/v1/observations/{id}', { params: { path: { id } }, signal }), '观察详情'),
  );
}

export async function getObservationEvidence(
  id: string,
  signal?: AbortSignal,
): Promise<Evidence[]> {
  const data = await unwrap(
    api.GET('/v1/observations/{id}/evidence', { params: { path: { id } }, signal }),
    '观察证据',
  );
  if (!Array.isArray(data.items)) {
    throw protocolError('观察证据', 'items 不是数组');
  }
  return data.items;
}

export async function getObservationRelations(
  id: string,
  signal?: AbortSignal,
): Promise<Relation[]> {
  const data = await unwrap(
    api.GET('/v1/observations/{id}/relations', { params: { path: { id } }, signal }),
    '观察关系',
  );
  if (!Array.isArray(data.items)) {
    throw protocolError('观察关系', 'items 不是数组');
  }
  return data.items;
}
