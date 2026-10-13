import { describe, expect, it } from 'vitest';
import { CARD_W, GAP_X, curve, laneAround, layered, nearest, needsAutoLayout } from './layout';
import { link, step } from '@/test/registry-fixture';

describe('layered', () => {
  it('places steps in columns by their longest path and centres short columns', () => {
    const nodes = [
      step('a', 'text_input', 0, 0),
      step('b', 'prompt_template', 0, 0),
      step('c', 'text_output', 0, 0),
      step('d', 'text_input', 0, 10),
    ];
    const edges = [
      link('1', 'a', 'text', 'b', 'text'),
      link('2', 'b', 'text', 'c', 'text'),
      link('3', 'd', 'text', 'c', 'text'),
    ];
    const out = layered(nodes, edges);
    expect(out.a?.x).toBe(0);
    expect(out.b?.x).toBe(CARD_W + GAP_X);
    expect(out.c?.x).toBe(2 * (CARD_W + GAP_X));
    // Column 0 holds a and d (two cards); column 1 holds one card, centred on them.
    expect(out.d?.y).toBe(120);
    expect(out.b?.y).toBe(64);
  });

  it('terminates on a loop from an imported workflow', () => {
    const nodes = [step('a', 'prompt_template', 0, 0), step('b', 'prompt_template', 0, 0)];
    const edges = [link('1', 'a', 'text', 'b', 'text'), link('2', 'b', 'text', 'a', 'text')];
    expect(Object.keys(layered(nodes, edges))).toHaveLength(2);
  });
});

describe('needsAutoLayout', () => {
  it('is true only when stored positions overlap', () => {
    expect(needsAutoLayout([step('a', 'agent', 0, 0), step('b', 'agent', 400, 0)])).toBe(false);
    expect(needsAutoLayout([step('a', 'agent', 0, 0), step('b', 'agent', 100, 40)])).toBe(true);
  });
});

describe('links', () => {
  it('route around a card between the ends, on the closer side', () => {
    const places = {
      a: { x: 0, y: 0, w: CARD_W, h: 88 },
      m: { x: 336, y: 0, w: CARD_W, h: 120 },
      b: { x: 672, y: 0, w: CARD_W, h: 88 },
    };
    expect(laneAround({ source: 'a', target: 'b' }, 280, 40, 672, 40, places, 1)).toBe(-16);
    expect(laneAround({ source: 'a', target: 'b' }, 280, 40, 336, 40, places, 1)).toBeNull();
  });

  it('bend further for a backwards link', () => {
    expect(curve(0, 0, 200, 0)).toBe('M0 0 C100 0 100 0 200 0');
    expect(curve(200, 0, 0, 0)).toBe('M200 0 C300 0 -100 0 0 0');
  });
});

describe('nearest', () => {
  it('prefers a card in line with the current one', () => {
    const places = {
      a: { x: 0, y: 0, w: CARD_W, h: 88 },
      b: { x: 336, y: 0, w: CARD_W, h: 88 },
      c: { x: 336, y: 400, w: CARD_W, h: 88 },
    };
    expect(nearest('a', 'ArrowRight', places)).toBe('b');
    expect(nearest('b', 'ArrowDown', places)).toBe('c');
    expect(nearest('a', 'ArrowLeft', places)).toBeNull();
  });
});
