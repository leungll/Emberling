import type { Edge, Node } from '@/api/types';

/** Canvas geometry in canvas units (px at 100% zoom). */
export const CARD_W = 280;
export const CARD_H = 88;
export const GAP_X = 56;
export const GAP_Y = 32;
export const GRID = 8;

export const snap = (v: number): number => Math.round(v / GRID) * GRID;

export interface Place {
  x: number;
  y: number;
  /** Width on the canvas; one-row cards may grow into the column gap. */
  w: number;
  /** Height as drawn at the current zoom level. */
  h: number;
}

export type PlaceMap = Record<string, Place>;

/**
 * Columns by the longest path from a start step; the steps of a column keep their vertical
 * order and centre on the tallest column. Heights come from `heightOf` (measured full cards).
 */
export function layered(
  nodes: readonly Node[],
  edges: readonly Edge[],
  heightOf: (id: string) => number = () => CARD_H,
): Record<string, { x: number; y: number }> {
  const ids = new Set(nodes.map((n) => n.id));
  const depth: Record<string, number> = {};
  for (const n of nodes) depth[n.id] = 0;
  // Bounded by the node count, so a cycle in an imported workflow cannot loop forever.
  let changed = true;
  for (let pass = 0; changed && pass < nodes.length; pass += 1) {
    changed = false;
    for (const e of edges) {
      if (!ids.has(e.source) || !ids.has(e.target)) continue;
      const next = (depth[e.source] ?? 0) + 1;
      if ((depth[e.target] ?? 0) < next) {
        depth[e.target] = next;
        changed = true;
      }
    }
  }
  const cols: Node[][] = [];
  for (const n of nodes) (cols[depth[n.id] ?? 0] ??= []).push(n);
  const dense = cols.filter(Boolean);
  const heights = dense.map(
    (c) => c.reduce((a, n) => a + heightOf(n.id), 0) + (c.length - 1) * GAP_Y,
  );
  const maxH = Math.max(CARD_H, ...heights);
  const out: Record<string, { x: number; y: number }> = {};
  dense.forEach((c, ci) => {
    c.sort((a, b) => a.position.y - b.position.y || a.position.x - b.position.x);
    let y = (maxH - (heights[ci] ?? 0)) / 2;
    for (const n of c) {
      out[n.id] = { x: ci * (CARD_W + GAP_X), y: snap(y) };
      y += heightOf(n.id) + GAP_Y;
    }
  });
  return out;
}

/** Whether any two stored positions overlap once cards are drawn at their full heights. */
export function needsAutoLayout(
  nodes: readonly Node[],
  heightOf: (id: string) => number = () => CARD_H,
): boolean {
  for (const [i, na] of nodes.entries()) {
    for (const nb of nodes.slice(i + 1)) {
      const a = na.position;
      const b = nb.position;
      const ha = heightOf(na.id);
      const hb = heightOf(nb.id);
      if (a.x < b.x + CARD_W && b.x < a.x + CARD_W && a.y < b.y + hb && b.y < a.y + ha) return true;
    }
  }
  return false;
}

/**
 * A link that skips a column would pass behind the steps in between. When a step there
 * overlaps the link's height, the link runs in a lane just above or below those steps,
 * whichever is closer. Clearances hold on screen, so they grow as the zoom shrinks.
 */
export function laneAround(
  edge: Pick<Edge, 'source' | 'target'>,
  x1: number,
  y1: number,
  x2: number,
  y2: number,
  places: PlaceMap,
  zoom: number,
): number | null {
  if (x2 - x1 < 2 * GAP_X + CARD_W / 2) return null;
  const m = Math.max(8, 6 / zoom);
  const off = Math.max(GAP_Y / 2, 10 / zoom);
  const lo = Math.min(y1, y2) - m;
  const hi = Math.max(y1, y2) + m;
  let top = Infinity;
  let bottom = -Infinity;
  let blocked = false;
  for (const [id, p] of Object.entries(places)) {
    if (id === edge.source || id === edge.target) continue;
    if (p.x + p.w <= x1 + 1 || p.x >= x2 - 1) continue;
    top = Math.min(top, p.y);
    bottom = Math.max(bottom, p.y + p.h);
    if (p.y < hi && p.y + p.h > lo) blocked = true;
  }
  if (!blocked) return null;
  const above = top - off;
  const below = bottom + off;
  const mid = (y1 + y2) / 2;
  return Math.abs(above - mid) <= Math.abs(below - mid) ? above : below;
}

/** Links leave and enter sideways; a backwards link bends out further so it stays readable. */
export function curve(x1: number, y1: number, x2: number, y2: number): string {
  const k = x2 >= x1 + 40 ? (x2 - x1) / 2 : Math.max(60, (x1 - x2) / 2);
  return `M${x1} ${y1} C${x1 + k} ${y1} ${x2 - k} ${y2} ${x2} ${y2}`;
}

