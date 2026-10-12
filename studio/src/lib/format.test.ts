import { describe, expect, it } from 'vitest';

import { formatBytes, formatClock, formatElapsed, formatRunClock } from './format';

describe('formatBytes', () => {
  it('prints sizes in KB or MB with at most one decimal, never raw bytes', () => {
    expect(formatBytes(3)).toBe('0.1 KB');
    expect(formatBytes(9540)).toBe('9.3 KB');
    expect(formatBytes(1024 * 1024 - 1)).toBe('1 MB');
    expect(formatBytes(1258291)).toBe('1.2 MB');
    expect(formatBytes(5 * 1024 * 1024)).toBe('5 MB');
  });
});

describe('formatClock', () => {
  it('prints the UTC time of a server timestamp', () => {
    expect(formatClock('2026-08-03T12:00:03.123Z')).toBe('12:00:03Z');
    expect(formatClock(null)).toBe('—');
  });
});

describe('formatElapsed', () => {
  it('prints a stopwatch between two server timestamps', () => {
    expect(formatElapsed('2026-08-03T12:00:00Z', '2026-08-03T12:00:42Z')).toBe('00:42');
    expect(formatElapsed('2026-08-03T12:00:00Z', '2026-08-03T13:02:05Z')).toBe('1:02:05');
  });

  it('never goes negative and reads "—" without a start', () => {
    expect(formatElapsed('2026-08-03T12:00:10Z', '2026-08-03T12:00:00Z')).toBe('00:00');
    expect(formatElapsed(null)).toBe('—');
  });
});

describe('formatRunClock', () => {
  it('uses the Recent Execution units below a minute instead of a 00:00 stopwatch', () => {
    expect(formatRunClock('2026-08-03T12:00:00.000Z', '2026-08-03T12:00:00.027Z')).toBe('27 ms');
    expect(formatRunClock('2026-08-03T12:00:00.000Z', '2026-08-03T12:00:01.200Z')).toBe('1.2 s');
  });

  it('switches to a stopwatch from a minute up', () => {
    expect(formatRunClock('2026-08-03T12:00:00Z', '2026-08-03T12:04:53Z')).toBe('04:53');
    expect(formatRunClock('2026-08-03T12:00:00Z', '2026-08-03T13:02:05Z')).toBe('1:02:05');
  });

  it('counts up to now when the Run is still waiting, and reads "—" without a start', () => {
    const since = new Date(Date.now() - 5 * 60_000).toISOString();
    expect(formatRunClock(since)).toMatch(/^0[45]:\d\d$/);
    expect(formatRunClock(null)).toBe('—');
  });
});
