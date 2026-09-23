import { applyNodeChanges, type Edge as FlowEdge, type NodeChange } from '@xyflow/react';
import { useCallback, useMemo, useState } from 'react';

import { useBoundDefinition, type BoundDefinition } from './boundDefinition';
import type { Definition, NodeRun } from '@/api/types';
import type { RegisteredFlowNode } from '@/features/editor/RegisteredNode';
import { WorkflowCanvas } from '@/features/editor/WorkflowCanvas';

interface ObserveCanvasProps {
  workflowId: string;
  /** The Run's own bound Definition version (Runtime safety rule #8). Never the latest. */
  definitionVersion: number;
  nodeRuns: NodeRun[];
  selectedNodeRunId: string | null;
  onSelectNodeRun: (nodeRunId: string | null) => void;
  /**
   * The bound Definition already read by the page. When given, the Canvas renders it and
   * issues no request of its own; when omitted, it reads the same immutable version itself.
   */
  bound?: BoundDefinition;
}

/**
 * Read-only topology snapshot of the Run's bound Definition version (04 §1.3/§1.4). Each
 * node shows the status of its own NodeRun, if one exists yet; a node with no NodeRun
 * renders idle. Selection is shared with the Timeline and Run Rail through the NodeRun id
 * (04 §3.1): a Canvas click resolves to that node's NodeRun, and selecting a NodeRun
 * elsewhere highlights its Definition node here. The card always shows the whole graph
 * (fitted at up to 100%); a selection never zooms the viewport onto one node.
 */
export function ObserveCanvas({
  workflowId,
  definitionVersion,
  nodeRuns,
  selectedNodeRunId,
  onSelectNodeRun,
  bound,
}: ObserveCanvasProps) {
  const own = useBoundDefinition(workflowId, definitionVersion, bound !== undefined);
  const { definition, nodeTypes, error } = bound ?? own;

  const metadataByType = useMemo(
    () => new Map(nodeTypes.map((metadata) => [metadata.type, metadata])),
    [nodeTypes],
  );
  const nodeRunByNodeId = useMemo(() => {
    const map = new Map<string, NodeRun>();
    for (const nodeRun of nodeRuns) map.set(nodeRun.nodeId, nodeRun);
    return map;
  }, [nodeRuns]);

  const selectedDefinitionNodeId =
    nodeRuns.find((nodeRun) => nodeRun.id === selectedNodeRunId)?.nodeId ?? null;

  // React Flow reports a node's rendered size through a `dimensions` NodeChange once it has
  // measured the DOM, and `fitView` waits for every node to carry one (WorkflowCanvas's own
  // `fitView` prop comment). A NodeRun status update gives `nodeRuns` a new reference and
  // rebuilds `flowNodes` below with brand-new node objects on every live update from the
  // Run's SSE stream; without re-applying the already-known measurement to each new object,
  // every such update makes React Flow treat the node as unmeasured again, and a `fitView`
  // landing in that window clips it at the canvas edge.
  const [measuredState, setMeasuredState] = useState<{
    definition: Definition | null;
    values: Record<string, { width: number; height: number }>;
  }>({ definition, values: {} });
  // A new Definition object (the initial fetch, or a different Run reusing this same
  // mounted Canvas) must not carry over another topology's sizes; this is React's "adjust
  // state during render" pattern, not an effect, so it never triggers the extra
  // render-after-commit an effect-based reset would.
  if (measuredState.definition !== definition) {
    setMeasuredState({ definition, values: {} });
  }
  const measured = useMemo(
    () => (measuredState.definition === definition ? measuredState.values : {}),
    [measuredState, definition],
  );
  const setMeasured = (
    updater: (
      prev: Record<string, { width: number; height: number }>,
    ) => Record<string, { width: number; height: number }>,
  ) => {
    setMeasuredState((prev) => ({ definition: prev.definition, values: updater(prev.values) }));
  };

  const flowNodes = useMemo<RegisteredFlowNode[]>(
    () =>
      (definition?.nodes ?? []).map((node) => ({
        id: node.id,
        type: 'registered' as const,
        position: node.position,
        ...(measured[node.id] ? { measured: measured[node.id] } : {}),
        selected: node.id === selectedDefinitionNodeId,
        data: {
          label: node.name,
          metadata: metadataByType.get(node.type),
          // The bound Definition version's own config, so the shared card summary states
          // the configured facts (model ID, input key) instead of empty-config placeholders.
          config: node.config,
          nodeRunStatus: nodeRunByNodeId.get(node.id)?.status,
        },
      })),
    [definition, metadataByType, nodeRunByNodeId, selectedDefinitionNodeId, measured],
  );

  const onNodesChange = useCallback(
    (changes: NodeChange<RegisteredFlowNode>[]) => {
      const next = applyNodeChanges(changes, flowNodes);
      setMeasured((prev) => {
        let changed = false;
        const merged = { ...prev };
        for (const node of next) {
          if (node.measured?.width !== undefined && node.measured.height !== undefined) {
            merged[node.id] = { width: node.measured.width, height: node.measured.height };
            changed = true;
          }
        }
        return changed ? merged : prev;
      });
    },
    [flowNodes],
  );

  const flowEdges = useMemo<FlowEdge[]>(
    () =>
      (definition?.edges ?? []).map((edge) => ({
        id: edge.id,
        source: edge.source,
        sourceHandle: edge.sourceHandle,
        target: edge.target,
        targetHandle: edge.targetHandle,
      })),
    [definition],
  );

  const onSelectDefinitionNode = (definitionNodeId: string | null) => {
    if (definitionNodeId === null) {
      // Blank-area click returns to the Run Summary (04 §3.1).
      onSelectNodeRun(null);
      return;
    }
    const nodeRun = nodeRunByNodeId.get(definitionNodeId);
    // A node with no NodeRun yet (idle) has nothing to select; the current selection holds.
    if (nodeRun) onSelectNodeRun(nodeRun.id);
  };

  return (
    <div className="h-full min-h-0 w-full">
      {error ? (
        <p className="p-3 text-sm text-[var(--destructive)]">{error}</p>
      ) : !definition ? (
        <p className="p-3 text-sm text-[var(--muted-foreground)]">Loading topology…</p>
      ) : (
        <WorkflowCanvas
          nodes={flowNodes}
          edges={flowEdges}
          readOnly
          onNodesChange={onNodesChange}
          onSelectNode={onSelectDefinitionNode}
        />
      )}
    </div>
  );
}
