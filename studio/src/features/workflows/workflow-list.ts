import type { DefinitionListItem } from '@/api/types';
import { PAGE_SIZES, type PageSize } from '@/components/list/page-size';

/**
 * The Workflows list's pure decisions: search, tab filters and counts, sort, paging and the
 * URL query. They filter the workflow list as the Backend returned it; nothing here derives
 * a run status, it only reads each workflow's `lastRun` summary.
 */

export type WorkflowTab = 'all' | 'failed' | 'never';
export type WorkflowSort = 'updated' | 'name';

export const WORKFLOW_TABS: readonly WorkflowTab[] = ['all', 'failed', 'never'];

/** How a row matched the search: by its name or description, or by its ID. */
export type MatchKind = 'text' | 'id';

export interface MatchedWorkflow {
  item: DefinitionListItem;
  /** Null when there is no search. */
  match: MatchKind | null;
}

/** The search as it is compared: trimmed and lower case. */
export function normaliseQuery(q: string): string {
  return q.trim().toLowerCase();
}

const WF_PREFIX = /^wf_/;

/**
 * Whether a workflow matches a normalised query: its name or description as text, else its
 * ID with or without the `wf_` prefix. An empty query matches everything.
 */
export function matchWorkflow(item: DefinitionListItem, q: string): MatchKind | null | false {
  if (!q) return null;
  if (`${item.name} ${item.description}`.toLowerCase().includes(q)) return 'text';
  const bare = q.replace(WF_PREFIX, '');
  if (bare && item.workflowId.toLowerCase().replace(WF_PREFIX, '').includes(bare)) return 'id';
  return false;
}

/** The workflows matching the search, before the tab filter: the tab counts use these. */
export function searchWorkflows(
  items: readonly DefinitionListItem[],
  q: string,
): MatchedWorkflow[] {
  const query = normaliseQuery(q);
  const out: MatchedWorkflow[] = [];
  for (const item of items) {
    const match = matchWorkflow(item, query);
    if (match !== false) out.push({ item, match });
  }
  return out;
}

export function inTab(item: DefinitionListItem, tab: WorkflowTab): boolean {
  if (tab === 'all') return true;
  if (tab === 'failed') return item.lastRun?.status === 'FAILED';
  return item.lastRun === null || item.lastRun === undefined;
}

export function tabCounts(matched: readonly MatchedWorkflow[]): Record<WorkflowTab, number> {
  const counts: Record<WorkflowTab, number> = { all: 0, failed: 0, never: 0 };
  for (const { item } of matched) {
    for (const tab of WORKFLOW_TABS) if (inTab(item, tab)) counts[tab] += 1;
  }
  return counts;
}

const byUpdated = (a: DefinitionListItem, b: DefinitionListItem): number =>
  a.updatedAt < b.updatedAt ? 1 : a.updatedAt > b.updatedAt ? -1 : 0;

/** Recently updated first, or by name with the more recently updated first among equal names. */
export function sortWorkflows<T extends { item: DefinitionListItem }>(
  rows: readonly T[],
  sort: WorkflowSort,
): T[] {
  const copy = [...rows];
  if (sort === 'name') {
    copy.sort((a, b) => a.item.name.localeCompare(b.item.name) || byUpdated(a.item, b.item));
  } else {
    copy.sort((a, b) => byUpdated(a.item, b.item));
  }
  return copy;
}

/** One piece of a text, highlighted when it is an occurrence of the query. */
export interface Segment {
  text: string;
  hit: boolean;
}

/** The text cut at every case-insensitive occurrence of the normalised query. */
export function highlight(text: string, q: string): Segment[] {
  if (!q) return text ? [{ text, hit: false }] : [];
  const lower = text.toLowerCase();
  const out: Segment[] = [];
  let i = 0;
  let at = lower.indexOf(q, i);
  while (at >= 0) {
    if (at > i) out.push({ text: text.slice(i, at), hit: false });
    out.push({ text: text.slice(at, at + q.length), hit: true });
    i = at + q.length;
    at = lower.indexOf(q, i);
  }
  if (i < text.length) out.push({ text: text.slice(i), hit: false });
  return out;
}

export interface PageWindow {
  /** The page shown, clamped to the pages that exist. */
  page: number;
  pages: number;
  /** 0-based slice bounds. */
  start: number;
  end: number;
}

export function pageWindow(total: number, page: number, size: number): PageWindow {
  const pages = Math.max(1, Math.ceil(total / size));
  const shown = Math.min(Math.max(1, page), pages);
  const start = (shown - 1) * size;
  return { page: shown, pages, start, end: Math.min(start + size, total) };
}

export interface ListQuery {
  tab: WorkflowTab;
  q: string;
  sort: WorkflowSort;
  page: number;
}

/** Reads `?tab=failed|never&q=&sort=name&page=`; anything unknown falls back to the default. */
export function parseListQuery(params: URLSearchParams): ListQuery {
  const tab = params.get('tab');
  const page = Number.parseInt(params.get('page') ?? '', 10);
  return {
    tab: tab === 'failed' || tab === 'never' ? tab : 'all',
    q: params.get('q') ?? '',
    sort: params.get('sort') === 'name' ? 'name' : 'updated',
    page: Number.isFinite(page) && page > 1 ? page : 1,
  };
}

/** The query string for a list state, defaults omitted; other parameters are kept. */
export function writeListQuery(base: URLSearchParams, query: ListQuery): URLSearchParams {
  const p = new URLSearchParams(base);
  for (const key of ['tab', 'q', 'sort', 'page']) p.delete(key);
  if (query.tab !== 'all') p.set('tab', query.tab);
  if (query.q) p.set('q', query.q);
  if (query.sort !== 'updated') p.set('sort', query.sort);
  if (query.page > 1) p.set('page', String(query.page));
  return p;
}

export const PAGE_SIZE_KEY = 'emb.workflows.pageSize';

export function parsePageSize(raw: string | null): PageSize {
  const n = Number(raw);
  return (PAGE_SIZES as readonly number[]).includes(n) ? (n as PageSize) : PAGE_SIZES[0];
}

export const editorHref = (workflowId: string): string =>
  `/workflows/${encodeURIComponent(workflowId)}`;
export const runsHref = (workflowId: string): string => `${editorHref(workflowId)}/runs`;
export const runHref = (workflowId: string, runId: string): string =>
  `${runsHref(workflowId)}/${encodeURIComponent(runId)}`;
