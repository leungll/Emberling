import { describe, expect, it } from 'vitest';

import { cardInfo, type CardInfo } from './card-info';
import type { RunModel } from './run-model';
import {
  agentTrace,
  at,
  definition,
  model,
  node,
  nodeAttempt,
  nodeRun,
  snapshot,
  toolAttempt,
  turn,
} from '@/test/run-fixtures';
import { PHOTO_RUN, recordedModel, recordedParts } from '@/test/recorded-run';

function info(m: RunModel, nodeId: string): CardInfo {
  const step = m.byNode.get(nodeId);
  if (!step) throw new Error(`no step ${nodeId}`);
  return cardInfo(m, step);
}

const recorded = (runId: string) => recordedModel(recordedParts('recorded', runId));

describe('a completed photo set run, as recorded', () => {
  const m = recordedModel(recordedParts(PHOTO_RUN.dir, PHOTO_RUN.runId));

  it('previews a text output in two lines, with the full text kept', () => {
    const c = info(m, 'node_brief');
    expect(c.line.text).toMatch(/^Boxed collectible figure\. Turn the person/);
    expect(c.clamp).toBe(true);
    expect(c.full).toBe(c.line.text);
    expect(c.refs).toEqual([]);
  });

  it('gives the length of the photo list the photo set returned, with the photos', () => {
    const c = info(m, 'node_photos');
    expect(c.line.text).toBe('3 photos');
    expect(c.refs.map((r) => r.kind)).toEqual(['ASSET', 'ASSET', 'ASSET']);
  });

  it('says what the agent answered with, and the turn it finished in', () => {
    const c = info(m, 'node_agent');
    expect(c.line.text).toBe('Answered with 3 images');
    expect(c.turn).toBe('Finished in turn 9');
    expect(c.refs.map((r) => r.id)).toEqual([
      'artifact_a09efbd58fef361792747db896e5a04f',
      'artifact_2bafb85f8945b42083a3d0afba07d752',
      'artifact_057235642ebba33207e13d6dd79caf96',
    ]);
  });

  it('reports the review counts the media result returned, with its cover image', () => {
    const c = info(m, 'node_result');
    expect(c.line.text).toBe('2 of 3 photos passed review');
    expect(c.refs.map((r) => r.id)).toEqual(['artifact_a09efbd58fef361792747db896e5a04f']);
  });

  it('names the final image and its format', () => {
    const c = info(m, 'node_output');
    expect(c.line.text).toBe('Final image, JPEG');
    expect(c.refs).toHaveLength(1);
  });
});

describe('failed steps, as recorded', () => {
  it('names the generation limit from the error details, with the images made before', () => {
    const c = info(recorded('run_f7c0377afa8541eb86fa06ee0ec41ae2'), 'node_agent');
    expect(c.line.text).toBe('Used all 2 generation calls');
    expect(c.refs).toHaveLength(2);
    expect(c.refsLabel).toBe('Images it made before it failed');
    expect(c.turn).toBeNull();
  });

  it('names the tool a missing review blocked', () => {
    const c = info(recorded('run_03f4f60dd11549b5bdea8ee3d70141be'), 'node_agent');
    expect(c.line).toEqual({
      text: 'Blocked from calling generate_video: it needs a passed review first',
      tools: ['generate_video'],
    });
  });

  it('uses the error code’s short sentence for a step that is not an agent', () => {
    const m = recorded('run_ff4ff4ff590f4f5199bf2165387a7168');
    expect(info(m, 'node_image').line.text).toBe('Ran out of time');
    expect(info(m, 'node_reference')).toMatchObject({
      line: { text: 'No image given' },
      idle: true,
    });
    expect(info(m, 'node_prompt').line.text).toBe('Rewrite this brief into an image prompt: probe');
  });
});

describe('a completed delivery run, as recorded', () => {
  const m = recorded('run_d9c23ab668c54acf9ec83c59c55ce742');

  it('lists the fields of a JSON object output, without a clamp', () => {
    const c = info(m, 'node_task');
    expect(c.line.text).toMatch(/^JSON with baseCommit, patch and /);
    expect(c.clamp).toBe(false);
  });

  it('says the agent finished with an answer when its last call returned nothing it saved', () => {
    expect(info(m, 'node_agent').line.text).toBe('Finished with an answer');
  });
});

/* ---------- States the recordings do not hold ---------------------------------------- */

const FLOW = definition(
  [
    node('in', 'text_input', 'Task'),
    node('gen', 'text_generation', 'Writer'),
    node('agent', 'agent', 'Delivery'),
    node('out', 'text_output', 'Result'),
  ],
  [
    ['in', 'gen'],
    ['gen', 'agent'],
    ['agent', 'out'],
  ],
);

describe('steps that have not run', () => {
  it('says not started while the run goes on, and did not run once it failed', () => {
    const live = model({ snapshot: snapshot('RUNNING', []), definition: FLOW });
    expect(info(live, 'out')).toMatchObject({ line: { text: 'Not started' }, idle: true });
    const failed = model({ snapshot: snapshot('FAILED', []), definition: FLOW });
    expect(info(failed, 'out').line.text).toBe('Did not run');
  });

  it('says queued for a ready step', () => {
    const m = model({
      snapshot: snapshot('RUNNING', [nodeRun('gen', 'text_generation', { status: 'READY' })]),
      definition: FLOW,
    });
    expect(info(m, 'gen').line.text).toBe('Queued');
  });
});

