import { readJson, writeJson } from '@/lib/storage';

/**
 * Where the viewer left each list page. Every way back to a list (the sidebar, a detail
 * page's back button, browser Back) lands on the same query, scroll position and row, kept
 * per browser tab in sessionStorage `emb.nav.<list>`. It is a convenience, never a fact:
 * missing or blocked storage simply opens the list as it opens by default.
 */
export type ListName = 'runs' | 'workflows';

export interface ListState {
  /** The list's query string, with its leading "?", or "" for none. */
  qs?: string;
  scroll?: number;
  /** The key of the row last opened from the list. */
  opened?: string;
}

const KEY = 'emb.nav.';
/** A back link asks the list to restore its scroll and highlight the row last opened. */
export const RETURN_PARAM = 'ret';
export const FROM_PARAM = 'from';

export const LIST_PATH: Record<ListName, string> = { runs: '/runs', workflows: '/workflows' };

function isListState(v: unknown): v is ListState {
  if (typeof v !== 'object' || v === null || Array.isArray(v)) return false;
  const s = v as Record<string, unknown>;
  return (
    (s.qs === undefined || typeof s.qs === 'string') &&
    (s.scroll === undefined || typeof s.scroll === 'number') &&
    (s.opened === undefined || typeof s.opened === 'string')
  );
}

export function readListState(list: ListName): ListState {
  return readJson<ListState>('session', KEY + list, {}, isListState);
}

export function writeListState(list: ListName, patch: ListState): void {
  writeJson('session', KEY + list, { ...readListState(list), ...patch });
}

/** The list as the viewer left it; `ret` asks it to restore scroll and the row last opened. */
export function listHref(list: ListName, ret: boolean): string {
  const q = new URLSearchParams(readListState(list).qs ?? '');
  q.delete(RETURN_PARAM);
  if (ret) q.set(RETURN_PARAM, '1');
  const s = q.toString();
  return `${LIST_PATH[list]}${s ? `?${s}` : ''}`;
}

/** A workflow page entered from the Runs list carries from=runs across its tabs and runs. */
export function fromRuns(search: string): boolean {
  return new URLSearchParams(search).get(FROM_PARAM) === 'runs';
}

/** Keeps from=runs on a link within a workflow, when the current page has it. */
export function carryFrom(href: string, search: string): string {
  if (!fromRuns(search)) return href;
  const hashAt = href.indexOf('#');
  const hash = hashAt >= 0 ? href.slice(hashAt) : '';
  const base = hashAt >= 0 ? href.slice(0, hashAt) : href;
  const queryAt = base.indexOf('?');
  const path = queryAt >= 0 ? base.slice(0, queryAt) : base;
  const q = new URLSearchParams(queryAt >= 0 ? base.slice(queryAt) : '');
  if (q.has(FROM_PARAM)) return href;
  q.set(FROM_PARAM, 'runs');
  return `${path}?${q.toString()}${hash}`;
}

/** The list a workflow page goes back to. */
export function backList(search: string): ListName {
  return fromRuns(search) ? 'runs' : 'workflows';
}
