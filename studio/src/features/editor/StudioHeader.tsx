import { Link } from 'react-router';

import { Badge } from '@/components/ui/badge';
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
    <header className="flex items-center justify-between gap-4 border-b border-[var(--border)] px-4 py-2">
      <div className="flex items-center gap-3">
        <Link to="/" className="text-xs text-[var(--muted-foreground)] hover:underline">
          Definitions
        </Link>
        <div>
          <h1 className="text-sm font-semibold">{name}</h1>
          <p className="text-[10px] text-[var(--muted-foreground)]">
            {workflowId} · {version === null ? 'unsaved' : `v${version}`}
          </p>
        </div>
        {hasUnsavedChanges ? (
          <Badge variant="waiting" data-testid="unsaved-indicator">
            Unsaved changes
          </Badge>
        ) : null}
      </div>

      <div className="flex items-center gap-2">
        <Button variant="outline" size="sm" onClick={onValidate} disabled={busy}>
          Validate
        </Button>
        <Button variant="outline" size="sm" onClick={onSave} disabled={busy}>
          Save
        </Button>
        {/* Run targets the saved, validated version only, so unsaved edits disable it. */}
        <Button
          size="sm"
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
