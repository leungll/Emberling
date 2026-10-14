import type { Definition } from '@/api/types';

/**
 * The read-only run graph's layout, the same rule as the Editor's: columns by the longest
 * path from a start step, steps in a column stacked in their saved order and centred on
 * each other, links left to right. Laid out once at full card size; zooming changes only
 * what a card shows, never where its top-left corner sits.
 */

export const CARD_W = 248;
export const CARD_H = 92;
export const GAP_X = 48;
export const GAP_Y = 24;
export const ZOOM_MIN = 0.25;
export const ZOOM_MAX = 1.5;
/** At 70% and above cards are drawn in full; below it each card is one header row. */
export const ZOOM_FULL = 0.7;
/** Below 50% the one-row card drops its icon tile. */
export const ZOOM_COMPACT = 0.5;
/** The space kept around the graph, and under it for the zoom controls. */
export const PAD = 24;
export const BAR = 56;

export type ZoomLevel = 'full' | 'compact' | 'far';

export function zoomLevel(z: number): ZoomLevel {
  return z >= ZOOM_FULL - 0.001 ? 'full' : z >= ZOOM_COMPACT - 0.001 ? 'compact' : 'far';
}

/** A one-row card: 56 stage px, never under 20 px on screen. */
export function lodCardHeight(z: number): number {
  return Math.max(56, Math.ceil(20 / z));
}

export interface CardPos {
  col: number;
  row: number;
  x: number;
  y: number;
}

export interface GraphLayout {
  pos: ReadonlyMap<string, CardPos>;
  width: number;
  height: number;
}

export function graphLayout(def: Pick<Definition, 'nodes' | 'edges'>): GraphLayout {
  const depth = new Map(def.nodes.map((n) => [n.id, 0]));
  // Longest path: one relaxation pass per step settles every depth.
  for (const _ of def.nodes) {
    for (const e of def.edges) {
      const s = depth.get(e.source);
      const t = depth.get(e.target);
      if (s !== undefined && t !== undefined && t < s + 1) depth.set(e.target, s + 1);
    }
  }
  const cols: (typeof def.nodes)[] = [];
  for (const n of def.nodes) {
    const d = depth.get(n.id) ?? 0;
    (cols[d] ??= []).push(n);
  }
  const dense = cols.filter(Boolean);
  const heights = dense.map((c) => c.length * CARD_H + (c.length - 1) * GAP_Y);
  const maxH = Math.max(0, ...heights);
  const pos = new Map<string, CardPos>();
  dense.forEach((c, ci) => {
    c.sort((a, b) => a.position.y - b.position.y || a.position.x - b.position.x);
    let y = (maxH - (heights[ci] ?? 0)) / 2;
    c.forEach((n, ri) => {
      pos.set(n.id, { col: ci, row: ri, x: ci * (CARD_W + GAP_X), y: Math.round(y) });
      y += CARD_H + GAP_Y;
    });
  });
  return {
    pos,
    width: dense.length * CARD_W + Math.max(0, dense.length - 1) * GAP_X,
    height: maxH,
  };
}

export interface View {
  z: number;
  tx: number;
  ty: number;
}

/**
 * Opens fitted: everything fits the pane and is centred when that keeps full cards (70%
 * or more, never above 100%); otherwise it stays at the full-card zoom and starts at the
 * leftmost, topmost steps.
 */
export function fitView(layout: GraphLayout, vw: number, vh: number): View {
  const cw = layout.width;
  const ch = layout.height + GAP_Y * 2;
  const aw = vw - PAD * 2;
  const ah = vh - PAD - BAR;
  const z = Math.max(ZOOM_FULL, Math.min(1, aw / Math.max(1, cw), ah / Math.max(1, ch)));
  return {
    z,
    tx: Math.round(cw * z <= aw ? (vw - cw * z) / 2 : PAD),
    ty: Math.round(ch * z <= ah ? PAD + (ah - ch * z) / 2 : PAD),
  };
}

/** Zooms about a point of the viewport, keeping that point still. */
export function zoomAt(view: View, z: number, px: number, py: number): View {
  const next = Math.max(ZOOM_MIN, Math.min(ZOOM_MAX, z));
  return {
    z: next,
    tx: px - ((px - view.tx) * next) / view.z,
    ty: py - ((py - view.ty) * next) / view.z,
  };
}

