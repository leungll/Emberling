import { Check, ChevronDown, ChevronRight, Ellipsis, Search } from 'lucide-react';
import {
  useEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
} from 'react';

import { ElementTime, RunOffset } from './RunFacts';
import { Words } from './Words';
import {
  axisOf,
  percentAt,
  spanOf,
  waitsIn,
  type Axis,
  type RunModel,
  type TraceElement,
} from './run-model';
import { filtering, visibleRows, type TraceFilter } from './trace-rows';
import { StateBlock } from '@/components/state/StateBlock';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import { IconButton } from '@/components/ui/button';
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@/components/ui/menu';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import { offsetText } from '@/lib/time';
import { cn } from '@/lib/utils';

/**
 * The trace: the run as a tree (run, steps, attempts, turns, decisions, tool calls,
 * waits) in a treegrid, with an optional timeline bar per row. Every bar is two server
 * timestamps on one axis; nothing is measured against the viewer's clock.
 */

export const NARROW_TRACE = 560;

interface TracePanelProps {
  model: RunModel;
  viewSwitch: ReactNode;
  selectedId: string;
  isOpen: (el: TraceElement) => boolean;
  onToggle: (id: string, open: boolean) => void;
  onExpandAll: () => void;
  onCollapseAll: () => void;
  onSelect: (id: string) => void;
  filter: TraceFilter;
  onFilter: (f: TraceFilter) => void;
  bars: boolean;
  onBars: (on: boolean) => void;
  /** Width of the pane; the bars column is left out below the narrow width. */
  width: number;
  /** Bumped (with the id) to move focus to a row. */
  focusSignal: { id: string; n: number };
  searchRef: RefObject<HTMLInputElement | null>;
}

// The name column keeps room for an indented name beside its step type at every depth;
// below that the trace scrolls sideways inside its pane rather than breaking names into
// single letters.
function columns(bars: boolean): string {
  return bars ? 'minmax(14rem, 1fr) minmax(32px, 20%) 80px 128px' : 'minmax(14rem, 1fr) 88px 128px';
}

function Bar({ model, el, axis }: { model: RunModel; el: TraceElement; axis: Axis }) {
  const copy = useCopy();
  const t = copy.runs.trace;
  const span = el.notRun ? null : spanOf(model, el);
  if (!span) return null;
  const off = (iso: string) =>
    offsetText(new Date(iso).getTime() - new Date(model.runStart).getTime());
  const seg = (
    key: string,
    start: string,
    end: string | null,
    kind: 'bar' | 'wait',
    tone: string,
  ) => {
    const left = percentAt(axis, start);
    const style = end
      ? {
          left: `${left.toFixed(2)}%`,
          width: `${Math.max(0, percentAt(axis, end) - left).toFixed(2)}%`,
        }
      : { left: `${left.toFixed(2)}%`, right: 0 };
    return (
      <span
        key={key}
        data-tone={tone}
        data-open={end ? undefined : 'true'}
        style={style}
        className={cn(
          'absolute inset-y-0 min-w-0.5',
          end ? 'rounded-sm' : 'rounded-l-sm',
          kind === 'bar'
            ? tone === 'failed'
              ? 'bg-st-failed-dot'
              : tone === 'running'
                ? 'bg-st-running-dot'
                : 'bg-border-control'
            : cn(
                'border border-dashed bg-surface-inset',
                tone === 'waiting' ? 'border-st-waiting-dot' : 'border-fg-muted',
              ),
        )}
      />
    );
  };
  const tone = el.tone === 'failed' || el.tone === 'running' ? el.tone : 'neutral';
  const parts = span.wait
    ? [seg('w', span.start, span.end, 'wait', el.live ? 'waiting' : 'waited')]
    : [
        seg('b', span.start, span.end, 'bar', tone),
        ...waitsIn(el).map((w) =>
          w.toolAttempt?.dispatchedAt
            ? seg(
                w.id,
                w.toolAttempt.dispatchedAt,
                w.toolAttempt.completedAt,
                'wait',
                w.live ? 'waiting' : 'waited',
              )
            : null,
        ),
      ];
  const tip = span.end
    ? t.barSpan(off(span.start), off(span.end))
    : span.wait
      ? t.waitOpen(off(span.start))
      : t.barOpen(off(span.start));
  return (
    <Tooltip content={tip}>
      <span className="relative block h-1.5">{parts}</span>
    </Tooltip>
  );
}

