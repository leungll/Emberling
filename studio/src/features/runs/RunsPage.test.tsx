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
