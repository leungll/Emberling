import { describe, expect, it } from 'vitest';

import type { NodeMetadata } from '@/api/types';
import { link, NODE_TYPES, step } from '@/test/registry-fixture';
import { addLinked, connect, deleteEdge, deleteSteps, duplicate, type IdSource } from './edit-ops';
import { CARD_W, GAP_X } from './layout';

function ids(): IdSource {
  let n = 0;
  let e = 0;
  return { node: (type) => `${type}_${String(++n)}`, edge: () => `edge_${String(++e)}` };
}

const meta = (type: string): NodeMetadata => {
  const m = NODE_TYPES.find((x) => x.type === type);
  if (!m) throw new Error(type);
  return m;
};

const IN = step('in', 'text_input', 0, 0, { inputKey: 'brief', label: 'Brief' }, 'Brief');
const GEN = step('gen', 'text_generation', 400, 0);
const OUT = step('out', 'text_output', 800, 0);
const graph = {
  nodes: [IN, GEN, OUT],
  edges: [link('a', 'in', 'text', 'gen', 'prompt'), link('b', 'gen', 'text', 'out', 'text')],
};
const places = {
  in: { x: 0, y: 0, h: 120 },
  gen: { x: 400, y: 0, h: 120 },
  out: { x: 800, y: 0, h: 120 },
};
const at = { places, portY: () => 40 };

describe('connect', () => {
  it('replaces the link already going into the same input', () => {
    const next = connect(graph, ids(), 'in', 'text', 'out', 'text');
    expect(next.edges.filter((e) => e.target === 'out')).toEqual([
      { id: 'edge_1', source: 'in', sourceHandle: 'text', target: 'out', targetHandle: 'text' },
    ]);
  });

  it('leaves the graph alone when the link exists', () => {
    expect(connect(graph, ids(), 'gen', 'text', 'out', 'text')).toBe(graph);
  });
});

describe('deleting', () => {
  it('removes a step with every link touching it', () => {
    const next = deleteSteps(graph, ['gen']);
    expect(next.nodes.map((n) => n.id)).toEqual(['in', 'out']);
    expect(next.edges).toEqual([]);
  });

  it('removes one link and keeps the steps', () => {
    const next = deleteEdge(graph, 'a');
    expect(next.nodes).toBe(graph.nodes);
    expect(next.edges.map((e) => e.id)).toEqual(['b']);
  });
});

describe('duplicate', () => {
  it('offsets and renames copies, keeps input keys unique and copies no links', () => {
    const { graph: next, created } = duplicate(graph, ['in', 'in'], places, ids());
    const copies = next.nodes.filter((n) => created.includes(n.id));
    expect(copies.map((c) => c.name)).toEqual(['Brief copy', 'Brief copy']);
    expect(copies.map((c) => c.config.inputKey)).toEqual(['briefCopy', 'briefCopy2']);
    expect(copies[0]?.config.label).toBe('Brief copy');
    expect(copies[0]?.position).toEqual({ x: 32, y: 32 });
    expect(next.edges).toBe(graph.edges);
    // The original's config is not shared with the copy.
    expect(IN.config.inputKey).toBe('brief');
  });
});

describe('addLinked', () => {
  it('adds a step one column after an output and links it', () => {
    const plan = { inPort: meta('text_output').inputs[0]! };
    const result = addLinked(
      graph,
      meta('text_output'),
      plan,
      { kind: 'from', sid: 'gen', handle: 'text', type: 'text' },
      at,
      ids(),
    );
    expect(result?.created).toBe('text_output_1');
    const added = result?.graph.nodes.find((n) => n.id === 'text_output_1');
    expect(added?.position.x).toBeGreaterThanOrEqual(400 + CARD_W + GAP_X);
    expect(result?.graph.edges.at(-1)).toMatchObject({ source: 'gen', target: 'text_output_1' });
  });

  it('inserts into a link, splitting it and making room to the right', () => {
    const tg = meta('text_generation');
    const plan = { inPort: tg.inputs[0]!, outPort: tg.outputs[0]! };
    const result = addLinked(graph, tg, plan, { kind: 'insert', edgeId: 'b' }, at, ids());
    const next = result?.graph;
    expect(next?.edges.some((e) => e.id === 'b')).toBe(false);
    expect(next?.edges).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ source: 'gen', target: result?.created }),
        expect.objectContaining({ source: result?.created, target: 'out' }),
      ]),
    );
    const out = next?.nodes.find((n) => n.id === 'out');
    const added = next?.nodes.find((n) => n.id === result?.created);
    expect(out && added && out.position.x >= added.position.x + CARD_W).toBe(true);
    // Steps left of the insertion stay put.
    expect(next?.nodes.find((n) => n.id === 'in')?.position).toEqual({ x: 0, y: 0 });
  });
});
