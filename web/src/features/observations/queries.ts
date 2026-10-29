import { useQuery } from '@tanstack/react-query';
import { normalizeObservationFilters, queryKeys } from '../../shared/api/query-keys';
import type { ObservationListFilters } from '../../shared/api/types';
import {
  getObservation,
  getObservationEvidence,
  getObservationRelations,
  listObservations,
} from './api';

export function useObservations(filters: ObservationListFilters) {
  const normalized = normalizeObservationFilters(filters);
  return useQuery({
    queryKey: queryKeys.observations.list(normalized),
    queryFn: ({ signal }) => listObservations(normalized, signal),
    staleTime: 10_000,
  });
}

export function useObservation(id: string) {
  return useQuery({
    queryKey: queryKeys.observations.detail(id),
    queryFn: ({ signal }) => getObservation(id, signal),
    enabled: id !== '',
  });
}

export function useObservationEvidence(id: string) {
  return useQuery({
    queryKey: queryKeys.observations.evidence(id),
    queryFn: ({ signal }) => getObservationEvidence(id, signal),
    enabled: id !== '',
  });
}

export function useObservationRelations(id: string) {
  return useQuery({
    queryKey: queryKeys.observations.relations(id),
    queryFn: ({ signal }) => getObservationRelations(id, signal),
    enabled: id !== '',
  });
}
