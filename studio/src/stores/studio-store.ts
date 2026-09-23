import { create } from 'zustand';

import type { Edge, Node } from '@/api/types';

/**
 * Client interaction state only.
 *
 * Nothing here is an execution fact. Run status, NodeRun status, Attempts, Turns and
 * Events come from the Backend Snapshot and the SSE stream and are held as they arrive.
 * Run aggregation, transition rules and retry decisions belong to the Backend; adding
 * them here would create a second, divergent execution model.
 *
 * The Edit history below is unsaved-Definition interaction state (04 §2.5 lists Undo and
 * Redo among the Canvas operations): it only restores a graph the user already had on this
 * page and never reaches the server.
 */

export type StudioMode = 'edit' | 'observe';

/** What Save would persist of the canvas: nodes (positions included) and edges. */
export interface EditorGraph {
  nodes: Node[];
  edges: Edge[];
}

/** Undo depth. The oldest entries are dropped first, so memory stays bounded. */
export const EDIT_HISTORY_LIMIT = 100;

/** JSON with object keys sorted, so graphs that differ only in key order compare equal. */
function canonicalJson(value: unknown): string {
  return JSON.stringify(value, (_key, item: unknown) => {
    if (item === null || typeof item !== 'object' || Array.isArray(item)) return item;
    return Object.fromEntries(
      Object.entries(item as Record<string, unknown>).sort(([a], [b]) => (a < b ? -1 : 1)),
    );
  });
}

export function graphsEqual(a: EditorGraph, b: EditorGraph): boolean {
  return canonicalJson(a) === canonicalJson(b);
}

const EMPTY_GRAPH: EditorGraph = { nodes: [], edges: [] };

interface StudioState {
  mode: StudioMode;
  /** Definition node id in edit mode, NodeRun id in observe mode. */
  selectedNodeId: string | null;
  selectedEventSeq: number | null;
  /** Timeline follows the newest Event until the user picks a historical one. */
  followLive: boolean;
  hasUnsavedChanges: boolean;

  /** Graphs undo can return to, oldest first. */
  past: EditorGraph[];
  /** Graphs undone, the next redo last. */
  future: EditorGraph[];
  /** The graph last loaded or saved: "unsaved changes" means the canvas differs from it. */
  savedGraph: EditorGraph;
  /** The graph on the canvas as last reported by `syncUnsavedChanges`. */
  canvasGraph: EditorGraph;
  /** Open coalescing session: a checkpoint with this key merges into the entry it opened. */
  coalesceKey: string | null;

  setMode: (mode: StudioMode) => void;
  selectNode: (nodeId: string | null) => void;
  selectEvent: (seq: number | null) => void;
  setFollowLive: (followLive: boolean) => void;
  setUnsavedChanges: (hasUnsavedChanges: boolean) => void;
  resetSelection: () => void;

  /** A Definition was (re)loaded: history starts over and `saved` is the clean point. */
  resetHistory: (saved: EditorGraph) => void;
  /**
   * Save succeeded with `saved`; history is kept, so undo can still step past it. The canvas
   * may have changed while the request was in flight, so dirtiness is recomputed against it.
   */
  markSaved: (saved: EditorGraph) => void;
  /**
   * Records `current` as the graph the edit about to happen can be undone to. A `key` equal
   * to the open session's key is the same continuous edit (one drag, typing in one field)
   * and adds nothing.
   */
  checkpoint: (current: EditorGraph, key?: string) => void;
  /** Ends the open coalescing session, so the next edit is a separate entry. */
  closeCoalescing: () => void;
  /** Returns the graph to restore, or null when there is nothing to undo. */
  undo: (current: EditorGraph) => EditorGraph | null;
  /** Returns the graph to restore, or null when there is nothing to redo. */
  redo: (current: EditorGraph) => EditorGraph | null;
  /** Records the graph now on the canvas and recomputes `hasUnsavedChanges` for it. */
  syncUnsavedChanges: (current: EditorGraph) => void;
}

export const useStudioStore = create<StudioState>((set, get) => ({
  mode: 'edit',
  selectedNodeId: null,
  selectedEventSeq: null,
  followLive: true,
  hasUnsavedChanges: false,

  past: [],
  future: [],
  savedGraph: EMPTY_GRAPH,
  canvasGraph: EMPTY_GRAPH,
  coalesceKey: null,

  setMode: (mode) => set({ mode, selectedNodeId: null, selectedEventSeq: null }),
  selectNode: (selectedNodeId) => set({ selectedNodeId }),
  // Picking a historical Event stops the timeline from jumping; Resume Live restores it.
  selectEvent: (selectedEventSeq) =>
    set(
      selectedEventSeq === null
        ? { selectedEventSeq: null }
        : { selectedEventSeq, followLive: false },
    ),
  setFollowLive: (followLive) =>
    set(followLive ? { followLive: true, selectedEventSeq: null } : { followLive: false }),
  setUnsavedChanges: (hasUnsavedChanges) => set({ hasUnsavedChanges }),
  resetSelection: () => set({ selectedNodeId: null, selectedEventSeq: null }),

  resetHistory: (saved) =>
    set({
      past: [],
      future: [],
      savedGraph: saved,
      canvasGraph: saved,
      coalesceKey: null,
      hasUnsavedChanges: false,
    }),
  // 04 §2.6: Run is disabled while unsaved changes exist, so an edit made during the Save
  // request must keep the page dirty even though the request itself succeeded.
  markSaved: (saved) =>
    set({
      savedGraph: saved,
      coalesceKey: null,
      hasUnsavedChanges: !graphsEqual(get().canvasGraph, saved),
    }),
  checkpoint: (current, key) => {
    const { past, coalesceKey } = get();
    if (key !== undefined && key === coalesceKey) return;
    const top = past.at(-1);
    const next = top && graphsEqual(top, current) ? past : [...past, current];
    set({
      past: next.length > EDIT_HISTORY_LIMIT ? next.slice(next.length - EDIT_HISTORY_LIMIT) : next,
      future: [],
      coalesceKey: key ?? null,
    });
  },
  closeCoalescing: () => set({ coalesceKey: null }),
  undo: (current) => {
    const { past, future, savedGraph } = get();
    const previous = past.at(-1);
    if (!previous) return null;
    set({
      past: past.slice(0, -1),
      future: [...future, current],
      coalesceKey: null,
      hasUnsavedChanges: !graphsEqual(previous, savedGraph),
    });
    return previous;
  },
  redo: (current) => {
    const { past, future, savedGraph } = get();
    const next = future.at(-1);
    if (!next) return null;
    set({
      past: [...past, current],
      future: future.slice(0, -1),
      coalesceKey: null,
      hasUnsavedChanges: !graphsEqual(next, savedGraph),
    });
    return next;
  },
  syncUnsavedChanges: (current) => {
    set({ canvasGraph: current, hasUnsavedChanges: !graphsEqual(current, get().savedGraph) });
  },
}));
