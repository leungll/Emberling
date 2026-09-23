import {
  Background,
  ReactFlow,
  type Edge as FlowEdge,
  type Connection,
  type NodeChange,
  type EdgeChange,
  type ReactFlowInstance,
} from '@xyflow/react';
import { useEffect, useMemo, useRef, useState } from 'react';

import { CanvasToolbar, type InteractionMode } from './CanvasToolbar';
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

/**
 * `<ReactFlow>` wraps its own children in a `ReactFlowProvider` internally (falling back
 * to `Fragment` if one already wraps it), so `CanvasToolbar` — rendered here as a `<Panel>`
 * child — can call `useReactFlow`/`useViewport` without this component adding a second,
 * redundant provider of its own.
 */
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
  const [mode, setMode] = useState<InteractionMode>('select');
  const [showGrid, setShowGrid] = useState(true);

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
        // Read-only never offers the select/pan mode toggle (the Toolbar itself is hidden
        // below), so it always drags-to-pan and leaves `selectionOnDrag` off. React Flow's
        // Pane only attaches a plain `onClick` (what drives blank-area deselection back to
        // the Run Summary) when `elementsSelectable && !isSelecting`; `selectionOnDrag: true`
        // makes every pointer-down a potential selection-box drag instead, which replaces
        // that click handler with a pointerup path that requires a real drag sequence to
        // have started one. Observe has no selection box to draw, so this would otherwise
        // silently break the blank-area click without ever failing to compile or lint.
        panOnDrag={readOnly ? true : mode === 'pan'}
        selectionOnDrag={readOnly ? false : mode === 'select'}
        // The fitView prop waits until every node is measured, so it only works when the
        // caller applies React Flow's `dimensions` changes back into `nodes`.
        fitView
        // Never magnify a small graph past 100%: cards keep the mock's reading size
        // instead of ballooning to fill the canvas.
        fitViewOptions={{ maxZoom: 1 }}
        onInit={(flow) => {
          instance.current = flow;
        }}
        {...editHandlers}
        onNodeClick={(_, node) => onSelectNode?.(node.id)}
        onPaneClick={() => onSelectNode?.(null)}
        proOptions={{ hideAttribution: true }}
      >
        {showGrid ? <Background color="var(--border)" gap={24} /> : null}
        {!readOnly ? (
          <CanvasToolbar
            mode={mode}
            onModeChange={setMode}
            showGrid={showGrid}
            onShowGridChange={setShowGrid}
          />
        ) : null}
      </ReactFlow>
    </div>
  );
}
