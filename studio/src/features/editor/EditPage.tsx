import {
  applyEdgeChanges,
  applyNodeChanges,
  type Connection,
  type Edge as FlowEdge,
  type EdgeChange,
  type NodeChange,
} from '@xyflow/react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useParams, useSearchParams } from 'react-router';

import { NodePalette } from './NodePalette';
import { PropertiesPanel } from './PropertiesPanel';
import { StudioHeader } from './StudioHeader';
import type { RegisteredFlowNode } from './RegisteredNode';
import { WorkflowCanvas } from './WorkflowCanvas';
import {
  ApiRequestError,
  createRun,
  getDefinition,
  listNodeTypes,
  saveDefinition,
  validateDefinition,
} from '@/api/client';
import type {
  Definition,
  Edge,
  JsonObject,
  Node,
  NodeMetadata,
  ValidationError,
} from '@/api/types';
import { Badge } from '@/components/ui/badge';
import { RunInputDialog } from '@/features/run-input/RunInputDialog';
import { useStudioStore } from '@/stores/studio-store';

export function EditPage() {
  const { workflowId = '' } = useParams();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();

  const selectedNodeId = useStudioStore((s) => s.selectedNodeId);
  const selectNode = useStudioStore((s) => s.selectNode);
  const hasUnsavedChanges = useStudioStore((s) => s.hasUnsavedChanges);
  const setUnsavedChanges = useStudioStore((s) => s.setUnsavedChanges);

  const [definition, setDefinition] = useState<Definition | null>(null);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [edges, setEdges] = useState<Edge[]>([]);
  const [positions, setPositions] = useState<Record<string, { x: number; y: number }>>({});
  const [nodeTypes, setNodeTypes] = useState<NodeMetadata[]>([]);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [validationErrors, setValidationErrors] = useState<ValidationError[] | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // Captured once, at mount, from the `?run=1` the Definitions table's Run action links
  // here with. `RunInputDialog` is only rendered once `definition` has loaded (see below),
  // so this lazy initial value alone reproduces "opens once the definition has loaded"
  // without ever reacting to the query string again — later URL changes (including the
  // param-clearing effect right below) cannot flip it back.
  const [runDialogOpen, setRunDialogOpen] = useState(() => searchParams.get('run') === '1');
  const [runError, setRunError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();

    listNodeTypes(controller.signal)
      .then(setNodeTypes)
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setLoadError(describeError(error, 'Could not load node types'));
      });

    getDefinition(workflowId, controller.signal)
      .then((loaded) => {
        setDefinition(loaded);
        setNodes(loaded.nodes);
        setEdges(loaded.edges);
        setPositions(Object.fromEntries(loaded.nodes.map((node) => [node.id, node.position])));
        setUnsavedChanges(false);
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setLoadError(describeError(error, 'Could not load definition'));
      });

    return () => controller.abort();
  }, [workflowId, setUnsavedChanges]);

  // This effect's only job is to drop the one-shot `run` param afterwards so a reload or
  // Back does not reopen the dialog. It runs at mount — well before the async Definition
  // fetch above can resolve — so the URL is already settled long before `definition` loads
  // and `RunInputDialog` below actually mounts; there is no window where the dialog's open
  // state and the URL disagree for a query-param-driven open.
  const consumedRunParam = useRef(false);
  useEffect(() => {
    if (consumedRunParam.current) return;
    if (searchParams.get('run') !== '1') return;
    consumedRunParam.current = true;
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.delete('run');
        return next;
      },
      { replace: true },
    );
  }, [searchParams, setSearchParams]);

  const metadataByType = useMemo(
    () => new Map(nodeTypes.map((metadata) => [metadata.type, metadata])),
    [nodeTypes],
  );

  const flowNodes = useMemo<RegisteredFlowNode[]>(
    () =>
      nodes.map((node) => ({
        id: node.id,
        type: 'registered' as const,
        position: positions[node.id] ?? node.position,
        selected: node.id === selectedNodeId,
        data: { label: node.name, metadata: metadataByType.get(node.type) },
      })),
    [nodes, positions, metadataByType, selectedNodeId],
  );

  const flowEdges = useMemo<FlowEdge[]>(
    () =>
      edges.map((edge) => ({
        id: edge.id,
        source: edge.source,
        sourceHandle: edge.sourceHandle,
        target: edge.target,
        targetHandle: edge.targetHandle,
      })),
    [edges],
  );

  const selectedNode = nodes.find((node) => node.id === selectedNodeId) ?? null;

  const markDirty = useCallback(() => {
    setUnsavedChanges(true);
    setValidationErrors(null);
    setNotice(null);
  }, [setUnsavedChanges]);

  const onNodesChange = useCallback(
    (changes: NodeChange<RegisteredFlowNode>[]) => {
      const next = applyNodeChanges(changes, flowNodes);
      setPositions(Object.fromEntries(next.map((node) => [node.id, node.position])));
      const removed = new Set(next.map((node) => node.id));
      setNodes((prev) => prev.filter((node) => removed.has(node.id)));
      if (changes.some((change) => change.type !== 'select')) markDirty();
    },
    [flowNodes, markDirty],
  );

  const onEdgesChange = useCallback(
    (changes: EdgeChange<FlowEdge>[]) => {
      const next = applyEdgeChanges(changes, flowEdges);
      const kept = new Set(next.map((edge) => edge.id));
      setEdges((prev) => prev.filter((edge) => kept.has(edge.id)));
      if (changes.some((change) => change.type !== 'select')) markDirty();
    },
    [flowEdges, markDirty],
  );

  const onConnect = useCallback(
    (connection: Connection) => {
      if (!connection.source || !connection.target) return;
      setEdges((prev) => [
        ...prev,
        {
          id: `edge_${prev.length + 1}_${connection.source}_${connection.target}`,
          source: connection.source,
          sourceHandle: connection.sourceHandle ?? '',
          target: connection.target,
          targetHandle: connection.targetHandle ?? '',
        },
      ]);
      markDirty();
    },
    [markDirty],
  );

  const addNode = useCallback(
    (metadata: NodeMetadata) => {
      const id = `node_${metadata.type}_${Date.now()}`;
      const position = { x: 80 + nodes.length * 40, y: 80 + nodes.length * 30 };
      setNodes((prev) => [
        ...prev,
        { id, type: metadata.type, name: metadata.displayName, position, config: {} },
      ]);
      setPositions((prev) => ({ ...prev, [id]: position }));
      selectNode(id);
      markDirty();
    },
    [nodes.length, selectNode, markDirty],
  );

  const currentDraft = useCallback(
    () => ({
      name: definition?.name ?? 'Untitled workflow',
      description: definition?.description ?? '',
      nodes: nodes.map((node) => ({
        ...node,
        position: positions[node.id] ?? node.position,
      })),
      edges,
    }),
    [definition, nodes, positions, edges],
  );

  const handleValidate = useCallback(async () => {
    setBusy(true);
    setNotice(null);
    try {
      const result = await validateDefinition(currentDraft());
      if (result.valid) {
        setValidationErrors(null);
        setNotice('Definition is valid.');
      } else {
        setValidationErrors(result.errors);
      }
    } catch (error) {
      setNotice(describeError(error, 'Validation request failed'));
    } finally {
      setBusy(false);
    }
  }, [currentDraft]);

  const handleSave = useCallback(async () => {
    if (!definition) return;
    setBusy(true);
    setNotice(null);
    try {
      const saved = await saveDefinition(workflowId, {
        ...currentDraft(),
        baseVersion: definition.version,
      });
      setDefinition(saved);
      setValidationErrors(null);
      setUnsavedChanges(false);
      setNotice(`Saved version ${saved.version}.`);
    } catch (error) {
      // A conflicting save keeps the local edits; the Backend created no version.
      if (error instanceof ApiRequestError && error.code === 'VERSION_CONFLICT') {
        setNotice(
          'This definition changed on the server since you loaded it. Your local edits are preserved; reload to see the latest version before saving again.',
        );
      } else {
        setNotice(describeError(error, 'Save failed'));
      }
    } finally {
      setBusy(false);
    }
  }, [definition, workflowId, currentDraft, setUnsavedChanges]);

  const handleCreateRun = useCallback(
    async (input: JsonObject) => {
      if (!definition) return;
      setBusy(true);
      setRunError(null);
      try {
        const run = await createRun({
          workflowId,
          definitionVersion: definition.version,
          input,
        });
        setRunDialogOpen(false);
        // Creating a Run switches Studio into observe mode.
        navigate(`/runs/${run.id}`);
      } catch (error) {
        setRunError(describeError(error, 'Could not create run'));
      } finally {
        setBusy(false);
      }
    },
    [definition, workflowId, navigate],
  );

  return (
    <div className="dark flex h-screen flex-col bg-[var(--background)] text-[var(--foreground)]">
      <StudioHeader
        name={definition?.name ?? workflowId}
        workflowId={workflowId}
        version={definition?.version ?? null}
        hasUnsavedChanges={hasUnsavedChanges}
        busy={busy}
        onValidate={() => void handleValidate()}
        onSave={() => void handleSave()}
        onRun={() => setRunDialogOpen(true)}
      />

      {loadError ? (
        <p role="alert" className="border-b border-[var(--border)] px-4 py-2 text-xs text-red-500">
          {loadError}
        </p>
      ) : null}

      {notice ? (
        <p className="border-b border-[var(--border)] px-4 py-2 text-xs text-[var(--muted-foreground)]">
          {notice}
        </p>
      ) : null}

      {validationErrors ? (
        <ul role="alert" className="border-b border-[var(--border)] px-4 py-2 text-xs text-red-500">
          {validationErrors.map((error) => (
            <li key={`${error.code}-${error.path}`}>
              <Badge variant="failed">{error.code}</Badge> {error.path} — {error.message}
            </li>
          ))}
        </ul>
      ) : null}

      <div className="flex min-h-0 flex-1">
        <NodePalette nodeTypes={nodeTypes} onAdd={addNode} error={loadError} />
        <div className="min-w-0 flex-1">
          <WorkflowCanvas
            nodes={flowNodes}
            edges={flowEdges}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            onSelectNode={selectNode}
          />
        </div>
        <PropertiesPanel
          node={selectedNode}
          metadata={selectedNode ? metadataByType.get(selectedNode.type) : undefined}
          onRename={(name) => {
            setNodes((prev) =>
              prev.map((node) => (node.id === selectedNodeId ? { ...node, name } : node)),
            );
            markDirty();
          }}
          onConfigChange={(config) => {
            setNodes((prev) =>
              prev.map((node) => (node.id === selectedNodeId ? { ...node, config } : node)),
            );
            markDirty();
          }}
        />
      </div>

      {definition ? (
        <RunInputDialog
          open={runDialogOpen}
          onClose={() => setRunDialogOpen(false)}
          workflowId={workflowId}
          definitionVersion={definition.version}
          runInputSchema={definition.runInputSchema}
          submitting={busy}
          errorMessage={runError}
          onSubmit={(input) => void handleCreateRun(input)}
        />
      ) : null}
    </div>
  );
}

function describeError(error: unknown, fallback: string): string {
  if (error instanceof ApiRequestError) return `${error.code}: ${error.message}`;
  if (error instanceof Error) return `${fallback}: ${error.message}`;
  return fallback;
}
