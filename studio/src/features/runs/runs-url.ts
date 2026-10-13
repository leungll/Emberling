import type { RunListItem, RunListQuery } from '@/api/types';
import { PAGE_SIZES, type PageSize } from '@/components/list/page-size';
import {
  RANGE_MS,
  STATUS_OF_PARAM,
  rangeParam,
  statusParam,
  type RangeParam,
  type StatusParam,
} from '@/lib/run-words';
import { readJson, readString, writeJson, writeString } from '@/lib/storage';

/**
 * The Runs page's state in its address: `?status=&wf=&range=&size=&cursor=`. Defaults are
 * left out, so the plain page is `/runs`. Cursors are opaque and bound by the server to the
 * filters they were issued for: any filter change drops the cursor.
 */
export interface RunsQuery {
  status: StatusParam | null;
  wf: string;
  range: RangeParam | null;
  size: PageSize;
  cursor: string | null;
}

export const PAGE_SIZE_KEY = 'emb.runs.pageSize';
const DEFAULT_SIZE: PageSize = PAGE_SIZES[0];

function pageSize(value: string | null | undefined): PageSize | null {
  const n = Number(value);
  return (PAGE_SIZES as readonly number[]).includes(n) ? (n as PageSize) : null;
}

/** The viewer's rows-per-page choice, kept per browser. */
export function storedPageSize(): PageSize {
  return pageSize(readString('local', PAGE_SIZE_KEY)) ?? DEFAULT_SIZE;
}

export function storePageSize(size: PageSize): void {
  writeString('local', PAGE_SIZE_KEY, size === DEFAULT_SIZE ? null : String(size));
}

export function parseRunsQuery(search: string, fallbackSize: PageSize = DEFAULT_SIZE): RunsQuery {
  const q = new URLSearchParams(search);
  return {
    status: statusParam(q.get('status')),
    wf: q.get('wf') ?? '',
    range: rangeParam(q.get('range')),
    size: pageSize(q.get('size')) ?? fallbackSize,
    cursor: q.get('cursor') ? q.get('cursor') : null,
  };
}

/** The search string for a state, with its leading "?" or "" for none. */
export function runsSearch(state: RunsQuery, keep?: URLSearchParams): string {
  const q = new URLSearchParams();
  if (state.status) q.set('status', state.status);
  if (state.wf) q.set('wf', state.wf);
  if (state.range) q.set('range', state.range);
  if (state.size !== DEFAULT_SIZE) q.set('size', String(state.size));
  if (state.cursor) q.set('cursor', state.cursor);
  // Navigation markers such as ret=1 belong to the list memory, not to this state.
  keep?.forEach((value, key) => {
    if (!['status', 'wf', 'range', 'size', 'cursor'].includes(key)) q.set(key, value);
  });
  const s = q.toString();
  return s ? `?${s}` : '';
}

/** A change to any filter or the page size starts again from the first page. */
export function withFilters(
  state: RunsQuery,
  patch: Partial<Omit<RunsQuery, 'cursor'>>,
): RunsQuery {
  return { ...state, ...patch, cursor: null };
}

export function hasFilters(state: RunsQuery): boolean {
  return state.wf !== '' || state.range !== null;
}

/**
 * The instant a start-time filter keeps runs from. A cursor is bound to the exact instant it
 * was issued for, so the instant is fixed when the first page loads (to the minute) and
 * reused while the viewer pages through the same filters.
 */
const ANCHOR_KEY = 'emb.runs.anchor';

interface Anchor {
  range: RangeParam;
  iso: string;
}

function isAnchor(v: unknown): v is Anchor {
  if (typeof v !== 'object' || v === null) return false;
  const a = v as Record<string, unknown>;
  return (a.range === '24h' || a.range === '7d') && typeof a.iso === 'string';
}

export function startedAfter(
  range: RangeParam | null,
  paging: boolean,
  now: Date,
): string | undefined {
  if (!range) return undefined;
  const stored = readJson<Anchor | null>('session', ANCHOR_KEY, null, isAnchor);
  if (paging && stored?.range === range) return stored.iso;
  const minute = Math.floor((now.getTime() - RANGE_MS[range]) / 60_000) * 60_000;
  const iso = new Date(minute).toISOString();
  writeJson('session', ANCHOR_KEY, { range, iso } satisfies Anchor);
  return iso;
}

export function toListQuery(state: RunsQuery, after: string | undefined): RunListQuery {
  return {
    ...(state.status ? { status: STATUS_OF_PARAM[state.status] } : {}),
    ...(state.wf ? { workflowId: state.wf } : {}),
    ...(after ? { startedAfter: after } : {}),
    ...(state.cursor ? { cursor: state.cursor } : {}),
    limit: state.size,
  };
}

/**
 * Where each page starts in the list, so the footer can say "21–40". Cursors are opaque, so
 * the page remembers, per browser tab, the offset of every cursor it followed.
 */
const OFFSETS_KEY = 'emb.runs.offsets';
const OFFSETS_LIMIT = 50;

function isOffsets(v: unknown): v is [string, number][] {
  return (
    Array.isArray(v) &&
    v.every((e) => Array.isArray(e) && typeof e[0] === 'string' && typeof e[1] === 'number')
  );
}

export function offsetOf(cursor: string | null): number | null {
  if (!cursor) return 0;
  const entries = readJson<[string, number][]>('session', OFFSETS_KEY, [], isOffsets);
  return entries.find(([c]) => c === cursor)?.[1] ?? null;
}

export function rememberOffset(cursor: string, offset: number): void {
  const entries = readJson<[string, number][]>('session', OFFSETS_KEY, [], isOffsets).filter(
    ([c]) => c !== cursor,
  );
  entries.push([cursor, Math.max(0, offset)]);
  writeJson('session', OFFSETS_KEY, entries.slice(-OFFSETS_LIMIT));
}

/**
 * The state for Next and Previous. Previous uses the server's cursor to the newer page; when
 * it lands on the first page the address drops the cursor, so the first page has one URL.
 */
export function nextPage(
  state: RunsQuery,
  nextCursor: string,
  offset: number | null,
  shown: number,
): RunsQuery {
  if (offset !== null) rememberOffset(nextCursor, offset + shown);
  return { ...state, cursor: nextCursor };
}

export function previousPage(
  state: RunsQuery,
  prevCursor: string | null,
  offset: number | null,
): RunsQuery {
  const back = offset === null ? null : offset - state.size;
  if (!prevCursor || (back !== null && back <= 0)) return { ...state, cursor: null };
  if (back !== null) rememberOffset(prevCursor, back);
  return { ...state, cursor: prevCursor };
}

/** A run opens in its workflow's Runs tab, with the Runs page as its way back. */
export function runHref(item: Pick<RunListItem, 'id' | 'workflowId'>): string {
  return `/workflows/${encodeURIComponent(item.workflowId)}/runs/${encodeURIComponent(item.id)}?from=runs`;
}
