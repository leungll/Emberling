import { describe, expect, it } from 'vitest';
import { emptyHistory, record, redo, undo } from './history';

describe('history', () => {
  it('undoes and redoes in order and clears redo on a new change', () => {
    let h = emptyHistory<number>();
    h = record(h, 1);
    h = record(h, 2);
    const u = undo(h, 3)!;
    expect(u.state).toBe(2);
    const r = redo(u.history, 2)!;
    expect(r.state).toBe(3);
    expect(record(u.history, 2).redo).toEqual([]);
  });

  it('keeps at most the limit', () => {
    let h = emptyHistory<number>();
    for (let i = 0; i < 5; i += 1) h = record(h, i, 3);
    expect(h.undo).toEqual([2, 3, 4]);
  });

  it('has nothing to undo or redo at the start', () => {
    expect(undo(emptyHistory<number>(), 0)).toBeNull();
    expect(redo(emptyHistory<number>(), 0)).toBeNull();
  });
});
