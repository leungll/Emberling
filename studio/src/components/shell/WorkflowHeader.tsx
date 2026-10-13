import { ArrowLeft } from 'lucide-react';
import type { ReactNode } from 'react';
import { Link, useLocation } from 'react-router';

import { HeaderSeparator } from './PageHeader';
import { TimeZonePicker } from '@/components/prefs/TimeZonePicker';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import { backList, carryFrom, listHref, writeListState } from '@/lib/nav/list-state';
import { cn } from '@/lib/utils';

export type WorkflowTab = 'editor' | 'runs';

interface WorkflowHeaderProps {
  /** Absent for a workflow that has not been saved yet: it has no Runs tab to go to. */
  workflowId?: string;
  name: string;
  /** Beside the name: the version, the workflow ID, an unsaved marker. */
  identity?: ReactNode;
  tab: WorkflowTab;
  /** The run on screen, so the Runs list can mark it on return. */
  runId?: string;
  actions?: ReactNode;
}

const TAB =
  'inline-flex h-control-sm items-center justify-center rounded-md border border-transparent px-3 text-sm font-medium whitespace-nowrap text-fg-muted hover:text-fg focus-visible:-outline-offset-2';
const TAB_CURRENT = 'border-border-control bg-surface text-fg hover:text-fg';

/**
 * The header of a workflow page (its Editor and Runs tabs): one back button to the list the
 * page was entered from ("Runs" with from=runs, else "Workflows"), the workflow name, the
 * Editor | Runs switch in the centre, then the page's actions and the time zone.
 */
export function WorkflowHeader({
  workflowId,
  name,
  identity,
  tab,
  runId,
  actions,
}: WorkflowHeaderProps) {
  const copy = useCopy();
  const nav = copy.nav;
  const { search } = useLocation();
  const list = backList(search);
  const backLabel = list === 'runs' ? nav.backToRuns : nav.backToWorkflows;
  const base = workflowId ? `/workflows/${encodeURIComponent(workflowId)}` : null;

  const tabs: { value: WorkflowTab; label: string; to: string | null }[] = [
    { value: 'editor', label: nav.editor, to: base ? carryFrom(base, search) : null },
    { value: 'runs', label: nav.runs, to: base ? carryFrom(`${base}/runs`, search) : null },
  ];

  return (
    <header
      data-ui-header
      style={{ gridTemplateColumns: 'minmax(0, 1fr) auto minmax(0, 1fr)' }}
      className="grid h-header shrink-0 items-center gap-3 border-b border-border bg-surface pr-4 pl-2"
    >
      <div className="flex min-w-0 items-center gap-2">
        <Tooltip content={backLabel}>
          <Link
            to={listHref(list, true)}
            aria-label={backLabel}
            onClick={() => {
              const opened = list === 'runs' ? runId : workflowId;
              if (opened) writeListState(list, { opened });
            }}
            className="inline-flex h-control-sm shrink-0 items-center gap-1 rounded-md pr-2 pl-1.5 text-md font-medium text-fg-secondary hover:bg-surface-hover hover:text-fg active:bg-surface-pressed"
          >
            <ArrowLeft aria-hidden="true" className="size-icon-sm" />
            <span>{list === 'runs' ? nav.runs : nav.workflows}</span>
          </Link>
        </Tooltip>
        <span aria-hidden="true" className="h-icon-md w-px shrink-0 bg-border" />
        <div className="flex min-w-0 items-center gap-2">
          {name ? (
            <h1 className="m-0 min-w-0 px-1 text-md font-semibold wrap-anywhere text-fg">{name}</h1>
          ) : null}
          {identity}
        </div>
      </div>

      <nav
        aria-label={nav.workflowViews}
        className="inline-flex w-fit items-center justify-self-center rounded-md bg-surface-hover outline outline-1 -outline-offset-1 outline-border"
      >
        {tabs.map((t) => {
          const current = t.value === tab;
          if (!t.to) {
            return (
              <Tooltip key={t.value} content={current ? null : nav.newWorkflowRuns}>
                <span
                  aria-current={current ? 'page' : undefined}
                  aria-disabled={current ? undefined : 'true'}
                  tabIndex={current ? undefined : 0}
                  className={cn(
                    TAB,
                    current
                      ? TAB_CURRENT
                      : 'cursor-default text-fg-placeholder hover:text-fg-placeholder',
                  )}
                >
                  {t.label}
                </span>
              </Tooltip>
            );
          }
          return (
            <Link
              key={t.value}
              to={t.to}
              aria-current={current ? 'page' : undefined}
              className={cn(TAB, current && TAB_CURRENT)}
            >
              {t.label}
            </Link>
          );
        })}
      </nav>

      <div className="flex min-w-0 items-center justify-end gap-2">
        {actions}
        {actions ? <HeaderSeparator /> : null}
        <TimeZonePicker />
      </div>
    </header>
  );
}
