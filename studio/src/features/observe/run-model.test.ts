import { describe, expect, it } from 'vitest';

import {
  axisOf,
  blockedBy,
  durationOf,
  externalTasks,
  failFocusId,
  openingId,
  percentAt,
  recordIdOf,
  spanOf,
  toolSequence,
  waitInfo,
} from './run-model';
import {
  AGENT_FLOW,
  agentTrace,
  at,
  event,
  model,
  node,
  nodeAttempt,
  nodeRun,
  snapshot,
  T0,
  definition,
  toolAttempt,
  turn,
} from '@/test/run-fixtures';

const binding = { id: 'cb_1', providerId: 'sandboxrunner', externalTaskId: 'task_42' };

function waitingAgent() {
  const waiting = turn(2, 'sandbox_test', 5, {
    action: { status: 'WAITING_CALLBACK', completedAt: null, completionSource: null },
    attempts: [
      toolAttempt('ta_2', 'sandbox_test', {
        status: 'DISPATCHED',
        startedAt: at(5),
        dispatchedAt: at(6),
        completedAt: null,
        callbackBinding: binding,
      }),
    ],
    turn: { completedAt: at(5) },
  });
  return model({
    definition: AGENT_FLOW,
    snapshot: snapshot('PAUSED', [
      nodeRun('in', 'text_input'),
      nodeRun('agent', 'agent', {
        status: 'WAITING_CALLBACK',
        startedAt: at(2),
        waitingAt: at(6),
        completedAt: null,
        latencyMs: null,
      }),
    ]),
    traces: {
      nr_agent: agentTrace([turn(1, 'lint', 3), waiting], { deadline: at(600) }),
    },
    events: [event(1, 'RUN_CREATED'), event(9, 'AGENT_ACTION_WAITING')],
  });
}

describe('buildRunModel', () => {
  it('orders steps by their server start, with steps that never started last', () => {
    const m = model({
      definition: definition(
        [node('a', 'text_input', 'A'), node('b', 'text_input', 'B'), node('c', 'text_input', 'C')],
        [],
      ),
      snapshot: snapshot('RUNNING', [
        nodeRun('c', 'text_input', { startedAt: at(1) }),
        nodeRun('a', 'text_input', { startedAt: at(2) }),
      ]),
    });
    expect(m.steps.map((s) => s.node.id)).toEqual(['c', 'a', 'b']);
    expect(m.steps[2]?.statusKey).toBe('NONE');
    expect(m.root.children.map((c) => c.id)).toEqual(['c', 'a', 'b']);
  });

  it('marks steps that never started in a failed run as not run because of the failure', () => {
    const m = model({
      definition: AGENT_FLOW,
      snapshot: snapshot('FAILED', [
        nodeRun('in', 'text_input'),
        nodeRun('agent', 'agent', { status: 'FAILED' }),
      ]),
    });
    expect(m.byNode.get('out')?.statusKey).toBe('NONE_RUN_FAILED');
    expect(m.failedStep?.node.id).toBe('agent');
    expect(m.terminal).toBe(true);
  });

  it('builds turns with their decision, tool call and wait under the Agent step', () => {
    const m = waitingAgent();
    const agent = m.byId.get('agent');
    expect(agent?.children.map((c) => c.label)).toEqual([
      'Turn 1: called lint',
      'Turn 2: calling sandbox_test',
    ]);
    const call = m.byId.get('act_2');
    expect(call?.kind).toBe('action');
    const wait = call?.children[0];
    expect(wait).toMatchObject({
      kind: 'wait',
      label: 'Waiting for Sandbox runner',
      live: true,
      task: 'task_42',
      tone: 'waiting',
    });
    expect(m.byId.get('dec:turn_1')?.label).toBe('Decided to call lint');
  });

  it('folds a repeating sequence of successful tool calls into one group', () => {
    const turns = [
      turn(1, 'edit', 1),
      turn(2, 'test', 2),
      turn(3, 'edit', 3),
      turn(4, 'test', 4),
      turn(5, null, 5),
    ];
    const m = model({
      definition: AGENT_FLOW,
      snapshot: snapshot('COMPLETED', [nodeRun('agent', 'agent')]),
      traces: { nr_agent: agentTrace(turns) },
    });
    const children = m.byId.get('agent')?.children ?? [];
    expect(children.map((c) => c.kind)).toEqual(['group', 'turn']);
    expect(children[0]).toMatchObject({ label: 'Turns 1–4: edit → test, 2 times', reps: 2 });
    expect(children[0]?.children).toHaveLength(4);
  });

  it('never folds fewer than three turns or a failed call', () => {
    const m = model({
      definition: AGENT_FLOW,
      snapshot: snapshot('FAILED', [nodeRun('agent', 'agent', { status: 'FAILED' })]),
      traces: {
        nr_agent: agentTrace([
          turn(1, 'lint', 1),
          turn(2, 'lint', 2),
          turn(3, 'lint', 3, { action: { status: 'FAILED', completionSource: null } }),
        ]),
      },
    });
    expect(m.byId.get('agent')?.children.map((c) => c.kind)).toEqual(['turn', 'turn', 'turn']);
    expect(m.byId.get('turn_3')?.label).toBe('Turn 3: lint failed');
  });

  it('lists every node attempt when a step was retried', () => {
    const m = model({
      definition: definition(
        [
          node('gen', 'image_generation', 'Image', {
            executionPolicy: { timeoutMs: 1000, maxAttempts: 3, backoff: 'FIXED' },
          }),
        ],
        [],
      ),
      snapshot: snapshot('FAILED', [nodeRun('gen', 'image_generation', { status: 'FAILED' })]),
      details: {
        nr_gen: {
          nodeRun: nodeRun('gen', 'image_generation', { status: 'FAILED' }),
          attempts: [nodeAttempt(1, { status: 'FAILED' }), nodeAttempt(2, { status: 'FAILED' })],
        },
      },
    });
    expect(m.byId.get('gen')?.children.map((c) => c.label)).toEqual([
      'Attempt 1 of 3',
      'Attempt 2 of 3',
    ]);
    expect(failFocusId(m)).toBe('att_2');
  });
});

