import { useQuery } from '@tanstack/react-query';
import { queryKeys } from '../../shared/api/query-keys';
import { getEmbeddingStatus, getHealth, getReadiness } from './api';

const STATUS_INTERVAL = 30_000;

export function useSystemHealth(poll = false) {
  return useQuery({
    queryKey: queryKeys.system.health,
    queryFn: ({ signal }) => getHealth(signal),
    refetchInterval: poll ? STATUS_INTERVAL : undefined,
    staleTime: 10_000,
  });
}

export function useSystemReadiness(poll = false) {
  return useQuery({
    queryKey: queryKeys.system.ready,
    queryFn: ({ signal }) => getReadiness(signal),
    refetchInterval: poll ? STATUS_INTERVAL : undefined,
    staleTime: 10_000,
  });
}

export function useEmbeddingStatus() {
  return useQuery({
    queryKey: queryKeys.system.embedding,
    queryFn: ({ signal }) => getEmbeddingStatus(signal),
    refetchInterval: STATUS_INTERVAL,
    staleTime: 10_000,
  });
}