export interface LinkPath {
  d: string;
  /** Midpoint, where the link's + and × sit. */
  mx: number;
  my: number;
}

export function linkPath(
  edge: Pick<Edge, 'source' | 'target'>,
  x1: number,
  y1: number,
  x2: number,
  y2: number,
  places: PlaceMap,
  zoom: number,
): LinkPath {
  const lane = laneAround(edge, x1, y1, x2, y2, places, zoom);
  if (lane === null) return { d: curve(x1, y1, x2, y2), mx: (x1 + x2) / 2, my: (y1 + y2) / 2 };
  const xa = x1 + GAP_X;
  const xb = x2 - GAP_X;
  const h = GAP_X / 2;
  const d =
    `M${x1} ${y1} C${x1 + h} ${y1} ${x1 + h} ${lane} ${xa} ${lane} L${xb} ${lane} ` +
    `C${xb + h} ${lane} ${xb + h} ${y2} ${x2} ${y2}`;
  return { d, mx: (xa + xb) / 2, my: lane };
}

export interface Bounds {
  x0: number;
  y0: number;
  w: number;
  h: number;
}

/** The area the steps cover, using full-card heights so Fit view doesn't change with the zoom level. */
export function boundsOf(
  places: Record<string, { x: number; y: number; h?: number }>,
  heightOf?: (id: string) => number,
): Bounds {
  const ids = Object.keys(places);
  if (!ids.length) return { x0: 0, y0: 0, w: CARD_W, h: CARD_H };
  let x0 = Infinity;
  let y0 = Infinity;
  let x1 = -Infinity;
  let y1 = -Infinity;
  for (const [id, p] of Object.entries(places)) {
    const h = heightOf ? heightOf(id) : (p.h ?? CARD_H);
    x0 = Math.min(x0, p.x);
    y0 = Math.min(y0, p.y);
    x1 = Math.max(x1, p.x + CARD_W);
    y1 = Math.max(y1, p.y + h);
  }
  return { x0, y0, w: x1 - x0, h: y1 - y0 };
}

/**
 * A free spot near `x, y` for a new card: moves down and right in 32 px steps while another
 * card sits within 40 px, so a click-to-add never lands exactly on top of a step.
 */
export function freeSpot(
  x: number,
  y: number,
  places: Record<string, { x: number; y: number }>,
): { x: number; y: number } {
  let px = snap(x);
  let py = snap(y);
  const taken = Object.values(places);
  for (let guard = 0; guard < 20; guard += 1) {
    if (!taken.some((p) => Math.abs(p.x - px) < 40 && Math.abs(p.y - py) < 40)) break;
    px += 32;
    py += 32;
  }
  return { x: px, y: py };
}

/** Steps down past any card the new card would cover, keeping its column. */
export function clearBelow(
  id: string,
  x: number,
  y: number,
  h: number,
  places: Record<string, { x: number; y: number; h: number }>,
): number {
  let py = y;
  for (let guard = 0; guard < 24; guard += 1) {
    const hit = Object.entries(places).find(
      ([k, p]) =>
        k !== id &&
        p.x < x + CARD_W + 16 &&
        p.x + CARD_W + 16 > x &&
        p.y < py + h + 16 &&
        p.y + p.h + 16 > py,
    );
    if (!hit) break;
    py = snap(hit[1].y + hit[1].h + GAP_Y);
  }
  return py;
}

export type ArrowKey = 'ArrowRight' | 'ArrowLeft' | 'ArrowDown' | 'ArrowUp';

/** The closest card in an arrow's direction, preferring cards in line with the current one. */
export function nearest(fromId: string, dir: ArrowKey, places: PlaceMap): string | null {
  const a = places[fromId];
  if (!a) return null;
  const ax = a.x + CARD_W / 2;
  const ay = a.y + a.h / 2;
  let best: string | null = null;
  let bestScore = Infinity;
  for (const [id, b] of Object.entries(places)) {
    if (id === fromId) continue;
    const dx = b.x + CARD_W / 2 - ax;
    const dy = b.y + b.h / 2 - ay;
    const main =
      dir === 'ArrowRight' ? dx : dir === 'ArrowLeft' ? -dx : dir === 'ArrowDown' ? dy : -dy;
    const side = dir === 'ArrowRight' || dir === 'ArrowLeft' ? Math.abs(dy) : Math.abs(dx);
    if (main <= 8) continue;
    const score = main + side * 2;
    if (score < bestScore) {
      bestScore = score;
      best = id;
    }
  }
  return best;
}
