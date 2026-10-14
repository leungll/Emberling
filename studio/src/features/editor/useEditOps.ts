import { useEffect, useMemo, useRef, type RefObject } from 'react';

import { copy } from '@/lib/copy';
import { readString, writeString } from '@/lib/storage';
import { showToast } from '@/lib/toast';
import type { EditOps, Geometry } from './editor-context';
import { ids, useEditorStore } from './editor-store';
import { CLIP_KEY, makeClip, parseClip, pasteClip, pasteOrigin } from './model/clipboard';
import { linkPlan } from './model/connect';
import * as op from './model/edit-ops';
import { CARD_H, CARD_W, freeSpot } from './model/layout';
import type { Registry } from './useEditorData';

const t = copy.editor;

/**
 * The Editor's edits as one object. Each records one undo step, selects what it created and
 * says what it did; a destructive edit offers Undo in its toast.
 */
export function useEditOps(
  workflowId: string | null,
  registry: Registry,
  geometry: RefObject<Geometry>,
  onRemoved: (ids: readonly string[]) => void,
): EditOps {
  const pasteRound = useRef<{ stamp: number; round: number }>({ stamp: 0, round: 0 });
  const reg = useRef(registry);
  const removed = useRef(onRemoved);
  useEffect(() => {
    reg.current = registry;
    removed.current = onRemoved;
  });

  return useMemo<EditOps>(() => {
    const store = useEditorStore.getState;
    const nameOf = (id: string) => store().doc.nodes.find((n) => n.id === id)?.name ?? '';
    const undoAction = { label: t.toasts.undo, onAction: () => ops.undo() };
    const afterAdd = (id: string) => {
      requestAnimationFrame(() => {
        geometry.current.reveal(id);
        geometry.current.focusCard(id);
      });
    };

    const ops: EditOps = {
      addStep: (type, at) => {
        const meta = reg.current.catalog.get(type);
        if (!meta) return;
        const g = geometry.current;
        const sel = store().selection.ids[0];
        const anchor = sel ? g.places[sel] : undefined;
        const pt =
          at ??
          (anchor
            ? { x: anchor.x + CARD_W / 2, y: anchor.y + anchor.h + 48 + CARD_H / 2 }
            : g.center());
        const spot = freeSpot(pt.x - CARD_W / 2, pt.y - CARD_H / 2, g.places);
        const id = ids.node(type);
        store().apply(
          (doc) => ({
            ...doc,
            nodes: [...doc.nodes, op.newStep(meta, doc.nodes, id, spot.x, spot.y)],
          }),
          [id],
        );
        afterAdd(id);
        showToast(t.toasts.added(meta.displayName));
      },
      addLinked: (type, link) => {
        const r = reg.current;
        const meta = r.catalog.get(type);
        const s = store();
        if (!meta) return;
        const plan = linkPlan(meta, link, s.doc, r.catalog);
        if (!plan) return;
        const g = geometry.current;
        let created = '';
        const inserted =
          link.kind === 'insert' ? s.doc.edges.find((x) => x.id === link.edgeId) : undefined;
        const names: [string, string] =
          link.kind === 'insert'
            ? [nameOf(inserted?.source ?? ''), nameOf(inserted?.target ?? '')]
            : [nameOf(link.sid), ''];
        s.apply((doc) => {
          const out = op.addLinked(
            doc,
            meta,
            plan,
            link,
            { places: g.places, portY: g.portY },
            ids,
          );
          if (!out) return doc;
          created = out.created;
          return { ...doc, ...out.graph };
        });
        if (!created) return;
        store().select([created]);
        afterAdd(created);
        const name = nameOf(created);
        showToast(
          link.kind === 'from'
            ? t.toasts.addedAfter(name, names[0])
            : t.toasts.inserted(name, names[0], names[1]),
          { action: undoAction, done: true },
        );
      },
      connect: (s, sh, tg, th) => {
        store().apply((doc) => ({ ...doc, ...op.connect(doc, ids, s, sh, tg, th) }));
      },
      deleteSteps: (which, verb = 'delete') => {
        const doc = store().doc;
        const list = which.filter((id) => doc.nodes.some((n) => n.id === id));
        if (!list.length) return;
        const first = nameOf(list[0] ?? '');
        store().apply((d) => ({ ...d, ...op.deleteSteps(d, list) }), []);
        removed.current(list);
        geometry.current.focusCanvas();
        const one = verb === 'cut' ? t.toasts.cut(first) : t.toasts.deleted(first);
        const many =
          verb === 'cut' ? t.toasts.cutMany(list.length) : t.toasts.deletedMany(list.length);
        showToast(list.length === 1 ? one : many, { action: undoAction });
      },
      deleteEdge: (id) => {
        const e = store().doc.edges.find((x) => x.id === id);
        if (!e) return;
        const a = nameOf(e.source);
        const b = nameOf(e.target);
        store().apply((d) => ({ ...d, ...op.deleteEdge(d, id) }), []);
        geometry.current.focusCanvas();
        showToast(t.toasts.deletedLink(a, b), { action: undoAction });
      },
      duplicate: (which) => {
        let created: string[] = [];
        store().apply((d) => {
          const out = op.duplicate(d, which, geometry.current.places, ids);
          created = out.created;
          return { ...d, ...out.graph };
        });
        store().select(created);
        if (created.length === 1 && created[0]) afterAdd(created[0]);
      },
      copy: (which, quiet = false) => {
        const s = store();
        const clip = makeClip(
          which,
          s.doc.nodes,
          s.doc.edges,
          geometry.current.places,
          workflowId ?? '',
          Date.now(),
        );
        if (!clip) return false;
        writeString('local', CLIP_KEY, JSON.stringify(clip));
        if (!quiet) {
          showToast(
            clip.nodes.length === 1
              ? t.toasts.copied(clip.nodes[0]?.name ?? '')
              : t.toasts.copiedMany(clip.nodes.length),
            {
              done: true,
            },
          );
        }
        return true;
      },
      cut: (which) => {
        if (ops.copy(which, true)) ops.deleteSteps(which, 'cut');
      },
      paste: (at) => {
        const clip = parseClip(readString('local', CLIP_KEY));
        if (!clip) {
          showToast(t.toasts.nothingToPaste);
          return;
        }
        const r = reg.current;
        if (!clip.nodes.some((c) => r.catalog.has(c.type))) {
          showToast(t.toasts.pasteUnknown);
          return;
        }
        const pr = pasteRound.current;
        if (pr.stamp !== clip.stamp) {
          pr.stamp = clip.stamp;
          pr.round = 0;
        }
        pr.round += 1;
        const origin = at ?? pasteOrigin(clip, pr.round);
        let created: string[] = [];
        let skipped = 0;
        store().apply((d) => {
          const out = pasteClip(clip, origin, d.nodes, r.catalog, ids.node, ids.edge);
          created = out.nodes.map((n) => n.id);
          skipped = out.skipped;
          return { ...d, nodes: [...d.nodes, ...out.nodes], edges: [...d.edges, ...out.edges] };
        });
        store().select(created);
        if (created[0]) {
          const first = created[0];
          requestAnimationFrame(() => {
            geometry.current.reveal(first);
            if (created.length === 1) geometry.current.focusCard(first);
          });
        }
        if (skipped) showToast(t.toasts.pastedSome(created.length, skipped));
      },
      rename: (id, name) => {
        const trimmed = name.trim();
        if (!trimmed || trimmed === nameOf(id)) return;
        store().begin();
        store().renameStep(id, trimmed);
      },
      undo: () => {
        if (store().undo()) {
          const s = store();
          const live = new Set(s.doc.nodes.map((n) => n.id));
          s.select(s.selection.ids.filter((id) => live.has(id)));
        }
      },
      redo: () => {
        if (store().redo()) {
          const s = store();
          const live = new Set(s.doc.nodes.map((n) => n.id));
          s.select(s.selection.ids.filter((id) => live.has(id)));
        }
      },
    };
    return ops;
  }, [workflowId, geometry]);
}
