import { MarkerType, type Edge as FlowEdge } from '@xyflow/react';

import type { Edge, Node, NodeMetadata, PortDataType } from '@/api/types';

/** Same stroke and arrow as the Observe topology, so a Definition reads alike in both modes. */
export const EDGE_COLOR = 'var(--muted-foreground)';
/** The FAILED red used for status dots elsewhere in Studio. */
export const INVALID_EDGE_COLOR = 'var(--status-failed-dot)';

export interface CanvasEdgeData extends Record<string, unknown> {
  incompatible: boolean;
}

function portType(
  node: Node | undefined,
  metadataByType: ReadonlyMap<string, NodeMetadata>,
  side: 'inputs' | 'outputs',
  handle: string,
): PortDataType | undefined {
  if (!node) return undefined;
  return metadataByType.get(node.type)?.[side]?.find((port) => port.name === handle)?.dataType;
}

/**
 * Mirrors the Backend compiler's port rule: an `any` input accepts every type, otherwise the
 * types must match. Unknown ports are not judged here; the Backend validation remains the
 * final authority and reports them.
 */
function isIncompatible(source: PortDataType | undefined, target: PortDataType | undefined) {
  if (source === undefined || target === undefined) return false;
  return target !== 'any' && source !== target;
}

/**
 * Edit canvas edges. Presentation follows the Edit mock (orthogonal step routing) with the
 * Observe topology's arrow and stroke. Ports are typed: a connection whose output type
 * the input cannot accept turns red as soon as it is drawn, before Validate, so it is flagged
 * at the place it was made.
 */
export function toCanvasEdges(
  edges: readonly Edge[],
  nodes: readonly Node[],
  metadataByType: ReadonlyMap<string, NodeMetadata>,
): FlowEdge<CanvasEdgeData>[] {
  const nodeById = new Map(nodes.map((node) => [node.id, node]));
  const label = (id: string) => nodeById.get(id)?.name || id;
  return edges.map((edge) => {
    const incompatible = isIncompatible(
      portType(nodeById.get(edge.source), metadataByType, 'outputs', edge.sourceHandle),
      portType(nodeById.get(edge.target), metadataByType, 'inputs', edge.targetHandle),
    );
    const color = incompatible ? INVALID_EDGE_COLOR : EDGE_COLOR;
    return {
      id: edge.id,
      type: 'step',
      source: edge.source,
      sourceHandle: edge.sourceHandle,
      target: edge.target,
      targetHandle: edge.targetHandle,
      style: { stroke: color, strokeWidth: 1.5 },
      markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16, color },
      data: { incompatible },
      ...(incompatible
        ? { ariaLabel: `Incompatible connection ${label(edge.source)} to ${label(edge.target)}` }
        : {}),
    };
  });
}
