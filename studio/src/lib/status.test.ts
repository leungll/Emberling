import { describe, expect, it } from 'vitest';

import {
  nodeAttemptStatusVariant,
  nodeRunStatusLabel,
  nodeRunStatusVariant,
  runStatusLabel,
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
