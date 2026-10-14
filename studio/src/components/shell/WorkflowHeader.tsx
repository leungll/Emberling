import { ArrowLeft } from 'lucide-react';
import { useLayoutEffect, useRef, useState, type ReactNode } from 'react';
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
  /** Replaces the name heading, for a page where the name is edited in place. */
  nameSlot?: ReactNode;
}

/**
 * How the header fits its width: the centred grid; one row with the switch after the name
 * when a side outgrows its column; the identity wrapping under the name; and two rows (name
 * first, then the switch and actions) when even that is cut. Nothing overlaps.
 */
type HeaderFit = 'grid' | 'inline' | 'wrap' | 'stack';

function measureFit(h: HTMLElement): HeaderFit {
  const left = h.querySelector<HTMLElement>('[data-part="left"]');
  const tabs = h.querySelector<HTMLElement>('[data-part="tabs"]');
  const right = h.querySelector<HTMLElement>('[data-part="right"]');
  const identity = h.querySelector<HTMLElement>('[data-part="identity"]');
  if (!left || !tabs || !right) return 'grid';
  // Each trial fit is read back at once, so nothing in the header may transition meanwhile:
  // under reduced motion every property change gets a brief transition and would read stale.
  const parts = [h, ...Array.from(h.querySelectorAll<HTMLElement>('*'))];
  for (const el of parts) el.style.transitionProperty = 'none';
  const shown = h.dataset.fit;
  h.dataset.fit = 'grid';
  const cs = getComputedStyle(h);
  const gap = parseFloat(cs.columnGap) || 0;
  const col =
    (h.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight) - tabs.offsetWidth) /
      2 -
    gap;
  left.style.width = 'max-content';
  right.style.width = 'max-content';
  const need = Math.max(left.offsetWidth, right.offsetWidth);
  left.style.width = '';
  right.style.width = '';
  // The wrapped identity keeps the header's height (its rule is drawn over the content then,
  // so two control rows fit); taller than that, the header stacks.
  const overflows = () =>
    left.scrollWidth > left.clientWidth + 1 ||
    (!!identity &&
      (identity.scrollWidth > identity.clientWidth + 1 ||
        identity.offsetHeight > h.clientHeight + 1));
  let fit: HeaderFit = 'grid';
  if (need > col) {
    fit = 'inline';
    for (const next of ['wrap', 'stack'] as const) {
      h.dataset.fit = fit;
      if (!overflows()) break;
      fit = next;
    }
  }
  if (shown) h.dataset.fit = shown;
  h.getBoundingClientRect(); // settles the restored fit before transitions return
  for (const el of parts) el.style.transitionProperty = '';
  return fit;
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
  nameSlot,
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

  const ref = useRef<HTMLElement>(null);
  const [fit, setFit] = useState<HeaderFit>('grid');
  // The parts and what they hold (an edited name, a chip that appears once loaded) are
  // watched, so the fit follows content changes as well as the window.
  useLayoutEffect(() => {
    const h = ref.current;
    if (!h) return;
    const ro = new ResizeObserver(() => {
      setFit(measureFit(h));
    });
    ro.observe(h);
    for (const el of Array.from(h.querySelectorAll('[data-part], [data-part] > *'))) ro.observe(el);
    return () => {
      ro.disconnect();
    };
  });

  return (
    <header
      ref={ref}
      data-ui-header
      data-fit={fit}
      style={{ gridTemplateColumns: 'minmax(0, 1fr) auto minmax(0, 1fr)' }}
      className="relative grid h-header shrink-0 items-center gap-3 border-b border-border bg-surface pr-4 pl-2 data-[fit=wrap]:border-b-0 data-[fit=wrap]:after:absolute data-[fit=wrap]:after:inset-x-0 data-[fit=wrap]:after:bottom-0 data-[fit=wrap]:after:h-px data-[fit=wrap]:after:bg-border data-[fit=inline]:flex data-[fit=stack]:h-auto data-[fit=stack]:min-h-header data-[fit=stack]:flex-wrap data-[fit=stack]:gap-y-1 data-[fit=stack]:py-1 data-[fit=wrap]:flex"
    >
      <div
        data-part="left"
        className="flex min-w-0 items-center gap-2 in-data-[fit=inline]:shrink in-data-[fit=stack]:basis-full in-data-[fit=wrap]:shrink"
      >
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
        <div
          data-part="identity"
          className="flex min-w-0 items-center gap-2 in-data-[fit=stack]:flex-wrap in-data-[fit=wrap]:flex-wrap in-data-[fit=wrap]:gap-y-0"
        >
          {nameSlot ??
            (name ? (
              <h1 className="m-0 min-w-0 px-1 text-md font-semibold wrap-anywhere text-fg">
                {name}
              </h1>
            ) : null)}
          {identity}
        </div>
      </div>

      <nav
        data-part="tabs"
        aria-label={nav.workflowViews}
        className="inline-flex w-fit shrink-0 items-center justify-self-center rounded-md bg-surface-hover outline outline-1 -outline-offset-1 outline-border"
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

      <div
        data-part="right"
        className="flex min-w-0 items-center justify-end gap-2 in-data-[fit=inline]:grow in-data-[fit=inline]:shrink-0 in-data-[fit=stack]:grow in-data-[fit=wrap]:grow in-data-[fit=wrap]:shrink-0"
      >
        {actions}
        {actions ? <HeaderSeparator /> : null}
        <TimeZonePicker />
      </div>
    </header>
  );
}