/** Pans the least distance that shows the whole card, or returns null when it shows. */
export function revealCard(
  view: View,
  p: CardPos,
  cardH: number,
  vw: number,
  vh: number,
): View | null {
  const x0 = view.tx + p.x * view.z;
  const x1 = x0 + CARD_W * view.z;
  const y0 = view.ty + (p.y + GAP_Y) * view.z;
  const y1 = y0 + cardH * view.z;
  const dx = x0 < PAD ? PAD - x0 : x1 > vw - PAD ? vw - PAD - x1 : 0;
  const dy = y0 < PAD ? PAD - y0 : y1 > vh - BAR ? vh - BAR - y1 : 0;
  if (!dx && !dy) return null;
  return { ...view, tx: view.tx + dx, ty: view.ty + dy };
}

/** The neighbour an arrow key moves to: across columns, or up and down within one. */
export function graphMove(
  layout: GraphLayout,
  from: string,
  key: 'ArrowLeft' | 'ArrowRight' | 'ArrowUp' | 'ArrowDown',
): string | null {
  const p = layout.pos.get(from);
  if (!p) return null;
  let best: string | null = null;
  let bestD = Infinity;
  for (const [id, q] of layout.pos) {
    if (id === from) continue;
    const dx = q.col - p.col;
    const dy = (q.y - p.y) / 100;
    let ok: boolean;
    let d: number;
    if (key === 'ArrowRight') {
      ok = dx > 0;
      d = dx * 10 + Math.abs(dy);
    } else if (key === 'ArrowLeft') {
      ok = dx < 0;
      d = -dx * 10 + Math.abs(dy);
    } else if (key === 'ArrowDown') {
      ok = dx === 0 && dy > 0;
      d = dy;
    } else {
      ok = dx === 0 && dy < 0;
      d = -dy;
    }
    if (ok && d < bestD) {
      bestD = d;
      best = id;
    }
  }
  return best;
}

/**
 * A link from the right edge of one card to the left edge of another. One that skips a
 * column runs in a lane above or below the steps in between when they are in the way.
 */
export function edgePath(
  layout: GraphLayout,
  source: string,
  target: string,
  heightOf: (id: string) => number,
  z: number,
): string | null {
  const a = layout.pos.get(source);
  const b = layout.pos.get(target);
  if (!a || !b) return null;
  const ha = heightOf(source);
  const hb = heightOf(target);
  const x1 = a.x + CARD_W;
  const y1 = a.y + ha / 2;
  const x2 = b.x;
  const y2 = b.y + hb / 2;
  let lane: number | null = null;
  if (b.col - a.col > 1) {
    const m = Math.max(8, 6 / z);
    const off = Math.max(GAP_Y / 2, 10 / z);
    const lo = Math.min(y1, y2) - m;
    const hi = Math.max(y1, y2) + m;
    let top = Infinity;
    let bottom = -Infinity;
    let blocked = false;
    for (const [id, q] of layout.pos) {
      if (id === source || id === target || q.col <= a.col || q.col >= b.col) continue;
      const h = heightOf(id);
      top = Math.min(top, q.y);
      bottom = Math.max(bottom, q.y + h);
      if (q.y < hi && q.y + h > lo) blocked = true;
    }
    if (blocked) {
      const above = top - off;
      const below = bottom + off;
      const mid = (y1 + y2) / 2;
      lane = Math.abs(above - mid) <= Math.abs(below - mid) ? above : below;
    }
  }
  if (lane !== null) {
    const xa = x1 + GAP_X;
    const xb = x2 - GAP_X;
    const h = GAP_X / 2;
    return `M${x1} ${y1} C${x1 + h} ${y1} ${x1 + h} ${lane} ${xa} ${lane} L${xb} ${lane} C${xb + h} ${lane} ${xb + h} ${y2} ${x2} ${y2}`;
  }
  const mx = (x1 + x2) / 2;
  return `M${x1} ${y1} C${mx} ${y1} ${mx} ${y2} ${x2} ${y2}`;
}
