import { Link } from 'react-router';

import type { DefinitionListItem } from '@/api/types';
import { Badge } from '@/components/ui/badge';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { formatRelativeTime, formatTimestamp } from '@/lib/format';
import { runStatusLabel, runStatusVariant } from '@/lib/status';

/**
 * Avatar background/foreground pairs, cycled by workflowId. Purely decorative
 * grouping: the API carries no colour or icon field, so no meaning is attached to which
 * pair a given Definition receives.
 */
const AVATAR_PALETTE = [
  { bg: 'var(--surface-selected)', fg: 'var(--accent-fg)' },
  { bg: 'var(--surface-hover)', fg: 'var(--fg-secondary)' },
  { bg: 'var(--surface-inset)', fg: 'var(--fg-muted)' },
];

// `index` is always in bounds (`% AVATAR_PALETTE.length`), but `noUncheckedIndexedAccess`
// cannot see that; this fallback only satisfies the type checker and is never actually hit.
const DEFAULT_AVATAR = { bg: 'var(--surface-hover)', fg: 'var(--fg-secondary)' };

function avatarStyle(workflowId: string): { bg: string; fg: string } {
  let hash = 0;
  for (let i = 0; i < workflowId.length; i += 1) hash = (hash * 31 + workflowId.charCodeAt(i)) | 0;
  const index = Math.abs(hash) % AVATAR_PALETTE.length;
  return AVATAR_PALETTE[index] ?? DEFAULT_AVATAR;
}

export function DefinitionsTable({ items }: { items: DefinitionListItem[] }) {
  if (items.length === 0) {
    return (
      <div className="rounded-xl border border-dashed border-border px-6 py-12 text-center">
        <p className="font-medium">No definitions yet</p>
        <p className="mt-1 text-fg-muted">
          Create a Definition in Studio to run it on the Emberling Runtime.
        </p>
      </div>
    );
  }

  return (
    <div className="overflow-hidden rounded-xl border border-border bg-surface">
      <Table>
        <TableHeader>
          <TableRow className="bg-surface-hover hover:bg-surface-hover">
            <TableHead className="h-12 text-xs font-semibold">Definition</TableHead>
            <TableHead className="h-12 text-xs font-semibold">Version</TableHead>
            <TableHead className="h-12 text-xs font-semibold">Last Run</TableHead>
            <TableHead className="h-12 text-xs font-semibold">Updated</TableHead>
            <TableHead className="h-12 text-xs font-semibold">Actions</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((item) => {
            const avatar = avatarStyle(item.workflowId);
            const initial = item.name.trim().charAt(0).toUpperCase() || '?';
            return (
              <TableRow key={item.workflowId} className="[&>td]:py-4">
                <TableCell>
                  <div className="flex items-center gap-3">
                    <span
                      aria-hidden="true"
                      className="flex h-control-lg w-control-lg shrink-0 items-center justify-center rounded-full text-md font-semibold"
                      style={{ backgroundColor: avatar.bg, color: avatar.fg }}
                    >
                      {initial}
                    </span>
                    <div>
                      <Link
                        to={`/studio/${item.workflowId}`}
                        className="text-md font-semibold hover:underline"
                      >
                        {item.name}
                      </Link>
                      {item.description ? (
                        <p className="mt-0.5 text-md text-fg-muted">{item.description}</p>
                      ) : null}
                    </div>
                  </div>
                </TableCell>
                <TableCell className="font-semibold tabular-nums">v{item.latestVersion}</TableCell>
                <TableCell>
                  {item.lastRun ? (
                    // Status and time: the pill states the server Run status and
                    // the helper line under it states when that Run was created.
                    <div className="flex flex-col items-start gap-1">
                      <Badge dot variant={runStatusVariant(item.lastRun.status)} className="">
                        {runStatusLabel(item.lastRun.status)}
                      </Badge>
                      <time
                        data-testid={`last-run-time-${item.workflowId}`}
                        dateTime={item.lastRun.createdAt}
                        title={formatTimestamp(item.lastRun.createdAt)}
                        className="pl-1 text-xs text-fg-muted"
                      >
                        {formatRelativeTime(item.lastRun.createdAt)}
                      </time>
                    </div>
                  ) : (
                    <span className="text-md text-fg-muted">Never run</span>
                  )}
                </TableCell>
                <TableCell className="text-md text-fg-muted">
                  {formatRelativeTime(item.updatedAt)}
                </TableCell>
                <TableCell>
                  <span className="flex gap-3 text-md font-medium">
                    <Link to={`/studio/${item.workflowId}`} className="hover:underline">
                      Edit
                    </Link>
                    <Link to={`/studio/${item.workflowId}?run=1`} className="hover:underline">
                      Run
                    </Link>
                    {item.lastRun ? (
                      <Link to={`/runs/${item.lastRun.id}`} className="hover:underline">
                        Open Last Run
                      </Link>
                    ) : null}
                  </span>
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}
