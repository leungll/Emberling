import { ancestors, type RunModel, type TraceElement } from './run-model';

/**
 * Which rows of the trace show. Without a search or Failures only, a row shows when every
 * row above it is open. With either, every matching row shows with its ancestors, so each
 * hit keeps its place; the ancestors are marked as the path to it.
 */

export interface TraceFilter {
  query: string;
  failuresOnly: boolean;
}

export interface TraceRows {
  rows: TraceElement[];
  /** Set while filtering: 'hit' for a match, 'path' for an ancestor kept to show it. */
  hits: ReadonlyMap<string, 'hit' | 'path'> | null;
}

export function filtering(f: TraceFilter): boolean {
  return f.query.trim() !== '' || f.failuresOnly;
}

export function rowMatches(el: TraceElement, f: TraceFilter): boolean {
  if (f.failuresOnly && el.tone !== 'failed') return false;
  const q = f.query.trim().toLowerCase();
  return !q || `${el.label} ${el.type}`.toLowerCase().includes(q);
}

export function visibleRows(
  model: RunModel,
  isOpen: (el: TraceElement) => boolean,
  f: TraceFilter,
  /** The element whose rows alone are shown; the run shows everything. */
  scope: TraceElement = model.root,
): TraceRows {
  const rows: TraceElement[] = [];
  if (!filtering(f)) {
    const walk = (el: TraceElement) => {
      rows.push(el);
      if (el === scope || isOpen(el)) el.children.forEach(walk);
    };
    walk(scope);
    return { rows, hits: null };
  }
  const within = (el: TraceElement) => el === scope || ancestors(el).includes(scope);
  const keep = new Map<string, 'hit' | 'path'>();
  for (const el of model.byId.values()) {
    if (!within(el) || !rowMatches(el, f)) continue;
    keep.set(el.id, 'hit');
    for (const a of ancestors(el)) if (within(a) && !keep.has(a.id)) keep.set(a.id, 'path');
  }
  const walk = (el: TraceElement) => {
    if (keep.has(el.id)) rows.push(el);
    el.children.forEach(walk);
  };
  walk(scope);
  return { rows, hits: keep };
}

/**
 * Open rows: every row starts open, including one that arrives later (an Agent's turns load
 * after its step's records), so the viewer never meets a collapsed row they did not close.
 * Only the rows the viewer closed are shut; the run itself cannot be.
 */
export function openState(closed: ReadonlySet<string>): (el: TraceElement) => boolean {
  return (el) => el.kind === 'run' || !closed.has(el.id);
}
