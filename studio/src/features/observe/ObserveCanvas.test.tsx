import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ObserveCanvas } from './ObserveCanvas';
import {
  COMPACT_NODE_HEIGHT,
  COMPACT_NODE_WIDTH,
  compactEdgeSides,
  compactTopologyExtent,
  compactTopologyPositions,
} from './topology';
import type { Definition, NodeMetadata, NodeRun } from '@/api/types';
import { requestUrl } from '@/test/requestUrl';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/** The Run's own bound Definition version, never the latest. */
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
    const url = new URL(requestUrl(input), 'http://studio.test');
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

/** Where a straight compact edge meets a node side (the hidden handle sits at its middle). */
function sidePoint(p: { x: number; y: number }, side: string): { x: number; y: number } {
  switch (side) {
    case 'top':
      return { x: p.x + COMPACT_NODE_WIDTH / 2, y: p.y };
    case 'bottom':
      return { x: p.x + COMPACT_NODE_WIDTH / 2, y: p.y + COMPACT_NODE_HEIGHT };
    case 'left':
      return { x: p.x, y: p.y + COMPACT_NODE_HEIGHT / 2 };
    default:
      return { x: p.x + COMPACT_NODE_WIDTH, y: p.y + COMPACT_NODE_HEIGHT / 2 };
  }
}

/**
 * Asserts geometrically that no edge's straight segment passes through the rectangle of a
 * node that is not one of its two ends, and that no two nodes overlap. The segment is
 * sampled finely rather than clipped, so the check does not share code with the layout.
 */
function expectNoEdgeCrossesForeignNode(definition: Definition) {
  const p = compactTopologyPositions(definition);
  const ids = definition.nodes.map((node) => node.id);
  for (const a of ids) {
    for (const b of ids) {
      if (a >= b) continue;
      const pa = p.get(a)!;
      const pb = p.get(b)!;
      const overlap =
        Math.abs(pa.x - pb.x) < COMPACT_NODE_WIDTH && Math.abs(pa.y - pb.y) < COMPACT_NODE_HEIGHT;
      expect(overlap, `${a} overlaps ${b}`).toBe(false);
    }
  }
  for (const edge of definition.edges) {
    const source = p.get(edge.source)!;
    const target = p.get(edge.target)!;
    const sides = compactEdgeSides(source, target);
    const from = sidePoint(source, sides.from);
    const to = sidePoint(target, sides.to);
    for (const id of ids) {
      if (id === edge.source || id === edge.target) continue;
      const r = p.get(id)!;
      for (let i = 0; i <= 400; i++) {
        const t = i / 400;
        const x = from.x + (to.x - from.x) * t;
        const y = from.y + (to.y - from.y) * t;
        const inside =
          x > r.x && x < r.x + COMPACT_NODE_WIDTH && y > r.y && y < r.y + COMPACT_NODE_HEIGHT;
        expect(inside, `edge ${edge.source}->${edge.target} passes through ${id}`).toBe(false);
      }
    }
  }
}

function dagOf(nodeIds: string[], pairs: [string, string][]): Definition {
  return {
    ...BOUND_DEFINITION,
    nodes: nodeIds.map((id) => ({
      id,
      type: 'captioning',
      name: id,
      position: { x: 0, y: 0 },
      config: {},
    })),
    edges: pairs.map(([source, target]) => ({
      id: `${source}-${target}`,
      source,
      sourceHandle: 'text',
      target,
      targetHandle: 'image',
    })),
  };
}

