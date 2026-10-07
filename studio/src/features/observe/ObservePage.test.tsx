import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ObservePage } from './ObservePage';
import { requestUrl } from '@/test/requestUrl';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/**
 * Browser-faithful EventSource stand-in: a named frame reaches only the listeners
 * registered for that name, exactly like the frames the Backend writes.
 */
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  readyState = 0;
  onopen: ((event: Event) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  closed = false;
  private readonly listeners = new Map<string, ((event: MessageEvent) => void)[]>();

  constructor(readonly url: string) {
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: (event: MessageEvent) => void): void {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }

  close(): void {
    this.closed = true;
    this.readyState = 2;
  }

  frame(event: { type: string; seq: number }): void {
    const message = new MessageEvent(event.type, {
      data: JSON.stringify(event),
      lastEventId: String(event.seq),
    });
    for (const listener of this.listeners.get(event.type) ?? []) listener(message);
  }
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
  nodes: [
    {
      id: 'node_prompt',
      type: 'text_input',
      name: 'Campaign Prompt',
      position: { x: 0, y: 0 },
      config: { inputKey: 'prompt', label: 'Campaign Prompt', required: true },
    },
  ],
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
  FakeEventSource.instances = [];
});

function liveEvent(seq: number, type: string, nodeRunId: string | null, payload = {}) {
  return {
    id: `evt_${seq}`,
    runId: 'run_live',
    nodeRunId,
    type,
    seq,
    timestamp: '2026-08-03T12:00:04Z',
    payload,
  };
}

function liveNodeRun(id: string, status: string) {
  return {
    id,
    runId: 'run_live',
    nodeId: id.replace('nr_', 'node_'),
    nodeType: 'image_generation',
    status,
    input: null,
    output: null,
    error: null,
    readyAt: '2026-08-03T12:00:01Z',
    startedAt: null,
    waitingAt: null,
    completedAt: null,
    latencyMs: null,
    tokenUsage: null,
  };
}

describe('ObservePage — live pipeline', () => {
  it('shows history, streams named SSE frames and re-reads the Snapshot for a new NodeRun', async () => {
    vi.stubGlobal('EventSource', FakeEventSource);

    const log = [
      liveEvent(1, 'RUN_CREATED', null),
      liveEvent(2, 'NODE_READY', 'nr_image'),
      liveEvent(3, 'NODE_STARTED', 'nr_image'),
    ];
    let nodeRuns = [liveNodeRun('nr_image', 'RUNNING')];
    const snapshot = () => ({
      run: { ...OLD_RUN, id: 'run_live', status: 'RUNNING' },
      nodeRuns,
      lastSeq: log.length,
    });

    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = new URL(requestUrl(input), 'http://studio.test');
        if (url.pathname === '/api/runs/run_live') {
          return Promise.resolve(jsonResponse(200, snapshot()));
        }
        if (url.pathname === '/api/runs/run_live/events') {
          const after = Number(url.searchParams.get('afterSeq'));
          return Promise.resolve(jsonResponse(200, { items: log.filter((e) => e.seq > after) }));
        }
        if (url.pathname.startsWith('/api/runs/run_live/nodes/')) {
          return Promise.resolve(jsonResponse(404, { error: { code: 'NOT_FOUND', message: '' } }));
        }
        throw new Error(`unexpected fetch: ${url.pathname}`);
      }),
    );

    render(
      <MemoryRouter initialEntries={['/runs/run_live']}>
        <Routes>
          <Route path="/runs/:runId" element={<ObservePage />} />
        </Routes>
      </MemoryRouter>,
    );

    // History at or below the Snapshot's lastSeq is loaded, then SSE opens after it.
    expect(await screen.findByText('Run created')).toBeInTheDocument();
    expect(screen.getByText('Node started')).toBeInTheDocument();
    const source = FakeEventSource.instances[0]!;
    expect(source.url).toBe('/api/runs/run_live/events?afterSeq=3');

    // The Image NodeRun succeeds and the Caption NodeRun is created after the page opened.
    nodeRuns = [liveNodeRun('nr_image', 'SUCCEEDED'), liveNodeRun('nr_caption', 'READY')];
    log.push(liveEvent(4, 'NODE_COMPLETED', 'nr_image'), liveEvent(5, 'NODE_READY', 'nr_caption'));
    act(() => {
      source.frame(log[3]!);
      source.frame(log[4]!);
    });

    expect(await screen.findByText('Node completed')).toBeInTheDocument();
    const caption = await screen.findByRole('button', { name: /node_caption/ });
    expect(within(caption).getByText('Ready')).toBeInTheDocument();
    // The Image NodeRun finished (SUCCEEDED), so the Run Rail drops it: only Active
    // NodeRuns are grouped there.
    expect(screen.queryByRole('button', { name: /node_image/ })).not.toBeInTheDocument();

    // The Run completes: the stream is closed and not reopened.
    log.push(liveEvent(6, 'RUN_COMPLETED', null, { from: 'RUNNING', to: 'COMPLETED' }));
    act(() => source.frame(log[5]!));
    expect(await screen.findByText('Run completed')).toBeInTheDocument();
    expect(source.closed).toBe(true);
    expect(FakeEventSource.instances).toHaveLength(1);
  });
});

