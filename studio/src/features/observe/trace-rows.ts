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
): TraceRows {
  const rows: TraceElement[] = [];
  if (!filtering(f)) {
    const walk = (el: TraceElement) => {
      rows.push(el);
      if (isOpen(el)) el.children.forEach(walk);
    };
    walk(model.root);
    return { rows, hits: null };
  }
  const keep = new Map<string, 'hit' | 'path'>();
  for (const el of model.byId.values()) {
    if (!rowMatches(el, f)) continue;
    keep.set(el.id, 'hit');
    for (const a of ancestors(el)) if (!keep.has(a.id)) keep.set(a.id, 'path');
  }
  const walk = (el: TraceElement) => {
    if (keep.has(el.id)) rows.push(el);
    el.children.forEach(walk);
  };
  walk(model.root);
  return { rows, hits: keep };
}

/**
 * Open rows: the run, every row the viewer opened, and the ancestors of the selection
 * unless the viewer closed them.
 */
export function openState(
  opened: ReadonlySet<string>,
  closed: ReadonlySet<string>,
  selected: TraceElement | null,
): (el: TraceElement) => boolean {
  const onPath = new Set(selected ? ancestors(selected).map((a) => a.id) : []);
  return (el) =>
    !closed.has(el.id) && (el.kind === 'run' || opened.has(el.id) || onPath.has(el.id));
}
