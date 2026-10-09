import { act, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi, type MockInstance } from 'vitest';

import { DetailPanel } from './DetailPanel';
import type { NodeRun, Run, RunEvent, RunSnapshot } from '@/api/types';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function run(): Run {
  return {
    id: 'run_1',
    workflowId: 'wf_1',
    definitionVersion: 2,
    status: 'RUNNING',
    input: {},
    output: null,
    error: null,
  };
}

// No token hash, credential or callback secret appears anywhere in this fixture; the
// Callback Binding summary the Backend returns never carries one either.
const WAITING_NODE_RUN: NodeRun = {
  id: 'nr_1',
  runId: 'run_1',
  nodeId: 'node_image',
  nodeType: 'image_generation',
  status: 'WAITING_CALLBACK',
  input: { prompt: 'a cat' },
  output: null,
  error: null,
  readyAt: '2026-08-03T12:00:00Z',
  startedAt: '2026-08-03T12:00:01Z',
  waitingAt: '2026-08-03T12:00:02Z',
  completedAt: null,
  latencyMs: null,
  tokenUsage: null,
};

const LATEST_EVENT: RunEvent = {
  id: 'evt_1',
  runId: 'run_1',
  nodeRunId: 'nr_1',
  type: 'NODE_DISPATCHED',
  seq: 5,
  timestamp: '2026-08-03T12:00:02Z',
  payload: {},
};

const NODE_RUN_DETAIL_BODY = {
  nodeRun: WAITING_NODE_RUN,
  attempts: [
    {
      id: 'att_1',
      attemptNo: 1,
      status: 'DISPATCHED',
      input: { prompt: 'a cat' },
      result: null,
      startedAt: '2026-08-03T12:00:01Z',
      deadlineAt: '2026-08-03T12:05:00Z',
      dispatchedAt: '2026-08-03T12:00:02Z',
      completedAt: null,
      error: null,
      callbackBinding: {
        id: 'cb_1',
        providerId: 'stability-ai',
        externalTaskId: 'ext_task_789',
        createdAt: '2026-08-03T12:00:02Z',
      },
    },
  ],
};

/**
 * Resolves once the component has read the body `text` spies on and applied it: the body
 * read itself is awaited, then one macrotask drains the parse-and-setState microtasks
 * inside `act`. Used where the loaded state renders identically to the in-flight one.
 */
