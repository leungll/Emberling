import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ObservePage } from './ObservePage';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/** Minimal EventSource stand-in: ObservePage never has to see it fire to pass this test. */
class FakeEventSource {
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  constructor(readonly url: string) {}
  close(): void {}
}

const OLD_RUN = {
  id: 'run_old',
  workflowId: 'wf_123',
  definitionVersion: 3,
  status: 'COMPLETED',
  input: { prompt: 'old prompt value' },
  output: null,
  error: null,
};

const OLD_DEFINITION_VERSION = {
  workflowId: 'wf_123',
  version: 3,
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

function renderObservePage() {
  return render(
    <MemoryRouter initialEntries={['/runs/run_old']}>
      <Routes>
        <Route path="/runs/:runId" element={<ObservePage />} />
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('ObservePage — Run Again', () => {
  it('opens the Run Input Dialog prefilled with the historical input and submits the same version', async () => {
    vi.stubGlobal('EventSource', FakeEventSource);

    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      void init;
      if (url === '/api/runs/run_old') {
        return Promise.resolve(jsonResponse(200, { run: OLD_RUN, nodeRuns: [], lastSeq: 0 }));
      }
      if (url === '/api/definitions/wf_123/versions/3') {
        return Promise.resolve(jsonResponse(200, OLD_DEFINITION_VERSION));
      }
      if (url === '/api/runs') {
        return Promise.resolve(
          jsonResponse(200, {
            id: 'run_new',
            workflowId: 'wf_123',
            definitionVersion: 3,
            status: 'RUNNING',
            lastSeq: 0,
          }),
        );
      }
      if (url === '/api/runs/run_new') {
        return Promise.resolve(
          jsonResponse(200, {
            run: { ...OLD_RUN, id: 'run_new', status: 'RUNNING' },
            nodeRuns: [],
            lastSeq: 0,
          }),
        );
      }
      throw new Error(`unexpected fetch: ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);

    renderObservePage();

    // Wait for the Observe page to load the historical Run before clicking Run Again.
    const runAgainButton = await screen.findByRole('button', { name: 'Run Again' });
    fireEvent.click(runAgainButton);

    // The dialog opens bound to the old version and prefilled with the old input.
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText('wf_123 · v3')).toBeInTheDocument();
    const promptField = (await within(dialog).findByLabelText('Prompt', {
      exact: false,
    })) as HTMLInputElement;
    expect(promptField.value).toBe('old prompt value');

    fireEvent.click(within(dialog).getByRole('button', { name: 'Create run' }));

    // The submitted body reuses the historical Run's own workflowId and definitionVersion,
    // never a newer one, and carries the (possibly edited) input through untouched.
    const createRunCall = fetchMock.mock.calls.find(([reqUrl]) => String(reqUrl) === '/api/runs');
    expect(createRunCall).toBeDefined();
    const [, init] = createRunCall as [RequestInfo | URL, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      workflowId: 'wf_123',
      definitionVersion: 3,
      input: { prompt: 'old prompt value' },
    });

    // On success, Studio navigates to the new Run's own Observe page.
    await screen.findByRole('heading', { name: 'run_new', level: 1 });
  });
});