function TraceRow({
  model,
  el,
  selected,
  within,
  path,
  open,
  filtered,
  focused,
  axis,
  query,
  bars,
  onToggle,
}: {
  model: RunModel;
  el: TraceElement;
  selected: boolean;
  within: boolean;
  path: boolean;
  open: boolean;
  filtered: boolean;
  focused: boolean;
  axis: Axis | null;
  query: string;
  bars: boolean;
  onToggle: (id: string, open: boolean) => void;
}) {
  const copy = useCopy();
  const t = copy.runs.trace;
  const has = el.children.length > 0;
  const Caret = open ? ChevronDown : ChevronRight;
  return (
    <div
      role="row"
      data-id={el.id}
      aria-level={el.depth + 1}
      aria-expanded={has ? open : undefined}
      aria-selected={selected}
      tabIndex={focused ? 0 : -1}
      style={{ gridTemplateColumns: columns(bars) }}
      className={cn(
        'relative grid min-h-row-dense cursor-pointer items-start text-md hover:bg-surface-hover focus-visible:-outline-offset-2',
        selected && 'bg-surface-selected hover:bg-surface-selected',
      )}
    >
      {selected || within ? (
        <span
          aria-hidden="true"
          className={cn(
            'absolute inset-y-0 left-0 w-0.5',
            selected ? 'bg-accent' : 'bg-border-control',
          )}
        />
      ) : null}
      <div role="gridcell" className="flex min-w-0 items-start gap-1.5 py-1.5 pl-2">
        {el.depth > 0 ? (
          <span aria-hidden="true" className="shrink-0" style={{ width: el.depth * 16 - 6 }} />
        ) : null}
        <span
          aria-hidden="true"
          data-toggle={has && !filtered ? el.id : undefined}
          onClick={(e) => {
            if (!has || filtered) return;
            e.stopPropagation();
            onToggle(el.id, !open);
          }}
          className={cn(
            'inline-flex h-5 w-control-xs shrink-0 items-center justify-center rounded-sm text-fg-muted',
            has && !filtered && 'hover:bg-surface-hover hover:text-fg',
          )}
        >
          {has ? <Caret className="size-icon-sm" /> : null}
        </span>
        <span className="inline-flex h-5 shrink-0 items-center">
          <StatusIndicator size="icon" status={el.status} endedWait={el.endedWait} />
        </span>
        <span
          className={cn(
            'min-w-0 wrap-anywhere',
            el.notRun ? 'text-fg-muted' : path ? 'text-fg-secondary' : 'text-fg',
          )}
        >
          <Words text={el.label} tools={el.tools} query={query} />
        </span>
        {el.type ? (
          <span className="shrink-0 text-sm whitespace-nowrap text-fg-muted">
            <Words text={el.type} query={query} />
          </span>
        ) : null}
        {el.notRun ? (
          <span className="shrink-0 text-sm whitespace-nowrap text-fg-muted">
            {el.step?.statusKey === 'NONE_RUN_FAILED' ? t.didNotRun : t.notStarted}
          </span>
        ) : null}
      </div>
      {axis ? (
        <div role="gridcell" className="min-w-0 pt-3 pr-2 pl-3">
          <Bar model={model} el={el} axis={axis} />
        </div>
      ) : null}
      <div
        role="gridcell"
        className="py-1.5 pr-3 text-right text-sm whitespace-nowrap text-fg-secondary tabular-nums"
      >
        <RunOffset model={model} iso={el.start} />
      </div>
      <div
        role="gridcell"
        className="py-1.5 pr-4 text-right text-sm whitespace-nowrap text-fg-secondary tabular-nums"
      >
        {el.notRun ? null : <ElementTime model={model} el={el} />}
      </div>
    </div>
  );
}

