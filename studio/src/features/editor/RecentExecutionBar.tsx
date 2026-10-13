import { Link } from 'react-router';

import type { RunSnapshot } from '@/api/types';
import { Badge } from '@/components/ui/badge';
import { formatDuration } from '@/lib/format';
import { runStatusLabel, runStatusVariant } from '@/lib/status';

interface RecentExecutionBarProps {
  /** False while the Recent Execution fetch is still in flight; nothing renders until then. */
  loaded: boolean;
  snapshot: RunSnapshot | null;
}

/**
 * Summarises the Definition's most recent Run, sourced from the Definition list's
 * `lastRun` plus one `GET /runs/:id` for duration and Event count. A Definition that has
 * never run shows an explicit empty state instead of a blank bar.
 */
export function RecentExecutionBar({ loaded, snapshot }: RecentExecutionBarProps) {
  if (!loaded) return null;

  if (!snapshot) {
    return (
      <div className="flex h-16 shrink-0 items-center border-t border-border bg-surface px-5 text-md text-fg-muted">
        <span className="text-xs font-semibold">RECENT EXECUTION</span>
        <span className="ml-6">No runs yet</span>
      </div>
    );
  }

  const { run, lastSeq } = snapshot;

  return (
    <div className="flex h-16 shrink-0 items-center justify-between border-t border-border bg-surface px-5 text-md">
      <div className="flex items-center gap-3">
        <span className="mr-3 text-xs font-semibold text-fg-muted">RECENT EXECUTION</span>
        <span className="font-mono text-md font-semibold">{run.id}</span>
        <Badge variant={runStatusVariant(run.status)} dot className="">
          {runStatusLabel(run.status)}
        </Badge>
        <span className="text-fg-muted">Definition v{run.definitionVersion}</span>
        <span className="text-fg-muted">{formatDuration(run.startedAt, run.completedAt)}</span>
        <span className="text-fg-muted">{lastSeq} events</span>
      </div>
      <Link
        to={`/runs/${run.id}`}
        className="rounded-md border border-border px-4 py-2 font-semibold hover:bg-surface-hover"
      >
        Open Last Run
      </Link>
    </div>
  );
}
