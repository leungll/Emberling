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
  { path: '/studio/:workflowId', element: <EditPage /> },
  { path: '/runs/:runId', element: <ObservePage /> },
];

export const router = createBrowserRouter(routes);
