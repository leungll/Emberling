import { ChevronDown, ChevronUp, RotateCw } from 'lucide-react';
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent,
} from 'react';
import { useLocation, useNavigate } from 'react-router';

import { DetailsPane } from './DetailsPane';
import { EventsPanel } from './EventsPanel';
import { RunBanner, RunHeader } from './RunHeader';
import { RunGraph } from './RunGraph';
import { Splitter } from '@/components/ui/splitter';
import { TracePanel } from './TracePanel';
import type { DetailContext } from './detail-context';
import type { DetailTab } from './detail-tabs';
import { crumbLabel } from './detail-tabs';
import { editHref, failureOf } from './run-diagnosis';
import { ancestors, failFocusId, recordIdOf, stepElementOf, waitInfo } from './run-model';
import {
  LOGS,
  DEBUG_KEY,
  NARROW_PAGE,
  PANE,
  clearPercent,
  parseRunView,
  runViewSearch,
  storeBars,
  storePercent,
  storeView,
  storedBars,
  storedPercent,
  storedView,
  type LogsView,
  type RunViewState,
} from './run-view-url';
import { openState } from './trace-rows';
import type { RunData, Registry } from './useRunData';
import { ApiRequestError } from '@/api/client';
import { StateBlock } from '@/components/state/StateBlock';
import { Segmented } from '@/components/ui/segmented';
import { copyText } from '@/lib/clipboard';
import { readString, writeString } from '@/lib/storage';
import { useCopy } from '@/lib/copy';
import { isTypingTarget, modalOpen } from '@/lib/shortcuts';
import { shortCode } from '@/lib/short-code';
import { showToast } from '@/lib/toast';
import { cn } from '@/lib/utils';
import { useShortcutGroups } from '@/stores/shortcuts-store';

/**
 * One run in its workflow's Runs tab: the header, the failure or wait, the read-only graph
 * and the Logs panel (trace or events beside the details). The selection, view and filters
 * live in the address; a run opens on where it failed, the live wait, or its newest
 * activity, and follows the run until the viewer picks something else.
 */

interface RunViewProps {
  runId: string;
  workflowId: string;
  latestVersion: number | null;
  registry: Registry;
  /** The run as read and followed by the page. */
  data: RunData;
  onRetry: () => void;
  onRunAgain: () => void;
  /** Whether the step map fits beside the details pane with full cards. */
  onGraphFit?: (fits: boolean) => void;
}

/** An element's width, observed from the moment it mounts (it may mount after a loading state). */
function useWidth<T extends HTMLElement>(): [number, T | null, (node: T | null) => void] {
  const [node, setNode] = useState<T | null>(null);
  const [w, setW] = useState(0);
  useEffect(() => {
    if (!node) return;
    const ro = new ResizeObserver(() => {
      setW(node.clientWidth);
    });
    ro.observe(node);
    return () => {
      ro.disconnect();
    };
  }, [node]);
  return [w, node, setNode];
}

