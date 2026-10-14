import { beforeEach, describe, expect, it } from 'vitest';

import {
  hasFilters,
  nextPage,
  offsetOf,
  parseRunsQuery,
  previousPage,
  runsSearch,
  startedAfter,
  storedPageSize,
  storePageSize,
  toListQuery,
  withFilters,
  type RunsQuery,
} from './runs-url';

const base: RunsQuery = { status: null, wf: '', range: null, size: 20, cursor: null };

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
});

describe('the Runs page address', () => {
  it('reads every parameter and ignores unknown values', () => {
    expect(parseRunsQuery('?status=waiting&wf=wf_1&range=24h&size=50&cursor=abc')).toEqual({
      status: 'waiting',
      wf: 'wf_1',
      range: '24h',
      size: 50,
      cursor: 'abc',
    });
    expect(parseRunsQuery('?status=PAUSED&range=30d&size=7')).toEqual(base);
    expect(parseRunsQuery('', 100).size).toBe(100);
  });

  it('writes only what differs from the defaults and keeps other markers', () => {
    expect(runsSearch(base)).toBe('');
    expect(runsSearch({ ...base, status: 'failed', size: 50 })).toBe('?status=failed&size=50');
    expect(runsSearch({ ...base, wf: 'wf_1' }, new URLSearchParams('ret=1&status=x'))).toBe(
      '?wf=wf_1&ret=1',
    );
  });

  it('drops the cursor whenever a filter changes', () => {
    const paged = { ...base, cursor: 'c2' };
    expect(withFilters(paged, { status: 'running' })).toEqual({ ...base, status: 'running' });
    expect(withFilters(paged, { size: 50 }).cursor).toBeNull();
    expect(hasFilters(base)).toBe(false);
    expect(hasFilters({ ...base, range: '7d' })).toBe(true);
    // The status tab is not one of the "Clear filters" filters.
    expect(hasFilters({ ...base, status: 'failed' })).toBe(false);
  });

  it('maps the state to the server query', () => {
    expect(toListQuery({ ...base, status: 'waiting', wf: 'wf_1', cursor: 'c' }, undefined)).toEqual(
      {
        status: 'PAUSED',
        workflowId: 'wf_1',
        cursor: 'c',
        limit: 20,
      },
    );
    expect(toListQuery(base, '2026-10-12T00:00:00.000Z')).toEqual({
      startedAfter: '2026-10-12T00:00:00.000Z',
      limit: 20,
    });
  });
});

describe('the start-time anchor', () => {
  it('is fixed to the minute on the first page and reused while paging', () => {
    const first = startedAfter('24h', false, new Date('2026-10-13T08:06:42.429Z'));
    expect(first).toBe('2026-10-12T08:06:00.000Z');
    // A later page of the same filters keeps the instant its cursor was issued for.
    expect(startedAfter('24h', true, new Date('2026-10-13T09:30:00Z'))).toBe(first);
    // Another range, or the first page again, takes a fresh instant.
    expect(startedAfter('7d', true, new Date('2026-10-13T09:30:00Z'))).toBe(
      '2026-10-06T09:30:00.000Z',
    );
    expect(startedAfter(null, true, new Date())).toBeUndefined();
  });
});

describe('cursor paging', () => {
  it('remembers where each followed page starts', () => {
    const p2 = nextPage(base, 'c2', 0, 20);
    expect(p2.cursor).toBe('c2');
    expect(offsetOf('c2')).toBe(20);
    const p3 = nextPage(p2, 'c3', offsetOf('c2'), 20);
    expect(offsetOf(p3.cursor)).toBe(40);
    expect(offsetOf(null)).toBe(0);
    expect(offsetOf('unknown')).toBeNull();
  });

  it('goes back with the server’s newer cursor and lands on the plain first page', () => {
    const p3 = { ...base, cursor: 'c3' };
    const p2 = previousPage(p3, 'p2', 40);
    expect(p2.cursor).toBe('p2');
    expect(offsetOf('p2')).toBe(20);
    expect(previousPage(p2, 'p1', 20).cursor).toBeNull();
    expect(previousPage(p2, null, null).cursor).toBeNull();
    // Without a known offset the server cursor is still followed.
    expect(previousPage(p3, 'p2', null).cursor).toBe('p2');
  });

  it('keeps the page size per browser', () => {
    expect(storedPageSize()).toBe(20);
    storePageSize(50);
    expect(storedPageSize()).toBe(50);
    storePageSize(20);
    expect(localStorage.getItem('emb.runs.pageSize')).toBeNull();
  });
});
