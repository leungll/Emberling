import { Link } from 'react-router';

import { Button } from '@/components/ui/button';

interface StudioHeaderProps {
  name: string;
  workflowId: string;
  version: number | null;
  hasUnsavedChanges: boolean;
  busy?: boolean;
  /** False while there is neither a loaded Definition nor a new draft to save. */
  canSave?: boolean;
  onValidate: () => void;
  onSave: () => void;
  onRun: () => void;
}

/** Header actions follow the mock's large, equal-weight buttons. */
const ACTION = 'h-control-lg px-5 font-semibold';

/** Edit-mode header. Observe mode keeps the same shell but swaps the actions. */
export function StudioHeader({
  name,
  workflowId,
  version,
  hasUnsavedChanges,
  busy = false,
  canSave = true,
  onValidate,
  onSave,
  onRun,
}: StudioHeaderProps) {
  return (
    <header className="flex h-header shrink-0 items-center justify-between gap-4 border-b border-border bg-surface px-4">
      <div className="flex items-center gap-3">
        <Link
          to="/"
          aria-label="Back to Definitions"
          className="flex h-control-lg w-control-lg shrink-0 items-center justify-center rounded-lg bg-accent text-lg font-semibold text-fg-on-accent"
        >
          E
        </Link>
        <div>
          <h1 className="text-md font-semibold">{name}</h1>
          {/* The mode is always explicit, so Edit states "Definition vN · Editing".
              The workflow id stays available on hover as the bound Definition identity. */}
          <p
            data-testid="studio-subtitle"
            title={version === null ? undefined : `Workflow ${workflowId}`}
            className="text-xs text-fg-muted"
          >
            {version === null ? 'Unsaved draft' : `Definition v${version}`}, Editing
          </p>
        </div>
        {hasUnsavedChanges ? (
          <span
            data-testid="unsaved-indicator"
            className="flex items-center gap-1.5 text-xs text-st-waiting-fg"
          >
            <span aria-hidden="true" className="h-2 w-2 rounded-full bg-st-waiting-dot" />
            Unsaved changes
          </span>
        ) : null}
      </div>

      <div className="flex items-center gap-2">
        <Button variant="outline" className={ACTION} onClick={onSave} disabled={busy || !canSave}>
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