async function settleBody(text: MockInstance<() => Promise<string>>) {
  await waitFor(() => expect(text).toHaveBeenCalled());
  await act(async () => {
    await text.mock.results[0]!.value;
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

/** Reads a `<Field label value>` row by its unique label, since the value text alone (for
 * example "DISPATCHED") can also appear in the unrelated Attempts list below it. */
function fieldValue(label: string): string {
  return screen.getByText(label).nextElementSibling?.textContent ?? '';
}

function renderPanel(snapshot: RunSnapshot, events: RunEvent[] = []) {
  return render(
    <DetailPanel
      snapshot={snapshot}
      selectedNodeRun={WAITING_NODE_RUN}
      selectedEvent={null}
      events={events}
    />,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('DetailPanel — WAITING_CALLBACK', () => {
  it('fetches the Node Detail and renders Provider, External Task ID, Attempt, Runtime State, Latest Event and Deadline', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, NODE_RUN_DETAIL_BODY));
    vi.stubGlobal('fetch', fetchMock);

    const snapshot: RunSnapshot = { run: run(), nodeRuns: [WAITING_NODE_RUN], lastSeq: 5 };
    renderPanel(snapshot, [LATEST_EVENT]);

    // "Provider" renders at once with "—" while the Node Detail is in flight; wait for the
    // loaded value itself before asserting the rest.
    await waitFor(() => expect(fieldValue('Provider')).toBe('stability-ai'));
    expect(fetchMock).toHaveBeenCalledWith('/api/runs/run_1/nodes/nr_1', expect.anything());

    expect(fieldValue('External Task ID')).toBe('ext_task_789');
    expect(fieldValue('Attempt')).toBe('1');
    expect(fieldValue('Runtime State')).toBe('DISPATCHED');
    expect(fieldValue('Latest Event')).toBe('NODE_DISPATCHED · seq 5');
    expect(fieldValue('Deadline')).toBe('2026-08-03 12:05:00Z');

    // The Attempts list is rendered in addition to the WAITING_CALLBACK summary fields.
    expect(screen.getByText('Attempts')).toBeInTheDocument();
    expect(screen.getByText('#1')).toBeInTheDocument();
  });

  it('shows "—" for the WAITING_CALLBACK fields when the Node Detail payload has no Attempts yet', async () => {
    const response = jsonResponse(200, { nodeRun: WAITING_NODE_RUN, attempts: [] });
    const body = vi.spyOn(response, 'text');
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response));

    const snapshot: RunSnapshot = { run: run(), nodeRuns: [WAITING_NODE_RUN], lastSeq: 5 };
    renderPanel(snapshot, []);

    // An empty Attempts list renders exactly like the in-flight state, so wait for the
    // Node Detail body itself to be read and applied before asserting the "—" values.
    await settleBody(body);
    // Never invented: every WAITING_CALLBACK field the Backend has not reported yet reads "—".
    expect(fieldValue('Provider')).toBe('—');
    expect(fieldValue('External Task ID')).toBe('—');
    expect(fieldValue('Attempt')).toBe('—');
    expect(fieldValue('Runtime State')).toBe('—');
    expect(fieldValue('Latest Event')).toBe('—');
    expect(fieldValue('Deadline')).toBe('—');
    expect(screen.queryByText('Attempts')).not.toBeInTheDocument();
  });

  it('shows "—" for Provider and External Task ID when the Attempt has no Callback Binding yet', async () => {
    // A synchronous Attempt never has a Callback Binding, so the
    // Backend emits `callbackBinding: null` on an otherwise-real, in-flight Attempt.
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(200, {
          nodeRun: WAITING_NODE_RUN,
          attempts: [
            {
              id: 'att_1',
              attemptNo: 1,
              status: 'DISPATCHED',
              input: { prompt: 'a cat' },
              result: null,
              startedAt: '2026-08-03T12:00:01Z',
              deadlineAt: '2026-08-03T12:05:00Z',
              dispatchedAt: '2026-08-03T12:00:02Z',
              completedAt: null,
              error: null,
              callbackBinding: null,
            },
          ],
        }),
      ),
    );

    const snapshot: RunSnapshot = { run: run(), nodeRuns: [WAITING_NODE_RUN], lastSeq: 5 };
    renderPanel(snapshot, [LATEST_EVENT]);

    // The loaded Attempt, not the "Provider" label that renders before the fetch lands.
    await waitFor(() => expect(fieldValue('Attempt')).toBe('1'));
    expect(fieldValue('Provider')).toBe('—');
    expect(fieldValue('External Task ID')).toBe('—');
    expect(fieldValue('Runtime State')).toBe('DISPATCHED');
    expect(screen.getByText('Attempts')).toBeInTheDocument();
  });
});

describe('DetailPanel — Attempt rows', () => {
  it('renders provider and external task id for a bound attempt', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, NODE_RUN_DETAIL_BODY)));

    const snapshot: RunSnapshot = { run: run(), nodeRuns: [WAITING_NODE_RUN], lastSeq: 5 };
    renderPanel(snapshot, [LATEST_EVENT]);

    await screen.findByText('Attempts');
    // The dispatch facts belong to the Attempt that made them, not only to the NodeRun
    // summary above: a retry keeps the older Attempt and its own Binding in this list.
    expect(fieldValue('Callback provider')).toBe('stability-ai');
    expect(fieldValue('External task')).toBe('ext_task_789');
    expect(fieldValue('Bound at')).toBe('2026-08-03 12:00:02Z');
  });

  it('renders no Binding fields on an attempt whose callbackBinding is null', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(200, {
          nodeRun: WAITING_NODE_RUN,
          attempts: [
            {
              id: 'att_1',
              attemptNo: 1,
              status: 'STARTED',
              input: { prompt: 'a cat' },
              result: null,
              startedAt: '2026-08-03T12:00:01Z',
              deadlineAt: null,
              dispatchedAt: null,
              completedAt: null,
              error: null,
              callbackBinding: null,
            },
          ],
        }),
      ),
    );

    const snapshot: RunSnapshot = { run: run(), nodeRuns: [WAITING_NODE_RUN], lastSeq: 5 };
    renderPanel(snapshot, [LATEST_EVENT]);

    await screen.findByText('Attempts');
    // A synchronous Attempt has no Binding at all; Studio must not print an empty row for it.
    expect(screen.queryByText('Callback provider')).not.toBeInTheDocument();
    expect(screen.queryByText('External task')).not.toBeInTheDocument();
    expect(screen.queryByText('Bound at')).not.toBeInTheDocument();
  });
});