describe('running steps', () => {
  const running = (extra = {}) =>
    nodeRun('agent', 'agent', {
      status: 'RUNNING',
      completedAt: null,
      latencyMs: null,
      ...extra,
    });

  it('says what a running step that is not an agent does', () => {
    const m = model({
      snapshot: snapshot('RUNNING', [
        nodeRun('gen', 'text_generation', {
          status: 'RUNNING',
          completedAt: null,
          latencyMs: null,
        }),
      ]),
      definition: FLOW,
    });
    expect(info(m, 'gen').line.text).toBe('Generating text');
  });

  it('names the tool an agent is calling, and its turn', () => {
    const m = model({
      snapshot: snapshot('RUNNING', [running()]),
      definition: FLOW,
      traces: {
        nr_agent: agentTrace([
          turn(1, 'sandbox_test', 1),
          turn(2, 'deploy', 2, {
            action: { status: 'RUNNING', completedAt: null },
            attempts: [toolAttempt('ta_2', 'deploy', { status: 'STARTED', completedAt: null })],
          }),
        ]),
      },
    });
    expect(info(m, 'agent')).toMatchObject({
      line: { text: 'Calling deploy', tools: ['deploy'] },
      turn: 'Turn 2',
    });
  });

  it('says the agent is deciding before its turn has a decision', () => {
    const m = model({
      snapshot: snapshot('RUNNING', [running()]),
      definition: FLOW,
      traces: {
        nr_agent: agentTrace([
          turn(1, 'sandbox_test', 1),
          turn(2, null, 2, {
            turn: { status: 'RUNNING', completedAt: null, decision: null, action: null },
            attempts: [],
          }),
        ]),
      },
    });
    expect(info(m, 'agent')).toMatchObject({
      line: { text: 'Deciding the next step' },
      turn: 'Turn 2',
    });
  });

  it('says the agent is passing on its answer while its final action runs', () => {
    const m = model({
      snapshot: snapshot('RUNNING', [running()]),
      definition: FLOW,
      traces: {
        nr_agent: agentTrace([
          turn(1, null, 1, { action: { status: 'RUNNING', completedAt: null } }),
        ]),
      },
    });
    expect(info(m, 'agent').line.text).toBe('Passing on its answer');
  });
});

describe('waiting steps', () => {
  it('names the service an agent waits for and the tool it reports', () => {
    const binding = { id: 'cb_1', providerId: 'sandboxrunner', externalTaskId: 'task_1' };
    const m = model({
      snapshot: snapshot('PAUSED', [
        nodeRun('agent', 'agent', {
          status: 'WAITING_CALLBACK',
          waitingAt: at(2),
          completedAt: null,
          latencyMs: null,
        }),
      ]),
      definition: FLOW,
      traces: {
        nr_agent: agentTrace([
          turn(1, 'sandbox_test', 1, {
            action: { status: 'WAITING_CALLBACK', completedAt: null },
            attempts: [
              toolAttempt('ta_1', 'sandbox_test', {
                status: 'DISPATCHED',
                dispatchedAt: at(1),
                completedAt: null,
                callbackBinding: binding,
              }),
            ],
          }),
        ]),
      },
    });
    expect(info(m, 'agent')).toMatchObject({
      line: {
        text: 'Waiting for Sandbox runner to report sandbox_test',
        tools: ['sandbox_test'],
      },
      turn: 'Turn 1',
    });
  });

  it('names the service a step that is not an agent waits for', () => {
    const m = model({
      snapshot: snapshot('PAUSED', [
        nodeRun('gen', 'text_generation', {
          status: 'WAITING_CALLBACK',
          completedAt: null,
          latencyMs: null,
        }),
      ]),
      definition: FLOW,
      details: {
        nr_gen: {
          nodeRun: nodeRun('gen', 'text_generation'),
          attempts: [
            nodeAttempt(1, {
              status: 'DISPATCHED',
              dispatchedAt: at(1),
              completedAt: null,
              callbackBinding: {
                id: 'cb_1',
                providerId: 'mock-task-provider-v1',
                externalTaskId: 'task_1',
              },
            }),
          ],
        },
      },
    });
    expect(info(m, 'gen').line.text).toBe('Waiting for Mock task provider to report back');
  });
});

describe('an agent that ran out of time', () => {
  it('names the tool that was running', () => {
    const m = model({
      snapshot: snapshot('FAILED', [
        nodeRun('agent', 'agent', {
          status: 'FAILED',
          error: { code: 'TIMEOUT', message: 'deadline exceeded' },
        }),
      ]),
      definition: FLOW,
      traces: {
        nr_agent: agentTrace([
          turn(1, 'deploy', 1, {
            action: { status: 'FAILED', error: { code: 'TIMEOUT', message: 'x' } },
            attempts: [toolAttempt('ta_1', 'deploy', { status: 'FAILED' })],
          }),
        ]),
      },
    });
    expect(info(m, 'agent').line).toEqual({
      text: 'Ran out of time while deploy was running',
      tools: ['deploy'],
    });
  });
});

describe('outputs without content', () => {
  it('says there is no text for an empty text output', () => {
    const m = model({
      snapshot: snapshot('COMPLETED', [
        nodeRun('gen', 'text_generation', { output: { text: '' } }),
      ]),
      definition: FLOW,
    });
    expect(info(m, 'gen')).toMatchObject({ line: { text: 'No text' }, idle: true });
  });
});
