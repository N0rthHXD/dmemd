import type { QueryClient } from '@tanstack/react-query';
import { queryKeys } from './query-keys';
import { removeSessionExperience, removeSessionIngestions } from './session-receipts';

const businessQueryRoots = [
  queryKeys.memories.all,
  queryKeys.conflicts.all,
  queryKeys.artifacts.all,
  queryKeys.targets.all,
  queryKeys.skills.all,
  queryKeys.sources.all,
  queryKeys.jobs.all,
] as const;

function isBusinessQuery(queryKey: readonly unknown[]): boolean {
  const root = String(queryKey[0] ?? '');
  return (
    root === 'memories' ||
    root === 'conflicts' ||
    root === 'artifacts' ||
    root === 'artifact-targets' ||
    root === 'skills' ||
    root === 'sources' ||
    root === 'jobs' ||
    root === 'recall'
  );
}

async function invalidateRoots(queryClient: QueryClient, roots: readonly (readonly unknown[])[]) {
  await Promise.allSettled(
    roots.map((queryKey) => queryClient.invalidateQueries({ queryKey })),
  );
}

/** Clear stale recall output after any write; never automatically re-run recall. */
export async function clearRecallResults(queryClient: QueryClient) {
  await queryClient.cancelQueries({ queryKey: ['recall'] });
  queryClient.removeQueries({ queryKey: ['recall'] });
}

export async function invalidateAfterMemoryChange(queryClient: QueryClient) {
  await invalidateRoots(queryClient, [
    queryKeys.memories.all,
    queryKeys.conflicts.all,
    queryKeys.artifacts.all,
    queryKeys.targets.all,
    queryKeys.skills.all,
    queryKeys.system.embedding,
  ]);
  await clearRecallResults(queryClient);
}

export async function invalidateAfterConflictChange(queryClient: QueryClient) {
  await invalidateAfterMemoryChange(queryClient);
}

export async function invalidateAfterJobTerminal(queryClient: QueryClient) {
  await invalidateAfterMemoryChange(queryClient);
}

export async function invalidateAfterExperienceChange(queryClient: QueryClient) {
  await invalidateRoots(queryClient, [
    queryKeys.artifacts.all,
    queryKeys.targets.all,
    queryKeys.skills.all,
    queryKeys.system.embedding,
  ]);
  await clearRecallResults(queryClient);
}

/** Source redaction can affect quoted evidence and cancelled jobs everywhere. */
export async function clearAfterSourceRedaction(queryClient: QueryClient, sourceId: string) {
  await queryClient.cancelQueries({ predicate: (query) => isBusinessQuery(query.queryKey) });
  queryClient.removeQueries({
    predicate: (query) => {
      const root = String(query.queryKey[0] ?? '');
      if (root === 'sources') {
        return query.queryKey[2] !== sourceId;
      }
      return isBusinessQuery(query.queryKey);
    },
  });
  removeSessionIngestions(queryClient, [sourceId]);
  await queryClient.invalidateQueries({ queryKey: queryKeys.sources.detail(sourceId) });
}

export interface PermanentTarget {
  memoryId?: string;
  sourceId?: string;
  sourceIds?: string[];
  experienceId?: string;
  experienceIds?: string[];
}

/**
 * The server may remove a closure that the UI cannot enumerate. Business caches
 * are therefore cleared conservatively; the caller keeps the committed result
 * in local state for display.
 */
export async function clearAfterPermanentRemoval(
  queryClient: QueryClient,
  target: PermanentTarget,
) {
  await queryClient.cancelQueries({ predicate: (query) => isBusinessQuery(query.queryKey) });
  queryClient.removeQueries({ predicate: (query) => isBusinessQuery(query.queryKey) });
  const sourceIds = [
    ...(target.sourceId ? [target.sourceId] : []),
    ...(target.sourceIds ?? []),
  ];
  if (sourceIds.length > 0) {
    removeSessionIngestions(queryClient, sourceIds);
  } else if (target.memoryId) {
    removeSessionIngestions(queryClient);
  }
  const experienceIds = [
    ...(target.experienceId ? [target.experienceId] : []),
    ...(target.experienceIds ?? []),
  ];
  experienceIds.forEach((experienceId) =>
    removeSessionExperience(queryClient, experienceId),
  );
  await queryClient.invalidateQueries({ queryKey: queryKeys.system.embedding });
}

export function allBusinessQueryRoots() {
  return businessQueryRoots;
}
