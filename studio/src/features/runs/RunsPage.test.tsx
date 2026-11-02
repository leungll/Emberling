import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { flushSync } from 'react-dom';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { RunsPage } from './RunsPage';
import { requestUrl } from '@/test/requestUrl';

const RUN = {
  id: 'run_1',
  workflowId: 'wf_1',
  workflowName: 'Answer a brief',
  definitionVersion: 1,
  latestVersion: 1,
  status: 'COMPLETED',
  createdAt: '2026-10-01T10:00:00Z',
  startedAt: '2026-10-01T10:00:00Z',
  completedAt: '2026-10-01T10:00:05Z',
  error: null,
  failedStep: null,
  waitingSteps: [],
  cost: { totals: [{ amount: '0.04', currency: 'USD' }], unknownCalls: 1 },
};

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}

beforeEach(() => {
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = requestUrl(input);
      if (url === '/api/definitions') return Promise.resolve(jsonResponse({ items: [] }));
      if (url.startsWith('/api/runs?')) {
        return Promise.resolve(
          jsonResponse({
            items: [RUN],
            nextCursor: null,
            prevCursor: null,
            counts: { all: 2, running: 0, paused: 0, completed: 1, failed: 1 },
          }),
        );
      }
      throw new Error(`unexpected fetch: ${url}`);
    }),
  );
  localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('the Runs page’s status tabs', () => {
  it('shows the chosen tab in the same commit that writes it to the address', async () => {
    const router = createMemoryRouter([{ path: '/runs', element: <RunsPage /> }], {
      initialEntries: ['/runs'],
    });
    // Studio's RouterProvider from react-router/dom passes React DOM's flushSync the same way.
    render(
      <RouterProvider
        router={router}
        flushSync={(fn) => {
          flushSync(fn);
        }}
      />,
    );
    await screen.findByRole('link', { name: /Answer a brief/ });
    const failed = screen.getByRole('tab', { name: /^Failed/ });

    // What the tabs show at each moment the address changes. A tab rendered only in a
    // later transition leaves a window where the address and the tabs disagree.
    const seen: string[] = [];
    const unsubscribe = router.subscribe((state) => {
      const selected = screen.getByRole('tab', { selected: true }).textContent;
      seen.push(`${state.location.search} ${selected ?? ''}`);
    });
    fireEvent.mouseDown(failed);
    await waitFor(() => {
      expect(router.state.location.search).toContain('status=failed');
    });
    unsubscribe();

    expect(seen).not.toHaveLength(0);
    expect(seen.filter((s) => !s.includes('status=failed Failed'))).toEqual([]);
  });
});

describe('the Runs page’s cost column', () => {
  it('shows each run’s cost with the calls that reported no price', async () => {
    const router = createMemoryRouter([{ path: '/runs', element: <RunsPage /> }], {
      initialEntries: ['/runs'],
    });
    render(<RouterProvider router={router} />);
    await screen.findByRole('link', { name: /Answer a brief/ });
    expect(screen.getByRole('columnheader', { name: 'Cost' })).toBeInTheDocument();
    expect(screen.getByText('$0.04')).toBeInTheDocument();
    expect(screen.getByText('1 call not priced')).toBeInTheDocument();
  });
});

describe('the Runs page’s version filter', () => {
  it('offers the chosen workflow’s versions and sends the picked one to the server', async () => {
    const fetchMock = vi.mocked(fetch);
    fetchMock.mockImplementation((input: RequestInfo | URL) => {
      const url = requestUrl(input);
      if (url === '/api/definitions') {
        return Promise.resolve(
          jsonResponse({
            items: [
              {
                workflowId: 'wf_1',
                name: 'Answer a brief',
                description: '',
                latestVersion: 3,
                updatedAt: '2026-10-01T10:00:00Z',
                lastRun: null,
              },
            ],
          }),
        );
      }
      if (url.startsWith('/api/runs?')) {
        return Promise.resolve(
          jsonResponse({
            items: [RUN],
            nextCursor: null,
            prevCursor: null,
            counts: { all: 1, running: 0, paused: 0, completed: 1, failed: 0 },
          }),
        );
      }
      throw new Error(`unexpected fetch: ${url}`);
    });
    const router = createMemoryRouter([{ path: '/runs', element: <RunsPage /> }], {
      initialEntries: ['/runs?wf=wf_1'],
    });
    render(<RouterProvider router={router} />);
    await screen.findByRole('link', { name: /Answer a brief/ });

    const version = await screen.findByRole('button', { name: 'Version: All versions' });
    fireEvent.click(version);
    fireEvent.click(await screen.findByRole('option', { name: 'v2' }));
    await waitFor(() => {
      expect(router.state.location.search).toBe('?wf=wf_1&v=2');
    });
    await waitFor(() => {
      const urls = fetchMock.mock.calls.map(([input]) => requestUrl(input));
      const last = urls.filter((u) => u.startsWith('/api/runs?')).at(-1) ?? '';
      expect(Object.fromEntries(new URL(last, 'http://studio.test').searchParams)).toEqual({
        workflowId: 'wf_1',
        definitionVersion: '2',
        limit: '20',
      });
    });
    expect(screen.getByRole('button', { name: 'Version: v2' })).toBeInTheDocument();

    // Another workflow, or none, has no version: the control leaves with the filter.
    fireEvent.click(screen.getByRole('button', { name: /^Workflow: / }));
    fireEvent.click(await screen.findByRole('option', { name: 'All workflows' }));
    await waitFor(() => {
      expect(router.state.location.search).toBe('');
    });
    expect(screen.queryByRole('button', { name: /^Version: / })).toBeNull();
  });
});
