import { Link } from 'react-router';

import type { Run } from '@/api/types';
import { Badge } from '@/components/ui/badge';
import { Button, buttonVariants } from '@/components/ui/button';
import { runStatusLabel, runStatusVariant } from '@/lib/status';

interface ObserveHeaderProps {
  run: Run;
  onRunAgain: () => void;
}

/**
 * Observe header. The status shown is the server's aggregated Run status, verbatim.
 * Studio must not compute it from the NodeRun list, even when a node is WAITING_CALLBACK.
 */
export function ObserveHeader({ run, onRunAgain }: ObserveHeaderProps) {
  return (
    <header className="flex items-center justify-between gap-4 border-b border-[var(--border)] px-4 py-2">
      <div className="flex items-center gap-3">
        <div>
          <h1 className="text-sm font-semibold">{run.id}</h1>
          <p className="text-[10px] text-[var(--muted-foreground)]">
            {run.workflowId} · v{run.definitionVersion}
          </p>
        </div>
        <Badge variant={runStatusVariant(run.status)} data-testid="run-status">
          {runStatusLabel(run.status)}
        </Badge>
      </div>

      <div className="flex items-center gap-2">
        <Link
          to={`/studio/${run.workflowId}`}
          className={buttonVariants({ variant: 'outline', size: 'sm' })}
        >
          Back to Edit
        </Link>
        {/* Run Again reuses this Run's bound version and input; it never jumps to latest. */}
        <Button size="sm" onClick={onRunAgain}>
          Run Again
        </Button>
      </div>
    </header>
  );
}
