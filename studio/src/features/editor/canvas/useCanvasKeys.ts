import { useReactFlow } from '@xyflow/react';
import { useEffect, useRef, type RefObject } from 'react';

import { isTypingTarget, modalOpen } from '@/lib/shortcuts';
import { useEditor } from '@/features/editor/editor-context';
import { useEditorStore } from '@/features/editor/editor-store';
import { nearest, type ArrowKey, type PlaceMap } from '@/features/editor/model/layout';
import type { MenuTarget } from './CanvasMenu';
import type { CanvasTool } from './CanvasControls';

const NUDGE_BURST_MS = 800;

interface CanvasKeys {
  areaRef: RefObject<HTMLDivElement | null>;
  places: RefObject<PlaceMap>;
  pointer: RefObject<{ x: number; y: number } | null>;
  tool: CanvasTool;
  setTool: (t: CanvasTool) => void;
  vp: {
    fit: () => void;
    zoomTo: (z: number) => void;
    zoomBy: (dir: 1 | -1) => void;
    reveal: (p: PlaceMap, id: string) => void;
  };
  menuOpen: boolean;
  cancelLink: (() => void) | null;
  focusCard: (id: string) => void;
  focusCanvas: () => void;
  openMenuAt: (x: number, y: number, target: MenuTarget, keyboard?: boolean) => void;
}

/**
 * The canvas keyboard model. Arrows move focus and the selection to the nearest step, Alt
 * with an arrow nudges the selection, Enter opens settings and Shift F10 the step menu. Edit
 * keys act on the selection, and single keys switch tools and zoom; none of them fire while
 * the viewer types in a field or a dialog is open.
 */
export function useCanvasKeys(k: CanvasKeys): void {
  const { ops, openSettings } = useEditor();
  const rf = useReactFlow();
  const nudgeAt = useRef(0);
  const nudgeKey = useRef(0);
  const latest = useRef({ k, ops, openSettings, rf });
  useEffect(() => {
    latest.current = { k, ops, openSettings, rf };
  });

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const { k: c, ops: o, openSettings: open, rf: flow } = latest.current;
      const mod = e.metaKey || e.ctrlKey;
      const key = e.key.toLowerCase();
      if (e.key === 'Escape' && c.cancelLink) {
        e.preventDefault();
        c.cancelLink();
        return;
      }
      if (e.defaultPrevented || modalOpen() || c.menuOpen) return;
      if (isTypingTarget(e.target)) return;
      const store = useEditorStore.getState();
      const sel = store.selection.ids;
      const target = e.target instanceof HTMLElement ? e.target : null;
      const card = target?.matches('[data-step-card]') ? target.getAttribute('data-node') : null;

      if (card) {
        if ((e.key === 'F10' && e.shiftKey) || e.key === 'ContextMenu') {
          e.preventDefault();
          const r = target?.getBoundingClientRect();
          if (!sel.includes(card)) store.select([card]);
          c.openMenuAt(
            (r?.left ?? 0) + 16,
            (r?.top ?? 0) + 32,
            { kind: 'step', ids: useEditorStore.getState().selection.ids },
            true,
          );
          return;
        }
        if (e.key.startsWith('Arrow') && !mod) {
          e.preventDefault();
          const dir = e.key as ArrowKey;
          if (e.altKey) {
            const d = e.shiftKey ? 32 : 8;
            const ids = sel.includes(card) ? sel : [card];
            const dx = dir === 'ArrowRight' ? d : dir === 'ArrowLeft' ? -d : 0;
            const dy = dir === 'ArrowDown' ? d : dir === 'ArrowUp' ? -d : 0;
            const now = Date.now();
            if (now - nudgeAt.current > NUDGE_BURST_MS) nudgeKey.current += 1;
            nudgeAt.current = now;
            const moves: Record<string, { x: number; y: number }> = {};
            for (const id of ids) {
              const p = c.places.current[id];
              if (p) moves[id] = { x: p.x + dx, y: p.y + dy };
            }
            store.moveSteps(moves, `nudge:${String(nudgeKey.current)}`);
            const first = ids[0];
            if (first) requestAnimationFrame(() => c.vp.reveal(c.places.current, first));
            return;
          }
          const to = nearest(card, dir, c.places.current);
          if (to) {
            store.select([to]);
            c.focusCard(to);
            requestAnimationFrame(() => c.vp.reveal(c.places.current, to));
          }
          return;
        }
        if ((e.key === 'Enter' || e.key === ' ') && !mod) {
          e.preventDefault();
          open(card, null);
          return;
        }
      }

      if ((e.key === 'Delete' || e.key === 'Backspace') && !mod && !e.altKey) {
        if (sel.length) {
          e.preventDefault();
          o.deleteSteps(sel);
        } else if (store.selection.edge) {
          e.preventDefault();
          o.deleteEdge(store.selection.edge);
        }
        return;
      }
      if (mod && !e.altKey) {
        // Copy and cut leave selected page text alone.
        const pageText = (window.getSelection()?.toString() ?? '').length > 0;
        if (key === 'd') {
          e.preventDefault();
          if (sel.length) o.duplicate(sel);
        } else if (key === 'c' && !e.shiftKey && !pageText) {
          if (sel.length) {
            e.preventDefault();
            o.copy(sel);
          }
        } else if (key === 'x' && !e.shiftKey && !pageText) {
          if (sel.length) {
            e.preventDefault();
            o.cut(sel);
          }
        } else if (key === 'v' && !e.shiftKey) {
          e.preventDefault();
          const p = c.pointer.current;
          o.paste(p ? flow.screenToFlowPosition(p) : undefined);
        } else if (key === 'a') {
          e.preventDefault();
          store.select(store.doc.nodes.map((n) => n.id));
        } else if (key === 'z') {
          e.preventDefault();
          if (e.shiftKey) o.redo();
          else o.undo();
        } else if (key === 'y' && e.ctrlKey && !e.metaKey) {
          e.preventDefault();
          o.redo();
        }
        return;
      }
      if (e.altKey) return;
      if (key === 'v' || key === 'h') {
        c.setTool(key === 'h' ? 'pan' : 'select');
      } else if (e.code === 'Digit1' && e.shiftKey) {
        e.preventDefault();
        c.vp.fit();
      } else if (e.code === 'Digit0' && e.shiftKey) {
        e.preventDefault();
        c.vp.zoomTo(1);
      } else if (e.key === '+' || e.key === '=') {
        c.vp.zoomBy(1);
      } else if (e.key === '-' || e.key === '_') {
        c.vp.zoomBy(-1);
      } else if (e.key === 'Escape') {
        const inCanvas = !!target && !!c.areaRef.current?.contains(target);
        if (
          (store.selection.edge || sel.length > 1 || (inCanvas && sel.length)) &&
          !e.defaultPrevented
        ) {
          store.select([]);
          c.focusCanvas();
        }
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);
}
