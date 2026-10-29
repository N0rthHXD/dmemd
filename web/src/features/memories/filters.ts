import {
  activeMemoryStates,
  assertionStatuses,
  memoryKinds,
  memoryStates,
  type AssertionStatus,
  type MemoryKind,
  type MemoryListFilters,
  type MemoryState,
} from '../../shared/api/types';
import { fromRFC3339 } from '../../shared/lib/time';

export const memoryLimitOptions = [20, 50, 100, 200] as const;

function csv(params: URLSearchParams, key: string): string[] {
  const value = params.get(key);
  if (!value) {
    return [];
  }
  return value
    .split(',')
    .map((part) => part.trim())
    .filter(Boolean);
}

function allowed<T extends string>(values: string[], options: readonly T[]): T[] {
  const set = new Set<string>(options);
  return [...new Set(values.filter((value): value is T => set.has(value)))];
}

export function readMemoryFilters(params: URLSearchParams): MemoryListFilters {
  const rawLimit = Number(params.get('limit'));
  const limit = memoryLimitOptions.includes(
    rawLimit as (typeof memoryLimitOptions)[number],
  )
    ? rawLimit
    : 50;
  const updatedAfter = params.get('updated_after') ?? '';
  const updatedBefore = params.get('updated_before') ?? '';
  return {
    kinds: allowed<MemoryKind>(csv(params, 'kinds'), memoryKinds),
    states: allowed<MemoryState>(csv(params, 'states'), memoryStates),
    assertionStatuses: allowed<AssertionStatus>(
      csv(params, 'assertion_status'),
      assertionStatuses,
    ),
    updatedAfter: fromRFC3339(updatedAfter) ? updatedAfter : undefined,
    updatedBefore: fromRFC3339(updatedBefore) ? updatedBefore : undefined,
    limit,
  };
}

export function writeMemoryFilters(filters: MemoryListFilters): URLSearchParams {
  const params = new URLSearchParams();
  if (filters.kinds.length > 0) {
    params.set('kinds', [...filters.kinds].sort().join(','));
  }
  if (filters.states.length > 0) {
    params.set('states', [...filters.states].sort().join(','));
  }
  if (filters.assertionStatuses.length > 0) {
    params.set('assertion_status', [...filters.assertionStatuses].sort().join(','));
  }
  if (filters.updatedAfter) {
    params.set('updated_after', filters.updatedAfter);
  }
  if (filters.updatedBefore) {
    params.set('updated_before', filters.updatedBefore);
  }
  if (filters.limit !== 50) {
    params.set('limit', String(filters.limit));
  }
  return params;
}

export function defaultMemoryFilters(): MemoryListFilters {
  return {
    kinds: [],
    states: [],
    assertionStatuses: [],
    limit: 50,
  };
}

export function isDefaultMemoryFilters(filters: MemoryListFilters): boolean {
  return (
    filters.kinds.length === 0 &&
    filters.states.length === 0 &&
    filters.assertionStatuses.length === 0 &&
    !filters.updatedAfter &&
    !filters.updatedBefore &&
    filters.limit === 50
  );
}

export { activeMemoryStates };
