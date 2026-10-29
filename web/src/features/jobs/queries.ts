import { useQuery } from '@tanstack/react-query';
import { isRetryableError } from '../../shared/api/errors';
import { normalizeGovernanceJobFilters, queryKeys } from '../../shared/api/query-keys';
import type { GovernorJobFilters } from '../../shared/api/types';
import { getJob, listGovernanceJobs, listJobs } from './api';

export function useJob(id: string) {
  return useQuery({
    queryKey: queryKeys.jobs.detail(id),
    queryFn: ({ signal }) => getJob(id, signal),
    enabled: Boolean(id),
    refetchInterval: (query) => {
      const data = query.state.data;
      if (!data) {
        return false;
      }
      if (data.status === 'succeeded' || data.status === 'failed' || data.status === 'cancelled') {
        return false;
      }
      return data.status === 'waiting' ? 10_000 : 2_000;
    },
    refetchIntervalInBackground: false,
    retry: (failureCount, error) => failureCount < 3 && isRetryableError(error),
    retryDelay: (attempt) => Math.min(1_000 * 2 ** attempt, 30_000),
  });
}

export function useJobs(status: string, limit: number) {
  return useQuery({
    queryKey: queryKeys.jobs.list(status, limit),
    queryFn: ({ signal }) => listJobs(status, limit, signal),
    staleTime: 5_000,
  });
}

export function useGovernanceJobs(filters: GovernorJobFilters) {
  const normalized = normalizeGovernanceJobFilters(filters);
  return useQuery({
    queryKey: queryKeys.jobs.governance.list(normalized),
    queryFn: ({ signal }) => listGovernanceJobs(normalized, signal),
    staleTime: 5_000,
  });
}
