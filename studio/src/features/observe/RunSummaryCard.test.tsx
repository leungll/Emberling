import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { RunSummaryCard } from './RunSummaryCard';
import type { NodeRun, RunSnapshot, RunStatus } from '@/api/types';

function waitingNodeRun(): NodeRun {
  return {
    id: 'nr_image',
    runId: 'run_1',
    nodeId: 'node_image',
    nodeType: 'image_generation',
    status: 'WAITING_CALLBACK',
    input: null,
    output: null,
    error: null,
    readyAt: '2026-08-03T12:00:01Z',
    startedAt: '2026-08-03T12:00:02Z',
    waitingAt: '2026-08-03T12:00:03Z',
    completedAt: null,
    latencyMs: null,
    tokenUsage: null,
  };
}

function snapshot(status: RunStatus, nodeRuns: NodeRun[]): RunSnapshot {
  return {
    run: {
      id: 'run_1',
      workflowId: 'wf_media',
      definitionVersion: 4,
      status,
      input: {},
      output: null,
      error: status === 'FAILED' ? { code: 'NODE_FAILED', message: 'provider rejected' } : null,
      startedAt: '2026-08-03T12:00:00Z',
      completedAt: status === 'COMPLETED' || status === 'FAILED' ? '2026-08-03T12:01:05Z' : null,
    },
    nodeRuns,
    lastSeq: 8,
  };
}

const NAMES = new Map([['node_image', 'Image Generation']]);

describe('RunSummaryCard', () => {
  it('grows with its RUNNING / WAITING lists and keeps the Run ID from orphaning characters', () => {
    render(
      <RunSummaryCard
        snapshot={snapshot('PAUSED', [waitingNodeRun()])}
        events={[]}
        followLive
        nodeNames={NAMES}
      >
        <ul data-testid="rail" />
      </RunSummaryCard>,
    );

    // The card is sized by its content, not clipped to a fixed-height scroll box.
    const card = screen.getByTestId('run-summary');
    expect(card).not.toHaveClass('overflow-auto');
    expect(card).not.toHaveClass('min-h-0');
    // The Run ID wraps anywhere instead of break-all with a one-character tail.
    const runId = screen.getByText('run_1');
    expect(runId).toHaveClass('[overflow-wrap:anywhere]');
    expect(runId).not.toHaveClass('break-all');
  });

  it('names the waiting NodeRuns when the server reports the Run PAUSED', () => {
    render(
      <RunSummaryCard
        snapshot={snapshot('PAUSED', [waitingNodeRun()])}
        events={[]}
        followLive
        definitionName="AIGC Media"
        nodeNames={NAMES}
      />,
    );

    expect(screen.getByTestId('run-summary-banner')).toHaveAttribute('data-tone', 'waiting');
    expect(screen.getByText('Waiting for Image Generation callback')).toBeInTheDocument();
    expect(screen.getByText('AIGC Media · v4')).toBeInTheDocument();
    expect(screen.getByText('wf_media · v4')).toBeInTheDocument();
    expect(screen.getByText('latest seq 8')).toBeInTheDocument();
  });

  it('keys the banner on the server Run status, never on a WAITING_CALLBACK NodeRun', () => {
    // A NodeRun can be WAITING_CALLBACK before the server has aggregated the Run to
    // PAUSED; Studio must keep showing the server's RUNNING.
    render(
      <RunSummaryCard snapshot={snapshot('RUNNING', [waitingNodeRun()])} events={[]} followLive />,
    );

    expect(screen.getByTestId('run-summary-banner')).toHaveAttribute('data-tone', 'running');
    expect(screen.getByText('Running')).toBeInTheDocument();
    expect(screen.queryByText(/callback$/)).not.toBeInTheDocument();
  });

  it('shows the duration of a finished Run and the error of a failed one', () => {
    const { rerender } = render(
      <RunSummaryCard snapshot={snapshot('COMPLETED', [])} events={[]} followLive={false} />,
    );
    expect(screen.getByText('Completed')).toBeInTheDocument();
    expect(screen.getByText('01:05')).toBeInTheDocument();
    expect(screen.getByText('Run finished')).toBeInTheDocument();

    rerender(<RunSummaryCard snapshot={snapshot('FAILED', [])} events={[]} followLive={false} />);
    expect(screen.getByTestId('run-summary-banner')).toHaveAttribute('data-tone', 'failed');
    expect(screen.getByText('NODE_FAILED: provider rejected')).toBeInTheDocument();
  });
});
