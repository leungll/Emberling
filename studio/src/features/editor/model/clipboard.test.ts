import { describe, expect, it } from 'vitest';
import { makeClip, parseClip, pasteClip, pasteOrigin } from './clipboard';
import { NODE_TYPES, link, step } from '@/test/registry-fixture';
import { catalogOf } from './steps';

const catalog = catalogOf(NODE_TYPES);
const nodes = [
  step('a', 'text_input', 0, 0, { inputKey: 'topic', label: 'Text Input', required: true }),
  step('b', 'prompt_template', 336, 40, { template: '{{text}}' }),
  step('c', 'text_output', 672, 0),
];
const edges = [link('e1', 'a', 'text', 'b', 'text'), link('e2', 'b', 'text', 'c', 'text')];
const places = { a: { x: 0, y: 0 }, b: { x: 336, y: 40 }, c: { x: 672, y: 0 } };

let seq = 0;
const newNodeId = (type: string) => `n_${type}_${++seq}`;
const newEdgeId = () => `l_${++seq}`;

describe('clipboard', () => {
  it('keeps only the links between the copied steps', () => {
    const clip = makeClip(['a', 'b'], nodes, edges, places, 'wf_1', 1)!;
    expect(clip.nodes.map((n) => [n.ref, n.dx, n.dy])).toEqual([
      ['a', 0, 0],
      ['b', 336, 40],
    ]);
    expect(clip.edges).toEqual([
      { source: 'a', sourceHandle: 'text', target: 'b', targetHandle: 'text' },
    ]);
  });

  it('remaps links, renames steps and keeps input keys unique', () => {
    const clip = parseClip(JSON.stringify(makeClip(['a', 'b'], nodes, edges, places, 'wf_1', 1)))!;
    const existing = [...nodes, step('x', 'text_input', 0, 0, { inputKey: 'topicCopy' })];
    const out = pasteClip(clip, { x: 100, y: 100 }, existing, catalog, newNodeId, newEdgeId);
    const [a, b] = out.nodes;
    expect(a?.name).toBe('Text Input copy');
    expect(a?.config.inputKey).toBe('topicCopy2');
    expect(a?.config.label).toBe('Text Input copy');
    expect(a?.position).toEqual({ x: 104, y: 104 });
    expect(b?.position).toEqual({ x: 440, y: 144 });
    expect(out.edges).toHaveLength(1);
    expect(out.edges[0]).toMatchObject({
      source: a?.id,
      sourceHandle: 'text',
      target: b?.id,
      targetHandle: 'text',
    });
  });

  it('skips steps whose type is not registered here', () => {
    const clip = makeClip(['a', 'b'], nodes, edges, places, 'wf_1', 1)!;
    clip.nodes[1]!.type = 'gone';
    const out = pasteClip(clip, { x: 0, y: 0 }, [], catalog, newNodeId, newEdgeId);
    expect(out.nodes).toHaveLength(1);
    expect(out.edges).toHaveLength(0);
    expect(out.skipped).toBe(1);
  });

  it('offsets each paste of the same copy by 32 px', () => {
    const clip = makeClip(['b'], nodes, edges, places, 'wf_1', 1)!;
    expect(pasteOrigin(clip, 2)).toEqual({ x: 400, y: 104 });
  });

  it('ignores a damaged clipboard', () => {
    expect(parseClip('{')).toBeNull();
    expect(parseClip('{"nodes":[]}')).toBeNull();
  });
});
