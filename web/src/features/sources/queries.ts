import { useQuery } from '@tanstack/react-query';
import { queryKeys } from '../../shared/api/query-keys';
import { getSource } from './api';

export function useSource(id: string) {
  return useQuery({
    queryKey: queryKeys.sources.detail(id),
    queryFn: ({ signal }) => getSource(id, signal),
    enabled: Boolean(id),
  });
}
