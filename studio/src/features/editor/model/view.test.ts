import { describe, expect, it } from 'vitest';
import {
  fitTarget,
  lodCardHeight,
  openTarget,
  revealPan,
  safeRect,
  zoomAbout,
  zoomLevel,
} from './view';

describe('zoom levels', () => {
  it('draws full cards from 70%, one-row cards from 50% and far below', () => {
    expect(zoomLevel(1)).toBe('full');
    expect(zoomLevel(0.7)).toBe('full');
    expect(zoomLevel(0.69)).toBe('compact');
    expect(zoomLevel(0.5)).toBe('compact');
    expect(zoomLevel(0.49)).toBe('far');
    expect(zoomLevel(0.25)).toBe('far');
  });

  it('keeps one-row cards at least 20 px tall on screen', () => {
    expect(lodCardHeight(0.6)).toBe(56);
    expect(lodCardHeight(0.25)).toBe(80);
  });
});

describe('fit view', () => {
  const r = safeRect(1000, 700, false);

  it('fits small workflows up to 100% and centres them', () => {
    const t = fitTarget(r, { x0: 0, y0: 0, w: 600, h: 200 });
    expect(t.zoom).toBe(1);
    expect(t.overflow).toBe(false);
    expect(t.x).toBe(200);
  });

  it('never goes below 70%, and then starts from the leftmost steps', () => {
    const t = fitTarget(r, { x0: 100, y0: 0, w: 4000, h: 200 });
    expect(t.zoom).toBe(0.7);
    expect(t.overflow).toBe(true);
    expect(t.x).toBe(24 - 70);
  });

  it('opens between 92% and 100%', () => {
    expect(openTarget(r, { x0: 0, y0: 0, w: 4000, h: 200 }).zoom).toBeCloseTo(12 / 13);
    expect(openTarget(r, { x0: 0, y0: 0, w: 900, h: 200 }).zoom).toBe(1);
  });

  it('leaves room for the minimap when it is open', () => {
    expect(safeRect(1000, 700, true).bottom).toBe(700 - 64 - 88);
  });
});

describe('zoom and reveal', () => {
  it('zooms about a point, keeping it still', () => {
    const v = zoomAbout({ x: 0, y: 0, zoom: 1 }, 2, 100, 100);
    expect(v).toEqual({ zoom: 2, x: -100, y: -100 });
    expect(zoomAbout({ x: 0, y: 0, zoom: 1 }, 10, 0, 0).zoom).toBe(2);
  });

  it('pans the least distance to bring a card into view', () => {
    const r = { left: 24, top: 72, right: 976, bottom: 636 };
    expect(revealPan({ x: 0, y: 0, zoom: 1 }, r, { x: 100, y: 100, w: 280, h: 88 })).toBeNull();
    expect(revealPan({ x: 0, y: 0, zoom: 1 }, r, { x: 900, y: 100, w: 280, h: 88 })).toEqual({
      zoom: 1,
      x: -204,
      y: 0,
    });
  });
});