// --- JsonBlock default visibility --------------------------------------------

describe('DetailPanel — JsonBlock default visibility', () => {
  it('shows a small Input value without interaction', () => {
    const snapshot: RunSnapshot = {
      run: { ...run(), input: { brief: 'x' } },
      nodeRuns: [],
      lastSeq: 0,
    };
    render(
      <DetailPanel snapshot={snapshot} selectedNodeRun={null} selectedEvent={null} events={[]} />,
    );

    // Only large fields collapse by default; a small value has no <details> toggle.
    expect(screen.queryByText('Raw JSON')).not.toBeInTheDocument();
    expect(screen.getByText(/"brief": "x"/)).toBeVisible();
  });

  it('collapses an Input value over the large-field threshold by default', () => {
    const snapshot: RunSnapshot = {
      run: { ...run(), input: { blob: 'x'.repeat(2500) } },
      nodeRuns: [],
      lastSeq: 0,
    };
    render(
      <DetailPanel snapshot={snapshot} selectedNodeRun={null} selectedEvent={null} events={[]} />,
    );

    expect(screen.getByText('Raw JSON').closest('details')).not.toHaveAttribute('open');
  });

  it('wraps long JSON strings inside a bounded, vertically scrolling block', () => {
    const snapshot: RunSnapshot = {
      run: { ...run(), input: { uri: 'https://cdn.example.com/' + 'a'.repeat(300) } },
      nodeRuns: [],
      lastSeq: 0,
    };
    render(
      <DetailPanel snapshot={snapshot} selectedNodeRun={null} selectedEvent={null} events={[]} />,
    );

    const block = screen.getByTestId('json-block');
    // A long unbroken value wraps at any character instead of running off the right edge.
    expect(block).toHaveClass('whitespace-pre-wrap', '[overflow-wrap:anywhere]');
    expect(block).toHaveClass('max-h-56', 'overflow-y-auto', 'text-[14px]');
  });
});

// --- ImageRef preview ---------------------------------------------------------

const IMAGE_NODE_RUN: NodeRun = {
  id: 'nr_image',
  runId: 'run_1',
  nodeId: 'node_image',
  nodeType: 'image_generation',
  status: 'SUCCEEDED',
  input: null,
  // NodeOutput.Ports wraps each port's value one level under its port name
  // (backend/internal/nodes/imagegeneration/node.go), so the ImageRef sits under "image".
  output: {
    image: {
      uri: 'https://cdn.example.com/generated/cat.png',
      width: 512,
      height: 512,
      source: 'EXTERNAL',
      mediaType: 'image/png',
    },
  },
  error: null,
  readyAt: '2026-08-03T12:00:00Z',
  startedAt: '2026-08-03T12:00:01Z',
  waitingAt: null,
  completedAt: '2026-08-03T12:00:05Z',
  latencyMs: 4000,
  tokenUsage: null,
};

