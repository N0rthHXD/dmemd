import type { components } from './schema.gen';

type Schemas = components['schemas'];

export type ErrorBody = Schemas['Error'];
export type Health = Schemas['Health'];
export type SubmitRequest = Schemas['SubmitRequest'];
export type SubmitResponse = Schemas['SubmitResponse'];
export type Memory = Schemas['Memory'];
export type MemoryDetail = Schemas['MemoryDetail'];
export type MemoryList = Schemas['MemoryList'];
export type TemporalDetail = Schemas['TemporalDetail'];
export type TemporalDetails = Schemas['TemporalDetails'];
export type Procedure = Schemas['Procedure'];
export type AssertionQualifiers = Schemas['AssertionQualifiers'];
export type Relation = Schemas['Relation'];
export type EvolutionEvent = Schemas['EvolutionEvent'];
export type Revision = Schemas['Revision'];
export type RevisionList = Schemas['RevisionList'];
export type Evidence = Schemas['Evidence'];
export type EvidenceList = Schemas['EvidenceList'];
export type SearchRequest = Schemas['SearchRequest'];
export type SearchFilters = Schemas['SearchFilters'];
export type SearchResponse = Schemas['SearchResponse'];
export type SearchItem = Schemas['SearchItem'];
export type SearchDiagnostics = Schemas['SearchDiagnostics'];
export type ArtifactItem = Schemas['ArtifactItem'];
export type ArtifactMeta = Schemas['ArtifactMeta'];
export type ArtifactRevision = Schemas['ArtifactRevision'];
export type ArtifactDetail = Schemas['ArtifactDetail'];
export type ArtifactList = Schemas['ArtifactList'];
export type ArtifactTarget = Schemas['ArtifactTarget'];
export type ArtifactTargetJob = Schemas['ArtifactTargetJob'];
export type ArtifactTargetList = Schemas['ArtifactTargetList'];
export type SkillContent = Schemas['SkillContent'];
export type ExperienceSubmit = Schemas['ExperienceSubmit'];
export type ExperienceReceipt = Schemas['ExperienceReceipt'];
export type Conflict = Schemas['Conflict'];
export type ConflictMember = Schemas['ConflictMember'];
export type ConflictEvent = Schemas['ConflictEvent'];
export type ConflictSummary = Schemas['ConflictSummary'];
export type ConflictList = Schemas['ConflictList'];
export type ConflictResolveRequest = Schemas['ConflictResolveRequest'];
export type Source = Schemas['Source'];
export type RedactResponse = Schemas['RedactResponse'];
export type ForgetResponse = Schemas['ForgetResponse'];
export type Job = Schemas['Job'];
export type EmbeddingStatus = Schemas['EmbeddingStatus'];
export type EmbeddingProbe = Schemas['EmbeddingProbe'];

export type MemoryKind = Memory['kind'];
export type MemoryState = Memory['state'];
export type AssertionStatus = Memory['assertion_status'];
export type MemoryPrecision = Memory['event_time_precision'];
export type ArtifactKind = ArtifactMeta['kind'];
export type ArtifactState = ArtifactMeta['state'];
export type TargetStatus = ArtifactTarget['status'];
export type JobStatus = Job['status'];
export type ConflictAction = ConflictResolveRequest['action'];

export const memoryKinds: MemoryKind[] = ['fact', 'preference', 'event', 'procedure'];
export const memoryStates: MemoryState[] = ['active', 'archived', 'deleted'];
export const activeMemoryStates: MemoryState[] = ['active', 'archived'];
export const assertionStatuses: AssertionStatus[] = ['accepted', 'pending', 'rejected'];
export const artifactKinds: ArtifactKind[] = [
  'topic_summary',
  'scope_summary',
  'profile',
  'experience_case',
  'skill',
];
export const artifactStates: ArtifactState[] = ['active', 'stale', 'disabled'];
export const targetStatuses: TargetStatus[] = [
  'queued',
  'running',
  'waiting',
  'active',
  'stale',
  'disabled',
  'no_eligible_input',
  'model_unavailable',
  'generation_failed',
  'invalid_output',
];

export interface MemoryListFilters {
  kinds: MemoryKind[];
  states: MemoryState[];
  assertionStatuses: AssertionStatus[];
  factGroupId: string;
  createdByApp: string;
  updatedAfter?: string;
  updatedBefore?: string;
  limit: number;
}

export interface ArtifactListFilters {
  scopeKey: string;
  kind?: ArtifactKind;
  state?: ArtifactState;
  reasonCode: string;
  limit: number;
}

export interface ArtifactTargetFilters {
  scopeKey: string;
  kind?: ArtifactKind;
  status?: TargetStatus;
  reasonCode: string;
  limit: number;
}

export interface ConflictListFilters {
  status: 'open' | 'resolved';
  limit: number;
}

export interface NormalizedRecallFilters {
  kinds?: MemoryKind[];
  states?: MemoryState[];
  asOf?: string;
  eventAt?: string;
  createdByApp?: string;
  updatedAfter?: string;
  updatedBefore?: string;
}

export interface NormalizedRecallRequest {
  query: string;
  scopeKey?: string;
  layers?: Array<'atomic' | 'summary' | 'experience' | 'skill'>;
  mode: 'keyword' | 'hybrid';
  topK: number;
  maxContextTokens?: number;
  explain: boolean;
  filters: NormalizedRecallFilters;
}
