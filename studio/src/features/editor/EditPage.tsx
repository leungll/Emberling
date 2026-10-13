import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router';

import {
  ApiRequestError,
  createDefinition,
  createRun,
  getDefinition,
  saveDefinition,
  validateDefinition,
} from '@/api/client';
import type { Definition, JsonObject, ValidationError } from '@/api/types';
import { ListShell } from '@/components/shell/ListShell';
import { NotFoundView } from '@/components/shell/NotFoundView';
import { StateBlock } from '@/components/state/StateBlock';
import { RunInputDialog } from '@/features/run-input/RunInputDialog';
import { runInputLabels } from '@/features/run-input/runInputLabels';
import { useCopy } from '@/lib/copy';
import { carryFrom } from '@/lib/nav/list-state';
import { readString, writeString } from '@/lib/storage';
import { showToast } from '@/lib/toast';
import { useDocumentTitle } from '@/lib/use-document-title';
import { EditorCanvas } from './canvas/EditorCanvas';
import { QuickAdd } from './canvas/QuickAdd';
import {
  EditorContext,
  type EditorContextValue,
  type FocusRequest,
  type Geometry,
  type QuickAddRequest,
} from './editor-context';
import { docKey, useEditorStore, type Doc } from './editor-store';
import { EditorHeader } from './EditorHeader';
import { LastRunStrip } from './LastRunStrip';
import { LeaveGuardDialog } from './LeaveGuardDialog';
import { serverIssues, validate, type Issue } from './model/checks';
import { stagedInput } from './model/images';
import { SidePanel } from './panel/SidePanel';
import { StepsSidebar } from './StepsSidebar';
import { useEditorShortcuts } from './useEditorShortcuts';
import { useEditorData } from './useEditorData';
import { useEditOps } from './useEditOps';
import { useStaging } from './useStaging';
import { useUnsavedGuard } from './useUnsavedGuard';

const PALETTE_KEY = 'emb.editor.palette';
const PANEL_KEY = 'emb.editor.panel';
const CHECK_DELAY_MS = 600;

const NO_GEOMETRY: Geometry = {
  places: {},
  portY: () => 0,
  center: () => ({ x: 0, y: 0 }),
  reveal: () => undefined,
  focusCard: () => undefined,
  focusCanvas: () => undefined,
};

/** Router state a "New workflow" navigation carries: the name and description to start with. */
interface NewWorkflowState {
  name?: string;
  description?: string;
}

function docOf(d: Definition): Doc {
  return { name: d.name, description: d.description ?? '', nodes: d.nodes, edges: d.edges };
}

/** The structured errors a rejected Save or Validate carries, if any. */
function validationErrorsOf(error: unknown): ValidationError[] | null {
  if (!(error instanceof ApiRequestError)) return null;
  const details = error.body.error.details;
  if (typeof details !== 'object' || details === null || Array.isArray(details)) return null;
  const errors = (details as { errors?: unknown }).errors;
  return Array.isArray(errors) ? (errors as ValidationError[]) : null;
}

/**
 * The value after it has stopped changing for `ms`. A new `key` (another workflow or version
 * loaded) takes the value at once, so a freshly loaded draft is never checked as empty.
 */
function useSettled<T>(value: T, key: string | null, ms: number): T {
  const [settled, setSettled] = useState({ key, value });
  if (settled.key !== key) setSettled({ key, value });
  useEffect(() => {
    const timer = window.setTimeout(() => setSettled({ key, value }), ms);
    return () => window.clearTimeout(timer);
  }, [key, value, ms]);
  return settled.key === key ? settled.value : value;
}

/**
 * The Editor: a workflow's steps on a canvas, the steps sidebar, the side panel and the
 * last run. The draft lives in the page until Save creates a new version.
 */
