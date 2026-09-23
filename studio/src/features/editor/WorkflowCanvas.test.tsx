import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import type { RegisteredFlowNode } from './RegisteredNode';
import { WorkflowCanvas } from './WorkflowCanvas';
import type { NodeMetadata } from '@/api/types';

const metadata: NodeMetadata = {
  type: 'text_generation',
  displayName: 'Text Generation',
  category: 'Prompt & Model',
  executionKind: 'SYNC',
  inputs: [{ name: 'prompt', dataType: 'text', required: true }],
  outputs: [{ name: 'text', dataType: 'text', required: true }],
  configSchema: { type: 'object', properties: {} },
  uiSchema: { fields: [] },
  sideEffect: { kind: 'EXTERNAL', idempotency: 'UNKNOWN' },
};

const nodes: RegisteredFlowNode[] = [
  {
    id: 'node_a',
    type: 'registered',
    position: { x: 0, y: 0 },
    selected: true,
    data: { label: 'Generate', metadata },
  },
];

function renderCanvas(readOnly: boolean) {
  const handlers = {
    onNodesChange: vi.fn(),
    onEdgesChange: vi.fn(),
    onConnect: vi.fn(),
    onSelectNode: vi.fn(),
  };
  render(<WorkflowCanvas nodes={nodes} edges={[]} readOnly={readOnly} {...handlers} />);
  return handlers;
}

function removals(mock: ReturnType<typeof vi.fn>): unknown[] {
  return mock.mock.calls.flatMap(([changes]) =>
    (changes as { type: string }[]).filter((change) => change.type === 'remove'),
  );
}

async function flushDeletes() {
  await act(async () => {
    await Promise.resolve();
  });
}

describe('WorkflowCanvas', () => {
  it('lets an editable canvas drag, connect and delete the selected node', async () => {
    const handlers = renderCanvas(false);

    expect(screen.getByTestId('rf__node-node_a')).toHaveClass('draggable');
    expect(screen.getByTestId('handle-out-text')).toHaveClass('connectable');

    fireEvent.keyDown(document.body, { key: 'Backspace' });
    // React Flow deletes through an async onBeforeDelete hook; one flushed microtask is
    // enough for it to land, which is what makes the read-only negative check meaningful.
    await flushDeletes();
    expect(removals(handlers.onNodesChange)).toHaveLength(1);
  });

  it('makes a read-only canvas inert: no drag, no connect, no delete', async () => {
    const handlers = renderCanvas(true);

    const node = screen.getByTestId('rf__node-node_a');
    expect(node).not.toHaveClass('draggable');
    expect(screen.getByTestId('handle-out-text')).not.toHaveClass('connectable');
    expect(screen.getByTestId('workflow-canvas')).toHaveAttribute('data-read-only', 'true');

    fireEvent.keyDown(document.body, { key: 'Backspace' });
    fireEvent.keyDown(document.body, { key: 'Delete' });
    await flushDeletes();
    expect(removals(handlers.onNodesChange)).toHaveLength(0);
    expect(removals(handlers.onEdgesChange)).toHaveLength(0);

    // Selection stays available: Observe reuses this canvas to pick a NodeRun.
    fireEvent.click(node);
    expect(handlers.onSelectNode).toHaveBeenCalledWith('node_a');
  });

  it('hides port hint labels, not ports, when a read-only fit shrinks cards below 100%', async () => {
    // A 180x60 canvas cannot fit a 176x128 card at 100%, so fitting the whole graph zooms
    // out: a 10px label there would render under the 10px port-hint floor.
    vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(180);
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(60);
    const sized = nodes.map((node) => ({ ...node, width: 176, height: 128 }));
    try {
      render(<WorkflowCanvas nodes={sized} edges={[]} readOnly />);
      const canvas = screen.getByTestId('workflow-canvas');
      await waitFor(() => expect(canvas).toHaveAttribute('data-port-hints', 'hidden'));
      expect(screen.getByTestId('handle-out-text')).toBeInTheDocument();
    } finally {
      vi.restoreAllMocks();
    }
  });

  it('keeps port hint labels on an editable canvas at any zoom', async () => {
    const sized = nodes.map((node) => ({ ...node, width: 176, height: 128 }));
    render(<WorkflowCanvas nodes={sized} edges={[]} />);
    await flushDeletes();
    expect(screen.getByTestId('workflow-canvas')).toHaveAttribute('data-port-hints', 'shown');
  });
});
