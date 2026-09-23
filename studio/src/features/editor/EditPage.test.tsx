import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation, useSearchParams } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { EditPage } from './EditPage';
import { useStudioStore } from '@/stores/studio-store';

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
    if (url === '/api/node-types' || url === '/api/models') {
      return Promise.resolve(jsonResponse(200, { items: [] }));
    }
    // The Recent Execution bar's list lookup (04 §2.6): no matching item means no Run yet,
    // which is the common case across these fixtures.
    if (url === '/api/definitions') {
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

beforeEach(() => {
  useStudioStore.setState({ selectedNodeId: null, hasUnsavedChanges: false });
});

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

// ---------------------------------------------------------------------------
// Save, create, conflict and validation feedback
// ---------------------------------------------------------------------------

/** Mirrors backend/internal/nodes/textgeneration/node.go's registration. */
const TEXT_GENERATION = {
  type: 'text_generation',
  displayName: 'Text Generation',
  category: 'Prompt & Model',
  executionKind: 'SYNC',
  inputs: [{ name: 'prompt', dataType: 'text', required: true }],
  outputs: [{ name: 'text', dataType: 'text', required: true }],
  configSchema: {
    type: 'object',
    properties: {
      modelId: { type: 'string', minLength: 1 },
      systemPrompt: { type: 'string' },
    },
    required: ['modelId'],
  },
  uiSchema: {
    fields: [
      {
        path: 'modelId',
        order: 10,
        group: 'MODEL',
        widget: 'MODEL_SELECTOR',
        capability: 'text_generation',
      },
      { path: 'systemPrompt', order: 30, group: 'BASIC', widget: 'TEXTAREA' },
    ],
  },
  sideEffect: { kind: 'EXTERNAL', idempotency: 'UNKNOWN' },
};

const MODELS = [
  {
    id: 'text-model-v1',
    displayName: 'Text Model',
    capabilities: ['text_generation'],
    configSchema: { type: 'object' },
  },
];

function definitionWithNode(version: number, nodeName: string) {
  return {
    ...DEFINITION,
    version,
    nodes: [
      {
        id: 'node_gen',
        type: 'text_generation',
        name: nodeName,
        position: { x: 0, y: 0 },
        config: { modelId: 'text-model-v1' },
      },
    ],
    createdAt: `2026-08-0${version}T00:00:00Z`,
  };
}

type Handler = (init: RequestInit | undefined) => Response | Promise<Response>;

function stubRoutes(routes: Record<string, Handler>) {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const key = `${init?.method ?? 'GET'} ${String(input)}`;
    if (key === 'GET /api/node-types') {
      return Promise.resolve(jsonResponse(200, { items: [TEXT_GENERATION] }));
    }
    if (key === 'GET /api/models') return Promise.resolve(jsonResponse(200, { items: MODELS }));
    // The Recent Execution bar's list lookup (04 §2.6): defaults to no Run unless a test
    // overrides this key in `routes` to exercise the bar itself.
    if (key === 'GET /api/definitions' && !routes[key]) {
      return Promise.resolve(jsonResponse(200, { items: [] }));
    }
    const handler = routes[key];
    if (!handler) throw new Error(`unexpected fetch: ${key}`);
    return Promise.resolve(handler(init));
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

function callsTo(fetchMock: ReturnType<typeof stubRoutes>, method: string, url: string) {
  return fetchMock.mock.calls
    .filter(([input, init]) => String(input) === url && (init?.method ?? 'GET') === method)
    .map(([, init]) => JSON.parse(String(init?.body ?? 'null')) as Record<string, unknown>);
}

function PathProbe() {
  const location = useLocation();
  return <div data-testid="location-path">{location.pathname}</div>;
}

function renderStudio(entry: string | { pathname: string; state: unknown }) {
  const element = (
    <>
      <EditPage />
      <PathProbe />
    </>
  );
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/studio/new" element={element} />
        <Route path="/studio/:workflowId" element={element} />
      </Routes>
    </MemoryRouter>,
  );
}

async function selectCanvasNode(nodeId: string) {
  fireEvent.click(await screen.findByTestId(`rf__node-${nodeId}`));
}

describe('EditPage — canvas measurement', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('renders measured nodes visibly and does not treat measurement as an edit', async () => {
    // jsdom has no layout; give every element a size so React Flow emits the same
    // `dimensions` changes a browser does after mounting a node.
    vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(180);
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(60);
    stubRoutes({
      'GET /api/definitions/wf_123': () => jsonResponse(200, definitionWithNode(4, 'Generate')),
    });

    renderStudio('/studio/wf_123');

    const node = await screen.findByTestId('rf__node-node_gen');
    await waitFor(() => expect(node).toHaveStyle({ visibility: 'visible' }));
    expect(screen.queryByTestId('unsaved-indicator')).toBeNull();
  });
});

