import { useQuery } from '@tanstack/react-query';
import { normalizeConflictFilters, queryKeys } from '../../shared/api/query-keys';
import type { ConflictListFilters } from '../../shared/api/types';
import { getConflict, getMemberSnapshots, listConflicts } from './api';

export function normalizeConflictFilterValues(filters: ConflictListFilters): ConflictListFilters {
  return normalizeConflictFilters(filters);
}

export function useConflicts(filters: ConflictListFilters) {
  return useQuery({
    queryKey: queryKeys.conflicts.list(filters),
    queryFn: ({ signal }) => listConflicts(filters, signal),
  });
}

export function useConflict(id: string) {
  return useQuery({
    queryKey: queryKeys.conflicts.detail(id),
    queryFn: ({ signal }) => getConflict(id, signal),
    enabled: Boolean(id),
    refetchOnWindowFocus: false,
  });
}

export function useConflictMembers(
  id: string,
  revision: number | undefined,
  memberIds: string[],
  enabled = true,
) {
  return useQuery({
    queryKey: queryKeys.conflicts.members(id, revision ?? 0, memberIds),
    queryFn: ({ signal }) => getMemberSnapshots(memberIds, signal),
    enabled: Boolean(id) && enabled && memberIds.length > 0 && revision !== undefined,
    refetchOnWindowFocus: false,
  });
}
