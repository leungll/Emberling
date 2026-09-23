import { createBrowserRouter } from 'react-router';

import { DefinitionsPage } from '@/features/definitions/DefinitionsPage';
import { EditPage } from '@/features/editor/EditPage';
import { ObservePage } from '@/features/observe/ObservePage';

/**
 * The MVP has exactly three pages. An Observe URL carries a Run ID, which is one of the
 * three ways to reach a historical Run; there is no Run search page.
 */
export const routes = [
  { path: '/', element: <DefinitionsPage /> },
  // An unsaved new Definition has no workflowId yet: POST /definitions creates the first
  // version with no baseVersion (08 §3.1), but since backend/internal/runtime/graph.go
  // rejects a graph with no Output Node, that POST is deferred until the first Save. This
  // is a distinct static route rather than a special :workflowId value so an actual
  // Definition can never collide with the literal segment "new".
  { path: '/studio/new', element: <EditPage /> },
  { path: '/studio/:workflowId', element: <EditPage /> },
  { path: '/runs/:runId', element: <ObservePage /> },
];

export const router = createBrowserRouter(routes);
