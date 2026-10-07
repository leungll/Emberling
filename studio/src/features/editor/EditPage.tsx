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

import { toCanvasEdges } from './canvasEdges';
import { newNodeId } from './nodeIds';
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
  listTools,
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
  ToolMetadata,
  ValidationError,
} from '@/api/types';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { RunInputDialog } from '@/features/run-input/RunInputDialog';
import { runInputLabels } from '@/features/run-input/runInputLabels';
import { type EditorGraph, useStudioStore } from '@/stores/studio-store';

/**
 * Router state a "New Definition" navigation from Definitions carries. POST /definitions
 * creates the first version with no baseVersion, but since
 * backend/internal/runtime/graph.go rejects a graph with no Output Node, that POST is
 * deferred until the first Save.
 */
interface NewDefinitionState {
  name?: string;
  description?: string;
}

/**
 * The unsaved name/description of a not-yet-created Definition. MVP defines no
 * server-side DRAFT state, so this only ever lives in this component's memory; it never
 * becomes a Definition version until the first Save.
 */
interface DraftMeta {
  name: string;
  description: string;
}

const EMPTY_GRAPH: EditorGraph = { nodes: [], edges: [] };

/** Focus in a text control keeps Cmd/Ctrl+Z for the control's own native text undo. */
function isTextEditingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return (
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement ||
    target instanceof HTMLSelectElement ||
    target.isContentEditable
  );
}

