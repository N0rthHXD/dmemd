import { useQuery } from '@tanstack/react-query';
import { isRetryableError } from '../../shared/api/errors';
import { queryKeys } from '../../shared/api/query-keys';
import { getJob } from './api';

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