describe('DetailPanel — ImageRef preview', () => {
  it('renders an <img> preview and the raw reference for an EXTERNAL ImageRef output', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(200, { nodeRun: IMAGE_NODE_RUN, attempts: [] })),
    );

    const snapshot: RunSnapshot = { run: run(), nodeRuns: [IMAGE_NODE_RUN], lastSeq: 5 };
    render(
      <DetailPanel
        snapshot={snapshot}
        selectedNodeRun={IMAGE_NODE_RUN}
        selectedEvent={null}
        events={[]}
      />,
    );

    await screen.findByText('Output');
    const img = await screen.findByRole<HTMLImageElement>('img');
    expect(img.src).toBe('https://cdn.example.com/generated/cat.png');
    // The raw reference (the URI itself, given as-is — never a proxy) is shown as text too,
    // distinct from the collapsed raw JSON below (which also contains this substring).
    expect(
      screen.getByText('EXTERNAL · https://cdn.example.com/generated/cat.png'),
    ).toBeInTheDocument();
  });

  it('shows the image inside a fixed 160px preview box with object-contain', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(200, { nodeRun: IMAGE_NODE_RUN, attempts: [] })),
    );

    const snapshot: RunSnapshot = { run: run(), nodeRuns: [IMAGE_NODE_RUN], lastSeq: 5 };
    render(
      <DetailPanel
        snapshot={snapshot}
        selectedNodeRun={IMAGE_NODE_RUN}
        selectedEvent={null}
        events={[]}
      />,
    );

    const img = await screen.findByRole('img');
    const box = screen.getByTestId('image-preview-box');
    expect(box).toContainElement(img);
    // A 16x16 mock image is scaled up into the box rather than rendered as a speck.
    expect(box).toHaveClass('h-40', 'w-40');
    expect(img).toHaveClass('h-full', 'w-full', 'object-contain');
  });

  it('keeps the raw JSON collapsed by default', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(200, { nodeRun: IMAGE_NODE_RUN, attempts: [] })),
    );

    const snapshot: RunSnapshot = { run: run(), nodeRuns: [IMAGE_NODE_RUN], lastSeq: 5 };
    render(
      <DetailPanel
        snapshot={snapshot}
        selectedNodeRun={IMAGE_NODE_RUN}
        selectedEvent={null}
        events={[]}
      />,
    );

    await screen.findByText('Output');
    // The raw JSON body is reachable but not visible until the <summary> is toggled open.
    expect(screen.getByText('Raw JSON').closest('details')).not.toHaveAttribute('open');
  });
});

// --- Agent Trace ------------------------------------------------------------

const AGENT_NODE_RUN: NodeRun = {
  id: 'nr_agent',
  runId: 'run_1',
  nodeId: 'node_agent',
  nodeType: 'agent',
  status: 'RUNNING',
  input: { goal: 'find the answer' },
  output: null,
  error: null,
  readyAt: '2026-08-03T12:00:00Z',
  startedAt: '2026-08-03T12:00:01Z',
  waitingAt: null,
  completedAt: null,
  latencyMs: null,
  tokenUsage: null,
};

/** No callback token, Provider credential or storage key appears in this fixture, exactly
 * as the Agent Trace projection the Backend returns carries none. */
const AGENT_TRACE_BODY = {
  agentRun: {
    id: 'ar_1',
    nodeRunId: 'nr_agent',
    termination: null,
    currentTurnNo: 2,
    currentContextVersion: 3,
    currentStateVersion: 1,
    deadline: '2026-08-03T12:10:00Z',
    terminatedAt: null,
    error: null,
  },
  turns: [
    {
      id: 'turn_1',
      turnNo: 1,
      status: 'COMPLETED',
      startedAt: '2026-08-03T12:00:01Z',
      completedAt: '2026-08-03T12:00:05Z',
      error: null,
      decision: { kind: 'TOOL_CALL', tool: 'lookup', hasStatePatch: true },
      action: {
        id: 'action_1',
        type: 'TOOL_CALL',
        status: 'SUCCEEDED',
        startedAt: '2026-08-03T12:00:02Z',
        completedAt: '2026-08-03T12:00:05Z',
        error: null,
      },
      toolAttempts: [
        {
          id: 'tool_attempt_1',
          toolName: 'lookup',
          attemptNo: 1,
          status: 'SUCCEEDED',
          callbackBinding: {
            id: 'binding_1',
            providerId: 'tool-provider-v1',
            externalTaskId: 'provider_task_789',
          },
          startedAt: '2026-08-03T12:00:02Z',
          dispatchedAt: '2026-08-03T12:00:03Z',
          completedAt: '2026-08-03T12:00:05Z',
          error: null,
        },
      ],
    },
    {
      id: 'turn_2',
      turnNo: 2,
      status: 'RUNNING',
      startedAt: '2026-08-03T12:00:06Z',
      completedAt: null,
      error: null,
      decision: null,
      action: null,
      toolAttempts: [],
    },
  ],
  facts: { items: [], truncated: false },
  generationBudget: { maxGenerationCalls: null, generationCallsUsed: 0 },
};

function renderAgentPanel(events: RunEvent[] = []) {
  const snapshot: RunSnapshot = { run: run(), nodeRuns: [AGENT_NODE_RUN], lastSeq: 9 };
  return render(
    <DetailPanel
      snapshot={snapshot}
      selectedNodeRun={AGENT_NODE_RUN}
      selectedEvent={null}
      events={events}
    />,
  );
}

