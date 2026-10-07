import { Link } from 'react-router';

import { Button } from '@/components/ui/button';

interface StudioHeaderProps {
  name: string;
  workflowId: string;
  version: number | null;
  hasUnsavedChanges: boolean;
  busy?: boolean;
  onValidate: () => void;
  onSave: () => void;
  onRun: () => void;
}

/** Header actions follow the mock's large, equal-weight buttons. */
const ACTION = 'h-9 min-w-[88px] px-5 font-semibold';

/** Edit-mode header. Observe mode keeps the same shell but swaps the actions. */
export function StudioHeader({
  name,
  workflowId,
  version,
  hasUnsavedChanges,
  busy = false,
  onValidate,
  onSave,
  onRun,
}: StudioHeaderProps) {
  return (
    <header className="flex h-[72px] shrink-0 items-center justify-between gap-4 border-b border-[var(--border)] bg-[var(--card)] px-4">
      <div className="flex items-center gap-3">
        <Link
          to="/"
          aria-label="Back to Definitions"
          className="flex h-9 w-9 shrink-0 items-center justify-center rounded-[9px] bg-[var(--primary)] text-[18px] font-bold text-[var(--primary-foreground)]"
        >
          E
        </Link>
        <div>
          <h1 className="text-[15px] font-semibold">{name}</h1>
          {/* The mode is always explicit, so Edit states "Definition vN · Editing".
              The workflow id stays available on hover as the bound Definition identity. */}
          <p
            data-testid="studio-subtitle"
            title={version === null ? undefined : `Workflow ${workflowId}`}
            className="text-xs text-[var(--muted-foreground)]"
          >
            {version === null ? 'Unsaved draft' : `Definition v${version}`} · Editing
          </p>
        </div>
        {hasUnsavedChanges ? (
          <span
            data-testid="unsaved-indicator"
            className="flex items-center gap-1.5 text-xs text-[var(--status-waiting-fg)]"
          >
            <span
              aria-hidden="true"
              className="h-2 w-2 rounded-full bg-[var(--status-waiting-dot)]"
            />
            Unsaved changes
          </span>
        ) : null}
      </div>

      <div className="flex items-center gap-2">
        <Button variant="outline" className={ACTION} onClick={onSave} disabled={busy}>
          Save
        </Button>
        <Button variant="outline" className={ACTION} onClick={onValidate} disabled={busy}>
          Validate
        </Button>
        {/* Run targets the saved, validated version only, so unsaved edits disable it. */}
        <Button
          className={ACTION}
          onClick={onRun}
          disabled={busy || hasUnsavedChanges || version === null}
          title={hasUnsavedChanges ? 'Save the Definition before running it' : undefined}
        >
          Run
        </Button>
      </div>
    </header>
  );
}
