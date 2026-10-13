import { afterEach, describe, expect, it } from 'vitest';

import { LOGS, parseRunView, runViewSearch, storedPercent, storePercent } from './run-view-url';

afterEach(() => {
  localStorage.clear();
});

describe('run view address', () => {
  it('reads every field and drops unknown values', () => {
    const s = parseRunView(
      '?el=turn_2&tab=raw&view=events&group=tools&q=lint&only=1&status=failed&range=7d',
    );
    expect(s).toMatchObject({
      el: 'turn_2',
      tab: 'raw',
      view: 'events',
      group: 'tools',
      q: 'lint',
      only: true,
      status: 'failed',
      range: '7d',
    });
    expect(parseRunView('?view=graph&group=nope&status=PAUSED')).toMatchObject({
      view: null,
      group: 'all',
      status: null,
    });
  });

  it('writes only non-default fields and keeps where the run was opened from', () => {
    const s = parseRunView('?tq=lint&fails=1&tab=overview');
    expect(runViewSearch(s, '?from=runs&el=x')).toBe('?from=runs&tq=lint&fails=1');
    expect(runViewSearch(parseRunView(''), '')).toBe('');
  });

  it('keeps the trace filters out of the address in the Events view and the reverse', () => {
    const s = { ...parseRunView('?tq=lint&q=deploy&group=errors'), view: 'events' as const };
    expect(runViewSearch(s, '')).toBe('?view=events&group=errors&q=deploy');
  });
});

describe('remembered sizes', () => {
  it('uses a stored size only within its bounds', () => {
    expect(storedPercent(LOGS.key, LOGS.min, LOGS.max, LOGS.fallback)).toBe(52);
    storePercent(LOGS.key, 64.5);
    expect(storedPercent(LOGS.key, LOGS.min, LOGS.max, LOGS.fallback)).toBe(64.5);
    storePercent(LOGS.key, 95);
    expect(storedPercent(LOGS.key, LOGS.min, LOGS.max, LOGS.fallback)).toBe(52);
  });
});
