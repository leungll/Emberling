import { describe, expect, it } from 'vitest';

import { applyEvent, needsSnapshotRead } from './applyEvent';
import type { EventType, NodeRun, RunEvent, RunSnapshot, RunStatus } from '@/api/types';

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
    startedAt: null,
    waitingAt: null,
    completedAt: null,
    latencyMs: null,
    tokenUsage: null,
  };
}

function snapshot(runStatus: RunStatus, nodeRuns: NodeRun[], lastSeq = 0): RunSnapshot {
  return {
    run: {
      id: 'run_123',
      workflowId: 'wf_123',
      definitionVersion: 4,
      status: runStatus,
      input: {},
      output: null,
      error: null,
      tokenUsage: null,
    },
    nodeRuns,
    lastSeq,
  };
}

function event(
  seq: number,
  type: EventType,
  nodeRunId: string | null = null,
  payload: RunEvent['payload'] = {},
): RunEvent {
  return {
    id: `evt_${seq}`,
    runId: 'run_123',
    nodeRunId,
    type,
    seq,
    timestamp: '2026-08-03T12:00:04Z',
    payload,
  };
}

/** A Run status Event as the Backend writes it: payload `{from, to}`. */
function runEvent(seq: number, type: EventType, from: RunStatus, to: RunStatus): RunEvent {
  return event(seq, type, null, { from, to });
}

const NON_RUN_EVENTS: EventType[] = [
  'NODE_READY',
  'NODE_STARTED',
  'NODE_RETRYING',
  'NODE_DISPATCHED',
  'NODE_CALLBACK_RECEIVED',
  'NODE_COMPLETED',
  'NODE_FAILED',
  'AGENT_STARTED',
  'AGENT_TURN_READY',
  'AGENT_TURN_STARTED',
  'AGENT_DECISION_COMMITTED',
  'AGENT_ACTION_STARTED',
  'AGENT_ACTION_WAITING',
  'AGENT_ACTION_COMPLETED',
  'AGENT_ACTION_FAILED',
  'AGENT_STATE_UPDATED',
  'AGENT_COMPLETED',
  'AGENT_FAILED',
];

