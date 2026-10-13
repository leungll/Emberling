import { useEffect, useRef, type MouseEvent } from 'react';
import { Link, useNavigate } from 'react-router';

import { runHref } from './runs-url';
import type { RunListItem } from '@/api/types';
import { IdChip } from '@/components/id/IdChip';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import { statusInfo } from '@/components/status/status-info';
import { Duration, LiveDuration, RelativeTime } from '@/components/time/Time';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import { endedDurationMs, isLive, olderTip, runSubline, usedOlderVersion } from '@/lib/run-words';
import { listTime } from '@/lib/time';
import { useNow } from '@/lib/use-now';
import { useRowNav } from '@/lib/use-row-nav';
import { cn } from '@/lib/utils';
import { useTimeZone } from '@/stores/prefs-store';

const HEAD = 'h-row-dense px-4 text-left align-middle text-xs font-medium text-fg-muted';
const CELL = 'px-4 py-3 align-top text-sm';
/** Columns that size to their content; the workflow column takes the rest and wraps. */
const FIT = { width: '1%' } as const;

/**
 * Every run as one row, newest first. The whole row opens the run (⌘ or Ctrl opens it in a
 * new tab; a click that ends a text selection does nothing). One Tab stop for the table:
 * ↑/↓ move between rows, ←/→ reach the row's other controls, Enter opens.
 */
export function RunsTable({ items, busy }: { items: readonly RunListItem[]; busy: boolean }) {
  const copy = useCopy();
  const t = copy.runs.page;
  const navigate = useNavigate();
  const nav = useRowNav();
  const body = useRef<HTMLTableSectionElement>(null);
  const zone = useTimeZone();
  const now = useNow();

  useEffect(() => {
    const el = body.current;
    if (!el) return;
    // The ID chip's link and copy button, and the version tag, are the row's secondary
    // controls: reachable with ←/→ rather than as Tab stops of their own.
    el.querySelectorAll<HTMLElement>('[data-row-controls] a, [data-row-controls] button').forEach(
      (c) => {
        c.setAttribute('data-row-control', '');
      },
    );
    nav.reset(el);
  }, [items, nav]);

  const onRowClick = (e: MouseEvent<HTMLTableRowElement>, href: string) => {
    if ((e.target as Element).closest('a, button, [data-row-control]')) return;
    if (String(window.getSelection() ?? '')) return;
    if (e.metaKey || e.ctrlKey) {
      window.open(href, '_blank', 'noopener');
      return;
    }
    void navigate(href);
  };

  return (
    <table aria-busy={busy || undefined} className="w-full border-collapse text-sm">
      <thead>
        <tr className="border-b border-border-subtle bg-surface-inset">
          <th scope="col" className={HEAD}>
            {t.columns.workflow}
          </th>
          <th scope="col" style={FIT} className={cn(HEAD, 'whitespace-nowrap')}>
            {t.columns.status}
          </th>
          <th scope="col" style={FIT} className={cn(HEAD, 'whitespace-nowrap')}>
            {t.columns.started}
          </th>
          <th scope="col" style={FIT} className={cn(HEAD, 'text-right whitespace-nowrap')}>
            {t.columns.duration}
          </th>
          <th scope="col" style={FIT} className={cn(HEAD, 'whitespace-nowrap')}>
            {t.columns.runId}
          </th>
        </tr>
      </thead>
      <tbody ref={body} onKeyDown={nav.onKeyDown} onFocus={nav.onFocus}>
        {items.map((item) => {
          const href = runHref(item);
          const sub = runSubline(item);
          const status = statusInfo({ kind: 'run', value: item.status });
          const ms = endedDurationMs(item);
          return (
            <tr
              key={item.id}
              data-row={item.id}
              onClick={(e) => {
                onRowClick(e, href);
              }}
              className="cursor-pointer border-b border-border-subtle last:border-b-0 hover:bg-surface-hover focus-within:bg-surface-hover"
            >
              <td className={CELL}>
                <div className="flex min-w-0 flex-col gap-0.5">
                  <div className="flex min-w-0 flex-wrap items-center gap-2">
                    <Link
                      to={href}
                      data-row-primary
                      aria-label={t.rowLabel(
                        item.workflowName,
                        status.label,
                        listTime(item.startedAt, now, zone),
                      )}
                      className="inline-flex min-h-control-xs items-center rounded-sm text-md font-medium wrap-anywhere text-fg no-underline hover:underline"
                    >
                      {item.workflowName}
                    </Link>
                    {usedOlderVersion(item) ? (
                      <Tooltip content={olderTip(item)}>
                        <span
                          data-row-control
                          tabIndex={-1}
                          className="inline-flex h-control-xs cursor-default items-center rounded-sm bg-surface-hover px-1.5 text-xs text-fg-secondary"
                        >
                          {t.olderVersion}
                        </span>
                      </Tooltip>
                    ) : null}
                  </div>
                  {sub ? <p className="m-0 text-sm text-fg-muted">{sub}</p> : null}
                </div>
              </td>
              <td style={FIT} className={cn(CELL, 'whitespace-nowrap')}>
                <StatusIndicator status={{ kind: 'run', value: item.status }} colourLabel />
              </td>
              <td style={FIT} className={cn(CELL, 'whitespace-nowrap text-fg-secondary')}>
                <RelativeTime iso={item.startedAt} />
              </td>
              <td
                style={FIT}
                className={cn(CELL, 'text-right whitespace-nowrap text-fg-secondary')}
              >
                {isLive(item.status) ? (
                  <span data-live-clock>
                    <LiveDuration start={item.startedAt} />
                  </span>
                ) : (
                  <Duration ms={ms} />
                )}
              </td>
              <td style={FIT} className={cn(CELL, 'whitespace-nowrap')}>
                <span data-row-controls>
                  <IdChip id={item.id} label={t.runIdLabel} to={href} />
                </span>
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}
