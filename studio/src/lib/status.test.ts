import { describe, expect, it } from 'vitest';

import {
  agentTurnStatusTone,
  agentTurnStatusVariant,
  nodeAttemptStatusVariant,
  nodeRunStatusLabel,
  nodeRunStatusVariant,
  nodeRunStatusTone,
  runStatusLabel,
  runStatusTone,
  runStatusVariant,
} from './status';

describe('status badges', () => {
  it('status badge labels WAITING_CALLBACK, DISPATCHED and PAUSED', () => {
    // The three async statuses share the amber "waiting" colour of 04 §4, and none of
    // them may fall through to the neutral style reserved for a node with no NodeRun.
    expect(nodeRunStatusLabel('WAITING_CALLBACK')).toBe('Waiting for callback');
    expect(nodeRunStatusVariant('WAITING_CALLBACK')).toBe('waiting');

    expect(nodeAttemptStatusVariant('DISPATCHED')).toBe('waiting');

    expect(runStatusLabel('PAUSED')).toBe('Waiting for external result');
    expect(runStatusVariant('PAUSED')).toBe('waiting');
  });

  it('does not give the async statuses the neutral fallback style', () => {
    expect(nodeRunStatusVariant('WAITING_CALLBACK')).not.toBe('neutral');
    expect(nodeAttemptStatusVariant('DISPATCHED')).not.toBe('neutral');
    expect(runStatusVariant('PAUSED')).not.toBe('neutral');
  });
});

describe('04 §4 status tones', () => {
  it('maps every NodeRun status to its documented colour class', () => {
    expect(nodeRunStatusTone('READY')).toBe('neutral');
    expect(nodeRunStatusTone('RUNNING')).toBe('running');
    expect(nodeRunStatusTone('WAITING_CALLBACK')).toBe('waiting');
    expect(nodeRunStatusTone('SUCCEEDED')).toBe('succeeded');
    expect(nodeRunStatusTone('FAILED')).toBe('failed');
  });

  it('maps every server Run status to its tone and documented label', () => {
    expect([runStatusTone('RUNNING'), runStatusLabel('RUNNING')]).toEqual(['running', 'Running']);
    expect([runStatusTone('PAUSED'), runStatusLabel('PAUSED')]).toEqual([
      'waiting',
      'Waiting for external result',
    ]);
    expect([runStatusTone('COMPLETED'), runStatusLabel('COMPLETED')]).toEqual([
      'succeeded',
      'Completed',
    ]);
    expect([runStatusTone('FAILED'), runStatusLabel('FAILED')]).toEqual(['failed', 'Failed']);
  });
});

describe('agentTurnStatusTone', () => {
  it('maps every Turn status to a 04 §4 tone, matching its Badge variant', () => {
    // A Turn succeeds as COMPLETED, unlike NodeRun/Action SUCCEEDED; both read green.
    expect(agentTurnStatusTone('READY')).toBe('neutral');
    expect(agentTurnStatusTone('RUNNING')).toBe('running');
    expect(agentTurnStatusTone('COMPLETED')).toBe('succeeded');
    expect(agentTurnStatusTone('FAILED')).toBe('failed');
    for (const status of ['READY', 'RUNNING', 'COMPLETED', 'FAILED'] as const) {
      expect(agentTurnStatusTone(status)).toBe(agentTurnStatusVariant(status));
    }
  });
});
