import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router';

import { createRun } from '@/api/client';
import type { Definition, JsonObject, Run } from '@/api/types';
import { RunInputDialog } from '@/features/run-input/RunInputDialog';
import { runInputLabels } from '@/features/run-input/runInputLabels';

interface RunAgainDialogProps {
  open: boolean;
  onClose: () => void;
  /** The run this one starts from: its version and input. */
  run: Run | null;
  /** That run's own Definition version, never the latest. */
  definition: Definition | null;
  definitionFailed: boolean;
  latestVersion: number | null;
  /** Kept on the new run's address, so its back button returns to the same list. */
  search: string;
}

/**
 * Run again: a new run bound to this run's Definition version, starting from its input.
 * The Backend validates the input and creates the run; the new run then opens here.
 */
export function RunAgainDialog({
  open,
  onClose,
  run,
  definition,
  definitionFailed,
  latestVersion,
  search,
}: RunAgainDialogProps) {
  const navigate = useNavigate();
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<unknown>(null);
  const labels = useMemo(() => runInputLabels(definition?.nodes), [definition]);
  const from = new URLSearchParams(search).get('from');

  const close = () => {
    setSubmitError(null);
    onClose();
  };

  return (
    <RunInputDialog
      open={open && run !== null}
      onClose={close}
      workflowName={definition?.name}
      definitionVersion={run?.definitionVersion ?? 0}
      runInputSchema={definition?.runInputSchema}
      inputLabels={labels}
      loading={!definition && !definitionFailed}
      loadFailed={definitionFailed}
      source={run ? { runId: run.id, startedAt: run.startedAt, input: run.input } : undefined}
      latestVersion={latestVersion ?? undefined}
      submitting={submitting}
      submitError={submitError}
      onSubmit={(input: JsonObject) => {
        if (!run) return;
        setSubmitting(true);
        setSubmitError(null);
        createRun({ workflowId: run.workflowId, definitionVersion: run.definitionVersion, input })
          .then((created) => {
            close();
            const path = `/workflows/${encodeURIComponent(created.workflowId)}/runs/${encodeURIComponent(created.id)}`;
            void navigate(from ? `${path}?from=${encodeURIComponent(from)}` : path);
          })
          .catch((error: unknown) => {
            setSubmitError(error);
          })
          .finally(() => {
            setSubmitting(false);
          });
      }}
    />
  );
}
