import { Link } from 'react-router';

import type { Run } from '@/api/types';
import { Button, buttonVariants } from '@/components/ui/button';
import { runStatusLabel, runStatusTone, type StatusTone } from '@/lib/status';
import { cn } from '@/lib/utils';

interface ObserveHeaderProps {
  run: Run;
  /** Name of the Run's bound Definition version; the workflow id stands in until it loads. */
  definitionName?: string;
  onRunAgain: () => void;
}

/** Header actions share the Edit header's large, equal-weight buttons. */
const ACTION = 'h-9 min-w-[88px] px-5 font-semibold';

const PILL_TONE: Record<StatusTone, string> = {
  neutral: 'border-[var(--border)] bg-[var(--status-neutral-bg)] text-[var(--status-neutral-fg)]',
  running:
    'border-[var(--status-running-dot)] bg-[var(--status-running-bg)] text-[var(--status-running-fg)]',
  waiting:
    'border-[var(--status-waiting-dot)] bg-[var(--status-waiting-bg)] text-[var(--status-waiting-fg)]',
  succeeded:
    'border-[var(--status-succeeded-dot)] bg-[var(--status-succeeded-bg)] text-[var(--status-succeeded-fg)]',
  failed:
    'border-[var(--status-failed-dot)] bg-[var(--status-failed-bg)] text-[var(--status-failed-fg)]',
};

const DOT_TONE: Record<StatusTone, string> = {
  neutral: 'bg-[var(--status-neutral-dot)]',
  running: 'bg-[var(--status-running-dot)] animate-pulse',
  waiting: 'bg-[var(--status-waiting-dot)]',
  succeeded: 'bg-[var(--status-succeeded-dot)]',
  failed: 'bg-[var(--status-failed-dot)]',
};

/**
 * Observe header: Run identity, bound Definition version and the server's
 * aggregated Run status, verbatim. Studio must not compute that status from the NodeRun
 * list, even when a node is WAITING_CALLBACK. Observe is read-only, so the only actions
 * are Back to Edit and Run Again.
 */
export function ObserveHeader({ run, definitionName, onRunAgain }: ObserveHeaderProps) {
  const tone = runStatusTone(run.status);

  return (
    <header className="grid h-[72px] shrink-0 grid-cols-[1fr_auto_1fr] items-center gap-4 border-b border-[var(--border)] bg-[var(--card)] px-4">
      <div className="flex min-w-0 items-center gap-3">
        <Link
          to="/"
          aria-label="Back to Definitions"
          className="flex h-9 w-9 shrink-0 items-center justify-center rounded-[9px] bg-[var(--primary)] text-[18px] font-bold text-[var(--primary-foreground)]"
        >
          E
        </Link>
        <div className="min-w-0">
          <h1 className="truncate text-[15px] font-semibold">{definitionName ?? run.workflowId}</h1>
          {/* The mode is always explicit, and the Run stays bound to its own
              immutable version, never the latest. */}
          <p
            data-testid="observe-subtitle"
            title={`Workflow ${run.workflowId}`}
            className="truncate text-xs text-[var(--muted-foreground)]"
          >
            Run {run.id} · Definition v{run.definitionVersion} · Observe
          </p>
        </div>
      </div>

      {/* The raw Run status is shown next to its display wording. */}
      <div
        className={cn(
          'flex items-center gap-2 rounded-full border px-4 py-1.5 text-[14px] font-semibold',
          PILL_TONE[tone],
        )}
      >
        <span aria-hidden="true" className={cn('h-2.5 w-2.5 rounded-full', DOT_TONE[tone])} />
        <span>{run.status}</span>
        <span aria-hidden="true">·</span>
        <span data-testid="run-status">{runStatusLabel(run.status)}</span>
      </div>

      <div className="flex items-center justify-end gap-2">
        <Link
          to={`/studio/${run.workflowId}`}
          className={cn(buttonVariants({ variant: 'outline' }), ACTION)}
        >
          Back to Edit
        </Link>
        {/* Run Again reuses this Run's bound version and input; it never jumps to latest. */}
        <Button className={ACTION} onClick={onRunAgain}>
          Run Again
        </Button>
      </div>
    </header>
  );
}
