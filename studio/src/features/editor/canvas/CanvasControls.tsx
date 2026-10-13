import { useReactFlow, useViewport } from '@xyflow/react';
import {
  ChevronsDownUp,
  ChevronsUpDown,
  Hand,
  Map as MapIcon,
  Minus,
  MousePointer2,
  Plus,
  Redo2,
  Scan,
  Undo2,
} from 'lucide-react';
import type { PointerEvent as ReactPointerEvent, ReactNode } from 'react';

import { IconButton } from '@/components/ui/button';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';
import { boundsOf, type PlaceMap } from '@/features/editor/model/layout';
import { ZOOM_MAX, ZOOM_MIN } from '@/features/editor/model/view';
import { keyTip } from './key-tip';

export type CanvasTool = 'select' | 'pan';

const FLOAT =
  'absolute bottom-3 z-canvas-ui flex h-10 items-center gap-0.5 rounded-lg border border-border bg-surface-raised px-1 shadow-2';

function Sep() {
  return <span aria-hidden="true" className="mx-1 h-4 w-px bg-border" />;
}

/** The zoom pill, bottom left: zoom out, the zoom level (100% on click), zoom in, Fit view, minimap. */
export function ZoomPill({
  onZoom,
  onReset,
  onFit,
  minimap,
  onMinimap,
}: {
  onZoom: (dir: 1 | -1) => void;
  onReset: () => void;
  onFit: () => void;
  minimap: boolean;
  onMinimap: (open: boolean) => void;
}) {
  const copy = useCopy();
  const t = copy.editor.toolbar;
  const { zoom } = useViewport();
  return (
    <div role="toolbar" aria-label={t.zoom} className={cn(FLOAT, 'left-3')}>
      <IconButton
        size="md"
        label={t.zoomOut}
        tooltip={keyTip(t.zoomOut, '−')}
        unavailableReason={zoom <= ZOOM_MIN + 0.001 ? t.zoomOut : undefined}
        onClick={() => onZoom(-1)}
      >
        <Minus aria-hidden="true" />
      </IconButton>
      <Tooltip content={keyTip(t.zoomReset, '⇧ 0')}>
        <button
          type="button"
          aria-label={t.zoomReset}
          onClick={onReset}
          className="h-control-sm min-w-12 rounded-md px-1 text-center text-sm text-fg-secondary tabular-nums hover:bg-surface-hover hover:text-fg"
        >
          {t.percent(zoom)}
        </button>
      </Tooltip>
      <IconButton
        size="md"
        label={t.zoomIn}
        tooltip={keyTip(t.zoomIn, '+')}
        unavailableReason={zoom >= ZOOM_MAX - 0.001 ? t.zoomIn : undefined}
        onClick={() => onZoom(1)}
      >
        <Plus aria-hidden="true" />
      </IconButton>
      <IconButton size="md" label={t.fit} tooltip={keyTip(t.fit, '⇧ 1')} onClick={onFit}>
        <Scan aria-hidden="true" />
      </IconButton>
      <Sep />
      <IconButton
        size="md"
        label={minimap ? t.hideMinimap : t.showMinimap}
        aria-pressed={minimap}
        className="aria-pressed:bg-surface-hover aria-pressed:text-fg"
        onClick={() => onMinimap(!minimap)}
      >
        <MapIcon aria-hidden="true" />
      </IconButton>
    </div>
  );
}

/** The canvas tools, bottom centre: Select and Pan, details on all steps, Undo and Redo. */
export function ToolBar({
  tool,
  onTool,
  anyExpanded,
  onToggleAll,
  canUndo,
  canRedo,
  onUndo,
  onRedo,
  narrow,
}: {
  tool: CanvasTool;
  onTool: (t: CanvasTool) => void;
  anyExpanded: boolean;
  onToggleAll: () => void;
  canUndo: boolean;
  canRedo: boolean;
  onUndo: () => void;
  onRedo: () => void;
  /** The canvas is narrow: the bar moves right so it never meets the zoom pill. */
  narrow: boolean;
}) {
  const copy = useCopy();
  const t = copy.editor.toolbar;
  const mode = (value: CanvasTool, label: string, key: string, icon: ReactNode) => (
    <IconButton
      size="md"
      label={label}
      tooltip={keyTip(label, key)}
      aria-pressed={tool === value}
      className="aria-pressed:bg-surface-hover aria-pressed:text-fg"
      onClick={() => onTool(value)}
    >
      {icon}
    </IconButton>
  );
  return (
    <div
      role="toolbar"
      aria-label={t.tools}
      className={cn(FLOAT, narrow ? 'right-3' : 'left-1/2 -translate-x-1/2')}
    >
      {mode('select', t.select, 'V', <MousePointer2 aria-hidden="true" />)}
      {mode('pan', t.pan, 'H', <Hand aria-hidden="true" />)}
      <Sep />
      <IconButton size="md" label={anyExpanded ? t.hideAll : t.showAll} onClick={onToggleAll}>
        {anyExpanded ? (
          <ChevronsDownUp aria-hidden="true" />
        ) : (
          <ChevronsUpDown aria-hidden="true" />
        )}
      </IconButton>
      <Sep />
      <IconButton
        size="md"
        label={t.undo}
        tooltip={keyTip(canUndo ? t.undo : t.nothingToUndo, '⌘ Z')}
        unavailableReason={canUndo ? undefined : t.nothingToUndo}
        onClick={onUndo}
      >
        <Undo2 aria-hidden="true" />
      </IconButton>
      <IconButton
        size="md"
        label={t.redo}
        tooltip={keyTip(canRedo ? t.redo : t.nothingToRedo, '⇧ ⌘ Z')}
        unavailableReason={canRedo ? undefined : t.nothingToRedo}
        onClick={onRedo}
      >
        <Redo2 aria-hidden="true" />
      </IconButton>
    </div>
  );
}

