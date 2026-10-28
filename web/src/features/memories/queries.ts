import { useQuery } from '@tanstack/react-query';
import { queryKeys } from '../../shared/api/query-keys';
import type { MemoryListFilters } from '../../shared/api/types';
import {
  getMemory,
  getMemoryEvidence,
  getMemoryRevision,
  listMemories,
  listMemoryHistory,
} from './api';

export function useMemories(filters: MemoryListFilters) {
  return useQuery({
    queryKey: queryKeys.memories.list(filters),
    queryFn: ({ signal }) => listMemories(filters, signal),
  });
}

export function useMemory(id: string) {
  return useQuery({
    queryKey: queryKeys.memories.detail(id),
    queryFn: ({ signal }) => getMemory(id, signal),
    enabled: Boolean(id),
  });
}

export function useMemoryHistory(id: string, limit: number, enabled: boolean) {
  return useQuery({
    queryKey: queryKeys.memories.history(id, limit),
    queryFn: ({ signal }) => listMemoryHistory(id, limit, signal),
    enabled: Boolean(id) && enabled,
  });
}

export function useMemoryRevision(id: string, version: number | null) {
  return useQuery({
    queryKey: queryKeys.memories.revision(id, version ?? 0),
    queryFn: ({ signal }) => getMemoryRevision(id, version as number, signal),
    enabled: Boolean(id) && version !== null,
  });
}

export function useMemoryEvidence(id: string, version: number | null, enabled: boolean) {
  return useQuery({
    queryKey: queryKeys.memories.evidence(id, version),
    queryFn: ({ signal }) => getMemoryEvidence(id, version, signal),
    enabled: Boolean(id) && enabled,
  });
}
