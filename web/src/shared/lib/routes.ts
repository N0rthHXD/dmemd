const id = (value: string) => encodeURIComponent(value);

export const routes = {
  overview: '/overview',
  memories: '/memories',
  memory: (memoryId: string) => `/memories/${id(memoryId)}`,
  memoryVersion: (memoryId: string, version: number) =>
    `/memories/${id(memoryId)}?version=${version}`,
  recall: '/recall',
  newIngestion: '/ingestions/new',
  jobs: '/jobs',
  job: (jobId: string) => `/jobs/${id(jobId)}`,
  conflicts: '/conflicts',
  conflict: (conflictId: string) => `/conflicts/${id(conflictId)}`,
  artifacts: '/artifacts',
  artifact: (artifactId: string) => `/artifacts/${id(artifactId)}`,
  artifactVersion: (artifactId: string, version: number) =>
    `/artifacts/${id(artifactId)}?version=${version}`,
  artifactTargets: '/artifact-targets',
  source: (sourceId: string) => `/sources/${id(sourceId)}`,
  system: '/system',
} as const;

export function versionParam(value: string | null): number | null {
  if (!value) {
    return null;
  }
  const parsed = Number(value);
  return Number.isInteger(parsed) && parsed > 0 ? parsed : null;
}
