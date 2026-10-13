import { Link } from 'react-router';

import type { Run } from '@/api/types';
import { Button } from '@/components/ui/button';
import { buttonVariants } from '@/components/ui/button-variants';
import { runStatusLabel, runStatusTone, type StatusTone } from '@/lib/status';
import { cn } from '@/lib/utils';

interface ObserveHeaderProps {
  run: Run;
  /** Name of the Run's bound Definition version; the workflow id stands in until it loads. */
  definitionName?: string;
  onRunAgain: () => void;
}

/** Header actions share the Edit header's large, equal-weight buttons. */
const ACTION = 'h-control-lg px-5 font-semibold';

const PILL_TONE: Record<StatusTone, string> = {
  neutral: 'border-border bg-st-queued-bg text-st-queued-fg',
  running: 'border-st-running-dot bg-st-running-bg text-st-running-fg',
  waiting: 'border-st-waiting-dot bg-st-waiting-bg text-st-waiting-fg',
  succeeded: 'border-st-succeeded-dot bg-st-succeeded-bg text-st-succeeded-fg',
  failed: 'border-st-failed-dot bg-st-failed-bg text-st-failed-fg',
};

const DOT_TONE: Record<StatusTone, string> = {
  neutral: 'bg-st-queued-dot',
  running: 'bg-st-running-dot animate-pulse',
  waiting: 'bg-st-waiting-dot',
  succeeded: 'bg-st-succeeded-dot',
  failed: 'bg-st-failed-dot',
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
    <header
      style={{ gridTemplateColumns: '1fr auto 1fr' }}
      className="grid h-header shrink-0 items-center gap-4 border-b border-border bg-surface px-4"
    >
      <div className="flex min-w-0 items-center gap-3">
        <Link
          to="/"
          aria-label="Back to Definitions"
          className="flex h-control-lg w-control-lg shrink-0 items-center justify-center rounded-lg bg-accent text-lg font-semibold text-fg-on-accent"
        >
          E
        </Link>
        <div className="min-w-0">
          <h1 className="truncate text-md font-semibold">{definitionName ?? run.workflowId}</h1>
          {/* The mode is always explicit, and the Run stays bound to its own
              immutable version, never the latest. */}
          <p
            data-testid="observe-subtitle"
            title={`Workflow ${run.workflowId}`}
            className="truncate text-xs text-fg-muted"
          >
            Run {run.id}, Definition v{run.definitionVersion}, Observe
          </p>
        </div>
      </div>

      {/* The raw Run status is shown next to its display wording. */}
      <div
        className={cn(
          'flex items-center gap-2 rounded-full border px-4 py-1.5 text-md font-semibold',
          PILL_TONE[tone],
        )}
      >
        <span aria-hidden="true" className={cn('h-2 w-2 rounded-full', DOT_TONE[tone])} />
        <span>{run.status}</span>
        <span aria-hidden="true">·</span>
        <span data-testid="run-status">{runStatusLabel(run.status)}</span>
      </div>

      <div className="flex items-center justify-end gap-2">
        <Link
          to={`/studio/${run.workflowId}`}
          className={cn(buttonVariants({ variant: 'secondary' }), ACTION)}
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
