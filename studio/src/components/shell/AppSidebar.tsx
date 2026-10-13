import {
  Activity,
  ChevronDown,
  Keyboard,
  PanelLeft,
  Workflow,
  type LucideIcon,
} from 'lucide-react';
import { useId, useState, type ReactNode } from 'react';
import { Link } from 'react-router';

import type { DefinitionLastRun } from '@/api/types';
import { statusInfo, type Tone } from '@/components/status/status-info';
import { Kbd } from '@/components/ui/kbd';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import type { ListName } from '@/lib/nav/list-state';
import { readString, writeString } from '@/lib/storage';
import { relativeWords } from '@/lib/time';
import { useNow } from '@/lib/use-now';
import type { Connection } from '@/lib/use-readiness';
import { cn } from '@/lib/utils';

export interface NavCounts {
  /** Runs across all workflows; null until the Backend can list them. */
  runs: { total: number; running: number; waiting: number } | null;
  workflows: number | null;
}

export interface RecentItem {
  workflowId: string;
  name: string;
  lastRun: DefinitionLastRun | null;
}

interface AppSidebarProps {
  /** The list shown, or on a workflow page the list it was entered from. */
  section: ListName | null;
  /** True when `section` is the page itself rather than the section it belongs to. */
  onListPage: boolean;
  /** Where each list link goes: the list as the viewer left it. */
  hrefs: Record<ListName, string>;
  counts: NavCounts;
  recent: readonly RecentItem[];
  connection: Connection;
  collapsed: boolean;
  onToggle: () => void;
  onShortcuts: () => void;
}

const RECENT_OPEN_KEY = 'emb.sidebar.recent';

const ROW =
  'relative flex min-h-control-md w-full min-w-0 items-center gap-2 rounded-md px-2 py-1.5 text-left text-fg-secondary';
const HOVER = 'hover:bg-surface-hover hover:text-fg active:bg-surface-pressed';

const DOT: Record<Tone | 'none', string> = {
  running: 'bg-st-running-dot',
  waiting: 'bg-st-waiting-dot',
  succeeded: 'bg-st-succeeded-dot',
  failed: 'bg-st-failed-dot',
  queued: 'bg-st-queued-dot',
  inactive: 'bg-st-inactive-dot',
  none: 'border border-st-inactive-dot',
};

const CONNECTION_DOT: Record<Connection, string> = {
  ready: 'bg-st-succeeded-dot ring-2 ring-st-succeeded-bg',
  starting: 'bg-st-waiting-dot ring-2 ring-st-waiting-bg',
  unreachable: 'bg-st-failed-dot ring-2 ring-st-failed-bg',
  checking: 'bg-st-inactive-dot ring-2 ring-st-inactive-bg',
};

function BrandMark() {
  return (
    <span
      aria-hidden="true"
      className="relative inline-block size-icon-lg shrink-0 rounded-md bg-accent"
    >
      <span
        style={{ left: 6, top: 4, width: 8, height: 12, borderRadius: '9999px 9999px 4px 4px' }}
        className="absolute bg-fg-on-accent opacity-90"
      />
    </span>
  );
}

/** The rail shows each item's name (and count) as a tooltip on the right. */
function RailTip({
  collapsed,
  content,
  children,
}: {
  collapsed: boolean;
  content: ReactNode;
  children: ReactNode;
}) {
  return (
    <Tooltip side="right" content={collapsed ? content : null}>
      {children}
    </Tooltip>
  );
}

/**
 * The list pages' sidebar: the brand (home: Runs), Runs and Workflows with their counts,
 * Recent workflows, and at the foot the Backend connection, keyboard shortcuts and the
 * collapse control. Workflow pages have no sidebar; they lead with one back button.
 */
