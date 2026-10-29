import type {
  ArtifactKind,
  ArtifactListFilters,
  ArtifactState,
  ArtifactTargetFilters,
  ConflictListFilters,
  GovernorJobFilters,
  MemoryListFilters,
  NormalizedRecallRequest,
  ObservationListFilters,
} from './types';

function sortedUnique<T extends string>(values: T[]): T[] {
  return [...new Set(values)].sort();
}

/** Stable key input: remove empty fields and sort set-like arrays. */
export function normalizeMemoryFilters(filters: MemoryListFilters): MemoryListFilters {
  return {
    kinds: sortedUnique(filters.kinds),
    states: sortedUnique(filters.states),
    assertionStatuses: sortedUnique(filters.assertionStatuses),
    updatedAfter: filters.updatedAfter || undefined,
    updatedBefore: filters.updatedBefore || undefined,
    limit: filters.limit,
  };
}

export function normalizeObservationFilters(
  filters: ObservationListFilters,
): ObservationListFilters {
  return {
    sourceId: filters.sourceId.trim(),
    scopeKey: filters.scopeKey.trim(),
    kind: filters.kind,
    status: filters.status,
    limit: filters.limit,
  };
}

export function normalizeArtifactFilters(filters: ArtifactListFilters): ArtifactListFilters {
  return {
    scopeKey: filters.scopeKey.trim(),
    kind: filters.kind,
    state: filters.state,
    reasonCode: filters.reasonCode.trim(),
    limit: filters.limit,
  };
}

export function normalizeTargetFilters(filters: ArtifactTargetFilters): ArtifactTargetFilters {
  return {
    scopeKey: filters.scopeKey.trim(),
    kind: filters.kind,
    status: filters.status,
    reasonCode: filters.reasonCode.trim(),
    limit: filters.limit,
  };
}

export function normalizeConflictFilters(filters: ConflictListFilters): ConflictListFilters {
  return { status: filters.status, limit: filters.limit };
}

export function normalizeGovernanceJobFilters(
  filters: GovernorJobFilters,
): GovernorJobFilters {
  return {
    category: filters.category,
    kind: filters.kind,
    status: filters.status,
    targetKey: filters.targetKey.trim(),
    limit: filters.limit,
  };
}

export function normalizeRecallRequest(request: NormalizedRecallRequest): NormalizedRecallRequest {
  const filters = request.filters;
  return {
    query: request.query.trim(),
    scopeKey: request.scopeKey?.trim() || undefined,
    queryMode: request.queryMode,
    layers: request.layers && request.layers.length > 0 ? sortedUnique(request.layers) : undefined,
    mode: request.mode,
    derivedStates:
      request.derivedStates && request.derivedStates.length > 0
        ? sortedUnique(request.derivedStates)
        : undefined,
    topK: request.topK,
    maxContextTokens: request.maxContextTokens,
    explain: request.explain,
    filters: {
      kinds: filters.kinds && filters.kinds.length > 0 ? sortedUnique(filters.kinds) : undefined,
      states: filters.states && filters.states.length > 0 ? sortedUnique(filters.states) : undefined,
      updatedAfter: filters.updatedAfter || undefined,
      updatedBefore: filters.updatedBefore || undefined,
    },
  };
}

export const queryKeys = {
  memories: {
    all: ['memories'] as const,
    list: (filters: MemoryListFilters) => ['memories', 'list', filters] as const,
    detail: (id: string) => ['memories', 'detail', id] as const,
    history: (id: string, limit: number) => ['memories', 'history', id, limit] as const,
    revision: (id: string, version: number) => ['memories', 'revision', id, version] as const,
    evidence: (id: string, version: number | null) =>
      ['memories', 'evidence', id, version ?? 'current'] as const,
  },
  observations: {
    all: ['observations'] as const,
    list: (filters: ObservationListFilters) => ['observations', 'list', filters] as const,
    detail: (id: string) => ['observations', 'detail', id] as const,
    evidence: (id: string) => ['observations', 'evidence', id] as const,
    relations: (id: string) => ['observations', 'relations', id] as const,
  },
  conflicts: {
    all: ['conflicts'] as const,
    list: (filters: ConflictListFilters) => ['conflicts', 'list', filters] as const,
    detail: (id: string) => ['conflicts', 'detail', id] as const,
    members: (id: string, revision: number, memberIds: string[]) =>
      ['conflicts', 'members', id, revision, [...memberIds].sort()] as const,
  },
  artifacts: {
    all: ['artifacts'] as const,
    list: (filters: ArtifactListFilters) => ['artifacts', 'list', filters] as const,
    layer: (kinds: ArtifactKind[], state: ArtifactState | undefined, limit: number) =>
      ['artifacts', 'layer', [...kinds].sort(), state ?? 'all', limit] as const,
    detail: (id: string, version: number | null) =>
      ['artifacts', 'detail', id, version ?? 'current'] as const,
  },
  targets: {
    all: ['artifact-targets'] as const,
    list: (filters: ArtifactTargetFilters) => ['artifact-targets', 'list', filters] as const,
  },
  skills: {
    all: ['skills'] as const,
    content: (id: string, version: number | null) =>
      ['skills', 'content', id, version ?? 'current'] as const,
  },
  sources: {
    all: ['sources'] as const,
    detail: (id: string) => ['sources', 'detail', id] as const,
  },
  jobs: {
    all: ['jobs'] as const,
    list: (status: string, limit: number) => ['jobs', 'list', status || 'all', limit] as const,
    detail: (id: string) => ['jobs', 'detail', id] as const,
    governance: {
      all: ['governance-jobs'] as const,
      list: (filters: GovernorJobFilters) => ['governance-jobs', 'list', filters] as const,
      detail: (id: string) => ['governance-jobs', 'detail', id] as const,
    },
  },
  recall: (request: NormalizedRecallRequest) => ['recall', request] as const,
  system: {
    health: ['system', 'health'] as const,
    ready: ['system', 'ready'] as const,
    embedding: ['system', 'embedding'] as const,
    probe: ['system', 'probe'] as const,
  },
  session: {
    ingestions: ['session', 'ingestions'] as const,
    experiences: ['session', 'experiences'] as const,
  },
} as const;
