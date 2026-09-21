import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { ObserveHeader } from './ObserveHeader';
import { RunRail } from './RunRail';
import type { NodeRun, Run, RunSnapshot, RunStatus } from '@/api/types';

function run(status: RunStatus): Run {
  return {
    id: 'run_123',
    workflowId: 'wf_123',
    definitionVersion: 4,
    status,
    input: {},
    output: null,
    error: null,
  };
}

function nodeRun(id: string, status: NodeRun['status']): NodeRun {
  return {
    id,
    runId: 'run_123',
    nodeId: id.replace('nr_', 'node_'),
    nodeType: 'image_generation',
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
  };
}

function renderHeader(status: RunStatus) {
  return render(
    <MemoryRouter>
      <ObserveHeader run={run(status)} onRunAgain={vi.fn()} />
    </MemoryRouter>,
  );
}

describe('ObserveHeader', () => {
  it('shows "Waiting for external result" when the server snapshot says PAUSED', () => {
    renderHeader('PAUSED');
    expect(screen.getByTestId('run-status')).toHaveTextContent('Waiting for external result');
  });

  it('does not show PAUSED wording when the snapshot says RUNNING but a NodeRun is WAITING_CALLBACK', () => {
    // The PAUSED aggregate belongs to the Backend. A dispatched Image Generation node
    // alongside a READY Caption node must still read Running.
    const snapshot: RunSnapshot = {
      run: run('RUNNING'),
      nodeRuns: [nodeRun('nr_image', 'WAITING_CALLBACK'), nodeRun('nr_caption', 'READY')],
      lastSeq: 7,
    };

    render(
      <MemoryRouter>
        <ObserveHeader run={snapshot.run} onRunAgain={vi.fn()} />
        <RunRail snapshot={snapshot} selectedNodeRunId={null} onSelect={vi.fn()} />
      </MemoryRouter>,
    );

    expect(screen.getByTestId('run-status')).toHaveTextContent('Running');
    expect(screen.getByTestId('run-status')).not.toHaveTextContent('Waiting for external result');
    // The NodeRun still reports its own waiting status in the rail.
    expect(screen.getByText('Waiting for callback')).toBeInTheDocument();
  });

  it('renders the remaining Run statuses with the documented wording', () => {
    renderHeader('RUNNING');
    expect(screen.getByTestId('run-status')).toHaveTextContent('Running');

    renderHeader('COMPLETED');
    expect(screen.getAllByTestId('run-status').at(-1)).toHaveTextContent('Completed');

    renderHeader('FAILED');
    expect(screen.getAllByTestId('run-status').at(-1)).toHaveTextContent('Failed');
  });
});