describe('EditPage — creating a Definition', () => {
  it('creates version 1 with POST on the first Save and opens the saved Definition', async () => {
    const created = {
      ...DEFINITION,
      workflowId: 'wf_new',
      version: 1,
      name: 'Document Processing',
    };
    const fetchMock = stubRoutes({
      'POST /api/definitions': () => jsonResponse(201, created),
      'GET /api/definitions/wf_new': () => jsonResponse(200, created),
    });
    const user = userEvent.setup();

    renderStudio({
      pathname: '/studio/new',
      state: { name: 'Document Processing', description: 'Summarise a document' },
    });

    expect(await screen.findByText('Document Processing')).toBeInTheDocument();
    expect(screen.getByTestId('studio-subtitle')).toHaveTextContent('Unsaved draft · Editing');

    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => {
      expect(screen.getByTestId('location-path')).toHaveTextContent('/studio/wf_new');
    });
    expect(callsTo(fetchMock, 'POST', '/api/definitions')).toEqual([
      { name: 'Document Processing', description: 'Summarise a document', nodes: [], edges: [] },
    ]);
    expect(await screen.findByText('Definition v1 · Editing')).toHaveAttribute(
      'title',
      'Workflow wf_new',
    );
  });
});

describe('EditPage — version conflict', () => {
  it('keeps local edits on 409, names the newer server version, and reloads only on request', async () => {
    let latest = definitionWithNode(4, 'Generate');
    const fetchMock = stubRoutes({
      'GET /api/definitions/wf_123': () => jsonResponse(200, latest),
      'PUT /api/definitions/wf_123': () => {
        // Another tab saved v5 before this page's Save arrived.
        latest = definitionWithNode(5, 'Server Generate');
        return jsonResponse(409, {
          error: { code: 'VERSION_CONFLICT', message: 'the definition has already been saved' },
        });
      },
    });
    const user = userEvent.setup();

    renderStudio('/studio/wf_123');
    await selectCanvasNode('node_gen');
    const nameInput = await screen.findByLabelText('Name');
    await user.clear(nameInput);
    await user.type(nameInput, 'Local Generate');

    await user.click(screen.getByRole('button', { name: 'Save' }));

    const conflict = await screen.findByRole('alert', { name: 'Version conflict' });
    expect(conflict).toHaveTextContent('v5');
    expect(conflict).toHaveTextContent('v4');
    // The banner overlays the canvas instead of taking a row above the palette, so the
    // palette, canvas and properties keep their full height (no squashed layout).
    expect(screen.getByTestId('canvas-region')).toContainElement(conflict);
    expect(conflict).toHaveClass('absolute');
    expect(conflict).toHaveClass('text-[14px]');
    expect(screen.getByLabelText('Name')).toHaveValue('Local Generate');
    expect(screen.getByTestId('unsaved-indicator')).toBeInTheDocument();

    // Keep: local edits stay, nothing is written, and the next Save still conflicts.
    await user.click(within(conflict).getByRole('button', { name: 'Keep my edits' }));
    expect(screen.queryByRole('alert', { name: 'Version conflict' })).toBeNull();
    expect(screen.getByLabelText('Name')).toHaveValue('Local Generate');

    await user.click(screen.getByRole('button', { name: 'Save' }));
    const again = await screen.findByRole('alert', { name: 'Version conflict' });
    expect(
      callsTo(fetchMock, 'PUT', '/api/definitions/wf_123').map((body) => body.baseVersion),
    ).toEqual([4, 4]);

    // Reload: local edits are discarded explicitly and the page is on the server's v5.
    await user.click(within(again).getByRole('button', { name: 'Reload latest' }));
    await waitFor(() => expect(screen.getByLabelText('Name')).toHaveValue('Server Generate'));
    expect(screen.getByText('Definition v5 · Editing')).toHaveAttribute('title', 'Workflow wf_123');
    expect(screen.queryByTestId('unsaved-indicator')).toBeNull();
    expect(screen.queryByRole('alert', { name: 'Version conflict' })).toBeNull();
  });
});

