import {
  Background,
  Controls,
  ReactFlow,
  type Edge as FlowEdge,
  type Connection,
  type NodeChange,
  type EdgeChange,
} from '@xyflow/react';
import { useMemo } from 'react';

import { RegisteredNode, type RegisteredFlowNode } from './RegisteredNode';

interface WorkflowCanvasProps {
  nodes: RegisteredFlowNode[];
  edges: FlowEdge[];
  readOnly?: boolean;
  onNodesChange?: (changes: NodeChange<RegisteredFlowNode>[]) => void;
  onEdgesChange?: (changes: EdgeChange<FlowEdge>[]) => void;
  onConnect?: (connection: Connection) => void;
  onSelectNode?: (nodeId: string | null) => void;
}

export function WorkflowCanvas({
  nodes,
  edges,
  readOnly = false,
  onNodesChange,
  onEdgesChange,
  onConnect,
  onSelectNode,
}: WorkflowCanvasProps) {
  const nodeTypes = useMemo(() => ({ registered: RegisteredNode }), []);

  return (
    <div className="h-full w-full" data-testid="workflow-canvas">
      <ReactFlow<RegisteredFlowNode, FlowEdge>
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        nodesDraggable={!readOnly}
        nodesConnectable={!readOnly}
        elementsSelectable
        fitView
        {...(onNodesChange ? { onNodesChange } : {})}
        {...(onEdgesChange ? { onEdgesChange } : {})}
        {...(onConnect ? { onConnect } : {})}
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
