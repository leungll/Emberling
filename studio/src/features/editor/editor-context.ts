import { createContext, useContext, type RefObject } from 'react';

import type { LinkContext } from './model/connect';
import type { Issue } from './model/checks';
import type { Staged } from './model/images';
import type { PlaceMap } from './model/layout';
import type { GroupId } from './model/steps';
import type { LastRunInfo, Registry } from './useEditorData';

/** Where things are drawn on the canvas, kept current by the canvas for edits that place steps. */
export interface Geometry {
  places: PlaceMap;
  portY: (id: string, dir: 'in' | 'out', handle: string) => number;
  /** The canvas point at the middle of the visible area. */
  center: () => { x: number; y: number };
  reveal: (id: string) => void;
  focusCard: (id: string) => void;
  focusCanvas: () => void;
}

/** Edits of steps and links. Each is one undoable change and says what it did in a toast. */
export interface EditOps {
  addStep: (type: string, at?: { x: number; y: number }) => void;
  addLinked: (type: string, link: LinkContext) => void;
  connect: (s: string, sh: string, t: string, th: string) => void;
  deleteSteps: (ids: readonly string[], verb?: 'delete' | 'cut') => void;
  deleteEdge: (id: string) => void;
  duplicate: (ids: readonly string[]) => void;
  copy: (ids: readonly string[], quiet?: boolean) => boolean;
  cut: (ids: readonly string[]) => void;
  paste: (at?: { x: number; y: number }) => void;
  rename: (id: string, name: string) => void;
  undo: () => void;
  redo: () => void;
}

export interface FocusRequest {
  nodeId: string;
  field: string | null;
  seq: number;
}

export interface QuickAddRequest {
  /** Screen point the menu opens at; omitted centres it over the canvas. */
  x?: number;
  y?: number;
  /** Canvas point an unlinked step lands at. */
  at?: { x: number; y: number };
  link?: LinkContext;
  /** Lists only the steps of one group, such as outputs for a workflow without one. */
  only?: GroupId;
}

export interface EditorContextValue {
  workflowId: string | null;
  registry: Registry;
  lastRun: LastRunInfo;
  issues: readonly Issue[];
  staged: Staged;
  stageFiles: (nodeId: string, files: File[], replace: boolean) => void;
  unstage: (nodeId: string) => void;
  pickFiles: (nodeId: string, replace: boolean) => void;
  /** Selects a step and opens its settings, optionally at one field. */
  openSettings: (nodeId: string, field?: string | null) => void;
  focusRequest: FocusRequest | null;
  showChecks: () => void;
  /** Goes to what an issue is about: its field, or quick add for a missing output step. */
  showIssue: (issue: Issue) => void;
  quickAdd: (req: QuickAddRequest) => void;
  ops: EditOps;
  geometry: RefObject<Geometry>;
}

export const EditorContext = createContext<EditorContextValue | null>(null);

export function useEditor(): EditorContextValue {
  const ctx = useContext(EditorContext);
  if (!ctx) throw new Error('useEditor outside the Editor');
  return ctx;
}
