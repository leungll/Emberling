import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useSearchParams } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { EditPage } from './EditPage';

/** Surfaces the router's own search string, since MemoryRouter never touches window.location. */
function LocationProbe() {
  const [params] = useSearchParams();
  return <div data-testid="location-search">{params.toString()}</div>;
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const DEFINITION = {
  workflowId: 'wf_123',
  version: 4,
  name: 'AIGC Media Generation',
  description: '',
  nodes: [],
  edges: [],
  runInputSchema: {
    type: 'object',
    properties: { prompt: { type: 'string', title: 'Prompt' } },
    required: ['prompt'],
  },
  validation: { status: 'VALID', validatorVersion: 'v1', validatedAt: '2026-08-03T12:00:00Z' },
  createdAt: '2026-08-01T00:00:00Z',
};

function stubFetch() {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/node-types') {
      return Promise.resolve(jsonResponse(200, { items: [] }));
    }
    if (url === '/api/definitions/wf_123') {
      return Promise.resolve(jsonResponse(200, DEFINITION));
    }
    throw new Error(`unexpected fetch: ${url}`);
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

function renderEditPage(initialPath: string) {
  return render(
    <MemoryRouter initialEntries={[initialPath]}>
      <Routes>
        <Route
          path="/studio/:workflowId"
          element={
            <>
              <EditPage />
              <LocationProbe />
            </>
          }
        />
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('EditPage — Run via query param', () => {
  it('opens the Run Input Dialog once the definition loads when the URL carries ?run=1', async () => {
    stubFetch();

    renderEditPage('/studio/wf_123?run=1');

    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('wf_123 · v4');

    // The param is a one-shot trigger and must not linger in the address bar. The probe div
    // is present from the very first render, so `findByTestId` alone would resolve on that
    // first render and race the effect that clears the param; `waitFor` retries the content
    // assertion itself until the clearing commit lands.
    await waitFor(() => {
      expect(screen.getByTestId('location-search')).toHaveTextContent('');
    });
  });

  it('does not open the Run Input Dialog without the query param', async () => {
    stubFetch();

    renderEditPage('/studio/wf_123');

    // Wait for the definition to finish loading before asserting the dialog stayed closed.
    await screen.findByText('AIGC Media Generation');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });
});
