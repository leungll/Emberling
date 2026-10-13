import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  backList,
  carryFrom,
  fromRuns,
  listHref,
  readListState,
  writeListState,
} from './list-state';
import { noteRecent, readRecent, recentWorkflows, RECENT_KEY } from './recent';

afterEach(() => {
  sessionStorage.clear();
  localStorage.clear();
  vi.restoreAllMocks();
});

describe('list state', () => {
  it('merges what each list page saves', () => {
    writeListState('runs', { qs: '?status=failed' });
    writeListState('runs', { scroll: 320, opened: 'run_1' });
    expect(readListState('runs')).toEqual({ qs: '?status=failed', scroll: 320, opened: 'run_1' });
    expect(readListState('workflows')).toEqual({});
  });

  it('goes back to the list as it was left, asking it to restore', () => {
    writeListState('workflows', { qs: '?q=photo&ret=1' });
    expect(listHref('workflows', true)).toBe('/workflows?q=photo&ret=1');
    expect(listHref('workflows', false)).toBe('/workflows?q=photo');
    expect(listHref('runs', false)).toBe('/runs');
  });

  it('opens the list by default when storage is blocked or holds something else', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    expect(() => writeListState('runs', { qs: '?a=1' })).not.toThrow();
    expect(readListState('runs')).toEqual({});
    expect(listHref('runs', true)).toBe('/runs?ret=1');
  });

  it('ignores a saved value of the wrong shape', () => {
    sessionStorage.setItem('emb.nav.runs', JSON.stringify({ qs: 4 }));
    expect(readListState('runs')).toEqual({});
  });
});

describe('from=runs', () => {
  it('is read from the query and decides the back list', () => {
    expect(fromRuns('?from=runs')).toBe(true);
    expect(fromRuns('?from=workflows')).toBe(false);
    expect(backList('?from=runs')).toBe('runs');
    expect(backList('')).toBe('workflows');
  });

  it('is carried onto links within the workflow', () => {
    expect(carryFrom('/workflows/wf_1/runs', '?from=runs')).toBe('/workflows/wf_1/runs?from=runs');
    expect(carryFrom('/workflows/wf_1?x=1#a', '?from=runs')).toBe(
      '/workflows/wf_1?x=1&from=runs#a',
    );
    expect(carryFrom('/workflows/wf_1', '')).toBe('/workflows/wf_1');
    expect(carryFrom('/workflows/wf_1?from=runs', '?from=runs')).toBe('/workflows/wf_1?from=runs');
  });
});

describe('recent workflows', () => {
  const items = [
    { workflowId: 'wf_a', name: 'Alpha', updatedAt: '2026-01-01T00:00:00Z' },
    { workflowId: 'wf_b', name: 'Beta', updatedAt: '2026-03-01T00:00:00Z' },
    { workflowId: 'wf_b2', name: 'Beta', updatedAt: '2026-02-01T00:00:00Z' },
    { workflowId: 'wf_c', name: 'Gamma', updatedAt: '2026-02-15T00:00:00Z' },
  ];

  it('lists visited workflows first, then the most recently updated, one per name', () => {
    expect(recentWorkflows(items, ['wf_a', 'wf_missing']).map((d) => d.workflowId)).toEqual([
      'wf_a',
      'wf_b',
      'wf_c',
    ]);
    expect(recentWorkflows(items, [], 2).map((d) => d.workflowId)).toEqual(['wf_b', 'wf_c']);
  });

  it('notes a visit at the front, once, keeping twenty', () => {
    for (let i = 0; i < 25; i += 1) noteRecent(`wf_${i}`);
    noteRecent('wf_3');
    const list = readRecent();
    expect(list[0]).toBe('wf_3');
    expect(list).toHaveLength(20);
    expect(list.filter((id) => id === 'wf_3')).toHaveLength(1);
  });

  it('reads nothing from a malformed value', () => {
    localStorage.setItem(RECENT_KEY, '{"a":1}');
    expect(readRecent()).toEqual([]);
  });
});
