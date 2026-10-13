import type { Run } from '@/api/types';
import { WorkflowHeader } from '@/components/shell/WorkflowHeader';
import { Button } from '@/components/ui/button';
import { runStatusLabel, runStatusTone, type StatusTone } from '@/lib/status';
import { cn } from '@/lib/utils';

interface ObserveHeaderProps {
  run: Run;
  /** Name of the Run's bound Definition version; the workflow id stands in until it loads. */
  definitionName?: string;
  onRunAgain: () => void;
}

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
 * Observe header: the workflow header with the Runs tab current, the Run identity and bound
 * Definition version beside the name, and the server's aggregated Run status, verbatim.
 * Studio must not compute that status from the NodeRun list, even when a node is
 * WAITING_CALLBACK. Observe is read-only, so the only action is Run Again; the Editor tab
 * returns to editing.
 */
export function ObserveHeader({ run, definitionName, onRunAgain }: ObserveHeaderProps) {
  const tone = runStatusTone(run.status);

  return (
    <WorkflowHeader
      workflowId={run.workflowId}
      runId={run.id}
      name={definitionName ?? run.workflowId}
      tab="runs"
      identity={
        <>
          {/* The mode is always explicit, and the Run stays bound to its own
              immutable version, never the latest. */}
          <p
            data-testid="observe-subtitle"
            title={`Workflow ${run.workflowId}`}
            className="m-0 min-w-0 text-xs wrap-anywhere text-fg-muted"
          >
            Run {run.id}, Definition v{run.definitionVersion}, Observe
          </p>
          {/* The raw Run status is shown next to its display wording. */}
          <div
            className={cn(
              'flex shrink-0 items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-semibold whitespace-nowrap',
              PILL_TONE[tone],
            )}
          >
            <span aria-hidden="true" className={cn('h-2 w-2 rounded-full', DOT_TONE[tone])} />
            <span>{run.status}</span>
            <span data-testid="run-status">{runStatusLabel(run.status)}</span>
          </div>
        </>
      }
      actions={
        // Run Again reuses this Run's bound version and input; it never jumps to latest.
        <Button onClick={onRunAgain}>Run Again</Button>
      }
    />
  );
}
