import { describe, expect, it } from 'vitest';

import { editHref, failureOf } from './run-diagnosis';
import type { ToolMetadata } from '@/api/types';
import {
  AGENT_FLOW,
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

const deploy: ToolMetadata = {
  name: 'deploy',
  description: '',
  inputSchema: {},
  outputSchema: {},
  sideEffect: { kind: 'EXTERNAL', idempotency: 'UNKNOWN' },
  executionKind: 'ASYNC',
  requires: [],
  countsTowardGenerationLimit: false,
  usesImageModel: false,
};

describe('failureOf', () => {
  it('returns nothing for a run without a failed step', () => {
    const m = model({
      definition: AGENT_FLOW,
      snapshot: snapshot('COMPLETED', [nodeRun('in', 'text_input')]),
    });
    expect(failureOf(m, [])).toBeNull();
  });

  it('explains a timed-out Agent with the turn, the tool, the steps that did not run and the field to change', () => {
    const failedCall = turn(2, 'deploy', 2, {
      action: { status: 'FAILED', completionSource: null },
      attempts: [toolAttempt('ta_2', 'deploy', { status: 'FAILED', completedAt: at(3) })],
    });
    const m = model({
      definition: definition(
        [
          node('agent', 'agent', 'Reviewer', { config: { timeoutMs: 120_000 } }),
          node('out', 'text_output', 'Result'),
        ],
        [['agent', 'out']],
      ),
      snapshot: snapshot('FAILED', [
        nodeRun('agent', 'agent', {
          status: 'FAILED',
          error: { code: 'TIMEOUT', message: 'deadline exceeded' },
        }),
      ]),
      traces: { nr_agent: agentTrace([turn(1, 'lint', 1), failedCall]) },
    });
    const f = failureOf(m, [deploy]);
    expect(f).toMatchObject({
      code: 'TIMEOUT',
      title: 'Reviewer ran out of time',
      cause: 'It reached its 2 min time limit in turn 2, while deploy was running.',
      effect: 'Result did not run.',
      field: 'timeoutMs',
      nextLabel: 'Change the time limit',
      notRetriedTool: 'deploy',
      focus: 'act_2',
    });
  });

  it('says each attempt of a service call hit its time limit', () => {
    const attempt = (n: number) =>
      nodeAttempt(n, {
        status: 'FAILED',
        startedAt: at(n * 10),
        deadlineAt: at(n * 10 + 5),
        completedAt: at(n * 10 + 5),
      });
    const m = model({
      definition: definition([node('gen', 'image_generation', 'Image')], []),
      snapshot: snapshot('FAILED', [
        nodeRun('gen', 'image_generation', {
          status: 'FAILED',
          error: { code: 'EXECUTOR_ERROR', message: 'x' },
        }),
      ]),
      details: {
        nr_gen: { nodeRun: nodeRun('gen', 'image_generation'), attempts: [attempt(1), attempt(2)] },
      },
    });
    const f = failureOf(m, []);
    expect(f?.title).toBe('Image’s service call failed');
    expect(f?.cause).toBe(
      'Each of its 2 attempts reached the 5 s time limit before the service answered.',
    );
    expect(f?.focus).toBe('att_2');
  });

  it('falls back to a plain sentence for a code it has no words for', () => {
    const m = model({
      definition: AGENT_FLOW,
      snapshot: snapshot('FAILED', [
        nodeRun('in', 'text_input', {
          status: 'FAILED',
          error: { code: 'NEW_CODE', message: 'x' },
        }),
      ]),
    });
    expect(failureOf(m, [])).toMatchObject({
      title: 'Task failed',
      cause: 'The server reported an error it did not describe.',
      nextLabel: 'Open step in the Editor',
      field: null,
    });
  });
});

describe('editHref', () => {
  it('links to the step and field in the Editor', () => {
    expect(editHref('wf 1', 'agent', 'timeoutMs')).toBe(
      '/workflows/wf%201?select=agent&field=timeoutMs',
    );
    expect(editHref('wf_1', null, null, '?from=runs&el=x')).toBe('/workflows/wf_1?from=runs');
    expect(editHref('wf_1', null, null)).toBe('/workflows/wf_1');
  });
});
