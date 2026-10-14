import { create } from 'zustand';

import type { Edge, JsonObject, JsonValue, Node } from '@/api/types';
import { newEdgeId, newNodeId } from './nodeIds';
import {
  emptyHistory,
  record,
  redo as redoStep,
  undo as undoStep,
  type History,
} from './model/history';
import { layered, needsAutoLayout } from './model/layout';
import { labelFollowsName } from './model/steps';

/**
 * The workflow being edited: its draft, selection and undo history. The draft lives only in
 * the page until Save creates a new version; nothing here is persisted.
 */
export interface Doc {
  name: string;
  description: string;
  nodes: Node[];
  edges: Edge[];
}

export interface Selection {
  /** Selected steps; one step opens its settings, several show the multi-select view. */
  ids: string[];
  /** A selected link, exclusive with steps. */
  edge: string | null;
}

interface EditorState {
  doc: Doc;
  /** The draft as last saved or loaded, for the unsaved-changes check. */
  savedKey: string;
  history: History<Doc>;
  selection: Selection;
  /** Stored positions overlap, so the canvas shows a computed layout until the first edit. */
  autoLayout: boolean;
  /** Full-card heights measured on the canvas, for the computed layout. */
  heights: Record<string, number>;
  /** The field the last history entry was recorded for, so typing records once per field. */
  editingKey: string | null;

  /** What the draft was loaded from, such as a workflow version; the page checks it is current. */
  source: string | null;
  load: (doc: Doc, source: string) => void;
  /** Marks the draft saved; `source` names the version the save created. */
  markSaved: (doc: Doc, source: string) => void;
  /** The draft as Save sends it: steps placed where the canvas shows them. */
  current: () => Doc;
  setHeights: (heights: Record<string, number>) => void;
  select: (ids: string[], edge?: string | null) => void;

  /** Starts an undoable change. A `key` records once per field while the user types. */
  begin: (key?: string) => void;
  setName: (name: string) => void;
  setDescription: (description: string) => void;
  renameStep: (id: string, name: string) => void;
  setConfig: (id: string, key: string, value: JsonValue | undefined) => void;
  patchConfig: (id: string, patch: JsonObject, remove?: readonly string[]) => void;

  /** Structural edits: each records one history entry. */
  apply: (fn: (doc: Doc) => Doc, select?: string[]) => void;
  /** Moves steps; a `key` records once for a burst of moves, such as arrow-key nudges. */
  moveSteps: (moves: Record<string, { x: number; y: number }>, key?: string) => void;
  undo: () => boolean;
  redo: () => boolean;
}

export const docKey = (doc: Doc): string =>
  JSON.stringify({ n: doc.name, d: doc.description, nodes: doc.nodes, edges: doc.edges });

const EMPTY: Doc = { name: '', description: '', nodes: [], edges: [] };

/** Writes the computed layout into the steps, so later edits start from what is on screen. */
function frozen(doc: Doc, heights: Record<string, number>): Doc {
  const at = layered(doc.nodes, doc.edges, (id) => heights[id] ?? 88);
  return {
    ...doc,
    nodes: doc.nodes.map((n) => {
      const p = at[n.id];
      return p ? { ...n, position: { x: p.x, y: p.y } } : n;
    }),
  };
}

export const useEditorStore = create<EditorState>((set, get) => {
  /** Records the current draft (pinned to the computed layout) before a change. */
  const checkpoint = (key: string | null): Doc => {
    const s = get();
    const base = s.autoLayout ? frozen(s.doc, s.heights) : s.doc;
    set({ history: record(s.history, s.doc), autoLayout: false, editingKey: key, doc: base });
    return base;
  };

  const updateNode = (id: string, fn: (n: Node) => Node) => {
    const s = get();
    set({ doc: { ...s.doc, nodes: s.doc.nodes.map((n) => (n.id === id ? fn(n) : n)) } });
  };

  return {
    source: null,
    doc: EMPTY,
    savedKey: docKey(EMPTY),
    history: emptyHistory(),
    selection: { ids: [], edge: null },
    autoLayout: false,
    heights: {},
    editingKey: null,

    load: (doc, source) => {
      set({
        source,
        doc,
        savedKey: docKey(doc),
        history: emptyHistory(),
        selection: { ids: [], edge: null },
        autoLayout: needsAutoLayout(doc.nodes),
        heights: {},
        editingKey: null,
      });
    },
    markSaved: (doc, source) => {
      set({ doc, source, savedKey: docKey(doc), editingKey: null, autoLayout: false });
    },
    current: () => {
      const s = get();
      return s.autoLayout ? frozen(s.doc, s.heights) : s.doc;
    },
    setHeights: (heights) => {
      const cur = get().heights;
      if (Object.keys(heights).every((k) => cur[k] === heights[k])) return;
      set({ heights: { ...cur, ...heights } });
    },
    select: (ids, edge = null) => {
      set({ selection: { ids, edge: ids.length ? null : edge }, editingKey: null });
    },

    begin: (key) => {
      if (key && get().editingKey === key) return;
      checkpoint(key ?? null);
    },
    setName: (name) => {
      set({ doc: { ...get().doc, name } });
    },
    setDescription: (description) => {
      set({ doc: { ...get().doc, description } });
    },
    renameStep: (id, name) => {
      updateNode(id, (n) => {
        // A field label that equals the step name follows the rename.
        const follows = 'label' in n.config && n.config.label !== '' && labelFollowsName(n);
        return { ...n, name, config: follows ? { ...n.config, label: name } : n.config };
      });
    },
    setConfig: (id, key, value) => {
      updateNode(id, (n) => {
        const config = Object.fromEntries(Object.entries(n.config).filter(([k]) => k !== key));
        if (value !== undefined) config[key] = value;
        return { ...n, config };
      });
    },
    patchConfig: (id, patch, remove = []) => {
      updateNode(id, (n) => {
        const merged = { ...n.config, ...patch };
        const config = Object.fromEntries(
          Object.entries(merged).filter(([k]) => !remove.includes(k)),
        );
        return { ...n, config };
      });
    },

    apply: (fn, select) => {
      const base = checkpoint(null);
      const doc = fn(base);
      const ids = new Set(doc.nodes.map((n) => n.id));
      const edges = new Set(doc.edges.map((e) => e.id));
      const cur = get().selection;
      const nextIds = (select ?? cur.ids).filter((id) => ids.has(id));
      set({
        doc,
        editingKey: null,
        selection: {
          ids: nextIds,
          edge: !nextIds.length && cur.edge && edges.has(cur.edge) && !select ? cur.edge : null,
        },
      });
    },
    moveSteps: (moves, key) => {
      const s = get();
      const base = key && s.editingKey === key ? s.doc : checkpoint(key ?? null);
      set({
        editingKey: key ?? null,
        doc: {
          ...base,
          nodes: base.nodes.map((n) => {
            const p = moves[n.id];
            return p ? { ...n, position: { x: p.x, y: p.y } } : n;
          }),
        },
      });
    },
    undo: () => {
      const s = get();
      const r = undoStep(s.history, s.doc);
      if (!r) return false;
      set({
        doc: r.state,
        history: r.history,
        editingKey: null,
        autoLayout: needsAutoLayout(r.state.nodes),
      });
      return true;
    },
    redo: () => {
      const s = get();
      const r = redoStep(s.history, s.doc);
      if (!r) return false;
      set({
        doc: r.state,
        history: r.history,
        editingKey: null,
        autoLayout: needsAutoLayout(r.state.nodes),
      });
      return true;
    },
  };
});

export const ids = { node: newNodeId, edge: newEdgeId };
