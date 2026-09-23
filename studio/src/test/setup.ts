import '@testing-library/jest-dom/vitest';

import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';

// jsdom has no ResizeObserver. @xyflow/react's <ReactFlow> reads it unconditionally on
// mount, so any test that renders the Workflow Canvas needs at least a stand-in.
//
// A real ResizeObserver always delivers one initial notification per observed element,
// even if nothing ever resizes; @xyflow/react's node measurement (the `dimensions`
// NodeChange that reports a node's rendered size) depends on that first callback firing.
// A pure no-op stub never calls back, so nodes never measure and stay
// `visibility: hidden` forever under test. This stub reproduces just that one guaranteed
// initial notification (batched onto a microtask, like a real observer), reading each
// target's current `offsetWidth`/`offsetHeight` at delivery time — which is what
// `getDimensions()` in `@xyflow/system` reads, and what tests mock via
// `HTMLElement.prototype.offsetWidth`/`offsetHeight`.
class ResizeObserverStub {
  private readonly callback: ResizeObserverCallback;
  private readonly pending = new Set<Element>();
  private scheduled = false;

  constructor(callback: ResizeObserverCallback) {
    this.callback = callback;
  }

  observe(target: Element): void {
    this.pending.add(target);
    this.scheduleFlush();
  }

  unobserve(target: Element): void {
    this.pending.delete(target);
  }

  disconnect(): void {
    this.pending.clear();
  }

  private scheduleFlush(): void {
    if (this.scheduled) return;
    this.scheduled = true;
    queueMicrotask(() => {
      this.scheduled = false;
      const targets = Array.from(this.pending);
      this.pending.clear();
      if (targets.length === 0) return;
      // `@xyflow/system`'s own pane-extent observer reads `entry.contentRect`; jsdom
      // never lays out elements, so `getBoundingClientRect()` is the closest available
      // stand-in (it returns zeroes unless a test mocks it, same as a real unstyled node).
      const entries = targets.map(
        (target) =>
          ({ target, contentRect: target.getBoundingClientRect() }) as ResizeObserverEntry,
      );
      this.callback(entries, this as unknown as ResizeObserver);
    });
  }
}

if (typeof globalThis.ResizeObserver === 'undefined') {
  globalThis.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver;
}

// jsdom does not implement CSS transform geometry (DOMMatrix/DOMMatrixReadOnly).
// @xyflow/react's node measurement reads the canvas's current zoom via
// `new DOMMatrixReadOnly(computedStyle.transform).m22`, so any test that lets a node
// actually measure (see ResizeObserverStub above) needs at least that one field. Parse
// just enough of a `scale(z)`/`matrix(...)` transform string to recover it; jsdom's
// `getComputedStyle` echoes the specified value rather than a browser's fully computed
// `matrix(...)` form, and the unset default (`none`) means no zoom, i.e. m22 = 1.
class DOMMatrixReadOnlyStub {
  readonly m22: number;

  constructor(transform?: string) {
    const matrixMatch = transform?.match(/matrix\(([^)]+)\)/);
    const scaleMatch = transform?.match(/scale\(([^,)]+)/);
    if (matrixMatch) {
      const m22 = parseFloat(matrixMatch[1]?.split(',')[3]?.trim() ?? '');
      this.m22 = Number.isFinite(m22) ? m22 : 1;
    } else if (scaleMatch) {
      const m22 = parseFloat(scaleMatch[1] ?? '');
      this.m22 = Number.isFinite(m22) ? m22 : 1;
    } else {
      this.m22 = 1;
    }
  }
}

if (typeof globalThis.DOMMatrixReadOnly === 'undefined') {
  globalThis.DOMMatrixReadOnly = DOMMatrixReadOnlyStub as unknown as typeof DOMMatrixReadOnly;
}

afterEach(() => {
  cleanup();
});
