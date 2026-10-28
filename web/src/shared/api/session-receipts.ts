import { useQuery, useQueryClient } from '@tanstack/react-query';
import type { QueryClient } from '@tanstack/react-query';
import { queryKeys } from './query-keys';
import type { ExperienceReceipt, SubmitResponse } from './types';

export interface IngestionSessionReceipt {
  source_id: string;
  job_id: string;
  status: SubmitResponse['status'];
  received_at: string;
}

export interface ExperienceSessionReceipt extends ExperienceReceipt {
  recorded_at: string;
}

const MAX_SESSION_RECEIPTS = 100;

function useSessionList<T>(key: readonly string[]) {
  const queryClient = useQueryClient();
  return useQuery<T[]>({
    queryKey: key,
    queryFn: () => queryClient.getQueryData<T[]>(key) ?? [],
    initialData: () => queryClient.getQueryData<T[]>(key) ?? [],
    initialDataUpdatedAt: 0,
    staleTime: Infinity,
    gcTime: Infinity,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
}

/** Session-only receipts live in QueryClient memory and disappear on reload. */
export function useSessionIngestions() {
  return useSessionList<IngestionSessionReceipt>(queryKeys.session.ingestions);
}

export function useSessionExperiences() {
  return useSessionList<ExperienceSessionReceipt>(queryKeys.session.experiences);
}

export function recordSessionIngestion(
  queryClient: QueryClient,
  receipt: Omit<IngestionSessionReceipt, 'received_at'>,
): IngestionSessionReceipt {
  const item: IngestionSessionReceipt = { ...receipt, received_at: new Date().toISOString() };
  queryClient.setQueryData<IngestionSessionReceipt[]>(
    queryKeys.session.ingestions,
    (previous = []) =>
      [item, ...previous.filter((entry) => entry.job_id !== item.job_id)].slice(
        0,
        MAX_SESSION_RECEIPTS,
      ),
  );
  return item;
}

export function recordSessionExperience(
  queryClient: QueryClient,
  receipt: ExperienceReceipt,
): ExperienceSessionReceipt {
  const item: ExperienceSessionReceipt = { ...receipt, recorded_at: new Date().toISOString() };
  queryClient.setQueryData<ExperienceSessionReceipt[]>(
    queryKeys.session.experiences,
    (previous = []) =>
      [item, ...previous.filter((entry) => entry.id !== item.id)].slice(0, MAX_SESSION_RECEIPTS),
  );
  return item;
}

export function removeSessionIngestions(queryClient: QueryClient, sourceIds?: string[]) {
  if (!sourceIds || sourceIds.length === 0) {
    queryClient.setQueryData<IngestionSessionReceipt[]>(queryKeys.session.ingestions, []);
    return;
  }
  const removed = new Set(sourceIds);
  queryClient.setQueryData<IngestionSessionReceipt[]>(
    queryKeys.session.ingestions,
    (previous = []) => previous.filter((entry) => !removed.has(entry.source_id)),
  );
}

export function removeSessionExperience(queryClient: QueryClient, experienceId: string) {
  queryClient.setQueryData<ExperienceSessionReceipt[]>(
    queryKeys.session.experiences,
    (previous = []) => previous.filter((entry) => entry.id !== experienceId),
  );
}