describe('where a run opens', () => {
  it('opens a waiting run on its live wait', () => {
    const m = waitingAgent();
    const info = waitInfo(m);
    expect(info).toMatchObject({
      provider: 'sandboxrunner',
      task: 'task_42',
      since: at(6),
      deadline: at(600),
      focus: 'wait:act_2',
    });
    expect(openingId(m)).toBe('wait:act_2');
  });

  it('reads a waiting service step’s outside task from its own dispatched attempt', () => {
    const gen = definition([node('gen', 'image_generation', 'Image')], []);
    const waiting = nodeRun('gen', 'image_generation', {
      status: 'WAITING_CALLBACK',
      waitingAt: at(3),
      completedAt: null,
      latencyMs: null,
    });
    const m = model({
      definition: gen,
      snapshot: snapshot('PAUSED', [waiting]),
      details: {
        nr_gen: {
          nodeRun: waiting,
          attempts: [
            nodeAttempt(1, { status: 'FAILED' }),
            nodeAttempt(2, {
              status: 'DISPATCHED',
              dispatchedAt: at(2.5),
              completedAt: null,
              callbackBinding: {
                id: 'cb_2',
                providerId: 'mock-task-provider-v1',
                externalTaskId: 'task_7',
              },
            }),
          ],
        },
      },
    });
    expect(waitInfo(m)).toMatchObject({
      provider: 'mock-task-provider-v1',
      task: 'task_7',
      since: at(2.5),
      focus: 'att_2',
    });
  });

  it('opens a run without a failure or wait on the run', () => {
    const m = model({
      definition: AGENT_FLOW,
      snapshot: snapshot('COMPLETED', [nodeRun('in', 'text_input')]),
    });
    expect(openingId(m)).toBe('run');
  });

  it('names the failed upstream step that stopped a step', () => {
    const m = model({
      definition: AGENT_FLOW,
      snapshot: snapshot('FAILED', [nodeRun('in', 'text_input', { status: 'FAILED' })]),
    });
    expect(blockedBy(m, 'out')?.node.id).toBe('in');
  });
});

describe('durations and the axis', () => {
  it('uses the server latency for a step and counts a live step from its start', () => {
    const m = waitingAgent();
    const step = (id: string) => m.byId.get(id) ?? m.root;
    expect(durationOf(m, step('in'))).toEqual({ ms: 1000 });
    expect(durationOf(m, step('agent'))).toEqual({ live: at(2) });
    expect(durationOf(m, m.root)).toEqual({ live: T0 });
  });

  it('spans a live run to its newest event at 70% of the axis', () => {
    const m = waitingAgent();
    const axis = axisOf(m);
    expect(percentAt(axis, at(9))).toBeCloseTo(70);
    expect(spanOf(m, m.byId.get('wait:act_2') ?? m.root)).toEqual({
      start: at(6),
      end: null,
      wait: true,
    });
  });

  it('spans a finished run from its start to its completion', () => {
    const m = model({ definition: AGENT_FLOW, snapshot: snapshot('COMPLETED', []) });
    const axis = axisOf(m);
    expect(axis.span).toBe(10_000);
    expect(percentAt(axis, at(20))).toBe(100);
  });
});

describe('record IDs and summaries', () => {
  it('copies the record’s own ID for each element', () => {
    const m = waitingAgent();
    expect(recordIdOf(m, m.root)).toBe('run_1');
    expect(recordIdOf(m, m.byId.get('agent') ?? m.root)).toBe('nr_agent');
    expect(recordIdOf(m, m.byId.get('dec:turn_1') ?? m.root)).toBe('turn_1');
    expect(recordIdOf(m, m.byId.get('wait:act_2') ?? m.root)).toBe('ta_2');
  });

  it('lists each external task once', () => {
    expect(externalTasks(waitingAgent())).toEqual([
      { id: 'task_42', provider: 'sandboxrunner', at: at(6) },
    ]);
  });

  it('describes an Agent’s tool calls in order', () => {
    const trace = agentTrace([
      turn(1, 'lint', 1),
      turn(2, 'lint', 2),
      turn(3, 'test', 3),
      turn(4, null, 4),
    ]);
    expect(toolSequence(trace)).toBe('Called lint 2 times and test, then finished.');
  });
});