function agentEvent(seq: number, nodeRunId: string): RunEvent {
  return {
    id: `evt_${seq}`,
    runId: 'run_1',
    nodeRunId,
    type: 'AGENT_ACTION_COMPLETED',
    seq,
    timestamp: '2026-08-03T12:00:05Z',
    payload: { turnNo: 1 },
  };
}

describe('DetailPanel — Agent Trace', () => {
  it('fetches the Agent Trace and renders the Agent Run summary, both Turns and the Tool Attempt', async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation((url: string) =>
        Promise.resolve(
          url.endsWith('/agent')
            ? jsonResponse(200, AGENT_TRACE_BODY)
            : jsonResponse(200, { nodeRun: AGENT_NODE_RUN, attempts: [] }),
        ),
      );
    vi.stubGlobal('fetch', fetchMock);

    renderAgentPanel();

    // The "Agent" heading renders before the Trace arrives; wait for the loaded Agent Run.
    await waitFor(() => expect(fieldValue('Agent Run')).toBe('ar_1'));
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/runs/run_1/nodes/nr_agent/agent',
      expect.anything(),
    );

    // An opaque ID takes its own full-width line under its label, so it never wraps one or
    // two orphan characters beside a squeezed label column.
    expect(screen.getByText('Agent Run').parentElement).toHaveAttribute(
      'data-field-layout',
      'stacked',
    );
    expect(screen.getByText('Agent Run').nextElementSibling).toHaveClass(
      '[overflow-wrap:anywhere]',
    );
    expect(screen.getByText('External task').parentElement).toHaveAttribute(
      'data-field-layout',
      'stacked',
    );
    // A still-running Agent Run has no server-side termination; Studio prints no outcome.
    expect(fieldValue('Termination')).toBe('running');
    expect(fieldValue('Current turn')).toBe('2');
    expect(fieldValue('Context version')).toBe('3');
    expect(fieldValue('State version')).toBe('1');
    expect(fieldValue('Agent deadline')).toBe('2026-08-03 12:10:00Z');

    // Turns are rendered in the persisted order the Backend returned them in.
    expect(screen.getByText('Turn 1')).toBeInTheDocument();
    expect(screen.getByText('Turn 2')).toBeInTheDocument();
    expect(fieldValue('Decision')).toBe('TOOL_CALL');
    expect(fieldValue('Tool')).toBe('lookup');
    expect(fieldValue('State patch')).toBe('yes');
    expect(screen.getByText('Action TOOL_CALL')).toBeInTheDocument();

    expect(screen.getByText('lookup #1')).toBeInTheDocument();
    expect(fieldValue('Callback provider')).toBe('tool-provider-v1');
    expect(fieldValue('External task')).toBe('provider_task_789');

    // Turn 2 committed no Decision and created no Action; neither is invented for it.
    expect(screen.getAllByText('Decision')).toHaveLength(1);

    // The fact ledger and generation budget sit in the detail panel, as received.
    expect(screen.getByTestId('generation-budget')).toHaveTextContent('0 · no limit');
    expect(screen.getByText('No facts recorded')).toBeInTheDocument();
  });

  it('renders the termination and the Agent error of a TIMEOUT trace', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) =>
        Promise.resolve(
          url.endsWith('/agent')
            ? jsonResponse(200, {
                agentRun: {
                  id: 'ar_2',
                  nodeRunId: 'nr_agent',
                  termination: 'TIMEOUT',
                  currentTurnNo: 1,
                  currentContextVersion: 1,
                  currentStateVersion: 0,
                  deadline: '2026-08-03T12:10:00Z',
                  terminatedAt: '2026-08-03T12:10:01Z',
                  error: { code: 'AGENT_TIMEOUT', message: 'agent deadline exceeded' },
                },
                turns: [
                  {
                    id: 'turn_1',
                    turnNo: 1,
                    status: 'FAILED',
                    startedAt: '2026-08-03T12:00:01Z',
                    completedAt: '2026-08-03T12:10:01Z',
                    error: { code: 'AGENT_TIMEOUT', message: 'agent deadline exceeded' },
                    decision: { kind: 'TOOL_CALL', tool: 'lookup', hasStatePatch: false },
                    action: {
                      id: 'action_1',
                      type: 'TOOL_CALL',
                      status: 'FAILED',
                      startedAt: '2026-08-03T12:00:02Z',
                      completedAt: '2026-08-03T12:10:01Z',
                      error: { code: 'AGENT_TIMEOUT', message: 'agent deadline exceeded' },
                    },
                    toolAttempts: [],
                  },
                ],
                facts: { items: [], truncated: false },
                generationBudget: { maxGenerationCalls: 3, generationCallsUsed: 0 },
              })
            : jsonResponse(200, { nodeRun: AGENT_NODE_RUN, attempts: [] }),
        ),
      ),
    );

    renderAgentPanel();

    // The termination is the Backend's, reported verbatim, not derived from the failed Turn.
    await waitFor(() => expect(fieldValue('Termination')).toBe('TIMEOUT'));
    expect(fieldValue('Terminated at')).toBe('2026-08-03 12:10:01Z');
    expect(screen.getByText('Agent error')).toBeInTheDocument();
    expect(screen.getByText('Turn error')).toBeInTheDocument();
    expect(screen.getByText('Action error')).toBeInTheDocument();
    expect(screen.getAllByText(/agent deadline exceeded/).length).toBeGreaterThan(0);
  });

  it('does not request the Agent Trace for a NodeRun that is not an Agent', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, NODE_RUN_DETAIL_BODY));
    vi.stubGlobal('fetch', fetchMock);

    const snapshot: RunSnapshot = { run: run(), nodeRuns: [WAITING_NODE_RUN], lastSeq: 5 };
    renderPanel(snapshot, [LATEST_EVENT]);

    await screen.findByText('Attempts');
    // The Agent Trace exists only for a MANAGED_AGENT NodeRun; asking for any other one
    // would be a 404 the panel invented.
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/agent'))).toHaveLength(0);
    expect(screen.queryByText('Agent')).not.toBeInTheDocument();
  });

  it('re-reads the Agent Trace when an AGENT_ACTION_COMPLETED event for this NodeRun arrives', async () => {
    const fetchMock = vi
      .fn()
      .mockImplementation((url: string) =>
        Promise.resolve(
          url.endsWith('/agent')
            ? jsonResponse(200, AGENT_TRACE_BODY)
            : jsonResponse(200, { nodeRun: AGENT_NODE_RUN, attempts: [] }),
        ),
      );
    vi.stubGlobal('fetch', fetchMock);

    const { rerender } = renderAgentPanel([]);
    await screen.findByText('Agent');
    const agentCalls = () => fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/agent'));
    expect(agentCalls()).toHaveLength(1);

    // An AGENT_* event changes no NodeRun status, so the re-read is driven by the Event
    // itself; the Trace is fetched again rather than patched from the Event payload.
    const snapshot: RunSnapshot = { run: run(), nodeRuns: [AGENT_NODE_RUN], lastSeq: 10 };
    rerender(
      <DetailPanel
        snapshot={snapshot}
        selectedNodeRun={AGENT_NODE_RUN}
        selectedEvent={null}
        events={[agentEvent(10, 'nr_agent')]}
      />,
    );

    await waitFor(() => expect(agentCalls()).toHaveLength(2));

    // An AGENT_* event naming a different NodeRun is not this NodeRun's business.
    rerender(
      <DetailPanel
        snapshot={snapshot}
        selectedNodeRun={AGENT_NODE_RUN}
        selectedEvent={null}
        events={[agentEvent(10, 'nr_agent'), agentEvent(11, 'nr_other')]}
      />,
    );
    await waitFor(() => expect(agentCalls()).toHaveLength(2));
  });
});