/** Shape of backend/test/fixtures/definitions/aigc_media.json: 7 nodes, 5 depth levels. */
const AIGC_MEDIA_SHAPE = dagOf(
  [
    'node_brief',
    'node_reference',
    'node_prompt',
    'node_rewrite',
    'node_image',
    'node_caption',
    'node_output',
  ],
  [
    ['node_brief', 'node_prompt'],
    ['node_prompt', 'node_rewrite'],
    ['node_rewrite', 'node_image'],
    ['node_reference', 'node_image'],
    ['node_rewrite', 'node_caption'],
    ['node_image', 'node_output'],
    ['node_caption', 'node_output'],
  ],
);

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
    expect(fetchMock.mock.calls.some(([input]) => requestUrl(input).includes('/versions/5'))).toBe(
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
    // Status colours are never overridden: a selected SUCCEEDED node still reads as
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
    fireEvent.click(pane!);
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
  it('renders compact name-and-status nodes, with no ports, port hints or config summary', async () => {
    stubFetch();
    render(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={[nodeRun('nr_image', 'node_image', 'WAITING_CALLBACK')]}
        selectedNodeRunId={null}
        onSelectNodeRun={vi.fn()}
      />,
    );

    const imageNode = await screen.findByTestId('rf__node-node_image');
    const captionNode = screen.getByTestId('rf__node-node_caption');
    expect(imageNode).toHaveTextContent('Generate');
    // The server status, in one word on screen; no tooltip repeats the hidden wording.
    const status = within(imageNode).getByTestId('compact-node-status');
    expect(status).toHaveTextContent('Waiting');
    expect(status).not.toHaveAttribute('title');
    // Screen readers get the full status wording, not the one-word visual status.
    expect(within(status).getByText('Waiting for callback')).toHaveClass('sr-only');
    expect(within(status).getByText('Waiting')).toHaveAttribute('aria-hidden', 'true');
    expect(within(captionNode).getByTestId('compact-node-status')).toHaveTextContent('idle');
    // Edit-card detail stays on the Edit canvas: no registered ports, hints or summary.
    expect(screen.queryByTestId('handle-out-image')).toBeNull();
    expect(document.querySelector('[data-port-hint]')).toBeNull();
    expect(imageNode).not.toHaveTextContent('IMAGE GENERATION');
    // Name and status keep the 12px label floor at the card's 100% fit.
    expect(imageNode.querySelector('.text-\\[13px\\]')).toHaveTextContent('Generate');
    expect(status).toHaveClass('text-[12px]');
  });

  it('grows to the whole layout height so a deep graph opens at 100% instead of shrinking its text', async () => {
    const deep = { ...AIGC_MEDIA_SHAPE, workflowId: 'wf_1', version: 3 };
    render(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={[]}
        selectedNodeRunId={null}
        onSelectNodeRun={vi.fn()}
        bound={{ definition: deep, nodeTypes: NODE_TYPES, error: null }}
      />,
    );
    const root = await screen.findByTestId('observe-canvas');
    const extent = compactTopologyExtent(compactTopologyPositions(deep));
    // Layout height plus the 8px fit margin on each side.
    expect(parseFloat(root.style.minHeight)).toBeGreaterThanOrEqual(extent.height + 16);
  });

  it('draws each Definition edge as a straight arrow between compact nodes', async () => {
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
    await screen.findByTestId('rf__node-node_image');
    await waitFor(() =>
      expect(document.querySelector('[data-testid="rf__edge-e1"]')).not.toBeNull(),
    );
  });
});

