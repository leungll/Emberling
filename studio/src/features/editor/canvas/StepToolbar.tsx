import { useViewport } from '@xyflow/react';
import { Copy, Pencil, Trash2 } from 'lucide-react';
import { useLayoutEffect, useRef, useState } from 'react';

import { IconButton } from '@/components/ui/button';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';
import { useEditor } from '@/features/editor/editor-context';
import type { PlaceMap } from '@/features/editor/model/layout';
import { keyTip } from './key-tip';

const FLOAT_H = 64;

/**
 * The floating step toolbar: above the selection when there is room, below it when a card
 * or the top edge is in the way, and inside the top of the selected card's header when
 * neither side is free. It never covers another card.
 */
export function StepToolbar({
  ids,
  places,
  area,
  hidden,
}: {
  ids: readonly string[];
  places: PlaceMap;
  area: { w: number; h: number };
  hidden: boolean;
}) {
  const copy = useCopy();
  const t = copy.editor.stepBar;
  const { ops, openSettings } = useEditor();
  const v = useViewport();
  const ref = useRef<HTMLDivElement>(null);
  const [box, setBox] = useState({ w: 120, h: 40 });

  const single = ids.length === 1;
  useLayoutEffect(() => {
    const el = ref.current;
    if (el && (el.offsetWidth !== box.w || el.offsetHeight !== box.h))
      setBox({ w: el.offsetWidth, h: el.offsetHeight });
  }, [single, hidden, box.w, box.h]);

  const shown = ids.filter((id) => places[id]);
  if (hidden || !shown.length) return null;
  let x0 = Infinity;
  let x1 = -Infinity;
  let y0 = Infinity;
  let y1 = -Infinity;
  for (const id of shown) {
    const p = places[id];
    if (!p) continue;
    x0 = Math.min(x0, p.x);
    x1 = Math.max(x1, p.x + p.w);
    y0 = Math.min(y0, p.y);
    y1 = Math.max(y1, p.y + p.h);
  }
  const cx = (x0 + x1) / 2;
  const bw = box.w / v.zoom;
  const bh = (box.h + 8) / v.zoom;
  const hits = (top: number, bottom: number) =>
    Object.entries(places).some(
      ([id, q]) =>
        !shown.includes(id) &&
        q.x < cx + bw / 2 &&
        q.x + q.w > cx - bw / 2 &&
        q.y < bottom &&
        q.y + q.h > top,
    );
  const x = v.x + cx * v.zoom;
  const aboveY = v.y + y0 * v.zoom - 8;
  const belowY = v.y + y1 * v.zoom + 8;
  let side: 'above' | 'below' | 'inside';
  if (!hits(y0 - bh, y0) && aboveY - box.h > 4) side = 'above';
  else if (!hits(y1, y1 + bh) && belowY + box.h < area.h - FLOAT_H) side = 'below';
  else side = 'inside';
  const y = side === 'above' ? aboveY : side === 'below' ? belowY : v.y + y0 * v.zoom + 4;
  if (x < -40 || x > area.w + 40 || y < 0 || y > area.h) return null;
  const one = shown.length === 1 ? shown[0] : undefined;

  return (
    <div
      ref={ref}
      role="toolbar"
      aria-label={t.label}
      data-side={side}
      style={{ left: Math.round(x), top: Math.round(y) }}
      className={cn(
        'absolute z-canvas-ui flex items-center gap-0.5 rounded-lg border border-border bg-surface-raised p-1 shadow-2',
        side === 'above' ? '-translate-x-1/2 -translate-y-full' : '-translate-x-1/2',
      )}
    >
      {one ? (
        <IconButton
          label={t.settings}
          tooltip={keyTip(t.settings, '↵')}
          onClick={() => openSettings(one, null)}
        >
          <Pencil aria-hidden="true" />
        </IconButton>
      ) : null}
      <IconButton
        label={t.duplicate}
        tooltip={keyTip(t.duplicate, '⌘ D')}
        onClick={() => ops.duplicate(shown)}
      >
        <Copy aria-hidden="true" />
      </IconButton>
      <span aria-hidden="true" className="mx-0.5 h-4 w-px bg-border" />
      <IconButton
        label={t.delete}
        tooltip={keyTip(t.delete, '⌫')}
        className="hover:bg-st-failed-bg hover:text-danger-fg"
        onClick={() => ops.deleteSteps(shown)}
      >
        <Trash2 aria-hidden="true" />
      </IconButton>
    </div>
  );
}
