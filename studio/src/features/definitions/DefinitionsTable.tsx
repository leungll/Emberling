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
import { formatTimestamp } from '@/lib/format';
import { runStatusLabel, runStatusVariant } from '@/lib/status';

export function DefinitionsTable({ items }: { items: DefinitionListItem[] }) {
  if (items.length === 0) {
    return (
      <div className="rounded-md border border-dashed border-[var(--border)] px-6 py-12 text-center">
        <p className="font-medium">No definitions yet</p>
        <p className="mt-1 text-[var(--muted-foreground)]">
          Create a Definition in Studio to run it on the Emberling Runtime.
        </p>
      </div>
    );
  }

  return (
    <div className="rounded-md border border-[var(--border)]">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Definition</TableHead>
            <TableHead>Version</TableHead>
            <TableHead>Last Run</TableHead>
            <TableHead>Updated</TableHead>
            <TableHead>Actions</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((item) => (
            <TableRow key={item.workflowId}>
              <TableCell>
                <Link to={`/studio/${item.workflowId}`} className="font-medium hover:underline">
                  {item.name}
                </Link>
                {item.description ? (
                  <p className="text-xs text-[var(--muted-foreground)]">{item.description}</p>
                ) : null}
              </TableCell>
              <TableCell className="tabular-nums">v{item.latestVersion}</TableCell>
              <TableCell>
                {item.lastRun ? (
                  <span className="flex items-center gap-2">
                    <Badge variant={runStatusVariant(item.lastRun.status)}>
                      {runStatusLabel(item.lastRun.status)}
                    </Badge>
                    <span className="text-xs text-[var(--muted-foreground)]">
                      {formatTimestamp(item.lastRun.createdAt)}
                    </span>
                  </span>
                ) : (
                  <span className="text-xs text-[var(--muted-foreground)]">Never run</span>
                )}
              </TableCell>
              <TableCell className="text-xs text-[var(--muted-foreground)]">
                {formatTimestamp(item.updatedAt)}
              </TableCell>
              <TableCell>
                <span className="flex gap-3 text-xs">
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
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
