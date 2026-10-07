import { describe, expect, it } from 'vitest';

import { eventTone } from './events';

describe('eventTone', () => {
  it('colours each Event by the transition it records', () => {
    expect(eventTone('RUN_CREATED')).toBe('succeeded');
    expect(eventTone('NODE_COMPLETED')).toBe('succeeded');
    expect(eventTone('RUN_COMPLETED')).toBe('succeeded');

    expect(eventTone('NODE_STARTED')).toBe('running');
    expect(eventTone('NODE_CALLBACK_RECEIVED')).toBe('running');

    // Handing work to an external system is the amber waiting class, like WAITING_CALLBACK.
    expect(eventTone('NODE_DISPATCHED')).toBe('waiting');
    expect(eventTone('RUN_PAUSED')).toBe('waiting');
    expect(eventTone('AGENT_ACTION_WAITING')).toBe('waiting');

    expect(eventTone('NODE_READY')).toBe('neutral');
  });

  it('marks every failure Event red', () => {
    expect(eventTone('RUN_FAILED')).toBe('failed');
    expect(eventTone('NODE_FAILED')).toBe('failed');
    expect(eventTone('AGENT_ACTION_FAILED')).toBe('failed');
    expect(eventTone('AGENT_FAILED')).toBe('failed');
  });
});
