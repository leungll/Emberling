import {
  applyEdgeChanges,
  applyNodeChanges,
  type Connection,
  type Edge as FlowEdge,
  type EdgeChange,
  type NodeChange,
} from '@xyflow/react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router';

import { NodePalette } from './NodePalette';
import { PropertiesPanel } from './PropertiesPanel';
import { RecentExecutionBar } from './RecentExecutionBar';
import { StudioHeader } from './StudioHeader';
import type { CanvasFocusRequest } from './WorkflowCanvas';
import { NODE_CARD_HEIGHT, NODE_CARD_WIDTH, type RegisteredFlowNode } from './RegisteredNode';
import { WorkflowCanvas } from './WorkflowCanvas';
import {
  ApiRequestError,
  createDefinition,
  createRun,
  getDefinition,
  getRun,
  listDefinitions,
  listModels,
  listNodeTypes,
  saveDefinition,
  validateDefinition,
} from '@/api/client';
import type {
  Definition,
  Edge,
  JsonObject,
  ModelMetadata,
  Node,
  NodeMetadata,
  RunSnapshot,
  ValidationError,
} from '@/api/types';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { RunInputDialog } from '@/features/run-input/RunInputDialog';
import { useStudioStore } from '@/stores/studio-store';

/**
 * Router state a "New Definition" navigation from Definitions carries. POST /definitions
 * creates the first version with no baseVersion (08 §3.1), but since
 * backend/internal/runtime/graph.go rejects a graph with no Output Node, that POST is
 * deferred until the first Save.
 */
interface NewDefinitionState {
  name?: string;
  description?: string;
}

/**
 * The unsaved name/description of a not-yet-created Definition. 08 §1: MVP defines no
 * server-side DRAFT state, so this only ever lives in this component's memory; it never
 * becomes a Definition version until the first Save.
 */
interface DraftMeta {
  name: string;
  description: string;
}

/** Finds the node a Backend `ValidationError.path` of shape `nodes[<id>]...` names. */
function nodeIdFromPath(path: string): string | undefined {
  return /^nodes\[([^\]]+)\]/.exec(path)?.[1];
}

/**
 * Finds the top-level config field a node-config ValidationError.path names, matching
 * `runtime.schemaValidationErrors`' `"nodes[" + id + "].config" + jsonPointer` shape
 * (backend/internal/runtime/configschema.go). A path that never enters `.config` (a port
 * or whole-graph error) has no field to point a form control at.
 */