export function TraceToolbar({
  filter,
  onFilter,
  searchRef,
  onSearchKey,
  bars,
  onBars,
  onExpandAll,
  onCollapseAll,
}: {
  filter: TraceFilter;
  onFilter: (f: TraceFilter) => void;
  searchRef: RefObject<HTMLInputElement | null>;
  onSearchKey: (e: KeyboardEvent<HTMLInputElement>) => void;
  bars: boolean;
  onBars: (on: boolean) => void;
  onExpandAll: () => void;
  onCollapseAll: () => void;
}) {
  const copy = useCopy();
  const t = copy.runs.trace;
  return (
    <>
      <span className="relative inline-flex min-w-0 shrink items-center">
        <Search
          aria-hidden="true"
          className="pointer-events-none absolute left-2 size-icon-sm text-fg-muted"
        />
        <input
          ref={searchRef}
          type="search"
          value={filter.query}
          placeholder={t.search}
          aria-label={t.search}
          onChange={(e) => {
            onFilter({ ...filter, query: e.target.value });
          }}
          onKeyDown={onSearchKey}
          style={{ width: '18ch' }}
          className="h-control-sm min-w-0 rounded-md border border-border-control bg-surface-inset pr-2 pl-8 text-sm text-fg placeholder:text-fg-muted [&::-webkit-search-cancel-button]:hidden"
        />
      </span>
      <label className="inline-flex min-h-control-sm shrink-0 cursor-pointer items-center gap-1.5 text-sm whitespace-nowrap text-fg-secondary">
        <input
          type="checkbox"
          checked={filter.failuresOnly}
          onChange={(e) => {
            onFilter({ ...filter, failuresOnly: e.target.checked });
          }}
          className="m-0 accent-accent"
        />
        <span>{t.failuresOnly}</span>
      </label>
      <span className="flex-1" />
      <Menu>
        <MenuTrigger asChild>
          <IconButton label={t.options}>
            <Ellipsis aria-hidden="true" className="size-icon-md" />
          </IconButton>
        </MenuTrigger>
        <MenuContent label={t.options}>
          <MenuItem onSelect={onExpandAll}>{t.expandAll}</MenuItem>
          <MenuItem onSelect={onCollapseAll}>{t.collapseAll}</MenuItem>
          <MenuSeparator />
          <MenuItem
            onSelect={() => {
              onBars(!bars);
            }}
            icon={
              bars ? (
                <Check aria-hidden="true" />
              ) : (
                <span aria-hidden="true" className="size-icon-sm" />
              )
            }
          >
            {t.bars}
          </MenuItem>
        </MenuContent>
      </Menu>
    </>
  );
}

