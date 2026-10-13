import { useReactFlow } from '@xyflow/react';
import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from 'react';

import type { Bounds, PlaceMap } from '@/features/editor/model/layout';
import {
  ZOOM_STEP,
  clampZoom,
  fitTarget,
  openTarget,
  revealPan,
  safeRect,
  zoomAbout,
  type Viewport,
} from '@/features/editor/model/view';

const MINIMAP_KEY = 'emb.editor.minimap';

function readStored(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStored(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Storage is unavailable; the choice lasts for this page only.
  }
}

function duration(): number {
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : 220;
}

/**
 * How the canvas frames the steps: the opening view until the user moves it, Fit view (which
 * follows the canvas size until the user moves away), zoom about the centre, and the least pan
 * that reveals a step. The minimap opens by itself when the steps don't fit, unless the user
 * closed it.
 */
export function useCanvasViewport({
  areaRef,
  bounds,
  ready,
  onOverflow,
}: {
  areaRef: RefObject<HTMLDivElement | null>;
  bounds: Bounds;
  /** The steps have their measured sizes, so the opening view can be chosen. */
  ready: boolean;
  /** The opening view doesn't fit every step. */
  onOverflow: () => void;
}) {
  const rf = useReactFlow();
  const [size, setSize] = useState({ w: 0, h: 0 });
  const [minimapStored, setMinimapStored] = useState<string | null>(() => readStored(MINIMAP_KEY));
  const [minimapAuto, setMinimapAuto] = useState(false);
  // 'auto' keeps the opening view; 'fit' keeps Fit view; 'user' leaves the view alone.
  const [mode, setMode] = useState<'auto' | 'fit' | 'user'>('auto');
  const overflowed = useRef(false);
  const rect0 = safeRect(
    size.w,
    size.h,
    minimapStored === '1' || (minimapStored === null && minimapAuto),
  );
  // The opening view doesn't fit every step: the minimap opens unless the user closed it.
  if (
    !minimapAuto &&
    ready &&
    size.w > 0 &&
    mode === 'auto' &&
    openTarget(rect0, bounds).overflow
  ) {
    setMinimapAuto(true);
  }
  const minimap = minimapStored === '1' || (minimapStored === null && minimapAuto);

  useLayoutEffect(() => {
    const el = areaRef.current;
    if (!el) return;
    const read = () => setSize({ w: el.clientWidth, h: el.clientHeight });
    read();
    const ro = new ResizeObserver(read);
    ro.observe(el);
    return () => ro.disconnect();
  }, [areaRef]);

  const rect = useCallback(() => safeRect(size.w, size.h, minimap), [size, minimap]);

  // Keeps the automatic views current as the canvas, the steps or the minimap change.
  useEffect(() => {
    if (!ready || !size.w || !size.h) return;
    if (mode === 'auto') {
      const v = openTarget(rect(), bounds);
      if (v.overflow && !overflowed.current) {
        overflowed.current = true;
        onOverflow();
      }
      void rf.setViewport({ x: v.x, y: v.y, zoom: v.zoom });
    } else if (mode === 'fit') {
      const v = fitTarget(rect(), bounds);
      void rf.setViewport({ x: v.x, y: v.y, zoom: v.zoom });
    }
  }, [ready, size, bounds, rect, rf, onOverflow, mode]);

  const animate = useCallback(
    (v: Viewport, next: 'fit' | 'user' = 'user') => {
      setMode(next);
      void rf.setViewport(v, { duration: duration() });
    },
    [rf],
  );

  const centre = useCallback(() => {
    const r = rect();
    return { px: (r.left + r.right) / 2, py: (r.top + r.bottom) / 2 };
  }, [rect]);

  const zoomTo = useCallback(
    (z: number) => {
      const { px, py } = centre();
      animate(zoomAbout(rf.getViewport(), clampZoom(z), px, py));
    },
    [animate, centre, rf],
  );

  const zoomBy = useCallback(
    (dir: 1 | -1) => zoomTo(rf.getViewport().zoom * (dir > 0 ? ZOOM_STEP : 1 / ZOOM_STEP)),
    [rf, zoomTo],
  );

  const fit = useCallback(() => {
    const v = fitTarget(rect(), bounds);
    if (v.overflow && minimapStored !== '0') setMinimapAuto(true);
    animate(v, 'fit');
  }, [animate, bounds, minimapStored, rect]);

  const reveal = useCallback(
    (places: PlaceMap, id: string) => {
      const p = places[id];
      if (!p) return;
      const to = revealPan(rf.getViewport(), rect(), p);
      if (to) animate(to);
    },
    [animate, rect, rf],
  );

  /** The canvas point in the middle of the area no floating control covers. */
  const centrePoint = useCallback(() => {
    const { px, py } = centre();
    const v = rf.getViewport();
    return { x: (px - v.x) / v.zoom, y: (py - v.y) / v.zoom };
  }, [centre, rf]);

  const setMinimap = useCallback((open: boolean) => {
    const value = open ? '1' : '0';
    writeStored(MINIMAP_KEY, value);
    setMinimapStored(value);
  }, []);

  /** The user moved the view: automatic framing stops. */
  const userMoved = useCallback(() => setMode('user'), []);

  /** Starts over with the opening view, for a newly loaded workflow. */
  const reset = useCallback(() => {
    setMode('auto');
    overflowed.current = false;
  }, []);

  return {
    size,
    minimap,
    setMinimap,
    zoomTo,
    zoomBy,
    fit,
    reveal,
    centrePoint,
    userMoved,
    reset,
    panTo: animate,
  };
}