describe('ObservePage — Run Again', () => {
  it('opens the Run Input Dialog prefilled with the historical input and submits the same version', async () => {
    vi.stubGlobal('EventSource', FakeEventSource);

    const fetchMock = vi.fn((input: RequestInfo | URL, _init?: RequestInit) => {
      const url = requestUrl(input);
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
    const promptField = await within(dialog).findByLabelText<HTMLInputElement>('Prompt', {
      exact: false,
    });
    expect(promptField.value).toBe('old prompt value');
    // The field is named by the bound version's Input node label, and its helper gives the
    // input key the value is submitted under.
    expect(within(dialog).getByRole('textbox', { name: /Campaign Prompt/ })).toBe(promptField);
    expect(promptField).toHaveAccessibleDescription('prompt · required · text');

    fireEvent.click(within(dialog).getByRole('button', { name: 'Create run' }));

    // The submitted body reuses the historical Run's own workflowId and definitionVersion,
    // never a newer one, and carries the (possibly edited) input through untouched.
    const createRunCall = fetchMock.mock.calls.find(
      ([reqUrl]) => requestUrl(reqUrl) === '/api/runs',
    );
    expect(createRunCall).toBeDefined();
    const [, init] = createRunCall as [RequestInfo | URL, RequestInit];
    expect(JSON.parse(init.body as string)).toEqual({
      workflowId: 'wf_123',
      definitionVersion: 3,
      input: { prompt: 'old prompt value' },
    });

    // On success, Studio navigates to the new Run's own Observe page.
    await waitFor(() =>
      expect(screen.getByTestId('observe-subtitle')).toHaveTextContent(
        'Run run_new · Definition v3 · Observe',
      ),
    );
  });
});

describe('ObservePage — Run view layout', () => {
  it('lets the top row grow with the Run Summary instead of fixing its height', async () => {
    vi.stubGlobal('EventSource', FakeEventSource);
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = requestUrl(input);
        if (url === '/api/runs/run_old') {
          return Promise.resolve(jsonResponse(200, { run: OLD_RUN, nodeRuns: [], lastSeq: 0 }));
        }
        if (url === '/api/definitions/wf_123/versions/3') {
          return Promise.resolve(jsonResponse(200, OLD_DEFINITION_VERSION));
        }
        return Promise.resolve(jsonResponse(200, { items: [] }));
      }),
    );

    renderObservePage();

    const row = await screen.findByTestId('run-view-top-row');
    expect(row).toContainElement(screen.getByTestId('run-summary'));
    // A fixed h-[400px] clipped the RUNNING / WAITING lists; a minimum height does not.
    expect(row).not.toHaveClass('h-[400px]');
    expect(row).toHaveClass('min-h-[400px]');
  });
});
