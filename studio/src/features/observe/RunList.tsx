import { Filter, PanelLeft, RotateCw } from 'lucide-react';
import { useEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from 'react';

import { Splitter } from './Splitter';
import type { RunListItem, RunStatusCounts } from '@/api/types';
import { StateBlock } from '@/components/state/StateBlock';
import { statusInfo } from '@/components/status/status-info';
import { Duration, LiveDuration, Timestamp } from '@/components/time/Time';
import { Button, IconButton } from '@/components/ui/button';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import {
  STATUS_PARAMS,
  endedDurationMs,
  isLive,
  olderTip,
  runSubline,
  usedOlderVersion,
  type RangeParam,
  type StatusParam,
} from '@/lib/run-words';
import { shortCode } from '@/lib/short-code';
import { absoluteTime } from '@/lib/time';
import { readString, writeString } from '@/lib/storage';
import { cn } from '@/lib/utils';
import { useTimeZone } from '@/stores/prefs-store';

/**
 * The runs of one workflow, newest first, on the left of its Runs tab. One item per run:
 * a status bar, the start time and run code, the status and how long from the run's own
 * timestamps. Filters live behind one button; the list collapses to a rail and resizes.
 */

const OPEN_KEY = 'emb.runs.list';
const WIDTH_KEY = 'emb.runs.listW';
const WIDTH = { min: 240, max: 480, fallback: 320 };

const BAR: Record<string, string> = {
  succeeded: 'before:bg-st-succeeded-dot',
  failed: 'before:bg-st-failed-dot',
  waiting: 'before:bg-st-waiting-dot',
  running: 'before:bg-st-running-dot',
  queued: 'before:bg-st-queued-dot',
  inactive: 'before:bg-st-inactive-dot',
};

const WORD: Record<string, string> = {
  succeeded: 'text-st-succeeded-fg',
  failed: 'text-st-failed-fg',
  waiting: 'text-st-waiting-fg',
  running: 'text-st-running-fg',
  queued: 'text-fg-secondary',
  inactive: 'text-fg-secondary',
};

export interface RunListFilters {
  status: StatusParam | null;
  range: RangeParam | null;
}

interface RunListProps {
  items: readonly RunListItem[];
  counts: RunStatusCounts | null;
  state: 'loading' | 'error' | 'ready';
  hasMore: boolean;
  loadingMore: boolean;
  onMore: () => void;
  onRetry: () => void;
  selectedId: string | null;
  filters: RunListFilters;
  onFilters: (f: RunListFilters) => void;
  /** A click opens the run as a new history entry; the arrow keys replace the entry. */
  onOpen: (id: string, how: 'click' | 'key') => void;
}

function storedWidth(): number {
  const n = Number.parseInt(readString('local', WIDTH_KEY) ?? '', 10);
  return n >= WIDTH.min && n <= WIDTH.max ? n : WIDTH.fallback;
}

function Item({
  item,
  selected,
  focused,
}: {
  item: RunListItem;
  selected: boolean;
  focused: boolean;
}) {
  const copy = useCopy();
  const zone = useTimeZone();
  const info = statusInfo({ kind: 'run', value: item.status });
  const sub = runSubline(item);
  const ended = endedDurationMs(item);
  const code = shortCode(item.id);
  const line = (
    <span className="max-w-full self-start text-sm wrap-anywhere text-fg-muted tabular-nums">
      <span className={cn('font-medium', WORD[info.tone])}>{info.label}</span>{' '}
      {isLive(item.status) ? (
        <>
          {copy.runs.forDuration('')}
          <LiveDuration start={item.startedAt} />
        </>
      ) : ended !== null ? (
        <>
          {copy.runs.inDuration('')}
          <Duration ms={ended} />
        </>
      ) : null}
    </span>
  );
  return (
    <div
      role="option"
      data-run={item.id}
      aria-selected={selected}
      tabIndex={focused ? 0 : -1}
      aria-label={copy.runs.list.itemLabel(code, absoluteTime(item.startedAt, zone), info.label)}
      className={cn(
        'relative flex min-h-control-md cursor-pointer flex-col gap-0.5 rounded-md py-2 pr-2 pl-5 hover:bg-surface-hover focus-visible:-outline-offset-2 active:bg-surface-pressed',
        'before:absolute before:top-2 before:bottom-2 before:left-2 before:w-1 before:rounded-full',
        BAR[info.tone],
        selected && 'bg-surface-selected hover:bg-surface-selected',
      )}
    >
      <span className="flex min-w-0 items-baseline gap-2">
        <span className="min-w-0 flex-1 text-md wrap-anywhere text-fg tabular-nums">
          <Timestamp iso={item.startedAt} />
        </span>
        <span aria-hidden="true" className="shrink-0 font-mono text-xs text-fg-muted">
          {code}
        </span>
      </span>
      {sub ? <Tooltip content={sub}>{line}</Tooltip> : line}
      {usedOlderVersion(item) ? (
        <span className="flex items-center gap-2 text-sm text-fg-muted">
          <Tooltip content={olderTip(item)}>
            <span className="rounded-sm border border-border px-1 text-xs text-fg-muted tabular-nums">
              {copy.runs.page.olderVersion}
            </span>
          </Tooltip>
        </span>
      ) : null}
    </div>
  );
}

function RadioGroup<V extends string>({
  name,
  label,
  value,
  options,
  onChange,
}: {
  name: string;
  label: string;
  value: V;
  options: readonly { value: V; label: string }[];
  onChange: (v: V) => void;
}) {
  return (
    <div className="flex flex-col">
      <h3 className="m-0 px-2 py-1 text-xs font-medium text-fg-muted">{label}</h3>
      <div role="radiogroup" aria-label={label} className="flex flex-col">
        {options.map((o) => (
          <label
            key={o.value}
            className="flex min-h-control-sm min-w-0 cursor-pointer items-center gap-2 rounded-md px-2 text-sm text-fg hover:bg-surface-hover has-[:focus-visible]:outline-2 has-[:focus-visible]:-outline-offset-2 has-[:focus-visible]:outline-focus"
          >
            <input
              type="radio"
              name={name}
              value={o.value}
              checked={o.value === value}
              onChange={() => {
                onChange(o.value);
              }}
              className="m-0 shrink-0 accent-accent outline-none"
            />
            <span>{o.label}</span>
          </label>
        ))}
      </div>
    </div>
  );
}

export function RunList({
  items,
  counts,
  state,
  hasMore,
  loadingMore,
  onMore,
  onRetry,
  selectedId,
  filters,
  onFilters,
  onOpen,
}: RunListProps) {
  const copy = useCopy();
  const l = copy.runs.list;
  const [open, setOpen] = useState(() => readString('local', OPEN_KEY) !== 'closed');
  const [width, setWidth] = useState(storedWidth);
  const [focusId, setFocusId] = useState<string | null>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const toggleRef = useRef<HTMLButtonElement>(null);
  const active = (filters.status ? 1 : 0) + (filters.range ? 1 : 0);
  const ids = items.map((i) => i.id);
  const tabStop =
    focusId && ids.includes(focusId)
      ? focusId
      : selectedId && ids.includes(selectedId)
        ? selectedId
        : ids[0];

  // Keep the open run in view in the list.
  useEffect(() => {
    if (!selectedId) return;
    listRef.current
      ?.querySelector<HTMLElement>(`[data-run="${CSS.escape(selectedId)}"]`)
      ?.scrollIntoView({ block: 'nearest' });
  }, [selectedId, items.length]);

  const setOpenStored = (next: boolean) => {
    setOpen(next);
    writeString('local', OPEN_KEY, next ? null : 'closed');
    window.requestAnimationFrame(() => {
      toggleRef.current?.focus();
    });
  };

  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const row = (e.target as HTMLElement).closest<HTMLElement>('[data-run]');
    if (!row || e.metaKey || e.ctrlKey || e.altKey) return;
    const i = ids.indexOf(row.dataset.run ?? '');
    let next: string | undefined;
    if (e.key === 'ArrowDown' || e.key === 'j') next = ids[i + 1];
    else if (e.key === 'ArrowUp' || e.key === 'k') next = ids[i - 1];
    else if (e.key === 'Home') next = ids[0];
    else if (e.key === 'End') next = ids[ids.length - 1];
    else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      if (row.dataset.run && row.dataset.run !== selectedId) onOpen(row.dataset.run, 'key');
      return;
    } else return;
    e.preventDefault();
    e.stopPropagation();
    if (!next) return;
    setFocusId(next);
    onOpen(next, 'key');
    listRef.current?.querySelector<HTMLElement>(`[data-run="${CSS.escape(next)}"]`)?.focus();
  };

  const resize = (e: PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    e.preventDefault();
    const target = e.currentTarget;
    target.setPointerCapture(e.pointerId);
    const x0 = e.clientX;
    const w0 = width;
    let last = w0;
    const move = (ev: globalThis.PointerEvent) => {
      last = Math.max(WIDTH.min, Math.min(WIDTH.max, w0 + ev.clientX - x0));
      setWidth(last);
    };
    const up = () => {
      target.removeEventListener('pointermove', move);
      target.removeEventListener('pointerup', up);
      writeString('local', WIDTH_KEY, String(Math.round(last)));
    };
    target.addEventListener('pointermove', move);
    target.addEventListener('pointerup', up);
  };

  if (!open) {
    return (
      <aside
        aria-label={l.label}
        className="flex w-12 shrink-0 flex-col items-center gap-3 border-r border-border bg-surface pt-2"
      >
        <IconButton ref={toggleRef} label={l.show} onClick={() => setOpenStored(true)}>
          <PanelLeft aria-hidden="true" className="size-icon-md" />
        </IconButton>
        <span style={{ writingMode: 'vertical-rl' }} className="text-sm font-medium text-fg-muted">
          {l.title}
        </span>
      </aside>
    );
  }

  const statusOptions: { value: StatusParam | 'all'; label: string }[] = [
    { value: 'all', label: l.statuses.all },
    ...STATUS_PARAMS.map((s) => ({ value: s, label: l.statuses[s] })),
  ];
  const rangeOptions: { value: RangeParam | 'all'; label: string }[] = [
    { value: 'all', label: copy.runs.page.ranges.all },
    { value: '24h', label: copy.runs.page.ranges['24h'] },
    { value: '7d', label: copy.runs.page.ranges['7d'] },
  ];
  const activeWords = [
    filters.status ? l.statuses[filters.status] : '',
    filters.range
      ? filters.status
        ? copy.runs.page.ranges[filters.range].toLowerCase()
        : copy.runs.page.ranges[filters.range]
      : '',
  ].filter(Boolean);

  return (
    <aside
      aria-label={l.label}
      style={{ width }}
      className="relative flex min-h-0 shrink-0 flex-col border-r border-border bg-surface"
    >
      <div className="flex shrink-0 items-center gap-0.5 pt-3 pr-2 pb-2 pl-4">
        <div className="flex min-w-0 flex-1 flex-col">
          <h2 className="m-0 text-md font-semibold text-fg">{l.title}</h2>
          <span className="text-sm wrap-anywhere text-fg-muted">
            {counts ? l.active(counts.running, counts.paused) : ' '}
          </span>
        </div>
        <Popover>
          <PopoverTrigger asChild>
            <IconButton
              label={active ? l.filterActive(active) : l.filter}
              tooltip={l.filter}
              className="relative"
            >
              <Filter aria-hidden="true" className="size-icon-md" />
              {active ? (
                <span
                  aria-hidden="true"
                  className="absolute -top-0.5 -right-0.5 inline-flex size-4 items-center justify-center rounded-full bg-accent text-xs text-fg-on-accent"
                >
                  {active}
                </span>
              ) : null}
            </IconButton>
          </PopoverTrigger>
          <PopoverContent
            label={l.filter}
            className="flex flex-col gap-2 p-2"
            style={{ width: 248 }}
          >
            <RadioGroup
              name="rl-status"
              label={l.status}
              value={filters.status ?? 'all'}
              options={statusOptions}
              onChange={(v) => {
                onFilters({ ...filters, status: v === 'all' ? null : v });
              }}
            />
            <div className="border-t border-border-subtle pt-2">
              <RadioGroup
                name="rl-started"
                label={l.started}
                value={filters.range ?? 'all'}
                options={rangeOptions}
                onChange={(v) => {
                  onFilters({ ...filters, range: v === 'all' ? null : v });
                }}
              />
            </div>
            <div className="flex justify-end border-t border-border-subtle pt-2">
              <Button
                variant="ghost"
                size="sm"
                disabled={!active}
                onClick={() => {
                  onFilters({ status: null, range: null });
                }}
              >
                {l.clearFilters}
              </Button>
            </div>
          </PopoverContent>
        </Popover>
        <IconButton ref={toggleRef} label={l.hide} onClick={() => setOpenStored(false)}>
          <PanelLeft aria-hidden="true" className="size-icon-md" />
        </IconButton>
      </div>
      {activeWords.length ? (
        <div className="flex min-w-0 shrink-0 items-center gap-2 px-4 pb-2 text-sm text-fg-secondary">
          <span className="min-w-0">{activeWords.join(', ')}</span>
          <Button
            variant="link"
            data-variant="link"
            className="min-h-control-xs shrink-0 text-sm"
            onClick={() => {
              onFilters({ status: null, range: null });
            }}
          >
            {l.clear}
          </Button>
        </div>
      ) : null}
      {state === 'error' ? (
        <StateBlock
          kind="error"
          title={l.loadFailed}
          action={{
            label: copy.retry,
            icon: <RotateCw aria-hidden="true" className="size-icon-sm" />,
            onClick: onRetry,
          }}
          className="px-4"
        />
      ) : state === 'loading' && !items.length ? (
        <div
          aria-busy="true"
          aria-label={copy.loading}
          role="status"
          className="flex flex-col gap-2 px-4 py-2"
        >
          {[0, 1, 2, 3].map((i) => (
            <span key={i} className="block h-10 animate-skeleton rounded-md bg-surface-hover" />
          ))}
        </div>
      ) : !items.length ? (
        <div className="p-4">
          <StateBlock
            kind={active ? 'nomatch' : 'empty'}
            title={active ? l.noMatch : l.empty}
            action={
              active
                ? {
                    label: l.clearFilters,
                    onClick: () => {
                      onFilters({ status: null, range: null });
                    },
                  }
                : undefined
            }
          />
        </div>
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto px-2 pt-1 pb-6">
          <div
            ref={listRef}
            role="listbox"
            aria-label={l.label}
            onKeyDown={onKey}
            onFocus={(e) => {
              const row = (e.target as HTMLElement).closest<HTMLElement>('[data-run]');
              if (row?.dataset.run) setFocusId(row.dataset.run);
            }}
            onClick={(e) => {
              const row = (e.target as HTMLElement).closest<HTMLElement>('[data-run]');
              const id = row?.dataset.run;
              if (id && id !== selectedId) onOpen(id, 'click');
            }}
            className="flex flex-col gap-0.5"
          >
            {items.map((item) => (
              <Item
                key={item.id}
                item={item}
                selected={item.id === selectedId}
                focused={item.id === tabStop}
              />
            ))}
          </div>
          {hasMore ? (
            <div className="flex justify-center pt-2">
              <Button variant="ghost" size="sm" disabled={loadingMore} onClick={onMore}>
                {l.more}
              </Button>
            </div>
          ) : null}
        </div>
      )}
      <Splitter
        orientation="vertical"
        label={l.resize}
        value={width}
        min={WIDTH.min}
        max={WIDTH.max}
        onDrag={resize}
        onReset={() => {
          setWidth(WIDTH.fallback);
          writeString('local', WIDTH_KEY, null);
        }}
        onKey={(e) => {
          if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
          e.preventDefault();
          const next = Math.max(
            WIDTH.min,
            Math.min(WIDTH.max, width + (e.key === 'ArrowRight' ? 16 : -16)),
          );
          setWidth(next);
          writeString('local', WIDTH_KEY, String(next));
        }}
        className="absolute inset-y-0 -right-0.5"
      />
    </aside>
  );
}
