import { MarkerType } from '@xyflow/react';
import { describe, expect, it } from 'vitest';

import { EDGE_COLOR, INVALID_EDGE_COLOR, toCanvasEdges } from './canvasEdges';
import type { Edge, Node, NodeMetadata } from '@/api/types';

function metadata(
  type: string,
  inputs: NodeMetadata['inputs'],
  outputs: NodeMetadata['outputs'],
): NodeMetadata {
  return {
    type,
    displayName: type,
    category: 'Prompt & Model',
    executionKind: 'SYNC',
    inputs,
    outputs,
    configSchema: { type: 'object', properties: {} },
    uiSchema: { fields: [] },
    sideEffect: { kind: 'NONE', idempotency: 'SAFE' },
  };
}

const TEXT_SOURCE = metadata(
  'text_input',
  [],
  [{ name: 'text', dataType: 'text', required: true }],
);
const IMAGE_SOURCE = metadata(
  'image_input',
  [],
  [{ name: 'image', dataType: 'image', required: true }],
);
const TEXT_SINK = metadata('text_output', [{ name: 'text', dataType: 'text', required: true }], []);
const ANY_SINK = metadata('any_output', [{ name: 'value', dataType: 'any', required: true }], []);

const METADATA = new Map(
  [TEXT_SOURCE, IMAGE_SOURCE, TEXT_SINK, ANY_SINK].map((item) => [item.type, item]),
);

function node(id: string, type: string, name: string): Node {
  return { id, type, name, position: { x: 0, y: 0 }, config: {} };
}

const NODES = [
  node('text_in', 'text_input', 'Creative Brief'),
  node('image_in', 'image_input', 'Reference Image'),
  node('text_out', 'text_output', 'Result'),
  node('any_out', 'any_output', 'Anything'),
];

function edge(source: string, sourceHandle: string, target: string, targetHandle: string): Edge {
  return { id: `${source}->${target}`, source, sourceHandle, target, targetHandle };
}

describe('toCanvasEdges', () => {
  it('draws every edge with the Observe topology arrow and the orthogonal routing of the Edit mock', () => {
    const [flow] = toCanvasEdges([edge('text_in', 'text', 'text_out', 'text')], NODES, METADATA);

    expect(flow).toMatchObject({
      id: 'text_in->text_out',
      type: 'step',
      source: 'text_in',
      sourceHandle: 'text',
      target: 'text_out',
      targetHandle: 'text',
      style: { stroke: EDGE_COLOR, strokeWidth: 1.5 },
      markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16, color: EDGE_COLOR },
    });
    expect(flow?.data).toEqual({ incompatible: false });
  });

  it('accepts any source type into an `any` input, like the Backend compiler', () => {
    const [flow] = toCanvasEdges([edge('image_in', 'image', 'any_out', 'value')], NODES, METADATA);
    expect(flow?.style?.stroke).toBe(EDGE_COLOR);
  });

  it('turns an incompatible connection red immediately, arrow included', () => {
    const [flow] = toCanvasEdges([edge('image_in', 'image', 'text_out', 'text')], NODES, METADATA);

    expect(flow?.style?.stroke).toBe(INVALID_EDGE_COLOR);
    expect(flow?.markerEnd).toMatchObject({ color: INVALID_EDGE_COLOR });
    expect(flow?.data).toEqual({ incompatible: true });
    expect(flow?.ariaLabel).toBe('Incompatible connection Reference Image to Result');
  });

  it('names an unnamed node by its id in the incompatible-connection label', () => {
    const unnamed = NODES.map((item) => (item.id === 'text_out' ? { ...item, name: '' } : item));
    const [flow] = toCanvasEdges(
      [edge('image_in', 'image', 'text_out', 'text')],
      unnamed,
      METADATA,
    );
    expect(flow?.ariaLabel).toBe('Incompatible connection Reference Image to text_out');
  });

  it('leaves an edge whose ports are not (yet) known to the Backend check', () => {
    // Registry metadata still loading, or a port the Registry does not declare: nothing
    // local can prove the edge wrong, so it is not marked.
    const [flow] = toCanvasEdges([edge('text_in', 'text', 'text_out', 'text')], NODES, new Map());
    expect(flow?.style?.stroke).toBe(EDGE_COLOR);
  });
});
