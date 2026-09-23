import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { WorkflowStepper } from './WorkflowStepper';
import { topologicalNodeOrder } from './topology';
import type { Definition, NodeRun, NodeRunStatus, RunEvent } from '@/api/types';

function node(id: string, type: string, name: string) {
  return { id, type, name, position: { x: 0, y: 0 }, config: {} };
}

// Declared out of topological order on purpose: the stepper must follow the edges.
const DEFINITION: Definition = {
  workflowId: 'wf_1',
  version: 2,
  name: 'Pipeline',
  description: '',
  nodes: [
    node('node_output', 'text_output', 'Text Output'),
    node('node_input', 'text_input', 'Text Input'),
    node('node_agent', 'agent', 'Tool Assistant'),
    node('node_running', 'text_generation', 'Summarise'),
    node('node_failed', 'image_generation', 'Render'),
    node('node_ready', 'captioning', 'Caption'),
  ],
  edges: [
    {
      id: 'e1',
      source: 'node_input',
      sourceHandle: 'text',
      target: 'node_agent',
      targetHandle: 'input',
    },
    {
      id: 'e2',
      source: 'node_agent',
      sourceHandle: 'text',
      target: 'node_output',
      targetHandle: 'text',
    },
  ],
  runInputSchema: { type: 'object', properties: {} },
  validation: { status: 'VALID', validatorVersion: 'v1', validatedAt: '2026-08-01T00:00:00Z' },
  createdAt: '2026-08-01T00:00:00Z',
};

function nodeRun(nodeId: string, status: NodeRunStatus, extra: Partial<NodeRun> = {}): NodeRun {
  return {
    id: nodeId.replace('node_', 'nr_'),
    runId: 'run_1',
    nodeId,
    nodeType: DEFINITION.nodes.find((n) => n.id === nodeId)?.type ?? 'unknown',
    status,
    input: null,
    output: null,
    error: null,
    readyAt: '2026-08-03T12:00:01Z',
    startedAt: '2026-08-03T12:00:02Z',
    waitingAt: null,
    completedAt: null,
    latencyMs: null,
    tokenUsage: null,
    ...extra,
  };
}

const NODE_RUNS = [
  nodeRun('node_input', 'SUCCEEDED', { latencyMs: 27 }),
  nodeRun('node_agent', 'WAITING_CALLBACK', { waitingAt: '2026-08-03T12:00:03Z' }),
  nodeRun('node_running', 'RUNNING'),
  nodeRun('node_failed', 'FAILED', { error: { code: 'PROVIDER_ERROR', message: 'rejected' } }),
  nodeRun('node_ready', 'READY'),
];

const EVENTS: RunEvent[] = [5, 9].map((seq, index) => ({
  id: `ev_${seq}`,
  runId: 'run_1',
  nodeRunId: 'nr_agent',
  type: index === 0 ? 'AGENT_TURN_READY' : 'AGENT_TURN_STARTED',
  seq,
  timestamp: '2026-08-03T12:00:03Z',
  payload: { turnNo: index + 1 },
}));

function renderStepper(selectedNodeRunId: string | null = null, onSelect = vi.fn()) {
  render(
    <WorkflowStepper
      definition={DEFINITION}
      nodeRuns={NODE_RUNS}
      events={EVENTS}
      selectedNodeRunId={selectedNodeRunId}
      onSelect={onSelect}
    />,
  );
  return onSelect;
}

function step(nodeId: string) {
  return screen.getByTestId(`stepper-item-${nodeId}`);
}

describe('topologicalNodeOrder', () => {
  it('orders Definition nodes along the edges, keeping Definition order for ties', () => {
    expect(topologicalNodeOrder(DEFINITION).map((n) => n.id)).toEqual([
      'node_input',
      'node_agent',
      'node_output',
      'node_running',
      'node_failed',
      'node_ready',
    ]);
  });
});

describe('WorkflowStepper', () => {
  it('maps each server NodeRun status to its 04 §4 glyph and helper line', () => {
    renderStepper();

    const glyph = (nodeId: string) => within(step(nodeId)).getByTestId('stepper-glyph');
    expect(glyph('node_input')).toHaveAttribute('data-tone', 'succeeded');
    expect(step('node_input')).toHaveTextContent('SUCCEEDED · 27 ms');

    expect(glyph('node_agent')).toHaveAttribute('data-tone', 'waiting');
    expect(glyph('node_running')).toHaveAttribute('data-tone', 'running');
    expect(step('node_running')).toHaveTextContent('RUNNING · started 12:00:02Z');
    expect(glyph('node_failed')).toHaveAttribute('data-tone', 'failed');
    expect(step('node_failed')).toHaveTextContent('FAILED · PROVIDER_ERROR');

    // READY and not-yet-created NodeRuns are numbered and neutral, by step position.
    expect(glyph('node_ready')).toHaveAttribute('data-tone', 'neutral');
    expect(glyph('node_ready')).toHaveTextContent('6');
    expect(step('node_output')).toHaveAttribute('data-status', 'NOT_STARTED');
    expect(glyph('node_output')).toHaveTextContent('3');
    expect(step('node_output')).toHaveTextContent('Not started · no NodeRun yet');
  });

  it('shows the Agent NodeRun as its own card with the latest committed Turn number', () => {
    renderStepper('nr_agent');

    const agent = step('node_agent');
    expect(agent).toHaveTextContent('Agent NodeRun');
    expect(agent).toHaveTextContent('Tool Assistant');
    expect(agent).toHaveTextContent('WAITING_CALLBACK · Turn 2');
    expect(within(agent).getByRole('button')).toHaveAttribute('aria-pressed', 'true');
  });

  it('selects a step’s NodeRun, and offers nothing to select before a NodeRun exists', () => {
    const onSelect = renderStepper();

    fireEvent.click(within(step('node_running')).getByRole('button'));
    expect(onSelect).toHaveBeenCalledWith('nr_running');

    const notStarted = within(step('node_output')).getByRole('button');
    expect(notStarted).toBeDisabled();
    fireEvent.click(notStarted);
    expect(onSelect).toHaveBeenCalledTimes(1);
  });
});
