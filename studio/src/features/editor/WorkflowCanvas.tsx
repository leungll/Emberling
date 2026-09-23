import {
  Background,
  ReactFlow,
  type Edge as FlowEdge,
  type Connection,
  type NodeChange,
  type EdgeChange,
  type Node as FlowNode,
  type NodeTypes,
  type ReactFlowInstance,
  useStore,
} from '@xyflow/react';
import { useEffect, useRef, useState, type CSSProperties } from 'react';

import { CanvasToolbar, type InteractionMode } from './CanvasToolbar';
import { RegisteredNode, type RegisteredFlowNode } from './RegisteredNode';

/** Asks the canvas to bring one node into view; `seq` repeats a request for the same node. */
export interface CanvasFocusRequest {
  nodeId: string;
  seq: number;
}

/**
 * Below 100% zoom a 10px port label renders under the 10px port-hint floor (04 §4), so a
 * read-only canvas fitted smaller than that hides the labels and keeps only the ports.
 */
const PORT_HINT_MIN_ZOOM = 1;

/**
 * React Flow's default minimum zoom (0.5) is too large to fit a wide topology into the
 * Observe card, which would clip its outer nodes; read-only may zoom out further so the
 * whole graph always shows.
 */
const READ_ONLY_MIN_ZOOM = 0.1;

/** Port-hint floor on screen (04 §4): hint text never renders under 10px at any zoom. */
const PORT_HINT_MIN_PX = 10;

/**
 * Fit margin around the whole graph. A fixed pixel margin, rather than React Flow's
 * default 10%-of-viewport padding, lets a graph that fits the canvas open at 100%.
 */
const FIT_PADDING = '8px';

/** Edit's full-size card; a read-only caller may pass its own node types instead. */
const REGISTERED_NODE_TYPES: NodeTypes = { registered: RegisteredNode };

interface WorkflowCanvasProps<N extends FlowNode> {
  nodes: N[];
  edges: FlowEdge[];
  /**
   * Read-only is a topology snapshot (04 §1.4, Observe): no drag, no connect, no delete.
   * It always shows the whole graph, fitted at up to 100% and re-fitted when the canvas
   * resizes; selecting a node highlights it and never moves the viewport.
   * Selection stays so Observe can pick a NodeRun. `onEdgesChange`/`onConnect` are never
   * attached, so nothing can reach the caller's Definition state even by keyboard.
   * `onNodesChange`, if given, still is: `nodesDraggable={false}` already rules out a
   * position edit reaching it, and it is the only channel React Flow reports a node's
   * measured `dimensions` through, which `fitView` needs (see the `fitView` prop below).
   */
  readOnly?: boolean;
  /** Edit only: zooms onto one node. Observe never passes it (see `readOnly`). */
  focus?: CanvasFocusRequest | null;
  onNodesChange?: (changes: NodeChange<N>[]) => void;
  onEdgesChange?: (changes: EdgeChange<FlowEdge>[]) => void;
  onConnect?: (connection: Connection) => void;
  onSelectNode?: (nodeId: string | null) => void;
  /**
   * Node renderers keyed by node `type`; a module-level constant, since React Flow
   * re-mounts every node when this object changes. Defaults to Edit's `RegisteredNode`.
   */
  nodeTypes?: NodeTypes;
}

/**
 * `<ReactFlow>` wraps its own children in a `ReactFlowProvider` internally (falling back
 * to `Fragment` if one already wraps it), so `CanvasToolbar` — rendered here as a `<Panel>`
 * child — can call `useReactFlow`/`useViewport` without this component adding a second,
 * redundant provider of its own.
 */
export function WorkflowCanvas<N extends FlowNode = RegisteredFlowNode>({
  nodes,
  edges,
  readOnly = false,
  focus = null,
  onNodesChange,
  onEdgesChange,
  onConnect,
  onSelectNode,
  nodeTypes = REGISTERED_NODE_TYPES,
}: WorkflowCanvasProps<N>) {
  const instance = useRef<ReactFlowInstance<N, FlowEdge> | null>(null);
  const [mode, setMode] = useState<InteractionMode>('select');
  const [showGrid, setShowGrid] = useState(true);

  useEffect(() => {
    if (!focus) return;
    void instance.current?.fitView({ nodes: [{ id: focus.nodeId }], duration: 200, maxZoom: 1 });
  }, [focus]);

  const container = useRef<HTMLDivElement>(null);
  const [zoom, setZoom] = useState(1);

  // Read-only re-fits the whole graph whenever its card changes size (a window resize, or
  // the Observe layout switching columns); an editable canvas keeps the user's viewport.
  useEffect(() => {
    const element = container.current;
    if (!readOnly || !element) return;
    const observer = new ResizeObserver(() => {
      void instance.current?.fitView({
        maxZoom: 1,
        minZoom: READ_ONLY_MIN_ZOOM,
        padding: FIT_PADDING,
      });
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, [readOnly]);

  const editHandlers = {
    // Dimension measurement, not editing: kept attached in read-only mode so a caller can
    // feed `dimensions` changes back into `nodes` (see the prop doc above).
    ...(onNodesChange ? { onNodesChange } : {}),
    ...(!readOnly && onEdgesChange ? { onEdgesChange } : {}),
    ...(!readOnly && onConnect ? { onConnect } : {}),
  };

  // An editable canvas keeps its port hints at any zoom: below 100% the hint font grows in
  // flow units by 1/zoom so it still lands at the 10px floor on screen, and at 100% or
  // more it stays the card's own 10px.
  const hintStyle = {
    '--port-hint-font': `${Math.max(PORT_HINT_MIN_PX, PORT_HINT_MIN_PX / zoom)}px`,
  } as CSSProperties;

  return (
    <div
      ref={container}
      className="group/canvas h-full w-full"
      style={hintStyle}
      data-testid="workflow-canvas"
      data-read-only={readOnly}
      data-port-hints={readOnly && zoom < PORT_HINT_MIN_ZOOM ? 'hidden' : 'shown'}
    >
      <ReactFlow<N, FlowEdge>
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
        fitViewOptions={
          readOnly
            ? { maxZoom: 1, minZoom: READ_ONLY_MIN_ZOOM, padding: FIT_PADDING }
            : { maxZoom: 1, padding: FIT_PADDING }
        }
        {...(readOnly ? { minZoom: READ_ONLY_MIN_ZOOM } : {})}
        onInit={(flow) => {
          instance.current = flow;
        }}
        {...editHandlers}
        onNodeClick={(_, node) => onSelectNode?.(node.id)}
        onPaneClick={() => onSelectNode?.(null)}
        proOptions={{ hideAttribution: true }}
      >
        {showGrid ? <Background color="var(--border)" gap={24} /> : null}
        <ZoomReporter onZoom={setZoom} />
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

/** Reports the viewport zoom out of React Flow's store, which only its children can read. */
function ZoomReporter({ onZoom }: { onZoom: (zoom: number) => void }) {
  const zoom = useStore((state) => state.transform[2]);
  useEffect(() => onZoom(zoom), [zoom, onZoom]);
  return null;
}