describe('EditPage — Backend validation errors', () => {
  it('lists Validate errors against their node and focuses the node and field on click', async () => {
    stubRoutes({
      'GET /api/definitions/wf_123': () => jsonResponse(200, definitionWithNode(4, 'Generate')),
      'POST /api/definitions/validate': () =>
        jsonResponse(200, {
          valid: false,
          errors: [
            // Path-only: the node is identified by the bracketed node ID.
            {
              code: 'MISSING_REQUIRED_INPUT',
              path: 'nodes[node_gen].inputs.prompt',
              message: 'required input "prompt" is not connected',
            },
            {
              code: 'VALIDATION_FAILED',
              path: 'nodes[node_gen].config/modelId',
              nodeId: 'node_gen',
              message: 'minLength: got 0, want 1',
            },
            { code: 'DAG_HAS_CYCLE', path: '', message: 'the graph has a cycle' },
          ],
        }),
    });
    const user = userEvent.setup();

    renderStudio('/studio/wf_123');
    await screen.findByText('Definition v4 · Editing');
    await user.click(screen.getByRole('button', { name: 'Validate' }));

    const list = await screen.findByRole('list', { name: 'Validation errors' });
    const items = within(list).getAllByRole('listitem');
    expect(items).toHaveLength(3);
    expect(items[2]).toHaveTextContent('the graph has a cycle');
    // 04 §4 palette: the row uses the shared FAILED tokens, the message is body text (≥14px)
    // and the path is a helper label (≥12px); no off-palette Tailwind red.
    expect(list.className).toContain('bg-[var(--status-failed-bg)]');
    expect(list.className).not.toMatch(/text-red-|text-xs/);
    expect(within(items[0]!).getByText('required input "prompt" is not connected')).toHaveClass(
      'text-sm',
    );
    expect(within(items[0]!).getByText('nodes[node_gen].inputs.prompt')).toHaveClass('text-xs');
    expect(within(items[0]!).getByRole('button', { name: /Generate/ })).toHaveClass(
      'text-[var(--status-failed-fg)]',
    );
    // A whole-graph error has no node to focus.
    expect(within(items[2]!).queryByRole('button')).toBeNull();

    expect(screen.queryByLabelText('Name')).toBeNull();
    await user.click(within(items[0]!).getByRole('button', { name: /Generate/ }));
    expect(screen.getByLabelText('Name')).toHaveValue('Generate');

    await user.click(within(items[1]!).getByRole('button', { name: /Generate/ }));
    expect(screen.getByLabelText(/^Model\s*\*?$/)).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getAllByText('minLength: got 0, want 1')).toHaveLength(2);
  });

  it('lists the structured errors of a rejected Save and creates no version', async () => {
    stubRoutes({
      'GET /api/definitions/wf_123': () => jsonResponse(200, definitionWithNode(4, 'Generate')),
      'PUT /api/definitions/wf_123': () =>
        jsonResponse(422, {
          error: {
            code: 'INCOMPATIBLE_EDGE',
            message: 'definition failed validation',
            details: {
              errors: [
                {
                  code: 'INCOMPATIBLE_EDGE',
                  path: 'edges[edge_1]',
                  nodeId: 'node_gen',
                  message: 'edge "edge_1" carries image into a text port',
                },
              ],
            },
          },
        }),
    });
    const user = userEvent.setup();

    renderStudio('/studio/wf_123');
    await screen.findByText('Definition v4 · Editing');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    const list = await screen.findByRole('list', { name: 'Validation errors' });
    expect(list).toHaveTextContent('edge "edge_1" carries image into a text port');
    expect(screen.getByText('Definition v4 · Editing')).toHaveAttribute('title', 'Workflow wf_123');
  });
});

