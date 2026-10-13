import { render, screen } from '@testing-library/react';
import { createMemoryRouter, RouterProvider, useLocation } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { LegacyStudioRedirect } from './LegacyStudioRedirect';
import { RouteErrorPage } from './RouteErrorPage';
import { routes } from './routes';
import { RunRedirect } from './RunRedirect';
import { requestUrl } from '@/test/requestUrl';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const RUN = {
  run: {
    id: 'run_0123456789abcdef',
    workflowId: 'wf_9',
    definitionVersion: 2,
    status: 'COMPLETED',
    input: {},
    output: null,
    error: null,
    createdAt: '2026-08-03T12:00:00Z',
    startedAt: '2026-08-03T12:00:01Z',
    completedAt: '2026-08-03T12:00:05Z',
  },
  nodeRuns: [],
  lastSeq: 3,
};

beforeEach(() => {
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = requestUrl(input);
      if (url === '/ready') return Promise.resolve(jsonResponse(200, { status: 'ready' }));
      if (url === '/api/definitions') return Promise.resolve(jsonResponse(200, { items: [] }));
      if (url === '/api/runs/run_0123456789abcdef') return Promise.resolve(jsonResponse(200, RUN));
      if (url.startsWith('/api/runs/')) {
        return Promise.resolve(
          jsonResponse(404, { error: { code: 'RUN_NOT_FOUND', message: 'run not found' } }),
        );
      }
      throw new Error(`unexpected fetch: ${url}`);
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function Where() {
  const { pathname, search } = useLocation();
  return <p data-testid="where">{`${pathname}${search}`}</p>;
}

describe('routes', () => {
  it('opens Runs at the root address', async () => {
    const router = createMemoryRouter(routes, { initialEntries: ['/'] });
    render(<RouterProvider router={router} />);

    expect(await screen.findByRole('heading', { name: 'Runs' })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/runs');
    expect(screen.getByRole('link', { name: 'Runs' })).toHaveAttribute('aria-current', 'page');
  });

  it('shows not found inside the sidebar shell, with the way back to Runs', async () => {
    const router = createMemoryRouter(routes, { initialEntries: ['/no/such/page'] });
    render(<RouterProvider router={router} />);

    expect(await screen.findByText('This page doesn’t exist.')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Go to Runs' })).toHaveAttribute('href', '/runs');
    expect(screen.getByRole('navigation', { name: 'Main' })).toBeInTheDocument();
    expect(screen.queryByText(/Hey developer/)).toBeNull();
    expect(document.title).toBe('Not found – Emberling');
  });

  it('replaces a page that throws while rendering with a calm error page', async () => {
    // React reports the caught render error on the console; it is expected here.
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    function Broken(): never {
      throw new Error('render failed');
    }
    const router = createMemoryRouter(
      [{ path: '/', element: <Broken />, errorElement: <RouteErrorPage /> }],
      { initialEntries: ['/'] },
    );
    render(<RouterProvider router={router} />);

    expect(
      await screen.findByRole('heading', { name: 'This page couldn’t be shown.' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Go to Runs' })).toHaveAttribute('href', '/runs');
    expect(screen.queryByText(/Hey developer/)).toBeNull();
  });

  it('keeps the old Editor addresses working, with their query', async () => {
    const router = createMemoryRouter(
      [
        { path: '/studio/:workflowId', element: <LegacyStudioRedirect /> },
        { path: '/workflows/:workflowId', element: <Where /> },
      ],
      { initialEntries: ['/studio/wf_1?run=1'] },
    );
    render(<RouterProvider router={router} />);
    expect(await screen.findByTestId('where')).toHaveTextContent('/workflows/wf_1?run=1');
  });

  it('opens a run’s own address in its workflow’s Runs tab, entered from Runs', async () => {
    const router = createMemoryRouter(
      [
        { path: '/runs/:runId', element: <RunRedirect /> },
        { path: '/workflows/:workflowId/runs/:runId', element: <Where /> },
      ],
      { initialEntries: ['/runs/run_0123456789abcdef'] },
    );
    render(<RouterProvider router={router} />);

    expect(await screen.findByTestId('where')).toHaveTextContent(
      '/workflows/wf_9/runs/run_0123456789abcdef?from=runs',
    );
    // The run's address is replaced, not stacked: Back skips it.
    expect(router.state.historyAction).toBe('REPLACE');
  });

  it('says a run that does not exist does not exist', async () => {
    const router = createMemoryRouter(routes, { initialEntries: ['/runs/run_missing'] });
    render(<RouterProvider router={router} />);

    expect(await screen.findByText(/^Run .+ doesn’t exist\.$/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Go to Runs' })).toHaveAttribute('href', '/runs');
  });

  it('gives every page the shared error page', () => {
    const [shell] = routes;
    expect(shell?.errorElement).toBeDefined();
    const paths = (shell?.children ?? []).flatMap((route) =>
      route.path ? [route.path] : (route.children ?? []).map((c) => c.path),
    );
    expect(paths).toEqual([
      '/',
      '/runs',
      '/runs/:runId',
      '/workflows',
      '*',
      '/workflows/new',
      '/workflows/:workflowId',
      '/workflows/:workflowId/runs',
      '/workflows/:workflowId/runs/:runId',
      '/studio/new',
      '/studio/:workflowId',
      '/__ui',
    ]);
  });
});