function configFieldFromPath(path: string): string | undefined {
  const marker = '.config';
  const at = path.indexOf(marker);
  if (at === -1) return undefined;
  const rest = path.slice(at + marker.length).replace(/^\//, '');
  return rest === '' ? undefined : rest.split('/')[0];
}

/** Extracts the structured errors a rejected Save's 400/422 envelope carries, if any. */
function extractValidationErrors(error: unknown): ValidationError[] | null {
  if (!(error instanceof ApiRequestError)) return null;
  const details = error.body.error.details;
  if (typeof details !== 'object' || details === null || Array.isArray(details)) return null;
  const errors = (details as { errors?: unknown }).errors;
  return Array.isArray(errors) ? (errors as ValidationError[]) : null;
}

export function EditPage() {
  // `/studio/new` (no :workflowId) is a brand-new, unsaved Definition; `/studio/:workflowId`
  // is a saved one. They are distinct routes, not a sentinel value, so a real workflowId can
  // never collide with the literal segment "new".
  const { workflowId: workflowIdParam } = useParams();
  const isNewDraft = workflowIdParam === undefined;
  const workflowId = workflowIdParam ?? '';
  const location = useLocation();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();

  const selectedNodeId = useStudioStore((s) => s.selectedNodeId);
  const selectNode = useStudioStore((s) => s.selectNode);
  const hasUnsavedChanges = useStudioStore((s) => s.hasUnsavedChanges);
  const setUnsavedChanges = useStudioStore((s) => s.setUnsavedChanges);

  // Captured once, at mount: later edits to Definition name/description do not come from
  // this router state, and a later re-render must not re-read a stale location.state.
  const [draftMeta] = useState<DraftMeta | null>(() => {
    if (!isNewDraft) return null;
    const state = (location.state as NewDefinitionState | null) ?? null;
    return { name: state?.name ?? 'Untitled workflow', description: state?.description ?? '' };
  });

  const [definition, setDefinition] = useState<Definition | null>(null);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [edges, setEdges] = useState<Edge[]>([]);
  const [positions, setPositions] = useState<Record<string, { x: number; y: number }>>({});
  // React Flow reports each node's rendered size through a `dimensions` NodeChange once it
  // has measured the DOM; that measurement decides visibility and `fitView` (WorkflowCanvas
  // §6 canvas defect). It is kept out of `nodes`/`positions` so it never marks the
  // Definition dirty, but it still has to be fed back into the controlled `nodes` prop or
  // every render looks unmeasured again and the canvas never becomes visible.
  const [measured, setMeasured] = useState<Record<string, { width: number; height: number }>>({});
  const [nodeTypes, setNodeTypes] = useState<NodeMetadata[]>([]);
  const [models, setModels] = useState<ModelMetadata[]>([]);
  const [recentRun, setRecentRun] = useState<RunSnapshot | null>(null);
  const [recentRunLoaded, setRecentRunLoaded] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [validationErrors, setValidationErrors] = useState<ValidationError[] | null>(null);
  const [conflict, setConflict] = useState<Definition | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [focusRequest, setFocusRequest] = useState<CanvasFocusRequest | null>(null);
  // Captured once, at mount, from the `?run=1` the Definitions table's Run action links
  // here with. `RunInputDialog` is only rendered once `definition` has loaded (see below),
  // so this lazy initial value alone reproduces "opens once the definition has loaded"
  // without ever reacting to the query string again — later URL changes (including the
  // param-clearing effect right below) cannot flip it back.
  const [runDialogOpen, setRunDialogOpen] = useState(() => searchParams.get('run') === '1');
  const [runError, setRunError] = useState<string | null>(null);

  const applyDefinition = useCallback(
    (loaded: Definition) => {
      setDefinition(loaded);
      setNodes(loaded.nodes);
      setEdges(loaded.edges);
      setPositions(Object.fromEntries(loaded.nodes.map((node) => [node.id, node.position])));
      setMeasured({});
      setValidationErrors(null);
      setConflict(null);
      setUnsavedChanges(false);
    },
    [setUnsavedChanges],
  );

  useEffect(() => {
    const controller = new AbortController();

    listNodeTypes(controller.signal)
      .then(setNodeTypes)
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setLoadError(describeError(error, 'Could not load node types'));
      });

    listModels(controller.signal)
      .then(setModels)
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setLoadError(describeError(error, 'Could not load models'));
      });

    // A brand-new Definition has nothing to fetch yet; it stays an empty, unsaved graph
    // until the first Save creates version 1.
    if (!isNewDraft) {
      getDefinition(workflowId, controller.signal)
        .then(applyDefinition)
        .catch((error: unknown) => {
          if (controller.signal.aborted) return;
          setLoadError(describeError(error, 'Could not load definition'));
        });

      // The Recent Execution bar (04 §2.6) reads the Definition list's `lastRun` summary,
      // then one `GET /runs/:id` for duration/Event count when a Run exists. A brand-new
      // draft cannot have a Run yet, so this only runs for an existing workflowId. A
      // failure here only empties the bar; it must never block or error the rest of Edit.
      listDefinitions(controller.signal)
        .then((items) => {
          const lastRun = items.find((item) => item.workflowId === workflowId)?.lastRun ?? null;
          if (!lastRun) {
            setRecentRun(null);
            setRecentRunLoaded(true);
            return;
          }
          return getRun(lastRun.id, controller.signal).then((snapshot) => {
            setRecentRun(snapshot);
            setRecentRunLoaded(true);
          });
        })
        .catch((_error: unknown) => {
          if (controller.signal.aborted) return;
          setRecentRun(null);
          setRecentRunLoaded(true);
        });
    }

    return () => controller.abort();
  }, [workflowId, isNewDraft, applyDefinition]);

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
        // Every card renders at this fixed CSS size (RegisteredNode); declaring it here
        // lets React Flow skip its measure-then-reveal pass on a controlled `nodes` array
        // (see NODE_CARD_WIDTH/HEIGHT). The `measured` feedback below still applies on top
        // for the internal dimension change React Flow reports after mounting each card.
        width: NODE_CARD_WIDTH,
        height: NODE_CARD_HEIGHT,
        ...(measured[node.id] ? { measured: measured[node.id] } : {}),
        selected: node.id === selectedNodeId,
        data: {
          label: node.name,
          metadata: metadataByType.get(node.type),
          config: node.config,
          models,
        },
      })),
    [nodes, positions, measured, metadataByType, selectedNodeId, models],
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
      const remaining = new Set(next.map((node) => node.id));
      setNodes((prev) => prev.filter((node) => remaining.has(node.id)));
      // `dimensions` is React Flow reporting its own measurement, and `select` is pure
      // canvas UI state; neither changes what Save would persist, so neither may mark the
      // Definition dirty (06 canvas defect: this used to flag every initial render as an
      // edit).
      if (changes.some((change) => change.type !== 'select' && change.type !== 'dimensions')) {
        markDirty();
      }
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
      name: definition?.name ?? draftMeta?.name ?? 'Untitled workflow',
      description: definition?.description ?? draftMeta?.description ?? '',
      nodes: nodes.map((node) => ({
        ...node,
        position: positions[node.id] ?? node.position,
      })),
      edges,
    }),
    [definition, draftMeta, nodes, positions, edges],
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
    setBusy(true);
    setNotice(null);
    try {
      if (isNewDraft) {
        const created = await createDefinition(currentDraft());
        setUnsavedChanges(false);
        // The Definition now exists: hand off to the saved-version route, which loads it
        // fresh rather than trusting this response as the new source of truth.
        navigate(`/studio/${created.workflowId}`, { replace: true });
        return;
      }
      if (!definition) return;
      const saved = await saveDefinition(workflowId, {
        ...currentDraft(),
        baseVersion: definition.version,
      });
      setDefinition(saved);
      setValidationErrors(null);
      setConflict(null);
      setUnsavedChanges(false);
      setNotice(`Saved version ${saved.version}.`);
    } catch (error) {
      if (error instanceof ApiRequestError && error.code === 'VERSION_CONFLICT') {
        // The Backend never includes the newer version in a 409 body (it names no
        // resource to avoid leaking one Definition's content into another's error path),
        // so naming it to the user needs a fresh read. Local edits are untouched either
        // way (04 §2.6): nothing here may overwrite them.
        try {
          setConflict(await getDefinition(workflowId));
        } catch (reloadError) {
          setNotice(
            describeError(reloadError, 'A newer version exists, but it could not be loaded'),
          );
        }
      } else {
        const errors = extractValidationErrors(error);
        if (errors) setValidationErrors(errors);
        else setNotice(describeError(error, 'Save failed'));
      }
    } finally {
      setBusy(false);
    }
  }, [isNewDraft, definition, workflowId, currentDraft, navigate, setUnsavedChanges]);

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

  const focusValidationError = useCallback(
    (nodeId: string) => {
      selectNode(nodeId);
      setFocusRequest((prev) => ({ nodeId, seq: (prev?.seq ?? 0) + 1 }));
    },
    [selectNode],
  );

  // Backend validation errors that name the selected node's config, keyed by the
  // top-level field they point at (04 §2's "clicking an entry focuses the offending
  // node"): SchemaForm renders each list under its field, so the field itself does not
  // need its own lookup UI.
  const selectedNodeFieldErrors = useMemo(() => {
    if (!validationErrors || !selectedNodeId) return {};
    const byField: Record<string, string[]> = {};
    for (const error of validationErrors) {
      const nodeId = error.nodeId ?? nodeIdFromPath(error.path);
      if (nodeId !== selectedNodeId) continue;
      const field = configFieldFromPath(error.path);
      if (!field) continue;
      (byField[field] ??= []).push(error.message);
    }
    return byField;
  }, [validationErrors, selectedNodeId]);

  return (
    <div className="dark flex h-screen flex-col bg-[var(--background)] text-[var(--foreground)]">
      <StudioHeader
        name={definition?.name ?? draftMeta?.name ?? workflowId}
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

      {conflict ? (
        <div
          role="alert"
          aria-label="Version conflict"
          className="space-y-2 border-b border-[var(--border)] px-4 py-2 text-xs text-amber-500"
        >
          <p>
            The definition was saved as v{conflict.version} on the server while you were editing v
            {definition?.version ?? '?'} locally. Reload to see the latest version, or keep your
            edits and compare before saving again.
          </p>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => setConflict(null)}>
              Keep my edits
            </Button>
            <Button size="sm" onClick={() => applyDefinition(conflict)}>
              Reload latest
            </Button>
          </div>
        </div>
      ) : null}

      {validationErrors ? (
        <ul
          role="list"
          aria-label="Validation errors"
          className="space-y-1 border-b border-[var(--border)] px-4 py-2 text-xs text-red-500"
        >
          {validationErrors.map((error) => {
            const nodeId = error.nodeId ?? nodeIdFromPath(error.path);
            const node = nodeId ? nodes.find((candidate) => candidate.id === nodeId) : undefined;
            return (
              <li key={`${error.code}-${error.path}`}>
                <Badge variant="failed">{error.code}</Badge> {error.path} —{' '}
                <span>{error.message}</span>
                {node ? (
                  <>
                    {' '}
                    <button
                      type="button"
                      className="underline"
                      onClick={() => focusValidationError(node.id)}
                    >
                      {node.name}
                    </button>
                  </>
                ) : null}
              </li>
            );
          })}
        </ul>
      ) : null}

      <div className="flex min-h-0 flex-1">
        <NodePalette nodeTypes={nodeTypes} onAdd={addNode} error={loadError} />
        <div className="flex min-w-0 flex-1 flex-col">
          <div className="flex shrink-0 items-center justify-between border-b border-[var(--border)] px-3 py-2">
            <h2 className="text-xs font-bold tracking-[0.08em] text-[var(--muted-foreground)]">
              WORKFLOW CANVAS
            </h2>
            <span className="text-xs text-[var(--muted-foreground)]">
              ports · schema · execution contract
            </span>
          </div>
          <div className="min-h-0 flex-1">
            <WorkflowCanvas
              nodes={flowNodes}
              edges={flowEdges}
              focus={focusRequest}
              onNodesChange={onNodesChange}
              onEdgesChange={onEdgesChange}
              onConnect={onConnect}
              onSelectNode={selectNode}
            />
          </div>
        </div>
        <PropertiesPanel
          node={selectedNode}
          metadata={selectedNode ? metadataByType.get(selectedNode.type) : undefined}
          models={models}
          fieldErrors={selectedNodeFieldErrors}
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

      <RecentExecutionBar loaded={recentRunLoaded} snapshot={recentRun} />

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
