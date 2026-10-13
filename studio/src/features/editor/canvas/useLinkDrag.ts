import type { OnConnectEnd, OnConnectStart } from '@xyflow/react';
import { useCallback, useEffect, useRef, useState, type RefObject } from 'react';

import { copy } from '@/lib/copy';
import { showToast } from '@/lib/toast';
import type { EditOps, QuickAddRequest } from '@/features/editor/editor-context';
import { useEditorStore } from '@/features/editor/editor-store';
import { cardTarget, linkProblem, type CardTarget } from '@/features/editor/model/connect';
import { portLabel, portOf, type Catalog } from '@/features/editor/model/steps';
import type { Dragging } from './canvas-view';

const SNAP = 24;

export interface LinkHint {
  x: number;
  y: number;
  text: string;
  tone: '' | 'busy' | 'bad';
}

function pointOf(e: MouseEvent | TouchEvent): { x: number; y: number } {
  if ('changedTouches' in e) {
    const t = e.changedTouches[0];
    return { x: t?.clientX ?? 0, y: t?.clientY ?? 0 };
  }
  return { x: e.clientX, y: e.clientY };
}

/**
 * Dragging a link from an output. Inputs show whether they would take it, a hint by the
 * pointer says what releasing does, and releasing connects, offers Replace for a busy input,
 * says why a link can't be made, or opens quick add on empty canvas.
 */
export function useLinkDrag({
  areaRef,
  catalog,
  ops,
  toFlow,
  quickAdd,
}: {
  areaRef: RefObject<HTMLDivElement | null>;
  catalog: Catalog;
  ops: EditOps;
  toFlow: (p: { x: number; y: number }) => { x: number; y: number };
  quickAdd: (req: QuickAddRequest) => void;
}) {
  const [dragging, setDragging] = useState<Dragging | null>(null);
  const [hint, setHint] = useState<LinkHint | null>(null);
  const [target, setTarget] = useState<CardTarget | null>(null);
  const drag = useRef<Dragging | null>(null);

  const find = useCallback(
    (d: Dragging, cx: number, cy: number): { target: CardTarget | null; empty: boolean } => {
      const graph = useEditorStore.getState().doc;
      const area = areaRef.current;
      if (!area) return { target: null, empty: false };
      let best: Element | null = null;
      let bestD = SNAP;
      for (const el of Array.from(area.querySelectorAll('.react-flow__handle.target'))) {
        const r = el.getBoundingClientRect();
        const dist = Math.hypot(cx - (r.left + r.width / 2), cy - (r.top + r.height / 2));
        if (dist < bestD) {
          bestD = dist;
          best = el;
        }
      }
      const nid = best?.closest('[data-id]')?.getAttribute('data-id');
      const handle = best?.getAttribute('data-handleid');
      if (nid && handle) {
        return {
          target: { nid, handle, problem: linkProblem(graph, catalog, d.sid, d.sh, nid, handle) },
          empty: false,
        };
      }
      const hit = document.elementFromPoint(cx, cy);
      const card = hit?.closest('[data-step-card]')?.getAttribute('data-node');
      if (card) return { target: cardTarget(graph, catalog, d.sid, d.sh, card), empty: false };
      const empty =
        !!hit &&
        area.contains(hit) &&
        !!hit.closest('.react-flow__pane, .react-flow__edges, .react-flow__edge');
      return { target: null, empty };
    },
    [areaRef, catalog],
  );

  const describe = useCallback(
    (
      found: { target: CardTarget | null; empty: boolean },
      cx: number,
      cy: number,
    ): LinkHint | null => {
      const t = copy.editor.links;
      const tg = found.target;
      if (tg) {
        if (!tg.problem && tg.handle) {
          const graph = useEditorStore.getState().doc;
          const n = graph.nodes.find((x) => x.id === tg.nid);
          const p = portOf(catalog, n, 'in', tg.handle);
          return {
            x: cx,
            y: cy,
            text: t.connectTo(p ? portLabel(p) : tg.handle, n?.name ?? ''),
            tone: '',
          };
        }
        if (tg.problem)
          return {
            x: cx,
            y: cy,
            text: tg.problem.text,
            tone: tg.problem.code === 'busy' ? 'busy' : 'bad',
          };
      }
      if (found.empty) return { x: cx, y: cy, text: t.releaseToAdd, tone: '' };
      return null;
    },
    [catalog],
  );

  useEffect(() => {
    if (!dragging) return;
    const move = (e: PointerEvent) => {
      const found = find(dragging, e.clientX, e.clientY);
      setTarget(found.target);
      setHint(describe(found, e.clientX, e.clientY));
    };
    window.addEventListener('pointermove', move);
    return () => window.removeEventListener('pointermove', move);
  }, [dragging, find, describe]);

  const clear = useCallback(() => {
    drag.current = null;
    setDragging(null);
    setHint(null);
    setTarget(null);
  }, []);

  const onConnectStart: OnConnectStart = useCallback(
    (_e, params) => {
      if (params.handleType !== 'source' || !params.nodeId || !params.handleId) return;
      const graph = useEditorStore.getState().doc;
      const n = graph.nodes.find((x) => x.id === params.nodeId);
      const port = portOf(catalog, n, 'out', params.handleId);
      if (!port) return;
      const d: Dragging = { sid: params.nodeId, sh: params.handleId, type: port.dataType };
      drag.current = d;
      setDragging(d);
    },
    [catalog],
  );

  const onConnectEnd: OnConnectEnd = useCallback(
    (e) => {
      const d = drag.current;
      clear();
      if (!d) return;
      const { x, y } = pointOf(e);
      const found = find(d, x, y);
      const tg = found.target;
      const t = copy.editor;
      if (tg?.handle && !tg.problem) {
        ops.connect(d.sid, d.sh, tg.nid, tg.handle);
        return;
      }
      if (tg?.handle && tg.problem?.code === 'busy') {
        const th = tg.handle;
        const prev = tg.problem.edge;
        showToast(tg.problem.text, {
          action: {
            label: t.toasts.replace,
            onAction: () => {
              const graph = useEditorStore.getState().doc;
              const from = graph.nodes.find((n) => n.id === prev?.source)?.name ?? '';
              ops.connect(d.sid, d.sh, tg.nid, th);
              showToast(t.toasts.replaced(from), {
                action: { label: t.toasts.undo, onAction: ops.undo },
                done: true,
              });
            },
          },
        });
        return;
      }
      if (tg?.problem) {
        showToast(tg.problem.text);
        return;
      }
      if (found.empty) {
        const at = toFlow({ x, y });
        quickAdd({ x, y, at, link: { kind: 'from', sid: d.sid, handle: d.sh, type: d.type, at } });
      }
    },
    [clear, find, ops, quickAdd, toFlow],
  );

  return { dragging, hint, target, onConnectStart, onConnectEnd, cancel: clear };
}
