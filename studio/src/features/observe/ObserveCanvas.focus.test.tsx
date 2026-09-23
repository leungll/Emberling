import { render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ObserveCanvas } from './ObserveCanvas';
import type { CanvasFocusRequest } from '@/features/editor/WorkflowCanvas';
import type { Definition, NodeMetadata, NodeRun } from '@/api/types';

/**
 * `WorkflowCanvas` and its `focus` contract already have their own coverage
 * (`features/editor/WorkflowCanvas.tsx`, `.test.tsx`): a real `fitView` call is proven
 * there. This file only proves that `ObserveCanvas` computes the right `focus` prop —
 * the Definition node id of whichever NodeRun is selected — when selection changes from
 * outside the Canvas (04 §3.1: "选择联动").
 */
let lastFocus: CanvasFocusRequest | null | undefined;
vi.mock('@/features/editor/WorkflowCanvas', () => ({
  WorkflowCanvas: (props: { focus?: CanvasFocusRequest | null }) => {
    lastFocus = props.focus;
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
      const url = new URL(String(input), 'http://studio.test');
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
  lastFocus = undefined;
});

describe('ObserveCanvas — focus on selection from elsewhere', () => {
  it("passes the selected NodeRun's own Definition node id as the focus request", async () => {
    stubFetch();
    const { rerender } = render(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={[nodeRun('nr_image', 'node_image'), nodeRun('nr_caption', 'node_caption')]}
        selectedNodeRunId={null}
        onSelectNodeRun={vi.fn()}
      />,
    );

    await screen.findByTestId('mock-workflow-canvas');
    expect(lastFocus).toBeNull();

    // Selecting a NodeRun elsewhere (e.g. the Run Rail or Timeline) re-renders with that
    // NodeRun's id; the Canvas must resolve it to the matching Definition node and focus it.
    rerender(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={[nodeRun('nr_image', 'node_image'), nodeRun('nr_caption', 'node_caption')]}
        selectedNodeRunId="nr_caption"
        onSelectNodeRun={vi.fn()}
      />,
    );

    expect(lastFocus?.nodeId).toBe('node_caption');
    const firstSeq = lastFocus?.seq;

    rerender(
      <ObserveCanvas
        workflowId="wf_1"
        definitionVersion={3}
        nodeRuns={[nodeRun('nr_image', 'node_image'), nodeRun('nr_caption', 'node_caption')]}
        selectedNodeRunId="nr_image"
        onSelectNodeRun={vi.fn()}
      />,
    );

    expect(lastFocus?.nodeId).toBe('node_image');
    // A changed selection always bumps `seq`, even switching between two already-seen nodes.
    expect(lastFocus?.seq).not.toBe(firstSeq);
  });
});
