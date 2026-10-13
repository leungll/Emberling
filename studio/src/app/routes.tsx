import { createBrowserRouter, type RouteObject } from 'react-router';

import { NotFoundPage, RouteErrorPage } from './RouteErrorPage';
import { DefinitionsPage } from '@/features/definitions/DefinitionsPage';
import { EditPage } from '@/features/editor/EditPage';
import { ObservePage } from '@/features/observe/ObservePage';

/**
 * The MVP has exactly three pages. An Observe URL carries a Run ID, which is one of the
 * three ways to reach a historical Run; there is no Run search page.
 *
 * One pathless parent gives every page the same error page, and the final catch-all
 * answers any other address with a not-found page that links back to Definitions.
 */
export const routes: RouteObject[] = [
  {
    errorElement: <RouteErrorPage />,
    children: [
      { path: '/', element: <DefinitionsPage /> },
      // An unsaved new Definition has no workflowId yet: POST /definitions creates the first
      // version with no baseVersion, but since backend/internal/runtime/graph.go
      // rejects a graph with no Output Node, that POST is deferred until the first Save. This
      // is a distinct static route rather than a special :workflowId value so an actual
      // Definition can never collide with the literal segment "new".
      { path: '/studio/new', element: <EditPage /> },
      { path: '/studio/:workflowId', element: <EditPage /> },
      { path: '/runs/:runId', element: <ObservePage /> },
      // The component gallery for the UI review suite; development builds only, so the
      // production bundle does not contain it.
      ...(import.meta.env.DEV
        ? [
            {
              path: '/__ui',
              lazy: async () => ({ Component: (await import('./dev/UiGallery')).UiGallery }),
            },
          ]
        : []),
      { path: '*', element: <NotFoundPage /> },
    ],
  },
];

export const router = createBrowserRouter(routes);
