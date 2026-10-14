import { describe, expect, it } from 'vitest';

import {
  CARD_H,
  CARD_W,
  GAP_X,
  GAP_Y,
  edgePath,
  fitView,
  graphLayout,
  graphMove,
  lodCardHeight,
  revealCard,
  zoomAt,
  zoomLevel,
} from './run-graph-layout';
import { definition, node } from '@/test/run-fixtures';

// a → b → d, a → c → d, and a → d skipping a column.
const def = definition(
  [
    node('a', 'text_input', 'A'),
    node('b', 'agent', 'B', { position: { x: 0, y: 0 } }),
    node('c', 'agent', 'C', { position: { x: 0, y: 200 } }),
    node('d', 'text_output', 'D'),
  ],
  [
    ['a', 'b'],
    ['a', 'c'],
    ['b', 'd'],
    ['c', 'd'],
    ['a', 'd'],
  ],
);
const layout = graphLayout(def);

describe('graphLayout', () => {
  it('places each step in the column of its longest path, centred in its column', () => {
    expect(layout.pos.get('a')).toMatchObject({ col: 0, x: 0 });
    expect(layout.pos.get('b')).toMatchObject({ col: 1, row: 0, y: 0 });
    expect(layout.pos.get('c')).toMatchObject({ col: 1, row: 1 });
    expect(layout.pos.get('d')).toMatchObject({ col: 2, x: 2 * (CARD_W + GAP_X) });
    expect(layout.pos.get('a')?.y).toBe((layout.height - CARD_H) / 2);
  });

  it('stacks cards by the height each one needs, so taller cards never overlap', () => {
    const tall = graphLayout(def, (id) => (id === 'b' ? 200 : CARD_H));
    const b = tall.pos.get('b');
    const c = tall.pos.get('c');
    expect(b && c && c.y - b.y).toBe(200 + GAP_Y);
    expect(tall.height).toBe(200 + GAP_Y + CARD_H);
    expect(tall.pos.get('a')?.y).toBe(Math.round((tall.height - CARD_H) / 2));
  });

  it('routes a link that skips a column around the steps in its way', () => {
    const chain = graphLayout(
      definition(
        [node('x', 'text_input', 'X'), node('y', 'agent', 'Y'), node('z', 'text_output', 'Z')],
        [
          ['x', 'y'],
          ['y', 'z'],
          ['x', 'z'],
        ],
      ),
    );
    expect(edgePath(chain, 'x', 'z', () => CARD_H, 1)).toContain(' L');
    // Between two stacked steps the straight line is clear.
    expect(edgePath(layout, 'a', 'd', () => CARD_H, 1)).not.toContain(' L');
    expect(edgePath(layout, 'a', 'b', () => CARD_H, 1)).toMatch(/^M\d+ [\d.]+ C/);
    expect(edgePath(layout, 'a', 'missing', () => CARD_H, 1)).toBeNull();
  });
});

describe('moving between cards', () => {
  it('moves across columns and within a column', () => {
    expect(graphMove(layout, 'a', 'ArrowRight')).toBe('b');
    expect(graphMove(layout, 'b', 'ArrowDown')).toBe('c');
    expect(graphMove(layout, 'c', 'ArrowUp')).toBe('b');
    expect(graphMove(layout, 'a', 'ArrowLeft')).toBeNull();
  });
});

describe('zoom', () => {
  it('names the card size for each zoom', () => {
    expect(zoomLevel(1)).toBe('full');
    expect(zoomLevel(0.6)).toBe('compact');
    expect(zoomLevel(0.3)).toBe('far');
    expect(lodCardHeight(0.25)).toBe(80);
  });

  it('fits a small graph centred and never above 100%', () => {
    const v = fitView(layout, 2000, 1000);
    expect(v.z).toBe(1);
    expect(v.tx).toBe(Math.round((2000 - layout.width) / 2));
  });

  it('keeps full cards in a narrow pane and starts at the left', () => {
    expect(fitView(layout, 400, 300)).toMatchObject({ z: 0.7, tx: 24 });
  });

  it('keeps the point under the pointer still and stays within the zoom range', () => {
    const v = zoomAt({ z: 1, tx: 0, ty: 0 }, 2, 100, 100);
    expect(v.z).toBe(1.5);
    expect(v.tx).toBe(100 - 100 * 1.5);
  });

  it('pans only when a card is out of view', () => {
    const p = layout.pos.get('d');
    if (!p) throw new Error('missing card');
    expect(revealCard({ z: 1, tx: 0, ty: 0 }, p, CARD_H, 2000, 1000)).toBeNull();
    expect(revealCard({ z: 1, tx: 0, ty: 0 }, p, CARD_H, 400, 1000)?.tx).toBeLessThan(0);
  });
});
