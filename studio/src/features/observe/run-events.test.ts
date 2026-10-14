import { describe, expect, it } from 'vitest';

import {
  elementCrumb,
  elementOfEvent,
  eventGroups,
  eventLabel,
  eventSummary,
  eventsFor,
  filterEvents,
  isFailureEvent,
} from './run-events';
import type { RunEvent } from '@/api/types';
import { AGENT_FLOW, agentTrace, event, model, nodeRun, snapshot, turn } from '@/test/run-fixtures';

const events = [
  event(1, 'RUN_CREATED', { payload: { definitionVersion: 3 } }),
  event(2, 'NODE_COMPLETED', { nodeRunId: 'nr_in', payload: { latencyMs: 1500 } }),
  event(3, 'AGENT_DECISION_COMMITTED', {
    nodeRunId: 'nr_agent',
    payload: { turnId: 'turn_1', kind: 'TOOL_CALL', toolName: 'lint' },
  }),
  event(4, 'AGENT_ACTION_STARTED', {
    nodeRunId: 'nr_agent',
    payload: { actionId: 'act_1', toolAttemptId: 'ta_1', claimSource: 'IMMEDIATE' },
  }),
  event(5, 'AGENT_ACTION_FAILED', {
    nodeRunId: 'nr_agent',
    payload: { actionId: 'act_2', error: { code: 'TIMEOUT' } },
  }),
  event(6, 'RUN_FAILED'),
];
const m = model({
  definition: AGENT_FLOW,
  snapshot: snapshot(
    'FAILED',
    [nodeRun('in', 'text_input'), nodeRun('agent', 'agent', { status: 'FAILED' })],
    { error: { code: 'TIMEOUT', message: 'deadline exceeded' } },
  ),
  traces: {
    nr_agent: agentTrace([
      turn(1, 'lint', 1),
      turn(2, null, 2, { action: { status: 'FAILED', completionSource: null } }),
    ]),
  },
  events,
});
function ev(seq: number): RunEvent {
  const e = events[seq - 1];
  if (!e) throw new Error(`no event ${String(seq)}`);
  return e;
}

describe('elementOfEvent', () => {
  it('places an Event on the narrowest element its payload names', () => {
    expect(elementOfEvent(m, ev(1)).id).toBe('run');
    expect(elementOfEvent(m, ev(2)).id).toBe('in');
    expect(elementOfEvent(m, ev(3)).id).toBe('dec:turn_1');
    expect(elementOfEvent(m, ev(5)).id).toBe('act_2');
  });

  it('collects the Events of an element and everything inside it', () => {
    expect(eventsFor(m, m.byId.get('agent') ?? m.root).map((e) => e.seq)).toEqual([3, 4, 5]);
    expect(elementCrumb(m, m.byId.get('act_2') ?? m.root)).toBe('Reviewer › Turn 2');
  });
});

describe('event groups and words', () => {
  it('treats an Agent action without a tool attempt as a state change', () => {
    expect(eventGroups(ev(4))).toEqual(['tools']);
    expect(eventGroups(ev(5))).toEqual(['errors', 'state']);
    expect(isFailureEvent(ev(5))).toBe(true);
    expect(isFailureEvent(ev(2))).toBe(false);
  });

  it('names a decision by its tool and summarises from the payload only', () => {
    expect(eventLabel(ev(3))).toEqual({ text: 'Decided to call lint', tools: ['lint'] });
    expect(eventSummary(m, ev(1))).toBe('Workflow version 3');
    expect(eventSummary(m, ev(2))).toBe('Took 1.5 s');
    expect(eventSummary(m, ev(5))).toBe('Ran out of time');
    expect(eventSummary(m, ev(6))).toBe('Ran out of time');
  });
});

describe('filterEvents', () => {
  it('filters by group, selection and text', () => {
    const agent = m.byId.get('agent') ?? m.root;
    expect(
      filterEvents(m, agent, { group: 'errors', query: '', only: false }).map((e) => e.seq),
    ).toEqual([5, 6]);
    expect(
      filterEvents(m, agent, { group: 'all', query: '', only: true }).map((e) => e.seq),
    ).toEqual([3, 4, 5]);
    expect(
      filterEvents(m, m.root, { group: 'all', query: 'lint', only: false }).map((e) => e.seq),
    ).toEqual([3]);
  });
});