export function TracePanel({
  model,
  viewSwitch,
  selectedId,
  isOpen,
  onToggle,
  onExpandAll,
  onCollapseAll,
  onSelect,
  filter,
  onFilter,
  bars,
  onBars,
  width,
  focusSignal,
  searchRef,
}: TracePanelProps) {
  const copy = useCopy();
  const t = copy.runs.trace;
  const gridRef = useRef<HTMLDivElement>(null);
  const [focusId, setFocusId] = useState<string | null>(null);
  const [pending, setPending] = useState<{ id: string; n: number } | null>(null);

  const { rows, hits } = visibleRows(model, isOpen, filter);
  const filtered = filtering(filter);
  const showBars = bars && width >= NARROW_TRACE;
  const axis = showBars ? axisOf(model) : null;
  const ids = rows.map((r) => r.id);
  const tabStop =
    focusId && ids.includes(focusId) ? focusId : ids.includes(selectedId) ? selectedId : ids[0];
  const selected = model.byId.get(selectedId) ?? null;
  const query = filter.query.trim().toLowerCase();

  const focusRow = (id: string) => {
    setFocusId(id);
    setPending((p) => ({ id, n: (p?.n ?? 0) + 1 }));
  };

  const focusDom = (id: string) => {
    const row = gridRef.current?.querySelector<HTMLElement>(`[data-id="${CSS.escape(id)}"]`);
    row?.focus({ preventScroll: true });
    row?.scrollIntoView({ block: 'nearest' });
  };

  useEffect(() => {
    if (pending) focusDom(pending.id);
  }, [pending]);

  useEffect(() => {
    if (focusSignal.n > 0) focusDom(focusSignal.id);
  }, [focusSignal]);

  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const rowEl = (e.target as HTMLElement).closest<HTMLElement>('[role="row"][data-id]');
    if (!rowEl || e.metaKey || e.ctrlKey || e.altKey) return;
    const i = ids.indexOf(rowEl.dataset.id ?? '');
    const el = rows[i];
    if (!el) return;
    const key = e.key === 'j' ? 'ArrowDown' : e.key === 'k' ? 'ArrowUp' : e.key;
    const go = (id: string | undefined) => {
      e.preventDefault();
      if (id) focusRow(id);
    };
    if (key === 'ArrowDown') go(ids[i + 1]);
    else if (key === 'ArrowUp') go(ids[i - 1]);
    else if (key === 'Home') go(ids[0]);
    else if (key === 'End') go(ids[ids.length - 1]);
    else if (key === 'ArrowRight') {
      e.preventDefault();
      const next = rows[i + 1];
      if (filtered) {
        if (next?.parent === el) focusRow(next.id);
      } else if (el.children.length && !isOpen(el)) onToggle(el.id, true);
      else if (el.children[0]) focusRow(el.children[0].id);
    } else if (key === 'ArrowLeft') {
      e.preventDefault();
      if (!filtered && el.children.length && isOpen(el)) onToggle(el.id, false);
      else if (el.parent) focusRow(el.parent.id);
    } else if (key === 'Enter' || key === ' ') {
      e.preventDefault();
      onSelect(el.id);
      focusRow(el.id);
    }
  };

  const onSearchKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      if (filter.query) onFilter({ ...filter, query: '' });
      else if (tabStop) focusRow(tabStop);
    } else if ((e.key === 'ArrowDown' || e.key === 'Enter') && tabStop) {
      e.preventDefault();
      focusRow(tabStop);
    }
  };

  const bar = (
    <div className="flex h-row-table shrink-0 items-center gap-3 border-b border-border-subtle pr-3 pl-4">
      {viewSwitch}
      <TraceToolbar
        filter={filter}
        onFilter={onFilter}
        searchRef={searchRef}
        onSearchKey={onSearchKey}
        bars={bars}
        onBars={onBars}
        onExpandAll={onExpandAll}
        onCollapseAll={onCollapseAll}
      />
    </div>
  );

  if (filtered && !rows.length) {
    const q = filter.query.trim();
    return (
      <>
        {bar}
        <div className="min-h-0 flex-1 overflow-auto">
          <StateBlock
            kind="nomatch"
            title={q ? t.noMatch(q, filter.failuresOnly) : t.nothingFailed}
            body={q ? t.noMatchBody : t.nothingFailedBody}
            action={{
              label: copy.runs.events.clear,
              onClick: () => {
                onFilter({ query: '', failuresOnly: false });
                searchRef.current?.focus();
              },
            }}
          />
        </div>
      </>
    );
  }

  const headCell = 'text-xs font-medium text-fg-muted';
  return (
    <>
      {bar}
      <div className="min-h-0 flex-1 overflow-auto pb-16">
        <div
          ref={gridRef}
          role="treegrid"
          aria-label={t.label}
          aria-readonly="true"
          className="min-w-fit"
          onKeyDown={onKey}
          onFocus={(e) => {
            const row = (e.target as HTMLElement).closest<HTMLElement>('[role="row"][data-id]');
            if (row?.dataset.id) setFocusId(row.dataset.id);
          }}
          onClick={(e) => {
            const target = e.target as HTMLElement;
            if (target.closest('a, button, input, label, summary')) return;
            const row = target.closest<HTMLElement>('[role="row"][data-id]');
            if (row?.dataset.id) {
              onSelect(row.dataset.id);
              focusRow(row.dataset.id);
            }
          }}
        >
          <div
            role="row"
            style={{ gridTemplateColumns: columns(showBars) }}
            className="sticky top-0 z-sticky grid h-row-dense items-center border-b border-border-subtle bg-surface"
          >
            <div role="columnheader" className={cn(headCell, 'pl-4')}>
              {t.columns.name}
            </div>
            {showBars ? (
              <div role="columnheader" className={cn(headCell, 'pr-2 pl-3')}>
                <Tooltip content={t.timelineTip}>
                  <span className="cursor-default">{t.columns.timeline}</span>
                </Tooltip>
              </div>
            ) : null}
            <div role="columnheader" className={cn(headCell, 'pr-3 text-right')}>
              <Tooltip content={t.startTip}>
                <span className="cursor-default">{t.columns.start}</span>
              </Tooltip>
            </div>
            <div role="columnheader" className={cn(headCell, 'pr-4 text-right')}>
              {t.columns.duration}
            </div>
          </div>
          {rows.map((el) => {
            const open = filtered ? true : isOpen(el);
            const within =
              !!selected &&
              selected.id !== el.id &&
              selectedId !== 'run' &&
              !open &&
              isWithinRow(selected, el);
            return (
              <TraceRow
                key={el.id}
                model={model}
                el={el}
                selected={el.id === selectedId}
                within={within}
                path={hits?.get(el.id) === 'path'}
                open={open}
                filtered={filtered}
                focused={el.id === tabStop}
                axis={axis}
                query={query}
                bars={showBars}
                onToggle={onToggle}
              />
            );
          })}
        </div>
      </div>
    </>
  );
}

function isWithinRow(selected: TraceElement, row: TraceElement): boolean {
  let x = selected.parent;
  while (x) {
    if (x === row) return true;
    x = x.parent;
  }
  return false;
}
