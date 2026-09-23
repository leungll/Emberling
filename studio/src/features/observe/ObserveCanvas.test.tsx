import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ObserveCanvas } from './ObserveCanvas';
import type { Definition, NodeMetadata, NodeRun } from '@/api/types';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/** The Run's own bound Definition version (04 §1.3, Runtime safety rule #8). */
const BOUND_DEFINITION: Definition = {
  workflowId: 'wf_1',
  version: 3,
  name: 'AIGC Media Generation',
  description: '',
  nodes: [
    {
      id: 'node_image',
      type: 'image_generation',
      name: 'Generate',
      position: { x: 0, y: 0 },
      config: {},
    },
    {
      id: 'node_caption',
      type: 'captioning',
      name: 'Caption',
      position: { x: 260, y: 0 },
      config: {},
    },
  ],
  edges: [
    {
      id: 'e1',
      source: 'node_image',
      sourceHandle: 'image',
      target: 'node_caption',
      targetHandle: 'image',
    },
  ],
  runInputSchema: { type: 'object', properties: {} },
  validation: { status: 'VALID', validatorVersion: 'v1', validatedAt: '2026-08-01T00:00:00Z' },
  createdAt: '2026-08-01T00:00:00Z',
};

/**
 * A later, "latest" version with an unrelated topology. The Canvas must render the Run's
 * own bound version, never this one, even though the workflow shares an id.
 */
const LATEST_DEFINITION: Definition = {
  ...BOUND_DEFINITION,
  version: 5,
  nodes: [
    {
      id: 'node_new',
      type: 'text_generation',
      name: 'A node only the latest version has',
      position: { x: 0, y: 0 },
      config: {},
    },
  ],
  edges: [],
};

const NODE_TYPES: NodeMetadata[] = [
  {
    type: 'image_generation',
    displayName: 'Image Generation',
    category: 'Media',
    executionKind: 'ASYNC',
    inputs: [],
    outputs: [{ name: 'image', dataType: 'image', required: true }],
    configSchema: { type: 'object', properties: {} },
    uiSchema: { fields: [] },
    sideEffect: { kind: 'EXTERNAL', idempotency: 'UNKNOWN' },
  },
  {
    type: 'captioning',
    displayName: 'Captioning',
    category: 'Media',
    executionKind: 'SYNC',
    inputs: [{ name: 'image', dataType: 'image', required: true }],
    outputs: [{ name: 'text', dataType: 'text', required: true }],
    configSchema: { type: 'object', properties: {} },
    uiSchema: { fields: [] },
    sideEffect: { kind: 'NONE', idempotency: 'SAFE' },
  },
];

function nodeRun(id: string, nodeId: string, status: NodeRun['status']): NodeRun {
  return {
    id,
    runId: 'run_1',
    nodeId,
    nodeType: nodeId === 'node_image' ? 'image_generation' : 'captioning',
    status,
    input: null,
    output: null,
    error: null,
    readyAt: '2026-08-03T12:00:00Z',
    startedAt: null,
    waitingAt: null,
    completedAt: null,
    latencyMs: null,
    tokenUsage: null,
  };
}