export function EditPage() {
  const { workflowId: param } = useParams();
  const workflowId = param ?? null;
  const copy = useCopy();
  const t = copy.editor;
  const location = useLocation();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const [reloadKey, setReloadKey] = useState(0);
  const { registry, load, setLoad, lastRun } = useEditorData(workflowId, reloadKey);
  const staging = useStaging(workflowId);

  // The draft is loaded once per version; a save keeps the draft and its history.
  const [seed] = useState<NewWorkflowState>(
    () => (location.state as NewWorkflowState | null) ?? {},
  );
  const definition = load.kind === 'ready' ? load.definition : null;
  const source = definition
    ? `${definition.workflowId}@${String(definition.version)}`
    : load.kind === 'new'
      ? 'new'
      : null;
  useLayoutEffect(() => {
    const store = useEditorStore.getState();
    if (!source || store.source === source) return;
    if (definition) store.load(docOf(definition), source);
    else
      store.load(
        { name: seed.name ?? '', description: seed.description ?? '', nodes: [], edges: [] },
        source,
      );
  }, [source, definition, seed]);
  const ready = useEditorStore((s) => s.source) === source && source !== null;

  const doc = useEditorStore((s) => s.doc);
  const dirty = useEditorStore((s) => docKey(s.doc) !== s.savedKey);
  const version = definition?.version ?? null;

  // Checks: Studio's own after typing settles, plus the server's from the last Save or
  // Validate until the next edit.
  const settled = useSettled(doc, ready ? source : null, CHECK_DELAY_MS);
  const local = useMemo(
    () => (registry.loaded ? validate(settled.nodes, settled.edges, registry) : []),
    [settled, registry],
  );
  const [server, setServer] = useState<{ key: string; errors: ValidationError[] } | null>(null);
  const currentKey = docKey(doc);
  const issues = useMemo<Issue[]>(
    () =>
      server?.key === currentKey
        ? [...local, ...serverIssues(server.errors, doc.nodes, local)]
        : local,
    [server, currentKey, local, doc.nodes],
  );

  const [issuesOpen, setIssuesOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [runOpen, setRunOpen] = useState(() => searchParams.get('run') === '1');
  const [runBusy, setRunBusy] = useState(false);
  const [runError, setRunError] = useState<unknown>(null);
  const [quick, setQuick] = useState<QuickAddRequest | null>(null);
  const [focusRequest, setFocusRequest] = useState<FocusRequest | null>(null);
  const [paletteChoice, setPaletteChoice] = useState(() => readString('local', PALETTE_KEY));
  const [paletteAuto, setPaletteAuto] = useState(() => window.innerWidth < 1280);
  const [panelHidden, setPanelHidden] = useState(() => readString('local', PANEL_KEY) === '1');
  const geometry = useRef<Geometry>(NO_GEOMETRY);
  const onGeometry = useCallback((g: Geometry) => {
    geometry.current = g;
  }, []);

  // `?run=1` (the workflow list's Run) opens the Run dialog once; the parameter is dropped
  // so a reload or Back doesn't open it again.
  useEffect(() => {
    if (searchParams.get('run') !== '1') return;
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.delete('run');
        return next;
      },
      { replace: true },
    );
  }, [searchParams, setSearchParams]);

  const name = doc.name.trim() || t.untitled;
  useDocumentTitle(ready ? copy.pageTitles.editor(name) : null);

  const blocker = useUnsavedGuard(() => {
    const s = useEditorStore.getState();
    return docKey(s.doc) !== s.savedKey;
  }, dirty);

  const ops = useEditOps(workflowId, registry, geometry, staging.forget);

  const openSettings = useCallback((nodeId: string, field: string | null = null) => {
    useEditorStore.getState().select([nodeId]);
    setPanelHidden(false);
    setIssuesOpen(false);
    setFocusRequest((r) => ({ nodeId, field, seq: (r?.seq ?? 0) + 1 }));
    requestAnimationFrame(() => geometry.current.reveal(nodeId));
  }, []);
  const showIssue = useCallback(
    (issue: Issue) => {
      if (issue.node) {
        openSettings(issue.node, issue.field);
        return;
      }
      setIssuesOpen(false);
      setQuick({ only: 'outputs' });
    },
    [openSettings],
  );

  /** Saves the draft as a new version; resolves true when it was saved. */
  const save = useCallback(
    async (leaving = false): Promise<boolean> => {
      if (issues.length) {
        setIssuesOpen(true);
        showToast(t.toasts.fixToSave(issues.length));
        return false;
      }
      const store = useEditorStore.getState();
      if (version !== null && docKey(store.doc) === store.savedKey) {
        showToast(t.toasts.noChanges);
        return true;
      }
      const draft = store.current();
      const body = {
        name: draft.name.trim() || t.untitled,
        description: draft.description,
        nodes: draft.nodes,
        edges: draft.edges,
      };
      setSaving(true);
      try {
        const saved =
          workflowId && definition
            ? await saveDefinition(workflowId, { ...body, baseVersion: definition.version })
            : await createDefinition(body);
        store.markSaved(
          { ...draft, name: saved.name },
          `${saved.workflowId}@${String(saved.version)}`,
        );
        setLoad(saved.workflowId, { kind: 'ready', definition: saved });
        setServer(null);
        showToast(t.toasts.saved(saved.version), { done: true });
        if (!workflowId && !leaving) {
          void navigate(`/workflows/${encodeURIComponent(saved.workflowId)}`, { replace: true });
        }
        return true;
      } catch (error) {
        if (error instanceof ApiRequestError && error.code === 'VERSION_CONFLICT' && workflowId) {
          // The newer version is read only to name it; local edits stay until the viewer
          // chooses to load it.
          const latest = await getDefinition(workflowId).catch(() => null);
          showToast(t.toasts.conflict(latest?.version ?? (version ?? 0) + 1), {
            action: { label: t.toasts.reloadLatest, onAction: () => setReloadKey((k) => k + 1) },
          });
          return false;
        }
        const errors = validationErrorsOf(error);
        if (errors) {
          setServer({ key: docKey(useEditorStore.getState().doc), errors });
          setIssuesOpen(true);
          showToast(t.toasts.saveRejected);
        } else {
          showToast(t.toasts.saveFailed);
        }
        return false;
      } finally {
        setSaving(false);
      }
    },
    [issues.length, version, workflowId, definition, setLoad, navigate, t],
  );

  const validateNow = useCallback(async () => {
    const s = useEditorStore.getState();
    const draft = s.current();
    try {
      const result = await validateDefinition({ ...draft, name: draft.name.trim() || t.untitled });
      const errors = result.valid ? [] : result.errors;
      setServer({ key: docKey(s.doc), errors });
      const n = validate(draft.nodes, draft.edges, registry).length + errors.length;
      showToast(n ? t.toasts.issuesFound(n) : t.toasts.noIssues, n ? undefined : { done: true });
      if (n) setIssuesOpen(true);
    } catch {
      showToast(t.toasts.validateFailed);
    }
  }, [registry, t]);

  const runReason =
    version === null
      ? t.header.runNeverSaved
      : issues.length
        ? t.header.runFixIssues(issues.length)
        : dirty
          ? t.header.runSaveFirst
          : null;
  const openRun = useCallback(() => {
    if (runReason) {
      showToast(runReason);
      if (issues.length) setIssuesOpen(true);
      return;
    }
    setRunError(null);
    setRunOpen(true);
  }, [runReason, issues.length]);

  const startRun = async (input: JsonObject) => {
    if (!definition || !workflowId) return;
    setRunBusy(true);
    setRunError(null);
    try {
      const run = await createRun({ workflowId, definitionVersion: definition.version, input });
      setRunOpen(false);
      void navigate(
        carryFrom(
          `/workflows/${encodeURIComponent(workflowId)}/runs/${encodeURIComponent(run.id)}`,
          location.search,
        ),
      );
    } catch (error) {
      setRunError(error);
    } finally {
      setRunBusy(false);
    }
  };

  useEditorShortcuts({ save: () => void save(), run: openRun, enabled: ready && !runOpen });

  const ctx = useMemo<EditorContextValue>(
    () => ({
      workflowId,
      registry,
      lastRun,
      issues,
      staged: staging.staged,
      stageFiles: staging.stageFiles,
      unstage: staging.unstage,
      pickFiles: staging.pickFiles,
      openSettings,
      focusRequest,
      showChecks: () => setIssuesOpen(true),
      showIssue,
      quickAdd: setQuick,
      ops,
      geometry,
    }),
    [workflowId, registry, lastRun, issues, staging, openSettings, focusRequest, showIssue, ops],
  );
  const runLabels = useMemo(() => runInputLabels(definition?.nodes), [definition]);

  if (load.kind === 'missing' && workflowId) {
    return (
      <ListShell>
        <NotFoundView kind="workflow" id={workflowId} />
      </ListShell>
    );
  }
  if (load.kind === 'error') {
    return (
      <ListShell>
        <StateBlock
          kind="error"
          page
          title={t.loadFailed}
          body={copy.states.errorBody}
          details={load.message}
          action={{ label: copy.retry, onClick: () => setReloadKey((k) => k + 1), primary: true }}
        />
      </ListShell>
    );
  }

  const paletteCollapsed = paletteChoice === null ? paletteAuto : paletteChoice === '1';

  return (
    <EditorContext.Provider value={ctx}>
      <div className="flex h-screen flex-col bg-bg text-fg">
        <EditorHeader
          workflowId={workflowId}
          name={doc.name}
          onName={(v) => {
            const s = useEditorStore.getState();
            s.begin('workflow.name');
            s.setName(v);
          }}
          onNameCommit={() => useEditorStore.setState({ editingKey: null })}
          version={version}
          dirty={dirty}
          saving={saving}
          issues={issues}
          nodes={doc.nodes}
          catalog={registry.catalog}
          issuesOpen={issuesOpen}
          onIssuesOpen={setIssuesOpen}
          onShowIssue={showIssue}
          onSave={() => void save()}
          onRun={openRun}
          runReason={runReason}
          onValidate={() => void validateNow()}
        />
        {ready ? (
          <>
            <div className="flex min-h-0 flex-1">
              <StepsSidebar
                collapsed={paletteCollapsed}
                onCollapsed={(v) => {
                  setPaletteChoice(v ? '1' : '0');
                  writeString('local', PALETTE_KEY, v ? '1' : '0');
                }}
                onQuickAdd={() => setQuick({})}
              />
              <main aria-label={t.canvas.label} className="relative flex min-w-0 flex-1 flex-col">
                <EditorCanvas
                  inserting={quick?.link?.kind === 'insert' ? quick.link.edgeId : null}
                  onOverflow={() => setPaletteAuto(true)}
                  onGeometry={onGeometry}
                />
              </main>
              <SidePanel
                hidden={panelHidden}
                onHidden={(v) => {
                  setPanelHidden(v);
                  writeString('local', PANEL_KEY, v ? '1' : null);
                }}
              />
            </div>
            <LastRunStrip savedNodes={definition?.nodes ?? []} version={version} />
          </>
        ) : (
          <div className="flex min-h-0 flex-1 items-center justify-center">
            <p role="status" className="m-0 text-sm text-fg-muted">
              {t.loading}
            </p>
          </div>
        )}
        {quick ? <QuickAdd request={quick} onClose={() => setQuick(null)} /> : null}
        <LeaveGuardDialog
          blocker={blocker}
          issues={issues.length}
          version={version}
          onSaveAndLeave={() => save(true)}
        />
        {definition ? (
          <RunInputDialog
            open={runOpen}
            onClose={() => setRunOpen(false)}
            workflowName={definition.name}
            definitionVersion={definition.version}
            runInputSchema={definition.runInputSchema}
            inputLabels={runLabels}
            lastRun={
              lastRun.snapshot
                ? {
                    runId: lastRun.snapshot.run.id,
                    startedAt: lastRun.snapshot.run.startedAt,
                    input: lastRun.snapshot.run.input,
                  }
                : undefined
            }
            prefill={stagedInput(definition.nodes, staging.staged)}
            submitting={runBusy}
            submitError={runError}
            onSubmit={(input) => void startRun(input)}
          />
        ) : null}
      </div>
    </EditorContext.Provider>
  );
}
