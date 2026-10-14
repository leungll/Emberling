import { describe, expect, it } from 'vitest';

import { rangeStart } from './useWorkflowRuns';

describe('rangeStart', () => {
  it('floors the start bound to the minute so repeated reads agree', () => {
    const now = new Date('2026-10-02T10:30:45.123Z');
    expect(rangeStart('24h', now)).toBe('2026-10-01T10:30:00.000Z');
    expect(rangeStart('7d', now)).toBe('2026-09-25T10:30:00.000Z');
    expect(rangeStart(null, now)).toBeUndefined();
  });
});
