import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { RunRail } from './RunRail';
import type { NodeRun, NodeRunStatus, RunSnapshot } from '@/api/types';

function nodeRun(id: string, status: NodeRunStatus, readyAt: string): NodeRun {
  return {
    id,
    runId: 'run_1',
    nodeId: id.replace('nr_', 'node_'),
    nodeType: 'image_generation',
    status,
    input: null,
    output: null,
    error: null,
    readyAt,
    startedAt: null,
    waitingAt: null,
    completedAt: null,
    latencyMs: null,
    tokenUsage: null,
  };
}

function snapshot(nodeRuns: NodeRun[]): RunSnapshot {
  return {
    run: {
      id: 'run_1',
      workflowId: 'wf_1',
      definitionVersion: 2,
      status: 'RUNNING',
      input: {},
      output: null,
      error: null,
    },
    nodeRuns,
    lastSeq: 9,
  };
}

describe('RunRail', () => {
  it('groups Active NodeRuns into Running and Waiting, and omits finished ones', () => {
    const snap = snapshot([
      nodeRun('nr_input', 'SUCCEEDED', '2026-08-03T12:00:01Z'),
      nodeRun('nr_image', 'WAITING_CALLBACK', '2026-08-03T12:00:02Z'),
      nodeRun('nr_caption', 'READY', '2026-08-03T12:00:03Z'),
      nodeRun('nr_prompt', 'RUNNING', '2026-08-03T12:00:04Z'),
      nodeRun('nr_summary', 'FAILED', '2026-08-03T12:00:05Z'),
    ]);

    render(<RunRail snapshot={snap} selectedNodeRunId={null} onSelect={() => {}} />);

    // Finished NodeRuns (SUCCEEDED, FAILED) are not part of the Run Rail; they are
    // reached via Timeline Event click / Canvas instead (docs/04-ux.md §3.1).
    expect(screen.queryByText('node_input')).not.toBeInTheDocument();
    expect(screen.queryByText('node_summary')).not.toBeInTheDocument();

    const runningGroup = screen.getByRole('heading', { name: 'Running' }).closest('section');
    const waitingGroup = screen.getByRole('heading', { name: 'Waiting' }).closest('section');
    expect(runningGroup).not.toBeNull();
    expect(waitingGroup).not.toBeNull();

    // READY and RUNNING NodeRuns are both Active and grouped under Running.
    expect(within(runningGroup!).getByText('node_caption')).toBeInTheDocument();
    expect(within(runningGroup!).getByText('node_prompt')).toBeInTheDocument();
    expect(within(runningGroup!).queryByText('node_image')).not.toBeInTheDocument();

    // WAITING_CALLBACK NodeRuns are grouped under Waiting.
    expect(within(waitingGroup!).getByText('node_image')).toBeInTheDocument();
    expect(within(waitingGroup!).queryByText('node_prompt')).not.toBeInTheDocument();
  });

  it('shows None for a group with no Active NodeRuns', () => {
    const snap = snapshot([nodeRun('nr_input', 'SUCCEEDED', '2026-08-03T12:00:01Z')]);

    render(<RunRail snapshot={snap} selectedNodeRunId={null} onSelect={() => {}} />);

    const runningGroup = screen.getByRole('heading', { name: 'Running' }).closest('section');
    const waitingGroup = screen.getByRole('heading', { name: 'Waiting' }).closest('section');
    expect(within(runningGroup!).getByText('None')).toBeInTheDocument();
    expect(within(waitingGroup!).getByText('None')).toBeInTheDocument();
  });

  it('makes every listed Active NodeRun selectable', () => {
    const onSelect = vi.fn();
    const snap = snapshot([
      nodeRun('nr_prompt', 'RUNNING', '2026-08-03T12:00:04Z'),
      nodeRun('nr_image', 'WAITING_CALLBACK', '2026-08-03T12:00:02Z'),
    ]);

    render(<RunRail snapshot={snap} selectedNodeRunId={null} onSelect={onSelect} />);

    fireEvent.click(screen.getByRole('button', { name: /node_prompt/ }));
    expect(onSelect).toHaveBeenCalledWith('nr_prompt');

    fireEvent.click(screen.getByRole('button', { name: /node_image/ }));
    expect(onSelect).toHaveBeenCalledWith('nr_image');
  });
});