describe('compact topology layout', () => {
  const chain: Definition = {
    ...BOUND_DEFINITION,
    nodes: ['a', 'b', 'c', 'd', 'e'].map((id, index) => ({
      id,
      type: 'captioning',
      name: id,
      // Authored far apart for full-size Edit cards; the compact layout ignores this.
      position: { x: index * 400, y: 0 },
      config: {},
    })),
    edges: [
      { id: 'ab', source: 'a', sourceHandle: 'text', target: 'b', targetHandle: 'image' },
      { id: 'bc', source: 'b', sourceHandle: 'text', target: 'c', targetHandle: 'image' },
      { id: 'cd', source: 'c', sourceHandle: 'text', target: 'd', targetHandle: 'image' },
      { id: 'de', source: 'd', sourceHandle: 'text', target: 'e', targetHandle: 'image' },
    ],
  };

  it('snakes a plain chain three to a row in topological order, so five nodes fit the card', () => {
    const positions = compactTopologyPositions(chain);
    const [a, b, c, d, e] = ['a', 'b', 'c', 'd', 'e'].map((id) => positions.get(id)!);
    expect(a!.y).toBe(b!.y);
    expect(b!.y).toBe(c!.y);
    expect(a!.x).toBeLessThan(b!.x);
    expect(b!.x).toBeLessThan(c!.x);
    // Second row runs right to left, starting under the first row's last node.
    expect(d!.y).toBeGreaterThan(c!.y);
    expect(d!.x).toBe(c!.x);
    expect(e!.x).toBeLessThan(d!.x);
    // Whole graph stays within the 430px card's width at 100%.
    const right = Math.max(...[...positions.values()].map((p) => p.x)) + 120;
    expect(right).toBeLessThanOrEqual(400);
  });

  it('layers a branching DAG by depth so no edge runs behind an unrelated node', () => {
    // AIGC shape: brief feeds image and prompt; prompt feeds caption; image and caption
    // feed output. Topological order is brief, image, prompt, caption, output, so a snake
    // would draw brief -> prompt straight through image.
    const edge = (source: string, target: string) => ({
      id: `${source}-${target}`,
      source,
      sourceHandle: 'text',
      target,
      targetHandle: 'image',
    });
    const dag: Definition = {
      ...chain,
      nodes: ['brief', 'image', 'prompt', 'caption', 'output'].map((id) => ({
        id,
        type: 'captioning',
        name: id,
        position: { x: 0, y: 0 },
        config: {},
      })),
      edges: [
        edge('brief', 'image'),
        edge('brief', 'prompt'),
        edge('prompt', 'caption'),
        edge('image', 'output'),
        edge('caption', 'output'),
      ],
    };
    const p = compactTopologyPositions(dag);
    const at = (id: string) => p.get(id)!;
    // One row per dependency depth.
    expect(at('image').y).toBe(at('prompt').y);
    expect(at('brief').y).toBeLessThan(at('image').y);
    expect(at('caption').y).toBeGreaterThan(at('prompt').y);
    expect(at('output').y).toBeGreaterThan(at('caption').y);
    // brief sits between its two successors, and nothing else shares a row with it.
    expect(at('brief').x).toBeGreaterThan(at('image').x);
    expect(at('brief').x).toBeLessThan(at('prompt').x);
    // caption sits under prompt; output under the mean of image and caption.
    expect(at('caption').x).toBe(at('prompt').x);
    expect(at('output').x).toBe((at('image').x + at('caption').x) / 2);
    // Every edge joins nodes in different rows, so no straight edge runs along a row
    // through a node between its ends.
    for (const e of dag.edges) expect(at(e.source).y).not.toBe(at(e.target).y);
    expectNoEdgeCrossesForeignNode(dag);
    const right = Math.max(...[...p.values()].map((q) => q.x)) + 120;
    expect(right).toBeLessThanOrEqual(400);
  });

  it('never draws a skip edge through the node it skips (A->B->C plus A->C)', () => {
    // The e2e AIGC fixture's shape: input feeds image and output; image feeds output.
    // Centring B and C under A would put all three in one column, so the straight A->C
    // edge would run behind B's card.
    const skip = dagOf(
      ['a', 'b', 'c'],
      [
        ['a', 'b'],
        ['b', 'c'],
        ['a', 'c'],
      ],
    );
    expectNoEdgeCrossesForeignNode(skip);
    const p = compactTopologyPositions(skip);
    // Still one row per depth: the fix moves B aside, it does not flatten the graph.
    expect(p.get('a')!.y).toBeLessThan(p.get('b')!.y);
    expect(p.get('b')!.y).toBeLessThan(p.get('c')!.y);
    // Deterministic: the same Definition always lays out the same way.
    expect([...compactTopologyPositions(skip)]).toEqual([...p]);
  });

  it('lays out the 7-node AIGC fixture with no edge through a foreign node, inside the card width', () => {
    expectNoEdgeCrossesForeignNode(AIGC_MEDIA_SHAPE);
    const extent = compactTopologyExtent(compactTopologyPositions(AIGC_MEDIA_SHAPE));
    // Three columns fit the 430px card at 100%; five rows are 416px tall.
    expect(extent.width).toBeLessThanOrEqual(400);
    expect(extent.height).toBe(5 * COMPACT_NODE_HEIGHT + 4 * 24);
  });

  it('keeps the chain snake free of foreign-node crossings too', () => {
    expectNoEdgeCrossesForeignNode(chain);
  });

  it('routes edges sideways within a row and vertically between rows', () => {
    const positions = compactTopologyPositions(chain);
    expect(compactEdgeSides(positions.get('a')!, positions.get('b')!)).toEqual({
      from: 'right',
      to: 'left',
    });
    expect(compactEdgeSides(positions.get('c')!, positions.get('d')!)).toEqual({
      from: 'bottom',
      to: 'top',
    });
    expect(compactEdgeSides(positions.get('d')!, positions.get('e')!)).toEqual({
      from: 'left',
      to: 'right',
    });
  });
});