describe('DetailPanel — Waiting Diagnostics', () => {
  it('shows when the NodeRun started waiting and for how long', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, NODE_RUN_DETAIL_BODY)));

    renderPanel({ run: run(), nodeRuns: [WAITING_NODE_RUN], lastSeq: 5 }, [LATEST_EVENT]);

    await waitFor(() => expect(fieldValue('Provider')).toBe('stability-ai'));
    expect(screen.getByTestId('waiting-diagnostics')).toBeInTheDocument();
    expect(fieldValue('Waiting Since')).toMatch(/^2026-08-03 12:00:02Z · \S/);
  });

  it('reads "—" for Waiting Since when the Backend reports no waitingAt', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(200, { nodeRun: WAITING_NODE_RUN, attempts: [] })),
    );

    render(
      <DetailPanel
        snapshot={{ run: run(), nodeRuns: [WAITING_NODE_RUN], lastSeq: 5 }}
        selectedNodeRun={{ ...WAITING_NODE_RUN, waitingAt: null }}
        selectedEvent={null}
        events={[]}
      />,
    );

    await screen.findByText('Provider');
    expect(fieldValue('Waiting Since')).toBe('—');
  });

  it('shows no Waiting Diagnostics for a NodeRun that is not WAITING_CALLBACK', () => {
    const succeeded: NodeRun = { ...WAITING_NODE_RUN, status: 'SUCCEEDED', output: { text: 'ok' } };
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(200, { nodeRun: succeeded, attempts: [] })),
    );

    render(
      <DetailPanel
        snapshot={{ run: run(), nodeRuns: [succeeded], lastSeq: 5 }}
        selectedNodeRun={succeeded}
        selectedEvent={null}
        events={[]}
        nodeName="Image Generation"
      />,
    );

    expect(screen.getByRole('heading', { name: 'Image Generation' })).toBeInTheDocument();
    expect(screen.queryByTestId('waiting-diagnostics')).not.toBeInTheDocument();
    expect(screen.queryByText('Provider')).not.toBeInTheDocument();
  });
});

