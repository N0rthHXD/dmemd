import { useQuery } from '@tanstack/react-query';
import { normalizeMemoryFilters, queryKeys } from '../../shared/api/query-keys';
import type { MemoryListFilters } from '../../shared/api/types';
import {
  getRecentMemories,
} from './api';

const recentFilters: MemoryListFilters = normalizeMemoryFilters({
  kinds: [],
  states: [],
  assertionStatuses: [],
  factGroupId: '',
  createdByApp: '',
  limit: 10,
});

export function useRecentMemories() {
  return useQuery({
    queryKey: queryKeys.memories.list(recentFilters),
    queryFn: ({ signal }) => getRecentMemories(recentFilters.limit, signal),
    staleTime: 15_000,
  });
}
