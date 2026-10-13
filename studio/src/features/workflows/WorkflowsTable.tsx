import { Activity, MoreVertical, Pencil, Play } from 'lucide-react';
import { useLayoutEffect, useRef, type MouseEvent, type ReactNode } from 'react';
import { Link, useNavigate } from 'react-router';

import { editorHref, highlight, runHref, runsHref, type MatchedWorkflow } from './workflow-list';
import type { DefinitionListItem } from '@/api/types';
import { IdChip } from '@/components/id/IdChip';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import { statusInfo } from '@/components/status/status-info';
import { RelativeTime } from '@/components/time/Time';
import { IconButton } from '@/components/ui/button';
import { Menu, MenuContent, MenuItem, MenuLinkItem, MenuTrigger } from '@/components/ui/menu';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import { shortCode } from '@/lib/short-code';
import { listTime } from '@/lib/time';
import { useNow } from '@/lib/use-now';
import { useRowNav } from '@/lib/use-row-nav';
import { cn } from '@/lib/utils';
import { useTimeZone } from '@/stores/prefs-store';

// Keyboard focus on a row's name outlines the whole row rather than the link's glyphs; a
// row restored on return holds focus quietly, without the ring, until a key is pressed.
const ROW_RING = [
  'has-[[data-row-primary]:focus-visible:not([data-quiet-focus])]:outline-2',
  'has-[[data-row-primary]:focus-visible:not([data-quiet-focus])]:outline-solid',
  'has-[[data-row-primary]:focus-visible:not([data-quiet-focus])]:outline-focus',
  'has-[[data-row-primary]:focus-visible:not([data-quiet-focus])]:-outline-offset-2',
].join(' ');

const MARK = 'rounded-sm bg-surface-selected text-accent-fg';

/**
 * The name column never narrows below this. Names may break anywhere, so without a floor
 * the fitted columns would squeeze them to one letter per line; the table scrolls inside
 * its card instead.
 */
const NAME_MIN_WIDTH = 192;

function Highlighted({ text, q }: { text: string; q: string }) {
  return (
    <>
      {highlight(text, q).map((s, i) =>
        s.hit ? (
          <mark key={i} className={MARK}>
            {s.text}
          </mark>
        ) : (
          s.text
        ),
      )}
    </>
  );
}

interface WorkflowsTableProps {
  rows: readonly MatchedWorkflow[];
  /** The normalised search, for highlighting. */
  q: string;
  onRun: (item: DefinitionListItem) => void;
}

/**
 * The workflow rows. The whole row opens the Editor; its links and buttons keep their own
 * behaviour. The table is one Tab stop: ↑/↓ move between rows, ←/→ between a row's
 * controls, and Shift+F10 opens the row's menu.
 */