export function AppSidebar({
  section,
  onListPage,
  hrefs,
  counts,
  recent,
  connection,
  collapsed,
  onToggle,
  onShortcuts,
}: AppSidebarProps) {
  const copy = useCopy();
  const nav = copy.nav;
  const now = useNow();
  const recentId = useId();
  const [recentOpen, setRecentOpen] = useState(
    () => readString('local', RECENT_OPEN_KEY) !== 'closed',
  );

  const runs = counts.runs;
  const live = runs ? nav.live(runs.running, runs.waiting) : '';
  const signal: Tone | null = runs
    ? runs.waiting
      ? 'waiting'
      : runs.running
        ? 'running'
        : null
    : null;

  const items: {
    list: ListName;
    label: string;
    icon: LucideIcon;
    total: number | null;
    tip: string;
  }[] = [
    {
      list: 'runs',
      label: nav.runs,
      icon: Activity,
      total: runs?.total ?? null,
      tip: runs ? nav.countTip(nav.runs, nav.runTotal(runs.total), live) : nav.runs,
    },
    {
      list: 'workflows',
      label: nav.workflows,
      icon: Workflow,
      total: counts.workflows,
      tip:
        counts.workflows === null
          ? nav.workflows
          : nav.countTip(nav.workflows, nav.workflowTotal(counts.workflows), ''),
    },
  ];

  const conn = copy.backend[connection];

  return (
    <aside
      aria-label={nav.main}
      data-state={collapsed ? 'collapsed' : 'expanded'}
      className={cn(
        'sticky top-0 z-header flex h-screen shrink-0 flex-col border-r border-border bg-surface-inset',
        collapsed ? 'w-rail' : 'w-sidebar',
      )}
    >
      <div className="flex h-header shrink-0 items-center border-b border-border px-2">
        <RailTip collapsed={collapsed} content={nav.runs}>
          <Link
            to="/runs"
            aria-label={nav.home}
            className={cn(
              'flex h-control-md min-w-0 items-center gap-2 rounded-md px-1.5 font-semibold text-fg hover:bg-surface-hover',
              collapsed && 'w-control-md justify-center px-0',
            )}
          >
            <BrandMark />
            {collapsed ? null : <span className="min-w-0 wrap-anywhere">{copy.product}</span>}
          </Link>
        </RailTip>
      </div>

      <div className="min-h-0 flex-1 overflow-x-hidden overflow-y-auto">
        <nav
          aria-label={nav.main}
          className={cn('flex flex-col gap-0.5 p-2', collapsed && 'items-center')}
        >
          {items.map((item) => {
            const Icon = item.icon;
            const current = item.list === section ? (onListPage ? 'page' : 'true') : undefined;
            const itemSignal = item.list === 'runs' ? signal : null;
            return (
              <RailTip key={item.list} collapsed={collapsed} content={item.tip}>
                <Link
                  to={hrefs[item.list]}
                  aria-current={current}
                  aria-label={item.tip}
                  className={cn(
                    ROW,
                    HOVER,
                    'font-medium aria-[current]:bg-surface-selected aria-[current]:text-fg',
                    collapsed && 'w-control-md justify-center px-0',
                  )}
                >
                  {current ? (
                    <span
                      aria-hidden="true"
                      style={{ width: 2 }}
                      className="absolute top-1.5 bottom-1.5 left-0 rounded-r-sm bg-accent"
                    />
                  ) : null}
                  <Icon
                    aria-hidden="true"
                    className={cn(
                      'size-icon-md shrink-0',
                      current ? 'text-accent-fg' : 'text-fg-muted',
                    )}
                  />
                  {collapsed ? (
                    itemSignal ? (
                      <span
                        aria-hidden="true"
                        className={cn(
                          'absolute top-1 right-1 size-2 rounded-full ring-2 ring-surface-inset',
                          DOT[itemSignal],
                        )}
                      />
                    ) : null
                  ) : (
                    <>
                      <span className="min-w-0 flex-1 wrap-anywhere">{item.label}</span>
                      <span className="inline-flex shrink-0 items-center gap-1.5">
                        {itemSignal ? (
                          <span
                            aria-hidden="true"
                            className={cn('size-2 rounded-full', DOT[itemSignal])}
                          />
                        ) : null}
                        {item.total === null ? null : (
                          <span className="text-sm font-normal text-fg-muted tabular-nums">
                            {item.total.toLocaleString('en-US')}
                          </span>
                        )}
                      </span>
                    </>
                  )}
                </Link>
              </RailTip>
            );
          })}
        </nav>

        {!collapsed && recent.length > 0 ? (
          <section
            aria-labelledby={`${recentId}-h`}
            className="mx-2 border-t border-border-subtle pt-2 pb-3"
          >
            <h2 id={`${recentId}-h`} className="m-0">
              <button
                type="button"
                aria-expanded={recentOpen}
                aria-controls={recentId}
                onClick={() => {
                  const next = !recentOpen;
                  setRecentOpen(next);
                  writeString('local', RECENT_OPEN_KEY, next ? 'open' : 'closed');
                }}
                className="flex h-control-xs w-full items-center gap-1 rounded-md px-2 text-xs font-medium text-fg-muted hover:bg-surface-hover hover:text-fg"
              >
                <span>{nav.recent}</span>
                <ChevronDown
                  aria-hidden="true"
                  className={cn(
                    'size-icon-sm transition-transform duration-1',
                    !recentOpen && '-rotate-90',
                  )}
                />
              </button>
            </h2>
            <div id={recentId} hidden={!recentOpen} className="mt-1 flex flex-col gap-0.5">
              {recent.map((d) => {
                const info = d.lastRun
                  ? statusInfo({ kind: 'run', value: d.lastRun.status })
                  : null;
                const last =
                  info && d.lastRun
                    ? nav.lastRun(info.label, relativeWords(d.lastRun.createdAt, now))
                    : nav.notRunYet;
                return (
                  <Tooltip
                    key={d.workflowId}
                    side="right"
                    content={
                      <>
                        <strong className="block font-semibold">{d.name}</strong>
                        <span className="block text-fg-secondary">{last}</span>
                      </>
                    }
                  >
                    <Link
                      to={`/workflows/${encodeURIComponent(d.workflowId)}`}
                      className={cn(ROW, HOVER, 'items-start text-sm')}
                    >
                      <span
                        aria-hidden="true"
                        className={cn(
                          'mx-1 mt-1.5 size-2 shrink-0 rounded-full',
                          DOT[info ? info.tone : 'none'],
                        )}
                      />
                      <span className="min-w-0 flex-1 wrap-anywhere">{d.name}</span>
                      <span className="sr-only">{`. ${last}`}</span>
                    </Link>
                  </Tooltip>
                );
              })}
            </div>
          </section>
        ) : null}
      </div>

      <div
        className={cn(
          'flex shrink-0 flex-col gap-0.5 border-t border-border p-2',
          collapsed && 'items-center',
        )}
      >
        <Tooltip
          side="right"
          content={
            <>
              <strong className="block font-semibold">{conn.label}</strong>
              <span className="block text-fg-secondary">{conn.help}</span>
            </>
          }
        >
          <div
            role="status"
            tabIndex={0}
            className={cn(
              ROW,
              'cursor-default text-sm',
              connection === 'unreachable' ? 'text-st-failed-fg' : 'text-fg-secondary',
              collapsed && 'w-control-md justify-center px-0',
            )}
          >
            <span
              aria-hidden="true"
              className={cn('mx-1 size-2 shrink-0 rounded-full', CONNECTION_DOT[connection])}
            />
            <span className={cn('min-w-0 flex-1 wrap-anywhere', collapsed && 'sr-only')}>
              {conn.label}
            </span>
          </div>
        </Tooltip>

        <RailTip collapsed={collapsed} content={copy.shortcuts.entryTip}>
          <button
            type="button"
            aria-keyshortcuts="?"
            aria-label={collapsed ? nav.shortcuts : undefined}
            onClick={onShortcuts}
            className={cn(
              ROW,
              HOVER,
              'text-sm text-fg-muted',
              collapsed && 'w-control-md justify-center px-0',
            )}
          >
            <Keyboard aria-hidden="true" className="size-icon-md shrink-0 text-fg-muted" />
            {collapsed ? null : (
              <>
                <span className="min-w-0 flex-1 wrap-anywhere">{nav.shortcuts}</span>
                <Kbd>?</Kbd>
              </>
            )}
          </button>
        </RailTip>

        <Tooltip
          side="right"
          content={
            collapsed ? (
              <span className="inline-flex items-center gap-1.5">
                {nav.expand}
                <Kbd>[</Kbd>
              </span>
            ) : null
          }
        >
          <button
            type="button"
            aria-keyshortcuts="["
            aria-expanded={!collapsed}
            aria-label={collapsed ? nav.expand : nav.collapse}
            onClick={onToggle}
            className={cn(
              ROW,
              HOVER,
              'text-sm text-fg-muted',
              collapsed && 'w-control-md justify-center px-0',
            )}
          >
            <PanelLeft aria-hidden="true" className="size-icon-md shrink-0 text-fg-muted" />
            {collapsed ? null : (
              <>
                <span className="min-w-0 flex-1 wrap-anywhere">{nav.collapse}</span>
                <Kbd>[</Kbd>
              </>
            )}
          </button>
        </Tooltip>
      </div>
    </aside>
  );
}
