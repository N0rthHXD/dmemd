import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { queryKeys } from '../../shared/api/query-keys';
import type {
  ArtifactKind,
  ArtifactListFilters,
  ArtifactState,
  ArtifactTargetFilters,
} from '../../shared/api/types';
import {
  getArtifactDetail,
  getSkillContent,
  listArtifactTargets,
  listArtifacts,
} from './api';

export function useArtifacts(filters: ArtifactListFilters) {
  return useInfiniteQuery({
    queryKey: queryKeys.artifacts.list(filters),
    queryFn: ({ pageParam, signal }) => listArtifacts(filters, pageParam, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
  });
}

export function useArtifactLayer(
  kinds: ArtifactKind[],
  state: ArtifactState | undefined,
  limit = 50,
) {
  const normalizedKinds = [...kinds].sort();
  return useQuery({
    queryKey: queryKeys.artifacts.layer(normalizedKinds, state, limit),
    queryFn: async ({ signal }) => {
      const pages = await Promise.all(
        normalizedKinds.map((kind) =>
          listArtifacts(
            { scopeKey: '', kind, state, reasonCode: '', limit },
            undefined,
            signal,
          ),
        ),
      );
      return pages
        .flatMap((page) => page.items)
        .sort((left, right) => right.updated_at.localeCompare(left.updated_at));
    },
    staleTime: 15_000,
  });
}

export function useArtifactTargets(filters: ArtifactTargetFilters) {
  return useInfiniteQuery({
    queryKey: queryKeys.targets.list(filters),
    queryFn: ({ pageParam, signal }) => listArtifactTargets(filters, pageParam, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    refetchInterval: (query) => {
      const pages = query.state.data?.pages;
      if (!pages) {
        return false;
      }
      const active = pages.some((page) =>
        page.items.some(
          (item) => item.status === 'queued' || item.status === 'running' || item.status === 'waiting',
        ),
      );
      return active ? 10_000 : false;
    },
    refetchIntervalInBackground: false,
  });
}

export function useArtifactDetail(id: string, version: number | null) {
  return useQuery({
    queryKey: queryKeys.artifacts.detail(id, version),
    queryFn: ({ signal }) => getArtifactDetail(id, version, signal),
    enabled: Boolean(id),
  });
}

export function useSkillContent(id: string, version: number | null, enabled: boolean) {
  return useQuery({
    queryKey: queryKeys.skills.content(id, version),
    queryFn: ({ signal }) => getSkillContent(id, version, signal),
    enabled: Boolean(id) && enabled,
  });
}