export function WorkflowsTable({ rows, q, onRun }: WorkflowsTableProps) {
  const copy = useCopy();
  const c = copy.workflows.columns;
  const navigate = useNavigate();
  const nav = useRowNav();
  const bodyRef = useRef<HTMLTableSectionElement>(null);

  // Every link and button in a row other than its name is a row control: reached with ←/→,
  // not with Tab. The ID chip's own link and copy button are marked here as well.
  const keys = rows.map((r) => r.item.workflowId).join(' ');
  useLayoutEffect(() => {
    const body = bodyRef.current;
    if (!body) return;
    body.querySelectorAll<HTMLElement>('tr[data-row]').forEach((row) => {
      row.querySelectorAll<HTMLElement>('a[href], button').forEach((el) => {
        if (!el.hasAttribute('data-row-primary')) el.setAttribute('data-row-control', '');
      });
    });
    const focused = document.activeElement?.closest<HTMLElement>('tr[data-row]');
    nav.reset(body, focused && body.contains(focused) ? focused.dataset.row : null);
  }, [keys, nav]);

  const onRowClick = (e: MouseEvent<HTMLTableRowElement>, href: string) => {
    const target = e.target as Element;
    // Clicks inside a portalled menu bubble through React but are not in the row.
    if (!e.currentTarget.contains(target)) return;
    if (target.closest('a, button')) return;
    if (String(window.getSelection() ?? '')) return;
    if (e.metaKey || e.ctrlKey) {
      window.open(href, '_blank', 'noopener');
      return;
    }
    void navigate(href);
  };

  return (
    <table className="w-full border-collapse">
      <thead>
        <tr>
          <Th minWidth={NAME_MIN_WIDTH}>{c.name}</Th>
          <Th fit>{c.lastRun}</Th>
          <Th fit>{c.updated}</Th>
          <Th fit>{c.workflowId}</Th>
          <Th fit className="text-right">
            {c.actions}
          </Th>
        </tr>
      </thead>
      <tbody ref={bodyRef} onKeyDown={nav.onKeyDown} onFocus={nav.onFocus}>
        {rows.map(({ item, match }) => {
          const href = editorHref(item.workflowId);
          const text = match === 'text' ? q : '';
          return (
            <tr
              key={item.workflowId}
              data-row={item.workflowId}
              onClick={(e) => {
                onRowClick(e, href);
              }}
              className={cn(
                'cursor-pointer border-b border-border-subtle transition-colors duration-1 last:border-b-0 hover:bg-surface-hover active:bg-surface-pressed has-[[aria-expanded=true]]:bg-surface-hover',
                ROW_RING,
              )}
            >
              <Td>
                <Link
                  to={href}
                  data-row-primary
                  className="inline-block rounded-sm py-0.5 text-md font-medium wrap-anywhere text-fg no-underline underline-offset-3 hover:text-accent-fg hover:underline focus-visible:outline-none"
                >
                  <Highlighted text={item.name} q={text} />
                </Link>
                {item.description ? (
                  <p className="m-0 text-sm wrap-anywhere text-fg-muted">
                    <Highlighted text={item.description} q={text} />
                  </p>
                ) : null}
              </Td>
              <Td fit>
                <LastRunCell item={item} />
              </Td>
              <Td fit>
                <span className="inline-flex" style={{ minWidth: 128 }}>
                  <RelativeTime iso={item.updatedAt} />
                </span>
              </Td>
              <Td fit>
                <IdChip
                  id={item.workflowId}
                  label={copy.workflows.idLabel}
                  to={href}
                  match={match === 'id' ? q : undefined}
                />
              </Td>
              <Td fit>
                <RowMenu item={item} onRun={onRun} />
              </Td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function Th({
  children,
  fit,
  minWidth,
  className,
}: {
  children: string;
  fit?: boolean;
  minWidth?: number;
  className?: string;
}) {
  return (
    <th
      scope="col"
      style={fit ? { width: '1%' } : { minWidth }}
      className={cn(
        'h-row-dense border-b border-border-subtle bg-surface-inset px-4 text-left align-middle text-xs font-medium whitespace-nowrap text-fg-muted',
        className,
      )}
    >
      {children}
    </th>
  );
}

function Td({ children, fit }: { children: ReactNode; fit?: boolean }) {
  return (
    <td
      style={fit ? { width: '1%' } : undefined}
      className={cn('px-4 py-3 align-middle text-sm text-fg-secondary', fit && 'whitespace-nowrap')}
    >
      {children}
    </td>
  );
}

/** The last run's status word in its colour over its time, linking to that run. */
function LastRunCell({ item }: { item: DefinitionListItem }) {
  const copy = useCopy();
  const zone = useTimeZone();
  const now = useNow();
  const run = item.lastRun;
  if (!run) return <span className="text-fg-muted">{copy.workflows.notRunYet}</span>;

  const status = { kind: 'run' as const, value: run.status };
  const when = listTime(run.createdAt, now, zone);
  const reason = failureReason(item, copy);
  const link = (
    <Link
      to={runHref(item.workflowId, run.id)}
      data-row-control
      aria-label={copy.workflows.lastRunLink(statusInfo(status).label, when, shortCode(run.id))}
      style={{ minWidth: 128 }}
      className="group/last inline-flex flex-col items-start gap-0.5 rounded-sm no-underline"
    >
      <StatusIndicator
        status={status}
        colourLabel
        help=""
        className="font-medium underline-offset-3 group-hover/last:underline"
      />
      <time
        dateTime={run.createdAt}
        className="text-sm text-fg-muted tabular-nums underline-offset-3 group-hover/last:underline"
      >
        {when}
      </time>
    </Link>
  );
  return reason ? <Tooltip content={reason}>{link}</Tooltip> : link;
}

/** A failed last run's reason from its error code; nothing when the code is unknown. */
function failureReason(item: DefinitionListItem, copy: ReturnType<typeof useCopy>): string | null {
  const run = item.lastRun;
  if (run?.status !== 'FAILED') return null;
  const code = run.error?.code;
  const step = run.failedStep?.nodeName ?? null;
  const titles: Readonly<Record<string, { title: string } | undefined>> = copy.errors;
  const known = code ? titles[code] : undefined;
  if (known) return copy.workflows.lastRunReason(known.title, step);
  return step ? copy.unknownError(step) : null;
}

function RowMenu({
  item,
  onRun,
}: {
  item: DefinitionListItem;
  onRun: (item: DefinitionListItem) => void;
}) {
  const copy = useCopy();
  const m = copy.workflows.rowMenu;
  const trigger = useRef<HTMLButtonElement>(null);
  // Run opens its dialog only after the menu has closed and given focus back to this
  // button, so the dialog returns focus here and the closing menu can't take it back.
  const runChosen = useRef(false);
  return (
    <div className="flex justify-end">
      <Menu>
        <MenuTrigger asChild>
          <IconButton
            ref={trigger}
            label={copy.actionsFor(item.name)}
            tooltip={m.tip}
            aria-haspopup="menu"
            data-row-menu
          >
            <MoreVertical aria-hidden="true" />
          </IconButton>
        </MenuTrigger>
        <MenuContent
          label={copy.actionsFor(item.name)}
          onCloseAutoFocus={(e) => {
            if (!runChosen.current) return;
            runChosen.current = false;
            e.preventDefault();
            trigger.current?.focus();
            onRun(item);
          }}
        >
          <MenuItem
            icon={<Play aria-hidden="true" />}
            onSelect={() => {
              runChosen.current = true;
            }}
          >
            {m.run}
          </MenuItem>
          <MenuLinkItem>
            <Link to={editorHref(item.workflowId)}>
              <Pencil aria-hidden="true" />
              {m.edit}
            </Link>
          </MenuLinkItem>
          <MenuLinkItem>
            <Link to={runsHref(item.workflowId)}>
              <Activity aria-hidden="true" />
              {m.runs}
            </Link>
          </MenuLinkItem>
        </MenuContent>
      </Menu>
    </div>
  );
}
