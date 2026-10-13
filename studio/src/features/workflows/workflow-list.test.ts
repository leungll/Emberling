import { describe, expect, it } from 'vitest';

import {
  highlight,
  inTab,
  matchWorkflow,
  pageWindow,
  parseListQuery,
  parsePageSize,
  searchWorkflows,
  sortWorkflows,
  tabCounts,
  writeListQuery,
} from './workflow-list';
import type { DefinitionListItem } from '@/api/types';

function wf(
  id: string,
  name: string,
  updatedAt: string,
  last: 'COMPLETED' | 'FAILED' | null,
  description = '',
): DefinitionListItem {
  return {
    workflowId: id,
    name,
    description,
    latestVersion: 1,
    updatedAt,
    lastRun: last
      ? { id: `run_${id}`, status: last, createdAt: updatedAt, error: null, failedStep: null }
      : null,
  };
}

const items = [
  wf('wf_aaaa1111', 'Release checker', '2026-10-01T00:00:00Z', 'COMPLETED', 'Checks a release'),
  wf('wf_bbbb2222', 'Image maker', '2026-10-03T00:00:00Z', 'FAILED', 'Makes release art'),
  wf('wf_cccc3333', 'Draft writer', '2026-10-02T00:00:00Z', null),
];

describe('search', () => {
  it('matches name and description case-insensitively', () => {
    expect(searchWorkflows(items, '  RELEASE ').map((m) => m.item.workflowId)).toEqual([
      'wf_aaaa1111',
      'wf_bbbb2222',
    ]);
    expect(searchWorkflows(items, 'release').every((m) => m.match === 'text')).toBe(true);
  });

  it('matches the ID with or without its wf_ prefix', () => {
    expect(searchWorkflows(items, 'bbbb22')).toEqual([{ item: items[1], match: 'id' }]);
    expect(searchWorkflows(items, 'wf_bbbb')).toEqual([{ item: items[1], match: 'id' }]);
  });

  it('finds rows by ID that a text search would not', () => {
    expect(matchWorkflow(items[2]!, 'cccc')).toBe('id');
    expect(matchWorkflow(items[2]!, 'draft')).toBe('text');
    expect(matchWorkflow(items[2]!, 'zzzz')).toBe(false);
  });

  it('a bare prefix matches no ID, and an empty search matches all', () => {
    expect(searchWorkflows(items, 'wf_')).toEqual([]);
    expect(searchWorkflows(items, '')).toHaveLength(3);
    expect(searchWorkflows(items, '').every((m) => m.match === null)).toBe(true);
  });
});

describe('highlight', () => {
  it('cuts the text at every occurrence and keeps the original case', () => {
    expect(highlight('Release a RELEASE', 'release')).toEqual([
      { text: 'Release', hit: true },
      { text: ' a ', hit: false },
      { text: 'RELEASE', hit: true },
    ]);
  });

  it('returns the whole text without a query', () => {
    expect(highlight('Draft', '')).toEqual([{ text: 'Draft', hit: false }]);
  });
});

describe('tabs', () => {
  it('filters by the last run and counts after the search', () => {
    expect(items.filter((i) => inTab(i, 'failed')).map((i) => i.name)).toEqual(['Image maker']);
    expect(items.filter((i) => inTab(i, 'never')).map((i) => i.name)).toEqual(['Draft writer']);
    expect(tabCounts(searchWorkflows(items, ''))).toEqual({ all: 3, failed: 1, never: 1 });
    expect(tabCounts(searchWorkflows(items, 'release'))).toEqual({ all: 2, failed: 1, never: 0 });
  });

  it('treats a missing lastRun like never run', () => {
    const recorded = { ...items[0]!, lastRun: undefined } as unknown as DefinitionListItem;
    expect(inTab(recorded, 'never')).toBe(true);
    expect(inTab(recorded, 'failed')).toBe(false);
  });
});

describe('sort', () => {
  const rows = searchWorkflows(items, '');
  it('puts the most recently updated first', () => {
    expect(sortWorkflows(rows, 'updated').map((r) => r.item.name)).toEqual([
      'Image maker',
      'Draft writer',
      'Release checker',
    ]);
  });

  it('sorts by name, the more recently updated first among equal names', () => {
    const twin = wf('wf_dddd4444', 'Draft writer', '2026-10-09T00:00:00Z', null);
    expect(
      sortWorkflows(searchWorkflows([...items, twin], ''), 'name').map((r) => r.item.workflowId),
    ).toEqual(['wf_dddd4444', 'wf_cccc3333', 'wf_bbbb2222', 'wf_aaaa1111']);
  });
});

describe('paging', () => {
  it('clamps the page to the pages that exist', () => {
    expect(pageWindow(65, 1, 20)).toEqual({ page: 1, pages: 4, start: 0, end: 20 });
    expect(pageWindow(65, 4, 20)).toEqual({ page: 4, pages: 4, start: 60, end: 65 });
    expect(pageWindow(65, 9, 20)).toEqual({ page: 4, pages: 4, start: 60, end: 65 });
    expect(pageWindow(0, 3, 20)).toEqual({ page: 1, pages: 1, start: 0, end: 0 });
    expect(pageWindow(5, 0, 20)).toEqual({ page: 1, pages: 1, start: 0, end: 5 });
  });

  it('reads only the offered page sizes', () => {
    expect(parsePageSize('50')).toBe(50);
    expect(parsePageSize('30')).toBe(20);
    expect(parsePageSize(null)).toBe(20);
  });
});

describe('URL state', () => {
  it('reads the list query and falls back to the defaults', () => {
    expect(parseListQuery(new URLSearchParams('tab=failed&q=x&sort=name&page=3'))).toEqual({
      tab: 'failed',
      q: 'x',
      sort: 'name',
      page: 3,
    });
    expect(parseListQuery(new URLSearchParams('tab=bogus&sort=size&page=-2'))).toEqual({
      tab: 'all',
      q: '',
      sort: 'updated',
      page: 1,
    });
  });

  it('writes only what differs from the defaults and keeps other parameters', () => {
    const base = new URLSearchParams('ret=1&tab=failed');
    expect(writeListQuery(base, { tab: 'all', q: '', sort: 'updated', page: 1 }).toString()).toBe(
      'ret=1',
    );
    expect(
      writeListQuery(new URLSearchParams(), {
        tab: 'never',
        q: 'a b',
        sort: 'name',
        page: 2,
      }).toString(),
    ).toBe('tab=never&q=a+b&sort=name&page=2');
  });
});
