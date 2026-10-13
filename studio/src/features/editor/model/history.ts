/** Undo and redo over whole-document snapshots. Session only; nothing here is persisted. */

export const HISTORY_LIMIT = 100;

export interface History<T> {
  undo: T[];
  redo: T[];
}

export const emptyHistory = <T>(): History<T> => ({ undo: [], redo: [] });

/** Records the state before a change. A new change clears the redo stack. */
export function record<T>(h: History<T>, before: T, limit = HISTORY_LIMIT): History<T> {
  const undo = [...h.undo, before];
  if (undo.length > limit) undo.splice(0, undo.length - limit);
  return { undo, redo: [] };
}

/** Steps back: returns the state to show and the new stacks, or `null` with nothing to undo. */
export function undo<T>(h: History<T>, current: T): { state: T; history: History<T> } | null {
  if (!h.undo.length) return null;
  const state = h.undo[h.undo.length - 1] as T;
  return { state, history: { undo: h.undo.slice(0, -1), redo: [...h.redo, current] } };
}

export function redo<T>(h: History<T>, current: T): { state: T; history: History<T> } | null {
  if (!h.redo.length) return null;
  const state = h.redo[h.redo.length - 1] as T;
  return { state, history: { undo: [...h.undo, current], redo: h.redo.slice(0, -1) } };
}
