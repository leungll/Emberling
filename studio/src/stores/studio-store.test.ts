import { beforeEach, describe, expect, it } from 'vitest';

import { EDIT_HISTORY_LIMIT, type EditorGraph, useStudioStore } from './studio-store';

function graphWith(...names: string[]): EditorGraph {
  return {
    nodes: names.map((name, index) => ({
      id: `node_${name}`,
      type: 'text_input',
      name,
      position: { x: index * 200, y: 0 },
      config: {},
    })),
    edges: [],
  };
}

const store = () => useStudioStore.getState();

const SAVED = graphWith('a');
const ONE = graphWith('a', 'b');
const TWO = graphWith('a', 'b', 'c');

beforeEach(() => {
  store().resetHistory(SAVED);
});

describe('studio store — Edit history', () => {
  it('starts empty and clean after a (re)load', () => {
    expect(store().past).toEqual([]);
    expect(store().future).toEqual([]);
    expect(store().hasUnsavedChanges).toBe(false);
    expect(store().undo(SAVED)).toBeNull();
    expect(store().redo(SAVED)).toBeNull();
  });

  it('undoes to the checkpointed graph and redoes back to the one it left', () => {
    store().checkpoint(SAVED);
    store().checkpoint(ONE);

    expect(store().undo(TWO)).toEqual(ONE);
    expect(store().undo(ONE)).toEqual(SAVED);
    expect(store().undo(SAVED)).toBeNull();

    expect(store().redo(SAVED)).toEqual(ONE);
    expect(store().redo(ONE)).toEqual(TWO);
    expect(store().redo(TWO)).toBeNull();
  });

  it('drops the redo stack when a new edit follows an undo', () => {
    store().checkpoint(SAVED);
    expect(store().undo(ONE)).toEqual(SAVED);
    expect(store().future).toHaveLength(1);

    store().checkpoint(SAVED);
    expect(store().future).toEqual([]);
    expect(store().redo(TWO)).toBeNull();
  });

  it('coalesces checkpoints that share a key until the session is closed', () => {
    store().checkpoint(SAVED, 'config:node_a:template');
    store().checkpoint(ONE, 'config:node_a:template');
    store().checkpoint(TWO, 'config:node_a:template');
    expect(store().past).toEqual([SAVED]);

    // A different key is a different entry.
    store().checkpoint(TWO, 'name:node_a');
    expect(store().past).toEqual([SAVED, TWO]);

    // Closing the session (a blur, a drag end) makes the same key start a new entry.
    store().closeCoalescing();
    store().checkpoint(ONE, 'name:node_a');
    expect(store().past).toEqual([SAVED, TWO, ONE]);
  });

  it('does not stack the same graph twice in a row', () => {
    store().checkpoint(SAVED);
    store().checkpoint(SAVED);
    expect(store().past).toEqual([SAVED]);
  });

  it(`keeps at most ${EDIT_HISTORY_LIMIT} entries and drops the oldest`, () => {
    const graphs = Array.from({ length: EDIT_HISTORY_LIMIT + 5 }, (_, index) =>
      graphWith(`n${index}`),
    );
    for (const graph of graphs) store().checkpoint(graph);

    expect(store().past).toHaveLength(EDIT_HISTORY_LIMIT);
    expect(store().past[0]).toEqual(graphs[5]);
    expect(store().past.at(-1)).toEqual(graphs.at(-1));
  });

  it('clears both stacks on reset, as a Definition (re)load does', () => {
    store().checkpoint(SAVED);
    store().undo(ONE);
    store().checkpoint(SAVED);
    store().resetHistory(TWO);

    expect(store().past).toEqual([]);
    expect(store().future).toEqual([]);
    expect(store().hasUnsavedChanges).toBe(false);
  });

  it('reports unsaved changes against the saved graph, not an edit counter', () => {
    store().checkpoint(SAVED);
    store().syncUnsavedChanges(ONE);
    expect(store().hasUnsavedChanges).toBe(true);

    // Undo back to exactly the saved graph is clean again.
    expect(store().undo(ONE)).toEqual(SAVED);
    expect(store().hasUnsavedChanges).toBe(false);

    // Redo past it is dirty again.
    expect(store().redo(SAVED)).toEqual(ONE);
    expect(store().hasUnsavedChanges).toBe(true);
  });

  it('treats config key order as the same graph', () => {
    const saved: EditorGraph = {
      nodes: [{ ...SAVED.nodes[0]!, config: { a: 1, b: { c: 2, d: 3 } } }],
      edges: [],
    };
    store().resetHistory(saved);
    store().syncUnsavedChanges({
      nodes: [{ ...SAVED.nodes[0]!, config: { b: { d: 3, c: 2 }, a: 1 } }],
      edges: [],
    });
    expect(store().hasUnsavedChanges).toBe(false);
  });

  it('moves the saved point on Save without clearing history', () => {
    store().checkpoint(SAVED);
    store().syncUnsavedChanges(ONE);
    store().markSaved(ONE);
    expect(store().hasUnsavedChanges).toBe(false);
    expect(store().past).toEqual([SAVED]);

    // Undoing past the new saved point is an unsaved change.
    expect(store().undo(ONE)).toEqual(SAVED);
    expect(store().hasUnsavedChanges).toBe(true);
  });

  it('stays dirty when the canvas moved on while the saved request was in flight', () => {
    // Save sent ONE; before it returned the user edited the canvas to TWO (04 §2.6: Run
    // stays disabled while unsaved changes exist).
    store().syncUnsavedChanges(TWO);
    store().markSaved(ONE);
    expect(store().savedGraph).toEqual(ONE);
    expect(store().hasUnsavedChanges).toBe(true);
  });
});
