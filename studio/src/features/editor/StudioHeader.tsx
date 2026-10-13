import { WorkflowHeader } from '@/components/shell/WorkflowHeader';
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

/**
 * The Editor's header: the workflow header with the Editor tab current, the saved version
 * and the unsaved marker beside the name, and Save, Validate and Run on the right.
 */
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
    <WorkflowHeader
      workflowId={version === null ? undefined : workflowId}
      name={name}
      tab="editor"
      identity={
        <>
          {/* The mode is always explicit, so Edit states "Definition vN, Editing".
              The workflow id stays available on hover as the bound Definition identity. */}
          <p
            data-testid="studio-subtitle"
            title={version === null ? undefined : `Workflow ${workflowId}`}
            className="m-0 shrink-0 text-xs whitespace-nowrap text-fg-muted"
          >
            {version === null ? 'Unsaved draft' : `Definition v${version}`}, Editing
          </p>
          {hasUnsavedChanges ? (
            <span
              data-testid="unsaved-indicator"
              className="flex shrink-0 items-center gap-1.5 text-xs whitespace-nowrap text-st-waiting-fg"
            >
              <span aria-hidden="true" className="h-2 w-2 rounded-full bg-st-waiting-dot" />
              Unsaved changes
            </span>
          ) : null}
        </>
      }
      actions={
        <>
          <Button variant="secondary" onClick={onSave} disabled={busy || !canSave}>
            Save
          </Button>
          <Button variant="secondary" onClick={onValidate} disabled={busy}>
            Validate
          </Button>
          {/* Run targets the saved, validated version only, so unsaved edits disable it. */}
          <Button
            onClick={onRun}
            disabled={busy || hasUnsavedChanges || version === null}
            title={hasUnsavedChanges ? 'Save the Definition before running it' : undefined}
          >
            Run
          </Button>
        </>
      }
    />
  );
}
