import { applyNodeChanges, type Edge as FlowEdge, type NodeChange } from '@xyflow/react';
import { useCallback, useEffect, useMemo, useState } from 'react';

import { ApiRequestError, getDefinitionVersion, listNodeTypes } from '@/api/client';
import type { Definition, NodeMetadata, NodeRun } from '@/api/types';
import type { RegisteredFlowNode } from '@/features/editor/RegisteredNode';
import { WorkflowCanvas, type CanvasFocusRequest } from '@/features/editor/WorkflowCanvas';

interface ObserveCanvasProps {
  workflowId: string;
  /** The Run's own bound Definition version (Runtime safety rule #8). Never the latest. */
  definitionVersion: number;
  nodeRuns: NodeRun[];
  selectedNodeRunId: string | null;
  onSelectNodeRun: (nodeRunId: string | null) => void;
}

/**
 * Read-only topology snapshot of the Run's bound Definition version (04 §1.3/§1.4). Each
 * node shows the status of its own NodeRun, if one exists yet; a node with no NodeRun
 * renders idle. Selection is shared with the Timeline and Run Rail through the NodeRun id
 * (04 §3.1): a Canvas click resolves to that node's NodeRun, and selecting a NodeRun
 * elsewhere focuses its Definition node here.
 */
export function ObserveCanvas({
  workflowId,
  definitionVersion,
  nodeRuns,
  selectedNodeRunId,
  onSelectNodeRun,
}: ObserveCanvasProps) {
  const { definition, nodeTypes, error } = useBoundDefinition(workflowId, definitionVersion);

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
  // mounted Canvas) must not carry over another topology's sizes; this is the same
  // "adjust state during render" pattern `useFocusRequest` below uses, not an effect, so it
  // never triggers the extra render-after-commit an effect-based reset would.
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

  const focus = useFocusRequest(selectedDefinitionNodeId);

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
    <div className="h-64 shrink-0 border-b border-[var(--border)]">
      {error ? (
        <p className="p-3 text-xs text-[var(--destructive)]">{error}</p>
      ) : !definition ? (
        <p className="p-3 text-xs text-[var(--muted-foreground)]">Loading topology…</p>
      ) : (
        <WorkflowCanvas
          nodes={flowNodes}
          edges={flowEdges}
          readOnly
          focus={focus}
          onNodesChange={onNodesChange}
          onSelectNode={onSelectDefinitionNode}
        />
      )}
    </div>
  );
}

/**
 * Fetches the Run's own bound Definition version and the registered Node Types needed to
 * render it, exactly as the Editor does for its own Canvas. This is a read-only lookup by
 * immutable version (08 §3): it never falls back to the Definition's latest version.
 */
function useBoundDefinition(
  workflowId: string,
  definitionVersion: number,
): { definition: Definition | null; nodeTypes: NodeMetadata[]; error: string | null } {
  const [state, setState] = useState<{
    definition: Definition | null;
    nodeTypes: NodeMetadata[];
    error: string | null;
  }>({ definition: null, nodeTypes: [], error: null });

  useEffect(() => {
    const controller = new AbortController();
    Promise.all([
      getDefinitionVersion(workflowId, definitionVersion, controller.signal),
      listNodeTypes(controller.signal),
    ])
      .then(([definition, nodeTypes]) => setState({ definition, nodeTypes, error: null }))
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setState({
          definition: null,
          nodeTypes: [],
          error:
            error instanceof ApiRequestError
              ? `${error.code}: ${error.message}`
              : 'Could not load the workflow topology',
        });
      });
    return () => controller.abort();
  }, [workflowId, definitionVersion]);

  return state;
}

/**
 * Bumps `seq` each time the selected Definition node id changes, so the Canvas re-runs
 * `fitView` even when the newly selected NodeRun maps back to the same node (`focus`'s own
 * contract requires a changed `seq` to repeat a request for the same node id). This is
 * React's own "adjust state during render" pattern (comparing a prop to state and calling
 * the setter inline): it needs no effect and no ref, since a ref must never be read during
 * render.
 */
function useFocusRequest(definitionNodeId: string | null): CanvasFocusRequest | null {
  const [state, setState] = useState({ nodeId: definitionNodeId, seq: 0 });
  if (definitionNodeId !== state.nodeId) {
    setState({ nodeId: definitionNodeId, seq: state.seq + 1 });
  }
  return state.nodeId ? { nodeId: state.nodeId, seq: state.seq } : null;
}
