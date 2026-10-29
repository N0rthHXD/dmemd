import { lazy, Suspense } from 'react';
import type { ReactNode } from 'react';
import { createBrowserRouter, Navigate } from 'react-router-dom';
import { ErrorBoundary, RouteErrorElement } from '../shared/ui/ErrorBoundary';
import PageLoading from '../shared/ui/PageLoading';
import { routes } from '../shared/lib/routes';
import AppLayout from './layout/AppLayout';
import NotFoundPage from './NotFoundPage';

const OverviewPage = lazy(() => import('../features/overview/OverviewPage'));
const MemoriesPage = lazy(() => import('../features/memories/MemoriesPage'));
const MemoryDetailPage = lazy(() => import('../features/memories/MemoryDetailPage'));
const ObservationDetailPage = lazy(
  () => import('../features/observations/ObservationDetailPage'),
);
const RecallPage = lazy(() => import('../features/recall/RecallPage'));
const IngestionPage = lazy(() => import('../features/ingestions/IngestionPage'));
const JobsPage = lazy(() => import('../features/jobs/JobsPage'));
const JobDetailPage = lazy(() => import('../features/jobs/JobDetailPage'));
const ConflictsPage = lazy(() => import('../features/conflicts/ConflictsPage'));
const ConflictDetailPage = lazy(() => import('../features/conflicts/ConflictDetailPage'));
const ArtifactsPage = lazy(() => import('../features/artifacts/ArtifactsPage'));
const ArtifactDetailPage = lazy(() => import('../features/artifacts/ArtifactDetailPage'));
const ArtifactTargetsPage = lazy(() => import('../features/artifacts/ArtifactTargetsPage'));
const SourceDetailPage = lazy(() => import('../features/sources/SourceDetailPage'));
const SystemPage = lazy(() => import('../features/system/SystemPage'));

function page(node: ReactNode) {
  return (
    <ErrorBoundary>
      <Suspense fallback={<PageLoading />}>{node}</Suspense>
    </ErrorBoundary>
  );
}

export const router = createBrowserRouter(
  [
    {
      path: '/',
      element: <AppLayout />,
      errorElement: <RouteErrorElement />,
      children: [
        { index: true, element: <Navigate to={routes.overview} replace /> },
        { path: 'overview', element: page(<OverviewPage />) },
        { path: 'memories', element: page(<MemoriesPage />) },
        { path: 'memories/:id', element: page(<MemoryDetailPage />) },
        { path: 'observations', element: <Navigate to={routes.memories} replace /> },
        { path: 'observations/:id', element: page(<ObservationDetailPage />) },
        { path: 'recall', element: page(<RecallPage />) },
        { path: 'ingestions/new', element: page(<IngestionPage />) },
        { path: 'jobs', element: page(<JobsPage />) },
        { path: 'jobs/:id', element: page(<JobDetailPage />) },
        { path: 'conflicts', element: page(<ConflictsPage />) },
        { path: 'conflicts/:id', element: page(<ConflictDetailPage />) },
        { path: 'artifacts', element: page(<ArtifactsPage />) },
        { path: 'artifacts/:id', element: page(<ArtifactDetailPage />) },
        { path: 'artifact-targets', element: page(<ArtifactTargetsPage />) },
        { path: 'sources/:id', element: page(<SourceDetailPage />) },
        { path: 'system', element: page(<SystemPage />) },
        { path: '*', element: page(<NotFoundPage />) },
      ],
    },
  ],
  { basename: '/ui' },
);