describe('applyEvent', () => {
  it('never changes run.status unless a RUN_* event says so', () => {
    const base = snapshot('RUNNING', [nodeRun('nr_1', 'RUNNING')]);

    let seq = 0;
    for (const type of NON_RUN_EVENTS) {
      seq += 1;
      const next = applyEvent(base, event(seq, type, 'nr_1'));
      expect(next.run.status, `${type} must not move the Run status`).toBe('RUNNING');
    }
  });

  it('does not turn a WAITING_CALLBACK NodeRun into a PAUSED Run', () => {
    // The PAUSED aggregate is a Backend decision that arrives as RUN_PAUSED. A single
    // dispatched node proves nothing about the rest of the graph.
    const base = snapshot('RUNNING', [
      nodeRun('nr_image', 'RUNNING'),
      nodeRun('nr_caption', 'READY'),
    ]);

    const next = applyEvent(base, event(1, 'NODE_DISPATCHED', 'nr_image'));

    expect(next.nodeRuns[0]?.status).toBe('WAITING_CALLBACK');
    expect(next.nodeRuns[1]?.status).toBe('READY');
    expect(next.run.status).toBe('RUNNING');
  });

  it('applies the run status a RUN_* event payload states as `to`', () => {
    const base = snapshot('RUNNING', [nodeRun('nr_1', 'WAITING_CALLBACK')]);

    expect(applyEvent(base, runEvent(1, 'RUN_PAUSED', 'RUNNING', 'PAUSED')).run.status).toBe(
      'PAUSED',
    );
    expect(applyEvent(base, runEvent(1, 'RUN_COMPLETED', 'RUNNING', 'COMPLETED')).run.status).toBe(
      'COMPLETED',
    );
    expect(applyEvent(base, runEvent(1, 'RUN_FAILED', 'RUNNING', 'FAILED')).run.status).toBe(
      'FAILED',
    );
    expect(
      applyEvent(snapshot('PAUSED', []), runEvent(1, 'RUN_RESUMED', 'PAUSED', 'RUNNING')).run
        .status,
    ).toBe('RUNNING');
  });

  it('does not guess a run status the RUN_* payload does not state', () => {
    const base = snapshot('RUNNING', []);

    // No `to` in the payload: the status stays as last read until the Snapshot is re-read.
    expect(applyEvent(base, event(1, 'RUN_PAUSED')).run.status).toBe('RUNNING');
    expect(applyEvent(base, event(1, 'RUN_COMPLETED', null, { to: 'SUCCEEDED' })).run.status).toBe(
      'RUNNING',
    );
    expect(needsSnapshotRead(base, event(1, 'RUN_PAUSED'))).toBe(true);
  });

  it('ignores an event whose seq is not newer than the snapshot', () => {
    const base = snapshot('RUNNING', [nodeRun('nr_1', 'RUNNING')], 12);

    expect(applyEvent(base, event(12, 'RUN_COMPLETED'))).toBe(base);
    expect(applyEvent(base, event(5, 'NODE_COMPLETED', 'nr_1'))).toBe(base);
  });

  it('advances lastSeq for every applied event', () => {
    const base = snapshot('RUNNING', [nodeRun('nr_1', 'RUNNING')], 3);

    expect(applyEvent(base, event(4, 'NODE_RETRYING', 'nr_1')).lastSeq).toBe(4);
    expect(applyEvent(base, event(9, 'RUN_PAUSED')).lastSeq).toBe(9);
  });

  it('does not invent a NodeRun the snapshot has not seen', () => {
    const base = snapshot('RUNNING', [nodeRun('nr_1', 'RUNNING')]);

    const next = applyEvent(base, event(1, 'NODE_READY', 'nr_unknown'));

    expect(next.nodeRuns).toHaveLength(1);
    expect(next.nodeRuns[0]?.id).toBe('nr_1');
  });

  it('leaves NodeRun status untouched for events that are not transitions', () => {
    const base = snapshot('RUNNING', [nodeRun('nr_1', 'RUNNING')]);

    // A retry keeps the NodeRun RUNNING; a duplicate callback proves no new status.
    expect(applyEvent(base, event(1, 'NODE_RETRYING', 'nr_1')).nodeRuns[0]?.status).toBe('RUNNING');
    expect(applyEvent(base, event(2, 'NODE_CALLBACK_RECEIVED', 'nr_1')).nodeRuns[0]?.status).toBe(
      'RUNNING',
    );
  });

  it('does not mutate the input snapshot', () => {
    const base = snapshot('RUNNING', [nodeRun('nr_1', 'RUNNING')]);

    applyEvent(base, event(1, 'NODE_COMPLETED', 'nr_1'));

    expect(base.run.status).toBe('RUNNING');
    expect(base.nodeRuns[0]?.status).toBe('RUNNING');
    expect(base.lastSeq).toBe(0);
  });

  it('never changes a NodeRun status for an AGENT_* event', () => {
    const base = snapshot('RUNNING', [nodeRun('nr_agent', 'RUNNING')]);

    for (const type of NON_RUN_EVENTS.filter((t) => t.startsWith('AGENT_'))) {
      expect(applyEvent(base, event(1, type, 'nr_agent')).nodeRuns[0]?.status).toBe('RUNNING');
    }
  });
});

describe('needsSnapshotRead', () => {
  const base = snapshot('RUNNING', [nodeRun('nr_1', 'RUNNING'), nodeRun('nr_agent', 'RUNNING')]);

  it('asks for a re-read when the Event names a NodeRun the view has not seen', () => {
    expect(needsSnapshotRead(base, event(1, 'NODE_READY', 'nr_new'))).toBe(true);
  });

  it('asks for a re-read after every AGENT_* Event, whose NodeRun status it does not state', () => {
    for (const type of NON_RUN_EVENTS.filter((t) => t.startsWith('AGENT_'))) {
      expect(needsSnapshotRead(base, event(1, type, 'nr_agent')), type).toBe(true);
    }
  });

  it('asks for a re-read when the Run reaches a terminal status, for output and error', () => {
    expect(needsSnapshotRead(base, runEvent(1, 'RUN_COMPLETED', 'RUNNING', 'COMPLETED'))).toBe(
      true,
    );
    expect(needsSnapshotRead(base, runEvent(1, 'RUN_FAILED', 'RUNNING', 'FAILED'))).toBe(true);
  });

  it('does not re-read for Events that state their own transition of a known NodeRun', () => {
    expect(needsSnapshotRead(base, event(1, 'NODE_STARTED', 'nr_1'))).toBe(false);
    expect(needsSnapshotRead(base, event(1, 'NODE_DISPATCHED', 'nr_1'))).toBe(false);
    expect(needsSnapshotRead(base, event(1, 'NODE_COMPLETED', 'nr_1'))).toBe(false);
    expect(needsSnapshotRead(base, runEvent(1, 'RUN_PAUSED', 'RUNNING', 'PAUSED'))).toBe(false);
    expect(needsSnapshotRead(base, event(1, 'RUN_CREATED'))).toBe(false);
  });
});
