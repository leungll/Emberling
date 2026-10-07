import { render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ObserveCanvas } from './ObserveCanvas';
import type { Definition, NodeMetadata, NodeRun } from '@/api/types';
import type { RegisteredFlowNode } from '@/features/editor/RegisteredNode';
import { requestUrl } from '@/test/requestUrl';

/**
 * The READ-ONLY TOPOLOGY card always shows the whole graph (fitted by `WorkflowCanvas`
 * itself, whose `fitView` behaviour has its own coverage). This file proves that selecting
 * a NodeRun from outside the Canvas (linked selection) only moves the highlight: the
 * Canvas receives no zoom-to-node request, and nothing but the nodes' `selected` flags
 * changes between renders.
 */
let lastProps: Record<string, unknown> | undefined;
vi.mock('@/features/editor/WorkflowCanvas', () => ({
  WorkflowCanvas: (props: Record<string, unknown>) => {
    lastProps = props;
    return <div data-testid="mock-workflow-canvas" />;
  },
}));

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const DEFINITION: Definition = {
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
  edges: [],
  runInputSchema: { type: 'object', properties: {} },
  validation: { status: 'VALID', validatorVersion: 'v1', validatedAt: '2026-08-01T00:00:00Z' },
  createdAt: '2026-08-01T00:00:00Z',
};

const NODE_TYPES: NodeMetadata[] = [];

function nodeRun(id: string, nodeId: string): NodeRun {
  return {
    id,
    runId: 'run_1',
    nodeId,
    nodeType: nodeId === 'node_image' ? 'image_generation' : 'captioning',
    status: 'SUCCEEDED',
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
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = new URL(requestUrl(input), 'http://studio.test');
      if (url.pathname === '/api/definitions/wf_1/versions/3') {
        return Promise.resolve(jsonResponse(200, DEFINITION));
      }
      if (url.pathname === '/api/node-types') {
        return Promise.resolve(jsonResponse(200, { items: NODE_TYPES }));
      }
      throw new Error(`unexpected fetch: ${url.pathname}`);
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  lastProps = undefined;
});

const NODE_RUNS = [nodeRun('nr_image', 'node_image'), nodeRun('nr_caption', 'node_caption')];

function canvasProps() {
  const props = lastProps ?? {};
  const nodes = props.nodes as RegisteredFlowNode[];
  return {
    props,
    selected: nodes.filter((node) => node.selected).map((node) => node.id),
    // Everything the viewport is fitted from: node ids, positions and sizes.
    fitTarget: nodes.map((node) => ({ id: node.id, position: node.position })),
  };
}

describe('ObserveCanvas — selection from elsewhere', () => {
  it('highlights the selected NodeRun without changing the viewport or its fit target', async () => {
    stubFetch();
    const { rerender } = render(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={NODE_RUNS}
        selectedNodeRunId={null}
        onSelectNodeRun={vi.fn()}
      />,
    );
    await screen.findByTestId('mock-workflow-canvas');
    const before = canvasProps();
    expect(before.selected).toEqual([]);
    expect(before.props).not.toHaveProperty('focus');
    expect(before.props.readOnly).toBe(true);

    // Selecting a NodeRun elsewhere (the Run Rail or Timeline) resolves to its Definition
    // node and marks only that node selected.
    rerender(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={NODE_RUNS}
        selectedNodeRunId="nr_caption"
        onSelectNodeRun={vi.fn()}
      />,
    );
    const after = canvasProps();
    expect(after.selected).toEqual(['node_caption']);
    expect(after.props).not.toHaveProperty('focus');
    expect(after.fitTarget).toEqual(before.fitTarget);
    expect(Object.keys(after.props).sort()).toEqual(Object.keys(before.props).sort());

    rerender(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={NODE_RUNS}
        selectedNodeRunId="nr_image"
        onSelectNodeRun={vi.fn()}
      />,
    );
    expect(canvasProps().selected).toEqual(['node_image']);
    expect(canvasProps().props).not.toHaveProperty('focus');
  });
});