describe('DetailPanel — Agent Turn timeline', () => {
  function stubAgentFetch(trace: unknown, nodeRun: NodeRun) {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockImplementation((url: string) =>
          Promise.resolve(
            url.endsWith('/agent')
              ? jsonResponse(200, trace)
              : jsonResponse(200, { nodeRun, attempts: [] }),
          ),
        ),
    );
  }

  it('lists the bound Definition allowedTools as the candidate Tools of every Turn', async () => {
    stubAgentFetch(AGENT_TRACE_BODY, AGENT_NODE_RUN);

    render(
      <DetailPanel
        snapshot={{ run: run(), nodeRuns: [AGENT_NODE_RUN], lastSeq: 9 }}
        selectedNodeRun={AGENT_NODE_RUN}
        selectedEvent={null}
        events={[]}
        allowedTools={['lookup', 'web_search']}
      />,
    );

    const turns = await screen.findAllByTestId('agent-turn');
    expect(turns).toHaveLength(2);
    for (const turn of turns) {
      const candidates = within(turn).getByTestId('candidate-tools');
      expect(within(candidates).getByText('lookup')).toBeInTheDocument();
      expect(within(candidates).getByText('web_search')).toBeInTheDocument();
    }
    // Turn 1's dot is the green class of its COMPLETED status, Turn 2's the running one.
    expect(turns[0]!.querySelector('[data-tone]')).toHaveAttribute('data-tone', 'succeeded');
    expect(turns[1]!.querySelector('[data-tone]')).toHaveAttribute('data-tone', 'running');
    expect(fieldValue('Turns')).toBe('2');
  });

  it('shows no candidate Tools when the bound Definition gave none to show', async () => {
    stubAgentFetch(AGENT_TRACE_BODY, AGENT_NODE_RUN);

    renderAgentPanel();

    expect(await screen.findAllByTestId('agent-turn')).toHaveLength(2);
    expect(screen.queryByTestId('candidate-tools')).not.toBeInTheDocument();
  });

  it('reads the waiting Provider and External Task ID from the dispatched Tool Attempt', async () => {
    const waitingAgent: NodeRun = {
      ...AGENT_NODE_RUN,
      status: 'WAITING_CALLBACK',
      waitingAt: '2026-08-03T12:00:03Z',
    };
    const [turn1] = AGENT_TRACE_BODY.turns;
    const dispatchedTrace = {
      ...AGENT_TRACE_BODY,
      turns: [
        {
          ...turn1!,
          status: 'RUNNING',
          toolAttempts: [{ ...turn1!.toolAttempts[0]!, status: 'DISPATCHED', completedAt: null }],
        },
      ],
    };
    stubAgentFetch(dispatchedTrace, waitingAgent);

    render(
      <DetailPanel
        snapshot={{ run: run(), nodeRuns: [waitingAgent], lastSeq: 9 }}
        selectedNodeRun={waitingAgent}
        selectedEvent={null}
        events={[]}
      />,
    );

    await screen.findByText('Tool Attempt');
    expect(fieldValue('Provider')).toBe('tool-provider-v1');
    expect(fieldValue('External Task ID')).toBe('provider_task_789');
    expect(fieldValue('Tool Attempt')).toBe('lookup · attempt 1');
  });
});
