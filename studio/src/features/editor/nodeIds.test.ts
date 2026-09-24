import { describe, expect, it } from 'vitest';

import { createNodeIdGenerator, newNodeId } from './nodeIds';

describe('createNodeIdGenerator', () => {
  it('yields distinct ids for two back-to-back adds even with a pinned random suffix', () => {
    // A pinned suffix stands in for the worst case (two ids minted in the same instant from
    // the same entropy); the counter alone must keep them apart.
    const next = createNodeIdGenerator(() => 'fixed');

    const first = next('text_generation');
    const second = next('text_generation');

    expect(first).not.toBe(second);
    expect(first).toBe('node_text_generation_1_fixed');
    expect(second).toBe('node_text_generation_2_fixed');
  });

  it('carries the node type and a random suffix in the default generator', () => {
    const id = newNodeId('text_input');
    expect(id).toMatch(/^node_text_input_\d+_[0-9a-f]{8}$/);
    expect(newNodeId('text_input')).not.toBe(id);
  });
});
