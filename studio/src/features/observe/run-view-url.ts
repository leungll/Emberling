import { EVENT_GROUPS, type EventGroup } from './run-events';
import { rangeParam, statusParam, type RangeParam, type StatusParam } from '@/lib/run-words';
import { readString, writeString } from '@/lib/storage';

/**
 * The Runs tab's state in its address, so a link opens the same view:
 * `?el=&tab=&view=&tq=&fails=1&group=&q=&only=1&status=&range=`, plus `from=runs`.
 * Defaults are left out. The run itself is in the path.
 */

export type LogsView = 'trace' | 'events';

export interface RunViewState {
  /** The selected element; null selects the run (or the run's opening element). */
  el: string | null;
  tab: string | null;
  view: LogsView | null;
  tq: string;
  fails: boolean;
  group: EventGroup;
  q: string;
  only: boolean;
  /** The run list's filters. */
  status: StatusParam | null;
  range: RangeParam | null;
}

export const VIEW_KEY = 'emb.obs.view';
export const BARS_KEY = 'emb.obs.bars';

export function parseRunView(search: string): RunViewState {
  const q = new URLSearchParams(search);
  const view = q.get('view');
  const group = q.get('group') ?? 'all';
  return {
    el: q.get('el') ?? null,
    tab: q.get('tab') ?? null,
    view: view === 'trace' || view === 'events' ? view : null,
    tq: q.get('tq') ?? '',
    fails: q.get('fails') === '1',
    group: (EVENT_GROUPS as readonly string[]).includes(group) ? (group as EventGroup) : 'all',
    q: q.get('q') ?? '',
    only: q.get('only') === '1',
    status: statusParam(q.get('status')),
    range: rangeParam(q.get('range')),
  };
}

/** The search string for a state, keeping `from` from the current address. */
export function runViewSearch(state: RunViewState, current: string): string {
  const q = new URLSearchParams();
  const from = new URLSearchParams(current).get('from');
  if (from) q.set('from', from);
  if (state.status) q.set('status', state.status);
  if (state.range) q.set('range', state.range);
  if (state.view === 'events') q.set('view', 'events');
  if (state.el) q.set('el', state.el);
  if (state.tab && state.tab !== 'overview') q.set('tab', state.tab);
  if (state.view === 'events') {
    if (state.group !== 'all') q.set('group', state.group);
    if (state.q) q.set('q', state.q);
    if (state.only) q.set('only', '1');
  } else {
    if (state.tq) q.set('tq', state.tq);
    if (state.fails) q.set('fails', '1');
  }
  const s = q.toString();
  return s ? `?${s}` : '';
}

/** The Logs view the viewer last used, remembered across runs. */
export function storedView(): LogsView {
  const v = readString('local', VIEW_KEY);
  return v === 'events' ? 'events' : 'trace';
}

export function storeView(v: LogsView): void {
  writeString('local', VIEW_KEY, v);
}

export function storedBars(): boolean {
  return readString('local', BARS_KEY) !== 'off';
}

export function storeBars(on: boolean): void {
  writeString('local', BARS_KEY, on ? 'on' : 'off');
}

/** A remembered size in percent, kept only within its bounds. */
export function storedPercent(key: string, min: number, max: number, fallback: number): number {
  const v = Number.parseFloat(readString('local', key) ?? '');
  return Number.isFinite(v) && v >= min && v <= max ? v : fallback;
}

export function storePercent(key: string, value: number): void {
  writeString('local', key, value.toFixed(2));
}

export function clearPercent(key: string): void {
  writeString('local', key, null);
}

/** The Logs panel's height in percent of the run view: 25 to 80, 52 by default. */
export const LOGS = { key: 'emb.obs.logs', min: 25, max: 80, fallback: 52 } as const;
/** The trace or events pane's width in percent of the Logs panel: 35 to 78, 60 by default. */
export const SPLIT = { key: 'emb.obs.split', min: 35, max: 78, fallback: 60 } as const;
/** Below this width of the Logs panel the details pane is a drawer over the trace. */
export const NARROW_LOGS = 760;