/** Top-level config keys whose values differ, naming the field a config edit touched. */
function changedConfigFields(before: JsonObject, after: JsonObject): string[] {
  const keys = new Set([...Object.keys(before), ...Object.keys(after)]);
  return [...keys]
    .filter((key) => JSON.stringify(before[key]) !== JSON.stringify(after[key]))
    .sort();
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
  const canUndo = useStudioStore((s) => s.past.length > 0);
  const canRedo = useStudioStore((s) => s.future.length > 0);
  const resetHistory = useStudioStore((s) => s.resetHistory);
  const markSaved = useStudioStore((s) => s.markSaved);
  const checkpoint = useStudioStore((s) => s.checkpoint);
  const closeCoalescing = useStudioStore((s) => s.closeCoalescing);
  const syncUnsavedChanges = useStudioStore((s) => s.syncUnsavedChanges);

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
  // has measured the DOM; that measurement decides visibility and `fitView` in
  // WorkflowCanvas. It is kept out of `nodes`/`positions` so it never marks the
  // Definition dirty, but it still has to be fed back into the controlled `nodes` prop or
  // every render looks unmeasured again and the canvas never becomes visible.
  const [measured, setMeasured] = useState<Record<string, { width: number; height: number }>>({});
  const [nodeTypes, setNodeTypes] = useState<NodeMetadata[]>([]);
  const [models, setModels] = useState<ModelMetadata[]>([]);
  const [tools, setTools] = useState<ToolMetadata[]>([]);
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
  // Bumped on every Undo/Redo so the Properties form remounts from the restored config
  // (a field such as the JSON editor keeps its own text state while mounted).
  const [historyRevision, setHistoryRevision] = useState(0);

  const applyDefinition = useCallback(
    (loaded: Definition) => {
      setDefinition(loaded);
      setNodes(loaded.nodes);
      setEdges(loaded.edges);
      setPositions(Object.fromEntries(loaded.nodes.map((node) => [node.id, node.position])));
      setMeasured({});
      setValidationErrors(null);
      setConflict(null);
      // A (re)load, including Reload latest after a version conflict, starts history over
      // with the loaded version as the clean point.
      resetHistory({ nodes: loaded.nodes, edges: loaded.edges });
    },
    [resetHistory],
  );

  useEffect(() => {
    const controller = new AbortController();
    // History and the saved point are page-local; nothing carries over from another
    // Definition opened earlier in this session.
    resetHistory(EMPTY_GRAPH);

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

    listTools(controller.signal)
      .then(setTools)
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setLoadError(describeError(error, 'Could not load tools'));
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

      // The Recent Execution bar reads the Definition list's `lastRun` summary,
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
  }, [workflowId, isNewDraft, applyDefinition, resetHistory]);

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
    () => toCanvasEdges(edges, nodes, metadataByType),
    [edges, nodes, metadataByType],
  );

  const selectedNode = nodes.find((node) => node.id === selectedNodeId) ?? null;

  /** What Save would persist of the canvas right now. */
  const graph = useMemo<EditorGraph>(
    () => ({
      nodes: nodes.map((node) => ({ ...node, position: positions[node.id] ?? node.position })),
      edges,
    }),
    [nodes, positions, edges],
  );

  // "Unsaved changes" compares the canvas with the last loaded or saved graph rather than
  // counting edits, so undoing back to exactly that graph is clean again.
  useEffect(() => {
    syncUnsavedChanges(graph);
  }, [graph, syncUnsavedChanges]);

  // React Flow reports one deletion as separate node and edge removals within the same
  // task; they share this key so a single Undo restores the node with its edges.
  const removalKey = useRef<string | null>(null);
  const removalCount = useRef(0);
  const sameTaskRemovalKey = useCallback(() => {
    if (removalKey.current === null) {
      removalCount.current += 1;
      removalKey.current = `remove:${removalCount.current}`;
      queueMicrotask(() => {
        removalKey.current = null;
      });
    }
    return removalKey.current;
  }, []);

  // An edit makes earlier Validate/Save feedback stale. Whether it is unsaved is decided
  // by the graph comparison above, not here.
  const markDirty = useCallback(() => {
    setValidationErrors(null);
    setNotice(null);
  }, []);

  const restoreGraph = useCallback(
    (restored: EditorGraph) => {
      setNodes(restored.nodes);
      setEdges(restored.edges);
      setPositions(Object.fromEntries(restored.nodes.map((node) => [node.id, node.position])));
      setHistoryRevision((revision) => revision + 1);
      setValidationErrors(null);
      setNotice(null);
      if (selectedNodeId && !restored.nodes.some((node) => node.id === selectedNodeId)) {
        selectNode(null);
      }
    },
    [selectedNodeId, selectNode],
  );

  // Undo/Redo only swap local graphs; they never call the server.
  const undo = useCallback(() => {
    const restored = useStudioStore.getState().undo(graph);
    if (restored) restoreGraph(restored);
  }, [graph, restoreGraph]);

  const redo = useCallback(() => {
    const restored = useStudioStore.getState().redo(graph);
    if (restored) restoreGraph(restored);
  }, [graph, restoreGraph]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (!(event.metaKey || event.ctrlKey) || event.altKey) return;
      if (isTextEditingTarget(event.target) || runDialogOpen) return;
      const key = event.key.toLowerCase();
      if (key === 'z' && !event.shiftKey) {
        event.preventDefault();
        undo();
      } else if ((key === 'z' && event.shiftKey) || (key === 'y' && event.ctrlKey)) {
        event.preventDefault();
        redo();
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [undo, redo, runDialogOpen]);

  const onNodesChange = useCallback(
    (changes: NodeChange<RegisteredFlowNode>[]) => {
      if (changes.some((change) => change.type === 'remove')) {
        checkpoint(graph, sameTaskRemovalKey());
      } else {
        const moves = changes.filter((change) => change.type === 'position');
        if (moves.some((change) => change.dragging)) {
          // One drag, however many frames, is one entry.
          checkpoint(graph, 'drag');
        } else if (moves.length > 0) {
          // The drag-end frame belongs to the drag it ends; any other move (a keyboard
          // nudge) is its own entry.
          if (useStudioStore.getState().coalesceKey === 'drag') closeCoalescing();
          else checkpoint(graph);
        }
      }
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
    [flowNodes, markDirty, graph, checkpoint, closeCoalescing, sameTaskRemovalKey],
  );

  const onEdgesChange = useCallback(
    (changes: EdgeChange<FlowEdge>[]) => {
      if (changes.some((change) => change.type === 'remove')) {
        checkpoint(graph, sameTaskRemovalKey());
      }
      const next = applyEdgeChanges(changes, flowEdges);
      const kept = new Set(next.map((edge) => edge.id));
      setEdges((prev) => prev.filter((edge) => kept.has(edge.id)));
      if (changes.some((change) => change.type !== 'select')) markDirty();
    },
    [flowEdges, markDirty, graph, checkpoint, sameTaskRemovalKey],
  );

  const onConnect = useCallback(
    (connection: Connection) => {
      if (!connection.source || !connection.target) return;
      checkpoint(graph);
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
    [markDirty, graph, checkpoint],
  );

  const addNode = useCallback(
    (metadata: NodeMetadata) => {
      const id = newNodeId(metadata.type);
      const position = { x: 80 + nodes.length * 40, y: 80 + nodes.length * 30 };
      checkpoint(graph);
      setNodes((prev) => [
        ...prev,
        { id, type: metadata.type, name: metadata.displayName, position, config: {} },
      ]);
      setPositions((prev) => ({ ...prev, [id]: position }));
      selectNode(id);
      markDirty();
    },
    [nodes.length, selectNode, markDirty, graph, checkpoint],
  );

  const currentDraft = useCallback(
    () => ({
      name: definition?.name ?? draftMeta?.name ?? 'Untitled workflow',
      description: definition?.description ?? draftMeta?.description ?? '',
      nodes: graph.nodes,
      edges: graph.edges,
    }),
    [definition, draftMeta, graph],
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
      // The graph just saved is the new clean point; history is kept.
      markSaved(graph);
      setNotice(`Saved version ${saved.version}.`);
    } catch (error) {
      if (error instanceof ApiRequestError && error.code === 'VERSION_CONFLICT') {
        // The Backend never includes the newer version in a 409 body (it names no
        // resource to avoid leaking one Definition's content into another's error path),
        // so naming it to the user needs a fresh read. Local edits are untouched either
        // way: nothing here may overwrite them.
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
  }, [
    isNewDraft,
    definition,
    workflowId,
    currentDraft,
    navigate,
    setUnsavedChanges,
    graph,
    markSaved,
  ]);

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
  // top-level field they point at (clicking an entry focuses the offending
  // node): SchemaForm renders each list under its field, so the field itself does not
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
        <p
          role="alert"
          // Same FAILED tokens and body-text size as the Validation errors row below.
          className="border-b border-[var(--status-failed-dot)]/40 bg-[var(--status-failed-bg)] px-4 py-2.5 text-sm text-[var(--foreground)]"
        >
          {loadError}
        </p>
      ) : null}

      {notice ? (
        <p className="border-b border-[var(--border)] px-4 py-2 text-xs text-[var(--muted-foreground)]">
          {notice}
        </p>
      ) : null}

      {validationErrors ? (
        <ul
          role="list"
          aria-label="Validation errors"
          // The FAILED red is shared with Canvas, Timeline and Detail, so the row uses
          // the status-failed tokens; the message is body text (≥14px) and the Backend path a
          // helper label (≥12px).
          className="space-y-1.5 border-b border-[var(--status-failed-dot)]/40 bg-[var(--status-failed-bg)] px-4 py-2.5"
        >
          {validationErrors.map((error) => {
            const nodeId = error.nodeId ?? nodeIdFromPath(error.path);
            const node = nodeId ? nodes.find((candidate) => candidate.id === nodeId) : undefined;
            return (
              <li
                key={`${error.code}-${error.path}`}
                className="flex flex-wrap items-center gap-x-2.5 gap-y-1"
              >
                <Badge variant="failed">{error.code}</Badge>
                <span className="text-sm text-[var(--foreground)]">{error.message}</span>
                {error.path ? (
                  <span className="font-mono text-xs text-[var(--muted-foreground)]">
                    {error.path}
                  </span>
                ) : null}
                {node ? (
                  <button
                    type="button"
                    className="text-sm font-medium text-[var(--status-failed-fg)] underline underline-offset-2 hover:text-[var(--foreground)]"
                    onClick={() => focusValidationError(node.id)}
                  >
                    {node.name}
                  </button>
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
          <div data-testid="canvas-region" className="relative min-h-0 flex-1">
            {conflict ? (
              // An overlay on the canvas rather than a row above it: the palette, canvas
              // and properties keep their full height while the conflict is unresolved.
              <div
                role="alert"
                aria-label="Version conflict"
                className="absolute inset-x-3 top-3 z-10 flex items-start gap-4 rounded-lg border border-amber-500/60 bg-[var(--card)] px-4 py-3 text-[14px] shadow-lg"
              >
                <p className="min-w-0 flex-1 text-amber-500">
                  The definition was saved as v{conflict.version} on the server while you were
                  editing v{definition?.version ?? '?'} locally. Reload to see the latest version,
                  or keep your edits and compare before saving again.
                </p>
                <div className="flex shrink-0 gap-2">
                  <Button variant="outline" onClick={() => setConflict(null)}>
                    Keep my edits
                  </Button>
                  <Button onClick={() => applyDefinition(conflict)}>Reload latest</Button>
                </div>
              </div>
            ) : null}
            <WorkflowCanvas
              nodes={flowNodes}
              edges={flowEdges}
              focus={focusRequest}
              onNodesChange={onNodesChange}
              onEdgesChange={onEdgesChange}
              onConnect={onConnect}
              onSelectNode={selectNode}
              history={{ canUndo, canRedo, onUndo: undo, onRedo: redo }}
            />
          </div>
        </div>
        {/* Leaving a Properties field ends its typing session, so the next edit of the same
            field is a separate Undo entry. `display: contents` keeps the panel's layout. */}
        <div className="contents" onBlur={closeCoalescing}>
          <PropertiesPanel
            key={historyRevision}
            node={selectedNode}
            metadata={selectedNode ? metadataByType.get(selectedNode.type) : undefined}
            models={models}
            tools={tools}
            fieldErrors={selectedNodeFieldErrors}
            // An edit that leaves the graph as it is (the same name, a config equal to the
            // current one) is not an edit: it must not add a do-nothing Undo entry.
            onRename={(name) => {
              if (!selectedNode || name === selectedNode.name) return;
              checkpoint(graph, `name:${selectedNode.id}`);
              setNodes((prev) =>
                prev.map((node) => (node.id === selectedNodeId ? { ...node, name } : node)),
              );
              markDirty();
            }}
            onConfigChange={(config) => {
              if (!selectedNode) return;
              const fields = changedConfigFields(selectedNode.config, config);
              if (fields.length === 0) return;
              checkpoint(graph, `config:${selectedNode.id}:${fields.join(',')}`);
              setNodes((prev) =>
                prev.map((node) => (node.id === selectedNodeId ? { ...node, config } : node)),
              );
              markDirty();
            }}
          />
        </div>
      </div>

      <RecentExecutionBar loaded={recentRunLoaded} snapshot={recentRun} />

      {definition ? (
        <RunInputDialog
          open={runDialogOpen}
          onClose={() => setRunDialogOpen(false)}
          workflowId={workflowId}
          definitionVersion={definition.version}
          runInputSchema={definition.runInputSchema}
          inputLabels={runInputLabels(definition.nodes)}
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
