import { createBrowserRouter, Navigate, type RouteObject } from 'react-router';

import { LegacyStudioRedirect } from './LegacyStudioRedirect';
import { RouteErrorPage } from './RouteErrorPage';
import { RunRedirect } from './RunRedirect';
import { ListShell } from '@/components/shell/ListShell';
import { NotFoundView } from '@/components/shell/NotFoundView';
import { DefinitionsPage } from '@/features/definitions/DefinitionsPage';
import { EditPage } from '@/features/editor/EditPage';
import { RunsPage } from '@/features/runs/RunsPage';
import { RunsTabPage } from '@/features/observe/RunsTabPage';

/**
 * The hierarchy: Runs (home) and Workflows are list pages with the sidebar; a workflow has
 * an Editor and a Runs tab, full width with one back button, and a run always opens in its
 * workflow's Runs tab.
 *
 * One pathless parent gives every page the same error page. The list shell also frames the
 * not-found page, so an address that names nothing keeps the sidebar.
 */
export const routes: RouteObject[] = [
  {
    errorElement: <RouteErrorPage />,
    children: [
      { path: '/', element: <Navigate to="/runs" replace /> },
      {
        element: <ListShell />,
        children: [
          { path: '/runs', element: <RunsPage /> },
          // A run's own address resolves to its workflow's Runs tab.
          { path: '/runs/:runId', element: <RunRedirect /> },
          { path: '/workflows', element: <DefinitionsPage /> },
          { path: '*', element: <NotFoundView kind="page" /> },
        ],
      },
      // An unsaved new workflow has no workflowId yet: creating it is deferred to the first
      // Save, because the Backend rejects a graph with no Output step. This is a distinct
      // static route rather than a special :workflowId value so a real workflow can never
      // collide with the literal segment "new".
      { path: '/workflows/new', element: <EditPage /> },
      { path: '/workflows/:workflowId', element: <EditPage /> },
      // One route for the tab with and without a run, so opening a run keeps the page.
      { path: '/workflows/:workflowId/runs/:runId?', element: <RunsTabPage /> },
      { path: '/studio/new', element: <LegacyStudioRedirect /> },
      { path: '/studio/:workflowId', element: <LegacyStudioRedirect /> },
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
    ],
  },
];

export const router = createBrowserRouter(routes);
