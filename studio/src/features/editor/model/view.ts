import type { Bounds } from './layout';

/** Zoom range and the levels at which cards change their drawing. */
export const ZOOM_MIN = 0.25;
export const ZOOM_MAX = 2;
export const ZOOM_LEVELS = { full: 0.7, compact: 0.5 } as const;
/** The opening view keeps 13 px card text at 12 px or more on screen. */
export const OPEN_MIN = 12 / 13;

export type ZoomLevel = 'full' | 'compact' | 'far';

/** Full cards at 70% and above, one-row cards from 50%, and far below that. */
export function zoomLevel(z: number): ZoomLevel {
  if (z >= ZOOM_LEVELS.full - 0.001) return 'full';
  if (z >= ZOOM_LEVELS.compact - 0.001) return 'compact';
  return 'far';
}

/** One-row cards keep at least 20 px on screen, so their canvas height grows as the zoom shrinks. */
export function lodCardHeight(z: number): number {
  return Math.max(56, Math.ceil(20 / z));
}

export function clampZoom(z: number): number {
  return Math.max(ZOOM_MIN, Math.min(ZOOM_MAX, z));
}

export interface Viewport {
  x: number;
  y: number;
  zoom: number;
}

export interface SafeRect {
  left: number;
  top: number;
  right: number;
  bottom: number;
}

const FIT_PAD = 24;
const BAR_H = 48;
const FLOAT_H = 64;
const MINIMAP_H = 88;

/**
 * The part of the canvas no floating control covers: room above for the step toolbar, below
 * for the zoom pill and canvas tools, and the minimap's corner when it is open.
 */
export function safeRect(width: number, height: number, minimapOpen: boolean): SafeRect {
  return {
    left: FIT_PAD,
    top: FIT_PAD + BAR_H,
    right: width - FIT_PAD,
    bottom: height - FLOAT_H - (minimapOpen ? MINIMAP_H : 0),
  };
}

export interface ViewTarget extends Viewport {
  /** The steps don't fit at this zoom; the view starts from the leftmost and topmost steps. */
  overflow: boolean;
}

function place(
  r: SafeRect,
  b: Bounds,
  z: number,
): { x: number; y: number; wide: boolean; tall: boolean } {
  const w = r.right - r.left;
  const h = r.bottom - r.top;
  const wide = b.w * z > w;
  const tall = b.h * z > h;
  const x = wide ? r.left - b.x0 * z : (r.left + r.right) / 2 - (b.x0 + b.w / 2) * z;
  const y = tall ? r.top - b.y0 * z : (r.top + r.bottom) / 2 - (b.y0 + b.h / 2) * z;
  return { x: Math.round(x), y: Math.round(y), wide, tall };
}

/**
 * Fit view never leaves full cards: it fits every step up to 100% when that keeps the
 * full-card zoom or more; otherwise it stops at the full-card threshold and starts from the
 * leftmost and topmost steps.
 */
export function fitTarget(r: SafeRect, b: Bounds): ViewTarget {
  const w = r.right - r.left;
  const h = r.bottom - r.top;
  let zoom = w > 0 && h > 0 ? Math.min(1, w / b.w, h / b.h) : 1;
  const overflow = zoom < ZOOM_LEVELS.full;
  if (overflow) zoom = ZOOM_LEVELS.full;
  const p = place(r, b, zoom);
  return { zoom, x: p.x, y: p.y, overflow };
}

/** The opening view: readable first, between 92% and 100%, from the leftmost steps when they overflow. */
export function openTarget(r: SafeRect, b: Bounds): ViewTarget {
  const w = r.right - r.left;
  const h = r.bottom - r.top;
  const zoom = Math.max(OPEN_MIN, Math.min(1, w / b.w, h / b.h));
  const p = place(r, b, zoom);
  return { zoom, x: p.x, y: p.y, overflow: p.wide || p.tall };
}

/** Zooms about a screen point (the safe area's centre by default), keeping that point still. */
export function zoomAbout(v: Viewport, z: number, px: number, py: number): Viewport {
  const zoom = clampZoom(z);
  return { zoom, x: px - ((px - v.x) * zoom) / v.zoom, y: py - ((py - v.y) * zoom) / v.zoom };
}

/** The least pan that brings a card wholly into the safe area, or `null` when it is already there. */
export function revealPan(
  v: Viewport,
  r: SafeRect,
  card: { x: number; y: number; w: number; h: number },
): Viewport | null {
  const z = v.zoom;
  const x0 = v.x + card.x * z;
  const x1 = x0 + card.w * z;
  const y0 = v.y + card.y * z;
  const y1 = y0 + card.h * z;
  let dx = 0;
  let dy = 0;
  if (x1 - x0 > r.right - r.left || x0 < r.left) dx = r.left - x0;
  else if (x1 > r.right) dx = r.right - x1;
  if (y1 - y0 > r.bottom - r.top || y0 < r.top) dy = r.top - y0;
  else if (y1 > r.bottom) dy = r.bottom - y1;
  if (!dx && !dy) return null;
  return { zoom: z, x: v.x + dx, y: v.y + dy };
}

/** Zoom steps for the + and − buttons and keys. */
export const ZOOM_STEP = 1.25;