function stubFetch() {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://studio.test');
    if (url.pathname === '/api/definitions/wf_1/versions/3') {
      return Promise.resolve(jsonResponse(200, BOUND_DEFINITION));
    }
    if (url.pathname === '/api/definitions/wf_1/versions/5') {
      return Promise.resolve(jsonResponse(200, LATEST_DEFINITION));
    }
    if (url.pathname === '/api/node-types') {
      return Promise.resolve(jsonResponse(200, { items: NODE_TYPES }));
    }
    throw new Error(`unexpected fetch: ${url.pathname}`);
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('ObserveCanvas', () => {
  it("renders the Run's own bound Definition version topology, never the latest", async () => {
    const fetchMock = stubFetch();
    render(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={[]}
        selectedNodeRunId={null}
        onSelectNodeRun={vi.fn()}
      />,
    );

    await screen.findByTestId('rf__node-node_image');
    expect(screen.getByTestId('rf__node-node_caption')).toBeInTheDocument();
    // The latest version's own node never appears, and that version is never fetched.
    expect(screen.queryByTestId('rf__node-node_new')).not.toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith('/api/definitions/wf_1/versions/3', expect.anything());
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/versions/5'))).toBe(
      false,
    );
  });

  it("shows each node's own NodeRun status from the Snapshot, and idle for a node with none", async () => {
    stubFetch();
    render(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={[nodeRun('nr_image', 'node_image', 'SUCCEEDED')]}
        selectedNodeRunId={null}
        onSelectNodeRun={vi.fn()}
      />,
    );

    const imageNode = await screen.findByTestId('rf__node-node_image');
    // SUCCEEDED uses the same `lib/status.ts` border class as everywhere else in Studio:
    // `nodeRunBorderClass` returns a `border-[var(--status-*-dot)]` Tailwind arbitrary-value
    // class, so a substring match on the CSS variable name is used instead of matching the
    // literal (bracket- and paren-laden) class string.
    expect(imageNode.querySelector('[class*="status-succeeded-dot"]')).toBeInTheDocument();

    const captionNode = screen.getByTestId('rf__node-node_caption');
    // No NodeRun yet: idle, not any terminal or in-flight status colour.
    expect(captionNode.querySelector('[class*="status-succeeded-dot"]')).not.toBeInTheDocument();
    expect(captionNode.querySelector('[class*="status-running-dot"]')).not.toBeInTheDocument();
    expect(captionNode.querySelector('[class*="status-waiting-dot"]')).not.toBeInTheDocument();
    expect(captionNode.querySelector('[class*="status-failed-dot"]')).not.toBeInTheDocument();
  });

  it('keeps the NodeRun status border on a selected node instead of the Edit selection colour', async () => {
    stubFetch();
    render(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={[nodeRun('nr_image', 'node_image', 'SUCCEEDED')]}
        selectedNodeRunId="nr_image"
        onSelectNodeRun={vi.fn()}
      />,
    );

    const imageNode = await screen.findByTestId('rf__node-node_image');
    const card = imageNode.querySelector<HTMLElement>('[title="Generate"]');
    expect(card).not.toBeNull();
    // Status colours are never overridden (04 §4): a selected SUCCEEDED node still reads as
    // succeeded, and the Edit-mode amber selected border/fill is not applied over it.
    expect(card).toHaveClass('border-[var(--status-succeeded-dot)]');
    expect(card?.style.borderColor).toBe('');
    expect(card?.style.backgroundColor).toBe('');
    expect(card?.outerHTML).not.toMatch(/fdb022|rgb\(253, 176, 34\)/i);
    // Selection is still visible, through a cue that does not replace the status border.
    expect(card).toHaveAttribute('data-selected', 'true');
  });

  it('resolves a Canvas node click to its own NodeRun id', async () => {
    stubFetch();
    const onSelectNodeRun = vi.fn();
    render(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={[nodeRun('nr_image', 'node_image', 'SUCCEEDED')]}
        selectedNodeRunId={null}
        onSelectNodeRun={onSelectNodeRun}
      />,
    );

    const imageNode = await screen.findByTestId('rf__node-node_image');
    fireEvent.click(imageNode);
    expect(onSelectNodeRun).toHaveBeenCalledWith('nr_image');
  });

  it('does nothing when clicking a node with no NodeRun yet (idle)', async () => {
    stubFetch();
    const onSelectNodeRun = vi.fn();
    render(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={[]}
        selectedNodeRunId={null}
        onSelectNodeRun={onSelectNodeRun}
      />,
    );

    const captionNode = await screen.findByTestId('rf__node-node_caption');
    fireEvent.click(captionNode);
    expect(onSelectNodeRun).not.toHaveBeenCalled();
  });

  it('returns to the Run Summary when the blank Canvas area is clicked', async () => {
    stubFetch();
    const onSelectNodeRun = vi.fn();
    render(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={[nodeRun('nr_image', 'node_image', 'SUCCEEDED')]}
        selectedNodeRunId="nr_image"
        onSelectNodeRun={onSelectNodeRun}
      />,
    );

    await screen.findByTestId('rf__node-node_image');
    // The pane, not the outer wrapper, is what React Flow attaches `onPaneClick` to.
    const pane = document.querySelector('.react-flow__pane');
    expect(pane).not.toBeNull();
    fireEvent.click(pane as Element);
    expect(onSelectNodeRun).toHaveBeenCalledWith(null);
  });

  it('renders every node visibly once React Flow measures it, so fitView sees real dimensions', async () => {
    // jsdom has no layout; give every element a size so React Flow emits the same
    // `dimensions` changes a browser does after mounting a node (mirrors
    // EditPage.test.tsx's "canvas measurement" coverage of the same defect class).
    vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(180);
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(60);
    stubFetch();
    render(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={[]}
        selectedNodeRunId={null}
        onSelectNodeRun={vi.fn()}
      />,
    );

    const imageNode = await screen.findByTestId('rf__node-node_image');
    const captionNode = await screen.findByTestId('rf__node-node_caption');
    // React Flow keeps a node's wrapper `visibility: hidden` until `node.measured` is fed
    // back into the controlled `nodes` prop; an unmeasured node's real bounds are never
    // included in `fitView`'s calculation, which is what clips it at the canvas edge.
    await waitFor(() => expect(imageNode).toHaveStyle({ visibility: 'visible' }));
    await waitFor(() => expect(captionNode).toHaveStyle({ visibility: 'visible' }));
  });

  it('keeps a measured node visible across a NodeRun update, instead of going unmeasured again', async () => {
    vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(180);
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(60);
    stubFetch();
    const { rerender } = render(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={[]}
        selectedNodeRunId={null}
        onSelectNodeRun={vi.fn()}
      />,
    );

    const imageNode = await screen.findByTestId('rf__node-node_image');
    await waitFor(() => expect(imageNode).toHaveStyle({ visibility: 'visible' }));

    // A live NodeRun status update (e.g. from SSE) gives `nodeRuns` a new array reference,
    // which recomputes every Canvas node object (`nodeRunStatus` is read off it). Without
    // feeding the already-known `measured` dimensions back into that recomputed node,
    // React Flow treats it as freshly unmeasured — hidden again — until the next
    // ResizeObserver tick; a `fitView` that lands inside that window clips the node.
    rerender(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={[nodeRun('nr_image', 'node_image', 'RUNNING')]}
        selectedNodeRunId={null}
        onSelectNodeRun={vi.fn()}
      />,
    );

    // Checked synchronously, before the stubbed ResizeObserver's microtask can re-measure,
    // so this only passes when the recomputed node already carried its dimensions forward.
    expect(imageNode).toHaveStyle({ visibility: 'visible' });
  });
});
