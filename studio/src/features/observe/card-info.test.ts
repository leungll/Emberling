import { describe, expect, it } from 'vitest';

import type { ExecutionFactTrace, ToolAttemptTrace } from '@/api/types';
import { callArguments, callSaved, cardInfo, turnResult, type CardInfo } from './card-info';
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

  it('names the version and target its last deploy reported', () => {
    expect(info(m, 'node_agent').line.text).toBe('Deployed v1 to checkout in staging');
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

/* ---------- Tool-call facts the trace projects on each Tool Attempt -------------------- */

describe('tool-call facts on a running call', () => {
  const runningCall = (tool: string, attempt: Partial<ToolAttemptTrace>) =>
    model({
      snapshot: snapshot('RUNNING', [
        nodeRun('agent', 'agent', { status: 'RUNNING', completedAt: null, latencyMs: null }),
      ]),
      definition: FLOW,
      traces: {
        nr_agent: agentTrace([
          turn(1, tool, 1, {
            action: { status: 'RUNNING', completedAt: null },
            attempts: [
              toolAttempt('ta_1', tool, { status: 'STARTED', completedAt: null, ...attempt }),
            ],
          }),
        ]),
      },
    });

  it('names the photo of the set a generate_image call works on, by its returned position', () => {
    const m = runningCall('generate_image', {
      photo: { photoAssetId: 'asset_2', position: 2, count: 3 },
    });
    expect(info(m, 'agent').line).toEqual({
      text: 'Calling generate_image for photo 2 of 3',
      tools: ['generate_image'],
    });
  });

  it('names only the tool when the photo has no returned position', () => {
    const m = runningCall('generate_image', { photo: { photoAssetId: 'asset_2' } });
    expect(info(m, 'agent').line.text).toBe('Calling generate_image');
    expect(info(runningCall('generate_image', {}), 'agent').line.text).toBe(
      'Calling generate_image',
    );
  });

  it('names the target a running deploy goes to', () => {
    const m = runningCall('deploy', {
      deployment: { target: { service: 'checkout', environment: 'staging' } },
    });
    expect(info(m, 'agent').line.text).toBe('Deploying to checkout in staging');
    expect(info(runningCall('deploy', {}), 'agent').line.text).toBe('Calling deploy');
  });
});

describe('tool-call facts on a finished call', () => {
  const finished = (
    tool: string,
    attempt: Partial<ToolAttemptTrace>,
    facts: ExecutionFactTrace[] = [],
  ) => {
    const trace = agentTrace(
      [
        turn(1, tool, 1, {
          attempts: [toolAttempt('ta_1', tool, { completedAt: at(1), ...attempt })],
        }),
        turn(2, null, 2),
      ],
      { termination: 'FINAL_RESPONSE' },
    );
    trace.facts.items = facts;
    return model({
      snapshot: snapshot('COMPLETED', [nodeRun('agent', 'agent', { output: { text: 'done' } })]),
      definition: FLOW,
      traces: { nr_agent: trace },
    });
  };
  const lineOf = (m: RunModel) => info(m, 'agent').line.text;

  it('gives the test total of a passed sandbox_test', () => {
    expect(
      lineOf(
        finished('sandbox_test', { test: { passed: true, summary: { total: 12, failed: 0 } } }),
      ),
    ).toBe('Tests passed (12)');
  });

  it('gives the failed and total counts of a failed sandbox_test, as recorded', () => {
    expect(
      lineOf(
        finished('sandbox_test', { test: { passed: false, summary: { total: 12, failed: 3 } } }),
      ),
    ).toBe('Tests failed (3 of 12)');
  });

  it('gives both recorded counts when a passed sandbox_test reports failures', () => {
    expect(
      lineOf(
        finished('sandbox_test', { test: { passed: true, summary: { total: 12, failed: 3 } } }),
      ),
    ).toBe('Tests passed, 3 of 12 failed');
  });

  it('keeps the saved verdict alone when the test summary is not returned', () => {
    const fact: ExecutionFactTrace = {
      id: 'f_1',
      factType: 'patch_tested',
      subject: 'patch',
      bindings: {},
      verdict: true,
      basisFactId: null,
      toolAttemptId: 'ta_1',
      createdAt: at(1),
    };
    expect(lineOf(finished('sandbox_test', {}, [fact]))).toBe('Tests passed');
  });

  it('names the version, service and environment of a deploy', () => {
    const target = { service: 'checkout', environment: 'staging' };
    expect(lineOf(finished('deploy', { deployment: { target, version: 'v1' } }))).toBe(
      'Deployed v1 to checkout in staging',
    );
    expect(lineOf(finished('deploy', { deployment: { target } }))).toBe(
      'Deployed to checkout in staging',
    );
    expect(lineOf(finished('deploy', {}))).toBe('Finished with an answer');
  });

  it('says a video is ready, and says so plainly when its link is withheld', () => {
    expect(
      lineOf(finished('generate_video', { video: { videoUrl: 'https://cdn.test/v/1.mp4' } })),
    ).toBe('Video ready');
    expect(
      lineOf(
        finished('generate_video', {
          withheld: [{ field: 'video.videoUrl', reason: 'UNSAFE_URL' }],
        }),
      ),
    ).toBe('Video ready (link not shown)');
    expect(lineOf(finished('generate_video', {}))).toBe('Finished with an answer');
  });
});

describe('a tool call’s arguments and result in the details pane', () => {
  const callModel = (tool: string, attempt: Partial<ToolAttemptTrace>) =>
    model({
      snapshot: snapshot('COMPLETED', [nodeRun('agent', 'agent', { output: { text: 'done' } })]),
      definition: FLOW,
      traces: {
        nr_agent: agentTrace([
          turn(1, tool, 1, { attempts: [toolAttempt('ta_1', tool, attempt)] }),
          turn(2, null, 2),
        ]),
      },
    });
  const actionEl = (m: RunModel) => {
    const el = m.byId.get('act_1');
    if (!el) throw new Error('no action element');
    return el;
  };

  it('lists the photo by its returned position and the deploy target', () => {
    const photo = callArguments(
      actionEl(
        callModel('generate_image', { photo: { photoAssetId: 'asset_2', position: 2, count: 3 } }),
      ),
    );
    expect(photo).toMatchObject({
      photo: { photoAssetId: 'asset_2', position: 2, count: 3 },
      target: null,
    });
    const deploy = callArguments(
      actionEl(
        callModel('deploy', {
          deployment: { target: { service: 'checkout', environment: 'staging' } },
        }),
      ),
    );
    expect(deploy).toMatchObject({ photo: null, target: 'checkout in staging' });
    expect(callArguments(actionEl(callModel('review_asset', {})))).toBeNull();
  });

  it('carries the video link, or why it is withheld, into the result', () => {
    const m = callModel('generate_video', { video: { videoUrl: 'https://cdn.test/v/1.mp4' } });
    expect(callSaved(m, actionEl(m))).toMatchObject({
      result: { text: 'Video ready' },
      videoUrl: 'https://cdn.test/v/1.mp4',
      withheld: [],
    });
    const w = callModel('generate_video', {
      withheld: [{ field: 'video.videoUrl', reason: 'TOO_LARGE' }],
    });
    expect(callSaved(w, actionEl(w))).toMatchObject({
      result: { text: 'Video ready (link not shown)' },
      videoUrl: null,
      withheld: [{ field: 'video.videoUrl', reason: 'TOO_LARGE' }],
    });
  });

  it('gives a Turns row the result of a call that saved no image or review', () => {
    const m = callModel('sandbox_test', {
      test: { passed: true, summary: { total: 12, failed: 0 } },
    });
    const step = m.byNode.get('agent');
    const t = step?.trace?.turns[0];
    if (!step || !t) throw new Error('no turn');
    expect(turnResult(m, step, t)?.text).toBe('Tests passed (12)');
    const img = callModel('generate_image', { photo: { photoAssetId: 'asset_2' } });
    const s2 = img.byNode.get('agent');
    const t2 = s2?.trace?.turns[0];
    if (!s2 || !t2) throw new Error('no turn');
    expect(turnResult(img, s2, t2)).toBeNull();
  });
});
