import { CircleAlert, Search } from 'lucide-react';
import {
  useEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
} from 'react';

import { RunOffset } from './RunFacts';
import { Words } from './Words';
import {
  EVENT_GROUPS,
  elementCrumb,
  elementOfEvent,
  elementPath,
  eventLabel,
  eventSummary,
  filterEvents,
  isFailureEvent,
  type EventFilter,
  type EventGroup,
} from './run-events';
import { isWithin, type RunModel, type TraceElement } from './run-model';
import { Select } from '@/components/list/Select';
import { StateBlock } from '@/components/state/StateBlock';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';

/**
 * The run's ordered Event log with its filters: a group, the selected element only, and
 * a text filter. The selected element's Events are tinted in the full log.
 */

const COLUMNS = '40px 80px minmax(0, 1.4fr) minmax(0, 1fr) minmax(0, 1.2fr)';

interface EventsPanelProps {
  model: RunModel;
  viewSwitch: ReactNode;
  selected: TraceElement;
  filter: EventFilter;
  onFilter: (f: EventFilter) => void;
  /** Selects the element an Event is about, keeping the full log in view. */
  onSelectEvent: (elementId: string) => void;
  searchRef: RefObject<HTMLInputElement | null>;
}

export function EventsPanel({
  model,
  viewSwitch,
  selected,
  filter,
  onFilter,
  onSelectEvent,
  searchRef,
}: EventsPanelProps) {
  const copy = useCopy();
  const e = copy.runs.events;
  const gridRef = useRef<HTMLDivElement>(null);
  const [focusSeq, setFocusSeq] = useState<number | null>(null);
  const [pending, setPending] = useState<{ seq: number; n: number } | null>(null);

  const canOnly = selected !== model.root;
  const only = canOnly && filter.only;
  const effective = { ...filter, only };
  const list = filterEvents(model, selected, effective);
  const total = model.events.length;
  const head = list.length === total ? e.total(total) : e.showing(list.length, total);
  const countLine = only ? e.inElement(head, elementPath(model, selected)) : head;
  const seqs = list.map((x) => x.seq);
  const tabStop = focusSeq !== null && seqs.includes(focusSeq) ? focusSeq : seqs[0];

  useEffect(() => {
    if (!pending) return;
    const row = gridRef.current?.querySelector<HTMLElement>(`[data-seq="${String(pending.seq)}"]`);
    row?.focus();
    row?.scrollIntoView({ block: 'nearest' });
  }, [pending]);

  const focusRow = (seq: number | undefined) => {
    if (seq === undefined) return;
    setFocusSeq(seq);
    setPending((p) => ({ seq, n: (p?.n ?? 0) + 1 }));
  };

  const onKey = (ev: KeyboardEvent<HTMLDivElement>) => {
    const row = (ev.target as HTMLElement).closest<HTMLElement>('[data-seq]');
    if (!row || ev.metaKey || ev.ctrlKey || ev.altKey) return;
    const i = seqs.indexOf(Number(row.dataset.seq));
    let next: number | undefined;
    if (ev.key === 'ArrowDown' || ev.key === 'j') next = seqs[i + 1];
    else if (ev.key === 'ArrowUp' || ev.key === 'k') next = seqs[i - 1];
    else if (ev.key === 'Home') next = seqs[0];
    else if (ev.key === 'End') next = seqs[seqs.length - 1];
    else if (ev.key === 'PageDown') next = seqs[Math.min(seqs.length - 1, i + 10)];
    else if (ev.key === 'PageUp') next = seqs[Math.max(0, i - 10)];
    else if (ev.key === 'Enter') {
      ev.preventDefault();
      if (row.dataset.el) onSelectEvent(row.dataset.el);
      return;
    } else return;
    ev.preventDefault();
    focusRow(next);
  };

  const groupOptions = EVENT_GROUPS.map((g: EventGroup) => ({
    value: g,
    label: copy.events.groups[g],
    description: e.count(filterEvents(model, selected, effective, g).length),
  }));

  return (
    <>
      <div className="flex h-row-table shrink-0 items-center gap-3 border-b border-border-subtle pr-3 pl-4">
        {viewSwitch}
      </div>
      <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 px-4 py-2">
        <Select<EventGroup>
          label={e.group}
          value={filter.group}
          options={groupOptions}
          size="sm"
          onChange={(g) => {
            onFilter({ ...filter, group: g });
          }}
        />
        <Tooltip
          content={canOnly ? e.selectedOnlyTip(elementPath(model, selected)) : e.selectFirst}
        >
          <label
            aria-disabled={canOnly ? undefined : true}
            className={cn(
              'inline-flex min-h-control-sm min-w-0 items-center gap-1.5 text-sm',
              canOnly ? 'cursor-pointer text-fg-secondary' : 'cursor-default text-fg-placeholder',
            )}
          >
            <input
              type="checkbox"
              checked={only}
              disabled={!canOnly}
              onChange={(ev) => {
                onFilter({ ...filter, only: ev.target.checked });
              }}
              className="m-0 accent-accent"
            />
            <span>{e.selectedOnly}</span>
          </label>
        </Tooltip>
        <span className="flex-1" />
        <span className="relative inline-flex min-w-0 items-center">
          <Search
            aria-hidden="true"
            className="pointer-events-none absolute left-2 size-icon-sm text-fg-muted"
          />
          <input
            ref={searchRef}
            type="search"
            value={filter.query}
            placeholder={e.filter}
            aria-label={e.filter}
            onChange={(ev) => {
              onFilter({ ...filter, query: ev.target.value });
            }}
            onKeyDown={(ev) => {
              if (ev.key === 'Escape' && filter.query) {
                ev.preventDefault();
                ev.stopPropagation();
                onFilter({ ...filter, query: '' });
              } else if (ev.key === 'ArrowDown') {
                ev.preventDefault();
                focusRow(tabStop);
              }
            }}
            style={{ width: '16ch' }}
            className="h-control-sm min-w-0 rounded-md border border-border-control bg-surface-inset pr-2 pl-8 text-sm text-fg placeholder:text-fg-muted [&::-webkit-search-cancel-button]:hidden"
          />
        </span>
      </div>
      <p
        role="status"
        className="m-0 shrink-0 border-b border-border-subtle px-4 py-1.5 text-sm text-fg-muted tabular-nums"
      >
        {countLine}
      </p>
      <div className="min-h-0 flex-1 overflow-auto pb-16">
        {list.length === 0 ? (
          <StateBlock
            kind="nomatch"
            title={e.noMatch}
            body={e.noMatchBody}
            action={{
              label: e.clear,
              onClick: () => {
                onFilter({ group: 'all', query: '', only: false });
              },
            }}
          />
        ) : (
          <div
            ref={gridRef}
            role="grid"
            aria-label={e.label}
            aria-readonly="true"
            aria-rowcount={list.length + 1}
            onKeyDown={onKey}
            onFocus={(ev) => {
              const row = (ev.target as HTMLElement).closest<HTMLElement>('[data-seq]');
              if (row) setFocusSeq(Number(row.dataset.seq));
            }}
            onClick={(ev) => {
              const row = (ev.target as HTMLElement).closest<HTMLElement>('[data-seq]');
              if (!row) return;
              setFocusSeq(Number(row.dataset.seq));
              if (row.dataset.el) onSelectEvent(row.dataset.el);
            }}
          >
            <div
              role="row"
              style={{ gridTemplateColumns: COLUMNS }}
              className="sticky top-0 z-sticky grid h-row-dense items-center gap-3 border-b border-border-subtle bg-surface px-4 text-xs font-medium text-fg-muted"
            >
              <span role="columnheader" className="text-right">
                {e.columns.seq}
              </span>
              <span role="columnheader">{e.columns.time}</span>
              <span role="columnheader">{e.columns.event}</span>
              <span role="columnheader">{e.columns.element}</span>
              <span role="columnheader">{e.columns.summary}</span>
            </div>
            {list.map((ev) => {
              const el = elementOfEvent(model, ev);
              const tinted = !only && canOnly && isWithin(el, selected);
              const failed = isFailureEvent(ev);
              return (
                <div
                  key={ev.seq}
                  role="row"
                  data-seq={ev.seq}
                  data-el={el.id}
                  tabIndex={ev.seq === tabStop ? 0 : -1}
                  style={{ gridTemplateColumns: COLUMNS }}
                  className={cn(
                    'grid min-h-row-dense cursor-pointer items-start gap-3 px-4 py-1.5 text-sm hover:bg-surface-hover focus-visible:-outline-offset-2',
                    tinted && 'bg-surface-hover',
                  )}
                >
                  <span role="gridcell" className="text-right font-mono text-fg-muted tabular-nums">
                    {ev.seq}
                  </span>
                  <span
                    role="gridcell"
                    className="whitespace-nowrap text-fg-secondary tabular-nums"
                  >
                    <RunOffset model={model} iso={ev.timestamp} />
                  </span>
                  <span
                    role="gridcell"
                    className="flex min-w-0 items-start gap-1.5 wrap-anywhere text-fg"
                  >
                    {failed ? (
                      <CircleAlert
                        aria-hidden="true"
                        className="mt-0.5 size-icon-sm shrink-0 text-st-failed-fg"
                      />
                    ) : null}
                    <Tooltip content={ev.type}>
                      <span className="cursor-default">
                        <Words {...eventLabel(ev)} />
                      </span>
                    </Tooltip>
                  </span>
                  <span role="gridcell" className="min-w-0 wrap-anywhere text-fg-secondary">
                    {elementCrumb(model, el)}
                  </span>
                  <span role="gridcell" className="min-w-0 wrap-anywhere text-fg-muted">
                    {eventSummary(model, ev)}
                  </span>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </>
  );
}
