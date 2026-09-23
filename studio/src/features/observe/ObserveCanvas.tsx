import {
  applyNodeChanges,
  MarkerType,
  type Edge as FlowEdge,
  type NodeChange,
  type NodeTypes,
} from '@xyflow/react';
import { useCallback, useMemo, useState } from 'react';

import { CompactStatusNode, type CompactStatusFlowNode } from './CompactStatusNode';
import { useBoundDefinition, type BoundDefinition } from './boundDefinition';
import {
  COMPACT_NODE_HEIGHT,
  COMPACT_NODE_WIDTH,
  compactEdgeSides,
  compactHandleId,
  compactTopologyExtent,
  compactTopologyPositions,
} from './topology';
import type { Definition, NodeRun } from '@/api/types';
import { WorkflowCanvas } from '@/features/editor/WorkflowCanvas';

const COMPACT_NODE_TYPES: NodeTypes = { compactStatus: CompactStatusNode };

/** WorkflowCanvas's 8px fit margin on both sides, plus a pixel of rounding slack. */
const FIT_MARGIN_PX = 2 * 8 + 2;

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
 * (fitted at up to 100%, and at exactly 100% for any graph no wider than the card; see
 * `compactTopologyPositions`); a selection never zooms the viewport onto one node.
 *
 * Nodes render as compact name-and-status boxes on their own display layout
 * (`compactTopologyPositions`): the Edit canvas's full-size cards fitted into this card
 * shrank every label far below the 12px floor.
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
  const { definition, error } = bound ?? own;

  const positions = useMemo(
    () =>
      definition
        ? compactTopologyPositions(definition)
        : new Map<string, { x: number; y: number }>(),
    [definition],
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

  const flowNodes = useMemo<CompactStatusFlowNode[]>(
    () =>
      (definition?.nodes ?? []).map((node) => ({
        id: node.id,
        type: 'compactStatus' as const,
        position: positions.get(node.id) ?? { x: 0, y: 0 },
        width: COMPACT_NODE_WIDTH,
        height: COMPACT_NODE_HEIGHT,
        ...(measured[node.id] ? { measured: measured[node.id] } : {}),
        selected: node.id === selectedDefinitionNodeId,
        data: {
          label: node.name,
          nodeRunStatus: nodeRunByNodeId.get(node.id)?.status,
        },
      })),
    [definition, positions, nodeRunByNodeId, selectedDefinitionNodeId, measured],
  );

  const onNodesChange = useCallback(
    (changes: NodeChange<CompactStatusFlowNode>[]) => {
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

  // Straight arrows between node sides; the port an edge binds is Edit-canvas detail.
  const flowEdges = useMemo<FlowEdge[]>(
    () =>
      (definition?.edges ?? []).map((edge) => {
        const sides = compactEdgeSides(
          positions.get(edge.source) ?? { x: 0, y: 0 },
          positions.get(edge.target) ?? { x: 0, y: 0 },
        );
        return {
          id: edge.id,
          type: 'straight',
          source: edge.source,
          sourceHandle: compactHandleId('source', sides.from),
          target: edge.target,
          targetHandle: compactHandleId('target', sides.to),
          style: { stroke: 'var(--muted-foreground)', strokeWidth: 1.5 },
          markerEnd: {
            type: MarkerType.ArrowClosed,
            width: 16,
            height: 16,
            color: 'var(--muted-foreground)',
          },
        };
      }),
    [definition, positions],
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

  // The card grows to the whole layout's height (the Run view's top row grows with it and
  // the page scrolls), so a deep graph is never fitted below 100%, where its 12px status
  // text would drop under the 04 §4 helper-text floor.
  const minHeight = definition ? compactTopologyExtent(positions).height + FIT_MARGIN_PX : 0;

  return (
    <div
      data-testid="observe-canvas"
      className="h-full min-h-0 w-full"
      style={minHeight > 0 ? { minHeight } : undefined}
    >
      {error ? (
        <p className="p-3 text-sm text-[var(--destructive)]">{error}</p>
      ) : !definition ? (
        <p className="p-3 text-sm text-[var(--muted-foreground)]">Loading topology…</p>
      ) : (
        <WorkflowCanvas<CompactStatusFlowNode>
          nodes={flowNodes}
          edges={flowEdges}
          nodeTypes={COMPACT_NODE_TYPES}
          readOnly
          onNodesChange={onNodesChange}
          onSelectNode={onSelectDefinitionNode}
        />
      )}
    </div>
  );
}
