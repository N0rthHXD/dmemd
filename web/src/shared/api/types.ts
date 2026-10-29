import type { components } from './schema.gen';

type Schemas = components['schemas'];

export type ErrorBody = Schemas['Error'];
export type Health = Schemas['Health'];
export type SubmitRequest = Schemas['SubmitRequest'];
export type SubmitResponse = Schemas['SubmitResponse'];

// Canonical current view.
export type Memory = Schemas['Memory'];
export type MemoryDetail = Schemas['MemoryDetail'];
export type MemoryList = Schemas['MemoryList'];
export type Relation = Schemas['Relation'];
export type RelationList = Schemas['RelationList'];
export type DerivedDependent = Schemas['DerivedDependent'];
export type Revision = Schemas['Revision'];
export type RevisionList = Schemas['RevisionList'];
export type Evidence = Schemas['Evidence'];
export type EvidenceList = Schemas['EvidenceList'];

// L0 raw observations.
export type Observation = Schemas['Observation'];
export type ObservationList = Schemas['ObservationList'];

// Derived L1-L4 memories.
export type ArtifactItem = Schemas['ArtifactMeta'];
export type ArtifactMeta = Schemas['ArtifactMeta'];
export type ArtifactRevision = Schemas['ArtifactRevision'];
export type ArtifactDetail = Schemas['ArtifactDetail'];
export type ArtifactList = Schemas['ArtifactList'];
export type ArtifactDependency = Schemas['ArtifactDependency'];
export type ArtifactTarget = Schemas['ArtifactTarget'];
export type ArtifactTargetJob = Schemas['ArtifactTargetJob'];
export type ArtifactTargetList = Schemas['ArtifactTargetList'];
export type SkillContent = Schemas['SkillContent'];
export type ExperienceSubmit = Schemas['ExperienceSubmit'];
export type ExperienceReceipt = Schemas['ExperienceReceipt'];

// Layered recall.
export type RecallRequest = Schemas['RecallRequest'];
export type SearchFilters = Schemas['SearchFilters'];
export type RecallResponse = Schemas['RecallResponse'];
export type SearchItem = Schemas['SearchItem'];
export type RecallObservationItem = Schemas['RecallObservationItem'];
export type RecallDerivedItem = Schemas['RecallDerivedItem'];
export type SearchDiagnostics = Schemas['SearchDiagnostics'];
export type Explanation = Schemas['Explanation'];

// Management.
export type Conflict = Schemas['Conflict'];
export type ConflictMember = Schemas['ConflictMember'];
export type ConflictEvent = Schemas['ConflictEvent'];
export type ConflictSummary = Schemas['ConflictSummary'];
export type ConflictList = Schemas['ConflictList'];
export type ConflictResolveRequest = Schemas['ConflictResolveRequest'];
export type Source = Schemas['Source'];
export type SourceSegment = Schemas['SourceSegment'];
export type RedactResponse = Schemas['RedactResponse'];
export type ForgetResponse = Schemas['ForgetResponse'];

// Jobs.
export type Job = Schemas['Job'];
export type JobList = Schemas['JobList'];
export type GovernanceJob = Schemas['GovernanceJob'];
export type GovernanceJobList = Schemas['GovernanceJobList'];

export type EmbeddingStatus = Schemas['EmbeddingStatus'];
export type EmbeddingProbe = Schemas['EmbeddingProbe'];

export type MemoryKind = Memory['kind'];
export type MemoryState = Memory['state'];
export type AssertionStatus = Memory['assertion_status'];
export type ObservationKind = Observation['kind'];
export type ObservationStatus = Observation['status'];
export type ObservationContentMode = Observation['content_mode'];
export type ArtifactKind = ArtifactMeta['kind'];
export type ArtifactLayer = ArtifactMeta['layer'];
export type ArtifactState = ArtifactMeta['state'];
export type TargetStatus = ArtifactTarget['status'];
export type JobStatus = Job['status'];
export type JobStage = Job['stage'];
export type JobCategory = GovernanceJob['category'];
export type ConflictAction = ConflictResolveRequest['action'];
export type RecallLayer = 'observations' | 'memories' | 'l1' | 'l2' | 'l3' | 'l4';
export type RecallQueryMode = 'overview' | 'detail' | 'history' | 'conflict' | 'guidance';

export const memoryKinds: MemoryKind[] = ['fact', 'preference', 'event', 'procedure'];
export const memoryStates: MemoryState[] = ['active', 'archived', 'deleted'];
export const activeMemoryStates: MemoryState[] = ['active', 'archived'];
export const assertionStatuses: AssertionStatus[] = ['accepted', 'pending', 'rejected'];
export const observationKinds: ObservationKind[] = ['fact', 'preference', 'event', 'procedure'];
export const observationStatuses: ObservationStatus[] = [
  'observed',
  'pending',
  'retracted',
  'deleted',
];
export const observationContentModes: ObservationContentMode[] = ['extractive', 'normalized'];
export const artifactKinds: ArtifactKind[] = [
  'local_summary',
  'topic_summary',
  'scope_summary',
  'profile',
  'experience_case',
  'skill',
  'guidance',
];
export const artifactLayers: ArtifactLayer[] = ['l1', 'l2', 'l3', 'l4'];
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
export const recallLayers: RecallLayer[] = ['observations', 'memories', 'l1', 'l2', 'l3', 'l4'];
export const recallQueryModes: RecallQueryMode[] = [
  'overview',
  'detail',
  'history',
  'conflict',
  'guidance',
];
export const jobStages: JobStage[] = [
  'received',
  'extracting',
  'observations_saved',
  'governance_pending',
  'memory_materialized',
  'aggregation_pending',
  'completed',
  'failed',
];

export interface MemoryListFilters {
  kinds: MemoryKind[];
  states: MemoryState[];
  assertionStatuses: AssertionStatus[];
  updatedAfter?: string;
  updatedBefore?: string;
  limit: number;
}

export interface ObservationListFilters {
  sourceId: string;
  scopeKey: string;
  kind?: ObservationKind;
  status?: ObservationStatus;
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

export interface GovernorJobFilters {
  category?: JobCategory;
  kind?: GovernanceJob['kind'];
  status?: JobStatus;
  targetKey: string;
  limit: number;
}

export interface NormalizedRecallFilters {
  kinds?: MemoryKind[];
  states?: MemoryState[];
  updatedAfter?: string;
  updatedBefore?: string;
}

export interface NormalizedRecallRequest {
  query: string;
  scopeKey?: string;
  queryMode?: RecallQueryMode;
  layers?: RecallLayer[];
  mode: 'keyword' | 'hybrid';
  derivedStates?: Array<'active' | 'stale'>;
  topK: number;
  maxContextTokens?: number;
  explain: boolean;
  filters: NormalizedRecallFilters;
}