const MINI_W = 200;
const MINI_H = 128;

/** The minimap, bottom right: every step, the visible area, and click or drag to move there. */
export function Minimap({
  places,
  selected,
  area,
}: {
  places: PlaceMap;
  selected: ReadonlySet<string>;
  area: { w: number; h: number };
}) {
  const copy = useCopy();
  const rf = useReactFlow();
  const v = useViewport();
  const b = boundsOf(places, (id) => places[id]?.h ?? 0);
  const view = {
    x0: -v.x / v.zoom,
    y0: -v.y / v.zoom,
    x1: (area.w - v.x) / v.zoom,
    y1: (area.h - v.y) / v.zoom,
  };
  const all = {
    x0: Math.min(b.x0, view.x0),
    y0: Math.min(b.y0, view.y0),
    x1: Math.max(b.x0 + b.w, view.x1),
    y1: Math.max(b.y0 + b.h, view.y1),
  };
  const pad = 24;
  const sc = Math.min(
    (MINI_W - 12) / (all.x1 - all.x0 + pad * 2),
    (MINI_H - 12) / (all.y1 - all.y0 + pad * 2),
  );
  const ox = (MINI_W - (all.x1 - all.x0) * sc) / 2 - all.x0 * sc;
  const oy = (MINI_H - (all.y1 - all.y0) * sc) / 2 - all.y0 * sc;

  const go = (e: ReactPointerEvent<HTMLDivElement> | PointerEvent, r: DOMRect) => {
    const x = (e.clientX - r.left - ox) / sc;
    const y = (e.clientY - r.top - oy) / sc;
    const z = rf.getViewport().zoom;
    void rf.setViewport({ x: area.w / 2 - x * z, y: area.h / 2 - y * z, zoom: z });
  };

  return (
    <div
      aria-hidden="true"
      title={copy.editor.toolbar.minimap}
      style={{ width: MINI_W, height: MINI_H }}
      className="absolute right-3 bottom-3 z-canvas-ui cursor-pointer overflow-hidden rounded-lg border border-border-subtle bg-surface-raised shadow-2"
      onPointerDown={(e) => {
        e.preventDefault();
        const r = e.currentTarget.getBoundingClientRect();
        go(e, r);
        const move = (ev: PointerEvent) => go(ev, r);
        const up = () => {
          window.removeEventListener('pointermove', move);
          window.removeEventListener('pointerup', up);
        };
        window.addEventListener('pointermove', move);
        window.addEventListener('pointerup', up);
      }}
    >
      <svg width={MINI_W} height={MINI_H} className="block">
        {Object.entries(places).map(([id, p]) => (
          <rect
            key={id}
            x={(ox + p.x * sc).toFixed(1)}
            y={(oy + p.y * sc).toFixed(1)}
            width={(p.w * sc).toFixed(1)}
            height={(p.h * sc).toFixed(1)}
            rx={2}
            style={{
              fill: selected.has(id) ? 'var(--accent)' : 'var(--fg-muted)',
              opacity: selected.has(id) ? 0.9 : 0.55,
            }}
          />
        ))}
        <rect
          x={(ox + view.x0 * sc).toFixed(1)}
          y={(oy + view.y0 * sc).toFixed(1)}
          width={((view.x1 - view.x0) * sc).toFixed(1)}
          height={((view.y1 - view.y0) * sc).toFixed(1)}
          rx={4}
          style={{
            fill: 'var(--surface-selected)',
            fillOpacity: 0.6,
            stroke: 'var(--accent)',
            strokeWidth: 1.25,
          }}
        />
      </svg>
    </div>
  );
}