export function RunView({
  runId,
  workflowId,
  latestVersion,
  registry,
  data,
  onRetry,
  onRunAgain,
  onGraphFit,
}: RunViewProps) {
  const copy = useCopy();
  const { search } = useLocation();
  const navigate = useNavigate();
  const { model } = data;
  const ui = parseRunView(search);
  const view: LogsView = ui.view ?? storedView();
  const [announced, setAnnounced] = useState('');

  // The address is the only record of this view's state. The router otherwise renders an
  // address change in a transition, which can leave a tab or selection showing the old
  // value after the address already holds the new one; flushing renders both together.
  const update = (patch: Partial<RunViewState>) => {
    void navigate(
      { search: runViewSearch({ ...ui, ...patch }, search) },
      { replace: true, flushSync: true },
    );
  };

  const [bars, setBars] = useState(storedBars);
  const [opened, setOpened] = useState<ReadonlySet<string>>(new Set());
  const [closed, setClosed] = useState<ReadonlySet<string>>(new Set());
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [logsPct, setLogsPct] = useState(() =>
    storedPercent(LOGS.key, LOGS.min, LOGS.max, LOGS.fallback),
  );
  // The details pane is the reading area, resized from its left edge like the
  // Editor's side panel; Trace and Events sit in a drawer under the step map.
  const [paneW, setPaneW] = useState<number | null>(() => {
    const n = Number.parseInt(readString('local', PANE.key) ?? '', 10);
    return Number.isFinite(n) ? n : null;
  });
  // An address that names a logs view opens the drawer; otherwise the last choice holds.
  // The trace and events drawer starts open; a closed drawer is the reader's remembered choice.
  const [debugOpen, setDebugOpen] = useState(
    () => ui.view !== null || readString('local', DEBUG_KEY) !== '0',
  );
  const showDebug = (on: boolean) => {
    setDebugOpen(on);
    writeString('local', DEBUG_KEY, on ? '1' : null);
  };
  const [rowFocus, setRowFocus] = useState({ id: '', n: 0 });
  const [titleFocus, setTitleFocus] = useState(0);
  const traceSearch = useRef<HTMLInputElement>(null);
  const eventSearch = useRef<HTMLInputElement>(null);
  const viewRef = useRef<HTMLElement>(null);
  const [rowWidth, , rowRef] = useWidth<HTMLDivElement>();
  const [leftWidth, leftNode, leftRef] = useWidth<HTMLDivElement>();
  const narrow = rowWidth > 0 && rowWidth < NARROW_PAGE;
  const paneMax = Math.max(PANE.min, Math.round(rowWidth * 0.6));
  // Until someone sets the pane's width, it leaves the step map the room its cards need at
  // the zoom floor; the floor itself never lowers.
  const [graphNeed, setGraphNeed] = useState(0);
  const paneDefault =
    graphNeed && rowWidth ? Math.min(PANE.fallback, rowWidth - graphNeed) : PANE.fallback;
  const paneWidth = Math.max(PANE.min, Math.min(paneMax, paneW ?? paneDefault));
  const onFit = useCallback(
    (fits: boolean, needed: number) => {
      setGraphNeed(needed);
      onGraphFit?.(fits);
    },
    [onGraphFit],
  );

  const following = ui.el === null;
  // Nothing selected reads the run itself.
  const selectedId = model && ui.el && model.byId.has(ui.el) ? ui.el : (model?.root.id ?? 'run');
  const selected = model?.byId.get(selectedId) ?? model?.root ?? null;
  const failure = useMemo(
    () => (model ? failureOf(model, registry.tools) : null),
    [model, registry.tools],
  );
  const wait = useMemo(() => (model ? waitInfo(model) : null), [model]);
  const isOpen = openState(opened, closed, selected);

  const select = (id: string, opts: { tab?: DetailTab; drawer?: boolean } = {}) => {
    if (!model?.byId.has(id)) return;
    const el = model.byId.get(id);
    const changed = id !== selectedId;
    update({ el: id, tab: opts.tab ?? (changed ? null : ui.tab) });
    if (el) {
      const path = new Set(ancestors(el).map((a) => a.id));
      setClosed((c) => new Set([...c].filter((x) => !path.has(x))));
    }
    if (opts.drawer !== false && narrow) {
      setDrawerOpen(true);
      setTitleFocus((n) => n + 1);
    }
  };

  const resumeLive = () => {
    update({ el: null, tab: null });
  };

  const setView = (v: LogsView, focus = false) => {
    storeView(v);
    showDebug(true);
    update({ view: v });
    if (focus) {
      window.requestAnimationFrame(() => {
        leftNode?.querySelector<HTMLElement>(`[role="radio"][value="${v}"]`)?.focus();
      });
    }
  };

  const focusRow = (id: string) => {
    if (view === 'trace') setRowFocus((f) => ({ id, n: f.n + 1 }));
  };

  // Page shortcuts: the run's actions and moving up one level.
  const handler = useRef<(e: globalThis.KeyboardEvent) => void>(() => undefined);
  const covered = narrow && drawerOpen;
  const closeDrawer = () => {
    setDrawerOpen(false);
    focusRow(selectedId);
  };

  const onShortcut = (e: globalThis.KeyboardEvent) => {
    if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
    if (isTypingTarget(e.target) || modalOpen() || !model) return;
    const k = e.key;
    if (k === 'Escape') {
      if (covered) {
        e.preventDefault();
        closeDrawer();
        return;
      }
      const cur = model.byId.get(selectedId);
      if (!cur?.parent) return;
      e.preventDefault();
      const active = document.activeElement;
      const inDetails = active?.closest('[data-details]');
      select(cur.parent.id, { drawer: false });
      if (inDetails) setTitleFocus((n) => n + 1);
      else focusRow(cur.parent.id);
      setAnnounced(copy.runs.view.selected(crumbLabel(cur.parent)));
      return;
    }
    if (k === '1' || k === '2') {
      e.preventDefault();
      setView(k === '1' ? 'trace' : 'events', true);
    } else if (k === 'f' && model.snapshot.run.status === 'FAILED') {
      e.preventDefault();
      const id = failFocusId(model);
      if (id) {
        select(id);
        focusRow(id);
      }
    } else if (k === 'l' && !model.terminal) {
      e.preventDefault();
      resumeLive();
    } else if (k === 'e') {
      e.preventDefault();
      const step = stepElementOf(model.byId.get(selectedId));
      void navigate(editHref(workflowId, step?.id ?? null, null, search));
    } else if (k === 'r') {
      e.preventDefault();
      onRunAgain();
    } else if (k === 'c') {
      e.preventDefault();
      const el = model.byId.get(selectedId) ?? model.root;
      const id = recordIdOf(model, el);
      void copyText(id).then((ok) => {
        showToast(ok ? copy.runs.view.copied(shortCode(id)) : copy.ids.copyFailed);
      });
    } else if (k === '/') {
      e.preventDefault();
      const input = view === 'events' ? eventSearch.current : traceSearch.current;
      input?.focus();
      input?.select();
    }
  };
  useEffect(() => {
    handler.current = onShortcut;
  });
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      handler.current(e);
    };
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('keydown', onKey);
    };
  }, []);

  const s = copy.runs.shortcuts;
  useShortcutGroups([
    { title: s.runs, rows: [{ keys: ['↑', '↓'], description: s.runsMove }] },
    {
      title: s.logs,
      rows: [
        { keys: ['1'], description: s.trace },
        { keys: ['2'], description: s.events },
      ],
    },
    {
      title: s.moving,
      rows: [
        { keys: ['↑', '↓'], description: s.move },
        { keys: ['J', 'K'], description: s.jk },
        { keys: ['←', '→'], description: s.collapse },
        { keys: ['↵'], description: s.show },
        { keys: ['Esc'], description: s.up },
      ],
    },
    {
      title: s.traceGroup,
      rows: [
        { keys: ['/'], description: s.search },
        { keys: ['Esc'], description: s.clearSearch },
      ],
    },
    {
      title: s.actions,
      rows: [
        { keys: ['F'], description: s.failure },
        { keys: ['L'], description: s.live },
        { keys: ['E'], description: s.editor },
        { keys: ['R'], description: s.again },
        { keys: ['C'], description: s.copyId },
        { keys: ['?'], description: s.sheet },
      ],
    },
  ]);

  /* ---------- Resizing ------------------------------------------------------------ */

  const dragLogs = (e: PointerEvent<HTMLDivElement>) => {
    const box = viewRef.current?.getBoundingClientRect();
    if (!box || e.button !== 0) return;
    e.preventDefault();
    const target = e.currentTarget;
    target.setPointerCapture(e.pointerId);
    let last = logsPct;
    const move = (ev: globalThis.PointerEvent) => {
      last = Math.max(LOGS.min, Math.min(LOGS.max, ((box.bottom - ev.clientY) / box.height) * 100));
      setLogsPct(last);
    };
    const up = () => {
      target.removeEventListener('pointermove', move);
      target.removeEventListener('pointerup', up);
      storePercent(LOGS.key, last);
    };
    target.addEventListener('pointermove', move);
    target.addEventListener('pointerup', up);
  };
  const setPane = (w: number, save: boolean) => {
    const v = Math.max(PANE.min, Math.min(paneMax, Math.round(w)));
    setPaneW(v);
    if (save) writeString('local', PANE.key, String(v));
  };
  const dragPane = (e: PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    e.preventDefault();
    const target = e.currentTarget;
    target.setPointerCapture(e.pointerId);
    const x0 = e.clientX;
    const w0 = paneWidth;
    let last = w0;
    const move = (ev: globalThis.PointerEvent) => {
      last = w0 - (ev.clientX - x0);
      setPane(last, false);
    };
    const up = () => {
      target.removeEventListener('pointermove', move);
      target.removeEventListener('pointerup', up);
      setPane(last, true);
    };
    target.addEventListener('pointermove', move);
    target.addEventListener('pointerup', up);
  };
  const keyResize = (
    e: KeyboardEvent<HTMLDivElement>,
    keys: [string, string],
    value: number,
    step: number,
    limits: { key: string; min: number; max: number },
    set: (v: number) => void,
  ) => {
    if (e.key !== keys[0] && e.key !== keys[1]) return;
    e.preventDefault();
    const next = Math.max(
      limits.min,
      Math.min(limits.max, value + (e.key === keys[1] ? step : -step)),
    );
    set(next);
    storePercent(limits.key, next);
  };

  /* ---------- States ---------------------------------------------------------------- */

  const loadError = data.observed.loadError;
  if (loadError) {
    const missing = loadError instanceof ApiRequestError && loadError.status === 404;
    return (
      <main className="flex min-w-0 flex-1 flex-col bg-bg">
        {missing ? (
          <StateBlock
            kind="notfound"
            title={copy.notFound.run.title(shortCode(runId))}
            body={copy.notFound.run.body}
            className="flex-1 justify-center"
          />
        ) : (
          <StateBlock
            kind="error"
            title={copy.runs.view.loadFailed}
            body={copy.states.errorBody}
            action={{
              label: copy.retry,
              icon: <RotateCw aria-hidden="true" className="size-icon-sm" />,
              onClick: () => {
                onRetry();
              },
            }}
            details={
              loadError instanceof ApiRequestError ? (
                <code className="font-mono text-xs">
                  {copy.states.errorCode(loadError.status, loadError.code)}
                </code>
              ) : undefined
            }
            className="flex-1 justify-center"
          />
        )}
      </main>
    );
  }
  if (data.definitionFailed) {
    return (
      <main className="flex min-w-0 flex-1 flex-col bg-bg">
        <StateBlock
          kind="error"
          title={copy.runs.graph.loadFailed}
          body={copy.states.errorBody}
          action={{
            label: copy.retry,
            onClick: onRetry,
          }}
          className="flex-1 justify-center"
        />
      </main>
    );
  }
  if (!model || !selected) {
    return (
      <main
        aria-busy="true"
        aria-label={copy.loading}
        className="flex min-w-0 flex-1 flex-col bg-bg"
      >
        <div className="flex h-16 shrink-0 items-center border-b border-border bg-surface px-4">
          <span
            className="block h-3 animate-skeleton rounded-sm bg-surface-hover"
            style={{ width: 320 }}
          />
        </div>
        <div className="flex flex-1 items-center justify-center gap-12">
          {[0, 1, 2].map((i) => (
            <span
              key={i}
              className="block animate-skeleton rounded-lg bg-surface-hover"
              style={{ width: 192, height: 96 }}
            />
          ))}
        </div>
      </main>
    );
  }

  const ctx: DetailContext = {
    model,
    failure,
    wait,
    tools: registry.tools,
    models: registry.models,
    nodeTypes: registry.nodeTypes,
    workflowId,
    latestVersion,
    selectedId,
    onSelect: (id, tab) => {
      select(id, { tab: tab as DetailTab | undefined, drawer: narrow && drawerOpen });
    },
  };

  const viewSwitch = (
    <Segmented<LogsView>
      label={copy.runs.logs.view}
      value={view}
      onValueChange={(v) => {
        setView(v);
      }}
      options={[
        { value: 'trace', label: copy.runs.logs.trace },
        {
          value: 'events',
          label: (
            <span className="inline-flex items-center gap-1.5">
              {copy.runs.logs.events}
              <span className="font-normal text-fg-muted tabular-nums">{model.events.length}</span>
            </span>
          ),
        },
      ]}
    />
  );

  const showDetails = !narrow || drawerOpen;
  const details = (
    <DetailsPane
      el={selected}
      ctx={ctx}
      tab={(ui.tab ?? 'overview') as DetailTab}
      onTab={(t) => {
        update({ tab: t });
      }}
      drawer={narrow}
      onClose={closeDrawer}
      focusKey={titleFocus}
    />
  );

  return (
    <main
      ref={viewRef}
      aria-label={copy.runs.view.run}
      className="flex min-h-0 min-w-0 flex-1 flex-col bg-bg"
    >
      <div aria-live="polite" className="sr-only">
        {announced}
      </div>
      <RunHeader
        model={model}
        connection={data.observed.connection}
        latestVersion={latestVersion}
        following={following}
        onResumeLive={resumeLive}
      />
      <RunBanner
        model={model}
        connection={data.observed.connection}
        failure={failure}
        wait={wait}
        selectedId={selectedId}
        detailsVisible={!narrow}
        onShow={(id) => {
          select(id);
          focusRow(id);
        }}
        onReload={() => {
          data.reconnect();
        }}
      />
      <div ref={rowRef} className="relative flex min-h-0 flex-1">
        <div
          ref={leftRef}
          // The drawer covers part of the page, so what it covers is set aside until it closes.
          inert={covered}
          aria-hidden={covered ? true : undefined}
          className="flex min-w-0 flex-1 flex-col"
        >
          <RunGraph
            model={model}
            onFit={onFit}
            selectedId={selectedId}
            onSelect={(id) => {
              select(id);
            }}
          />
          {debugOpen ? (
            <>
              <Splitter
                orientation="horizontal"
                label={copy.runs.logs.resize}
                value={logsPct}
                min={LOGS.min}
                max={LOGS.max}
                onDrag={dragLogs}
                onReset={() => {
                  clearPercent(LOGS.key);
                  setLogsPct(LOGS.fallback);
                }}
                onKey={(e) => {
                  keyResize(e, ['ArrowDown', 'ArrowUp'], logsPct, 4, LOGS, setLogsPct);
                }}
                className="-my-0.5"
              />
              <section
                aria-label={copy.runs.logs.view}
                style={{ height: `${logsPct.toFixed(2)}%` }}
                className="flex min-h-0 shrink-0 flex-col bg-surface"
              >
                {view !== 'events' ? (
                  <TracePanel
                    model={model}
                    viewSwitch={viewSwitch}
                    selectedId={selectedId}
                    isOpen={isOpen}
                    onToggle={(id, open) => {
                      const el = model.byId.get(id);
                      const onPath = el && selected ? ancestors(selected).includes(el) : false;
                      setOpened((o) => {
                        const n = new Set(o);
                        if (open) n.add(id);
                        else n.delete(id);
                        return n;
                      });
                      setClosed((c) => {
                        const n = new Set(c);
                        if (!open && onPath) n.add(id);
                        else n.delete(id);
                        return n;
                      });
                    }}
                    onExpandAll={() => {
                      setOpened(
                        new Set(
                          [...model.byId.values()]
                            .filter((x) => x.children.length)
                            .map((x) => x.id),
                        ),
                      );
                      setClosed(new Set());
                    }}
                    onCollapseAll={() => {
                      setOpened(new Set());
                      setClosed(
                        new Set(
                          [...model.byId.values()]
                            .filter((x) => x.kind === 'step')
                            .map((x) => x.id),
                        ),
                      );
                    }}
                    onSelect={(id) => {
                      select(id);
                    }}
                    filter={{ query: ui.tq, failuresOnly: ui.fails }}
                    onFilter={(f) => {
                      update({ tq: f.query, fails: f.failuresOnly });
                    }}
                    bars={bars}
                    onBars={(on) => {
                      setBars(on);
                      storeBars(on);
                    }}
                    width={leftWidth}
                    focusSignal={rowFocus}
                    searchRef={traceSearch}
                    only={ui.only}
                    onOnly={(on) => {
                      update({ only: on });
                    }}
                  />
                ) : (
                  <EventsPanel
                    model={model}
                    viewSwitch={viewSwitch}
                    selected={selected}
                    filter={{ group: ui.group, query: ui.q, only: ui.only }}
                    onFilter={(f) => {
                      update({ group: f.group, q: f.query, only: f.only });
                    }}
                    onSelectEvent={(id) => {
                      update({ el: id, tab: id === selectedId ? ui.tab : null, only: false });
                      if (narrow) {
                        setDrawerOpen(true);
                        setTitleFocus((n) => n + 1);
                      }
                    }}
                    searchRef={eventSearch}
                  />
                )}
              </section>
            </>
          ) : null}
          <div className="flex h-row-table shrink-0 items-center gap-2 border-t border-border-subtle bg-surface px-2">
            <button
              type="button"
              aria-expanded={debugOpen}
              onClick={() => {
                showDebug(!debugOpen);
              }}
              className="inline-flex h-control-xs cursor-pointer items-center gap-1.5 rounded-sm border-0 bg-transparent px-2 text-sm text-fg-secondary hover:bg-surface-hover hover:text-fg"
            >
              {debugOpen ? (
                <ChevronDown aria-hidden="true" className="size-icon-sm" />
              ) : (
                <ChevronUp aria-hidden="true" className="size-icon-sm" />
              )}
              {copy.runs.logs.drawer}
              <span className="text-fg-muted tabular-nums">{model.events.length}</span>
            </button>
            {selected !== model.root ? (
              <span className="text-sm text-fg-muted">
                {copy.runs.logs.forElement(crumbLabel(selected))}
              </span>
            ) : null}
          </div>
        </div>
        {covered ? (
          <div aria-hidden="true" onClick={closeDrawer} className="absolute inset-0 z-drawer" />
        ) : null}
        {showDetails ? (
          <aside
            data-details
            aria-label={copy.runs.details.label}
            style={narrow ? { width: 'min(420px, 100%)' } : { width: paneWidth }}
            className={cn(
              'relative flex min-w-0 shrink-0 flex-col border-l border-border-subtle bg-surface',
              narrow && 'absolute inset-y-0 right-0 z-drawer border-border shadow-2',
            )}
          >
            {narrow ? null : (
              <Splitter
                orientation="vertical"
                label={copy.runs.logs.resizeSplit}
                value={paneWidth}
                min={PANE.min}
                max={paneMax}
                onDrag={dragPane}
                onReset={() => {
                  setPaneW(null);
                  writeString('local', PANE.key, null);
                }}
                onKey={(e) => {
                  if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
                  e.preventDefault();
                  setPane(paneWidth + (e.key === 'ArrowLeft' ? 24 : -24), true);
                }}
                className="absolute inset-y-0 -left-0.5"
              />
            )}
            {details}
          </aside>
        ) : null}
      </div>
    </main>
  );
}
