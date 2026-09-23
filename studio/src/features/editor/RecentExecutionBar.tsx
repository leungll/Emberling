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
 * Summarises the Definition's most recent Run (04 §2.6), sourced from the Definition list's
 * `lastRun` plus one `GET /runs/:id` for duration and Event count. A Definition that has
 * never run shows an explicit empty state instead of a blank bar.
 */
export function RecentExecutionBar({ loaded, snapshot }: RecentExecutionBarProps) {
  if (!loaded) return null;

  if (!snapshot) {
    return (
      <div className="flex h-14 shrink-0 items-center border-t border-[var(--border)] bg-[var(--card)] px-5 text-sm text-[var(--muted-foreground)]">
        <span className="text-xs font-bold tracking-[0.08em]">RECENT EXECUTION</span>
        <span className="ml-6">No runs yet</span>
      </div>
    );
  }

  const { run, lastSeq } = snapshot;

  return (
    <div className="flex h-14 shrink-0 items-center justify-between border-t border-[var(--border)] bg-[var(--card)] px-5 text-sm">
      <div className="flex items-center gap-3">
        <span className="mr-3 text-xs font-bold tracking-[0.08em] text-[var(--muted-foreground)]">
          RECENT EXECUTION
        </span>
        <span className="font-mono text-[14px] font-semibold">{run.id}</span>
        <Badge variant={runStatusVariant(run.status)} dot className="uppercase">
          {runStatusLabel(run.status)}
        </Badge>
        <span className="text-[var(--muted-foreground)]">Definition v{run.definitionVersion}</span>
        <span className="text-[var(--muted-foreground)]">
          {formatDuration(run.startedAt, run.completedAt)}
        </span>
        <span className="text-[var(--muted-foreground)]">{lastSeq} events</span>
      </div>
      <Link
        to={`/runs/${run.id}`}
        className="rounded-md border border-[var(--border)] px-4 py-2 font-semibold hover:bg-[var(--accent)]"
      >
        Open Last Run
      </Link>
    </div>
  );
}
