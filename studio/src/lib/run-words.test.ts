import { describe, expect, it } from 'vitest';

import {
  endedDurationMs,
  failureSentence,
  olderTip,
  rangeParam,
  runSubline,
  statusParam,
  usedOlderVersion,
  waitingSentence,
} from './run-words';

const step = { nodeId: 'node_agent', nodeName: 'Deliver' };

describe('failureSentence', () => {
  it('names the failed step for each stable code', () => {
    expect(failureSentence({ error: { code: 'TIMEOUT' }, failedStep: step })).toBe(
      'Deliver ran out of time.',
    );
    expect(failureSentence({ error: { code: 'MAX_TURNS' }, failedStep: step })).toBe(
      'Deliver used all its turns without finishing.',
    );
    expect(failureSentence({ error: { code: 'GENERATION_LIMIT_REACHED' }, failedStep: step })).toBe(
      'Deliver reached its limit of generation calls.',
    );
    expect(failureSentence({ error: { code: 'MODEL_ERROR' }, failedStep: step })).toBe(
      'Deliver’s model call failed.',
    );
    expect(failureSentence({ error: { code: 'TOOL_ERROR' }, failedStep: step })).toBe(
      'A tool call from Deliver failed.',
    );
    expect(failureSentence({ error: { code: 'INVALID_ACTION' }, failedStep: step })).toBe(
      'Deliver’s answer was rejected.',
    );
    expect(failureSentence({ error: { code: 'EXECUTOR_ERROR' }, failedStep: step })).toBe(
      'Deliver failed.',
    );
  });

  it('keeps codes that are about a tool rather than a step', () => {
    expect(failureSentence({ error: { code: 'PRECONDITION_UNMET' }, failedStep: step })).toBe(
      'A tool was blocked by a missing requirement.',
    );
    expect(failureSentence({ error: { code: 'FACT_BASIS_MISSING' }, failedStep: null })).toBe(
      'A tool result couldn’t be traced to its source.',
    );
  });

  it('falls back without a step, for an unknown code, and without an error', () => {
    expect(failureSentence({ error: { code: 'TIMEOUT' }, failedStep: null })).toBe(
      'A step ran out of time.',
    );
    expect(failureSentence({ error: { code: 'MAX_TURNS' }, failedStep: null })).toBe(
      'The agent used all its turns without finishing.',
    );
    expect(failureSentence({ error: { code: 'SOMETHING_NEW' }, failedStep: step })).toBe(
      'Deliver failed.',
    );
    expect(failureSentence({ error: null, failedStep: null })).toBe('The run failed.');
  });
});

describe('waitingSentence and runSubline', () => {
  it('names the waiting steps, or an outside service', () => {
    expect(
      waitingSentence({ waitingSteps: [{ ...step, waitingAt: '2026-10-12T03:41:16Z' }] }),
    ).toBe('Waiting for Deliver to report back.');
    expect(waitingSentence({ waitingSteps: [] })).toBe(
      'Waiting for an outside service to report back.',
    );
  });

  it('says nothing for running and completed runs', () => {
    const base = { error: null, failedStep: null, waitingSteps: [] };
    expect(runSubline({ ...base, status: 'RUNNING' })).toBe('');
    expect(runSubline({ ...base, status: 'COMPLETED' })).toBe('');
    expect(runSubline({ ...base, status: 'FAILED' })).toBe('The run failed.');
  });
});

describe('versions and durations', () => {
  it('marks only an older version, in words', () => {
    expect(usedOlderVersion({ definitionVersion: 3, latestVersion: 3 })).toBe(false);
    expect(usedOlderVersion({ definitionVersion: 2, latestVersion: 3 })).toBe(true);
    expect(olderTip({ definitionVersion: 2, latestVersion: 3 })).toBe(
      'Ran on version 2. This workflow has been saved 1 more time since this run. Opening it in the Editor shows the latest version.',
    );
    expect(olderTip({ definitionVersion: 1, latestVersion: 4 })).toContain('3 more times');
  });

  it('subtracts the run’s own timestamps and has none while live', () => {
    expect(
      endedDurationMs({
        startedAt: '2026-10-12T03:41:16.500Z',
        completedAt: '2026-10-12T03:41:39.100Z',
      }),
    ).toBe(22_600);
    expect(
      endedDurationMs({ startedAt: '2026-10-12T03:41:16.500Z', completedAt: null }),
    ).toBeNull();
  });
});

describe('URL values', () => {
  it('accepts only the known statuses and ranges', () => {
    expect(statusParam('waiting')).toBe('waiting');
    expect(statusParam('PAUSED')).toBeNull();
    expect(statusParam(null)).toBeNull();
    expect(rangeParam('7d')).toBe('7d');
    expect(rangeParam('30d')).toBeNull();
  });
});
