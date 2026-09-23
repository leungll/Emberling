import {
  Background,
  Controls,
  ReactFlow,
  type Edge as FlowEdge,
  type Connection,
  type NodeChange,
  type EdgeChange,
  type ReactFlowInstance,
} from '@xyflow/react';
import { useEffect, useMemo, useRef } from 'react';

import { RegisteredNode, type RegisteredFlowNode } from './RegisteredNode';

/** Asks the canvas to bring one node into view; `seq` repeats a request for the same node. */
export interface CanvasFocusRequest {
  nodeId: string;
  seq: number;
}

interface WorkflowCanvasProps {
  nodes: RegisteredFlowNode[];
  edges: FlowEdge[];
  /**
   * Read-only is a topology snapshot (04 §1.4, Observe): no drag, no connect, no delete.
   * Selection stays so Observe can pick a NodeRun. `onEdgesChange`/`onConnect` are never
   * attached, so nothing can reach the caller's Definition state even by keyboard.
   * `onNodesChange`, if given, still is: `nodesDraggable={false}` already rules out a
   * position edit reaching it, and it is the only channel React Flow reports a node's
   * measured `dimensions` through, which `fitView` needs (see the `fitView` prop below).
   */
  readOnly?: boolean;
  focus?: CanvasFocusRequest | null;
  onNodesChange?: (changes: NodeChange<RegisteredFlowNode>[]) => void;
  onEdgesChange?: (changes: EdgeChange<FlowEdge>[]) => void;
  onConnect?: (connection: Connection) => void;
  onSelectNode?: (nodeId: string | null) => void;
}

export function WorkflowCanvas({
  nodes,
  edges,
  readOnly = false,
  focus = null,
  onNodesChange,
  onEdgesChange,
  onConnect,
  onSelectNode,
}: WorkflowCanvasProps) {
  const nodeTypes = useMemo(() => ({ registered: RegisteredNode }), []);
  const instance = useRef<ReactFlowInstance<RegisteredFlowNode, FlowEdge> | null>(null);

  useEffect(() => {
    if (!focus) return;
    void instance.current?.fitView({ nodes: [{ id: focus.nodeId }], duration: 200, maxZoom: 1 });
  }, [focus]);

  const editHandlers = {
    // Dimension measurement, not editing: kept attached in read-only mode so a caller can
    // feed `dimensions` changes back into `nodes` (see the prop doc above).
    ...(onNodesChange ? { onNodesChange } : {}),
    ...(!readOnly && onEdgesChange ? { onEdgesChange } : {}),
    ...(!readOnly && onConnect ? { onConnect } : {}),
  };

  return (
    <div className="h-full w-full" data-testid="workflow-canvas" data-read-only={readOnly}>
      <ReactFlow<RegisteredFlowNode, FlowEdge>
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        nodesDraggable={!readOnly}
        nodesConnectable={!readOnly}
        edgesReconnectable={!readOnly}
        deleteKeyCode={readOnly ? null : undefined}
        elementsSelectable
        // The fitView prop waits until every node is measured, so it only works when the
        // caller applies React Flow's `dimensions` changes back into `nodes`.
        fitView
        onInit={(flow) => {
          instance.current = flow;
        }}
        {...editHandlers}
        onNodeClick={(_, node) => onSelectNode?.(node.id)}
        onPaneClick={() => onSelectNode?.(null)}
        proOptions={{ hideAttribution: true }}
      >
        <Background />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  );
}