describe('EditPage — Undo and Redo', () => {
  const undoButton = () => screen.getByRole('button', { name: 'Undo' });
  const redoButton = () => screen.getByRole('button', { name: 'Redo' });
  const canvasNodes = () => document.querySelectorAll('.react-flow__node');

  it('disables both buttons until there is something to undo or redo', async () => {
    stubRoutes({
      'GET /api/definitions/wf_123': () => jsonResponse(200, definitionWithNode(4, 'Generate')),
    });

    renderStudio('/studio/wf_123');
    await screen.findByTestId('rf__node-node_gen');

    expect(undoButton()).toBeDisabled();
    expect(redoButton()).toBeDisabled();
  });

  it('undoes and redoes a node add from the keyboard, keeping Unsaved changes truthful', async () => {
    const fetchMock = stubRoutes({
      'GET /api/definitions/wf_123': () => jsonResponse(200, definitionWithNode(4, 'Generate')),
    });
    const user = userEvent.setup();

    renderStudio('/studio/wf_123');
    await screen.findByTestId('rf__node-node_gen');
    const requestsBefore = fetchMock.mock.calls.length;

    await user.click(screen.getAllByRole('button', { name: /^Text Generation/ })[0]!);
    await waitFor(() => expect(canvasNodes()).toHaveLength(2));
    expect(screen.getByTestId('unsaved-indicator')).toBeInTheDocument();
    expect(undoButton()).toBeEnabled();
    expect(redoButton()).toBeDisabled();

    fireEvent.keyDown(document.body, { key: 'z', metaKey: true });
    await waitFor(() => expect(canvasNodes()).toHaveLength(1));
    // Back at exactly the loaded graph: nothing is unsaved any more.
    expect(screen.queryByTestId('unsaved-indicator')).toBeNull();
    expect(undoButton()).toBeDisabled();
    expect(redoButton()).toBeEnabled();

    fireEvent.keyDown(document.body, { key: 'Z', metaKey: true, shiftKey: true });
    await waitFor(() => expect(canvasNodes()).toHaveLength(2));
    expect(screen.getByTestId('unsaved-indicator')).toBeInTheDocument();

    fireEvent.keyDown(document.body, { key: 'z', ctrlKey: true });
    await waitFor(() => expect(canvasNodes()).toHaveLength(1));
    fireEvent.keyDown(document.body, { key: 'y', ctrlKey: true });
    await waitFor(() => expect(canvasNodes()).toHaveLength(2));

    // Undo and Redo are local only: they never talk to the server.
    expect(fetchMock.mock.calls.length).toBe(requestsBefore);
  });

  it('restores a deleted node together with its edges in one undo', async () => {
    const withEdge = {
      ...definitionWithNode(4, 'Generate'),
      nodes: [
        ...definitionWithNode(4, 'Generate').nodes,
        {
          id: 'node_next',
          type: 'text_generation',
          name: 'Next',
          position: { x: 200, y: 0 },
          config: { modelId: 'text-model-v1' },
        },
      ],
      edges: [
        {
          id: 'edge_1',
          source: 'node_gen',
          sourceHandle: 'text',
          target: 'node_next',
          targetHandle: 'prompt',
        },
      ],
    };
    const fetchMock = stubRoutes({
      'GET /api/definitions/wf_123': () => jsonResponse(200, withEdge),
      'POST /api/definitions/validate': () =>
        jsonResponse(200, { valid: true, runInputSchema: DEFINITION.runInputSchema }),
    });
    const user = userEvent.setup();
    // jsdom has no layout, so React Flow never draws edge paths; the graph Validate sends
    // is what shows which edges the page holds.
    const validatedGraph = async () => {
      await user.click(screen.getByRole('button', { name: 'Validate' }));
      await screen.findByText('Definition is valid.');
      const body = callsTo(fetchMock, 'POST', '/api/definitions/validate').at(-1)!;
      return { nodes: (body.nodes as unknown[]).length, edges: (body.edges as unknown[]).length };
    };

    renderStudio('/studio/wf_123');
    await selectCanvasNode('node_gen');
    await waitFor(() => expect(screen.getByTestId('rf__node-node_gen')).toHaveClass('selected'));
    fireEvent.keyDown(document.body, { key: 'Backspace' });
    await waitFor(() => expect(canvasNodes()).toHaveLength(1));
    expect(await validatedGraph()).toEqual({ nodes: 1, edges: 0 });

    fireEvent.keyDown(document.body, { key: 'z', ctrlKey: true });
    await waitFor(() => expect(canvasNodes()).toHaveLength(2));
    expect(screen.queryByTestId('unsaved-indicator')).toBeNull();
    expect(undoButton()).toBeDisabled();
    expect(await validatedGraph()).toEqual({ nodes: 2, edges: 1 });
  });

  it('leaves Cmd/Ctrl+Z to a focused text field and undoes one typing session as one step', async () => {
    stubRoutes({
      'GET /api/definitions/wf_123': () => jsonResponse(200, definitionWithNode(4, 'Generate')),
    });
    const user = userEvent.setup();

    renderStudio('/studio/wf_123');
    await selectCanvasNode('node_gen');
    const nameInput = await screen.findByLabelText('Name');
    await user.type(nameInput, ' Look');
    expect(nameInput).toHaveValue('Generate Look');

    // Focus is in the input: the page must not take over the native text undo.
    fireEvent.keyDown(nameInput, { key: 'z', metaKey: true });
    expect(nameInput).toHaveValue('Generate Look');
    expect(redoButton()).toBeDisabled();

    // The whole typing session is one entry: one undo restores the loaded name.
    await user.click(undoButton());
    await waitFor(() => expect(screen.getByLabelText('Name')).toHaveValue('Generate'));
    expect(screen.queryByTestId('unsaved-indicator')).toBeNull();
    expect(undoButton()).toBeDisabled();

    await user.click(redoButton());
    await waitFor(() => expect(screen.getByLabelText('Name')).toHaveValue('Generate Look'));
    expect(screen.getByTestId('unsaved-indicator')).toBeInTheDocument();
  });

  it('starts a new entry for a second typing session in the same field', async () => {
    stubRoutes({
      'GET /api/definitions/wf_123': () => jsonResponse(200, definitionWithNode(4, 'Generate')),
    });
    const user = userEvent.setup();

    renderStudio('/studio/wf_123');
    await selectCanvasNode('node_gen');
    await user.type(await screen.findByLabelText('Name'), ' A');
    // Leaving the field ends the typing session.
    await user.tab();
    await user.type(screen.getByLabelText('Name'), ' B');

    await user.click(undoButton());
    await waitFor(() => expect(screen.getByLabelText('Name')).toHaveValue('Generate A'));
    await user.click(undoButton());
    await waitFor(() => expect(screen.getByLabelText('Name')).toHaveValue('Generate'));
  });

  it('clears history when Reload latest replaces the local edits after a conflict', async () => {
    let latest = definitionWithNode(4, 'Generate');
    stubRoutes({
      'GET /api/definitions/wf_123': () => jsonResponse(200, latest),
      'PUT /api/definitions/wf_123': () => {
        latest = definitionWithNode(5, 'Server Generate');
        return jsonResponse(409, {
          error: { code: 'VERSION_CONFLICT', message: 'the definition has already been saved' },
        });
      },
    });
    const user = userEvent.setup();

    renderStudio('/studio/wf_123');
    await selectCanvasNode('node_gen');
    await user.type(await screen.findByLabelText('Name'), ' Local');
    expect(undoButton()).toBeEnabled();

    await user.click(screen.getByRole('button', { name: 'Save' }));
    const conflict = await screen.findByRole('alert', { name: 'Version conflict' });
    await user.click(within(conflict).getByRole('button', { name: 'Reload latest' }));

    await waitFor(() => expect(screen.getByLabelText('Name')).toHaveValue('Server Generate'));
    expect(undoButton()).toBeDisabled();
    expect(redoButton()).toBeDisabled();
  });

  it('keeps Unsaved changes and Run disabled when the canvas changes during Save', async () => {
    let resolveSave: (response: Response) => void = () => {};
    stubRoutes({
      'GET /api/definitions/wf_123': () => jsonResponse(200, definitionWithNode(4, 'Generate')),
      'PUT /api/definitions/wf_123': () =>
        new Promise<Response>((resolve) => {
          resolveSave = resolve;
        }),
    });
    const user = userEvent.setup();

    renderStudio('/studio/wf_123');
    await screen.findByTestId('rf__node-node_gen');
    await user.click(screen.getAllByRole('button', { name: /^Text Generation/ })[0]!);
    await waitFor(() => expect(canvasNodes()).toHaveLength(2));

    await user.click(screen.getByRole('button', { name: 'Save' }));
    // The Save request is in flight; the user keeps editing.
    await user.click(screen.getAllByRole('button', { name: /^Text Generation/ })[0]!);
    await waitFor(() => expect(canvasNodes()).toHaveLength(3));

    resolveSave(jsonResponse(200, definitionWithNode(5, 'Generate')));
    await screen.findByText('Saved version 5.');

    // 04 §2.6: Run is disabled while unsaved changes exist; the third node was never saved.
    expect(screen.getByTestId('unsaved-indicator')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Run' })).toBeDisabled();
  });

  it('records no Undo entry for an edit that leaves the graph unchanged', async () => {
    stubRoutes({
      'GET /api/definitions/wf_123': () => jsonResponse(200, definitionWithNode(4, 'Generate')),
    });
    const user = userEvent.setup();

    renderStudio('/studio/wf_123');
    await selectCanvasNode('node_gen');
    // Re-selecting the Model already configured reports a config equal to the current one.
    await user.selectOptions(screen.getByLabelText(/^Model\s*\*?$/), 'text-model-v1');

    expect(undoButton()).toBeDisabled();
    expect(screen.queryByTestId('unsaved-indicator')).toBeNull();
  });
});
