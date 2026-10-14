import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router';

import { createRun, getDefinition, getRun, listRuns } from '@/api/client';
import type { Definition, JsonObject } from '@/api/types';
import { RunInputDialog, type RunInputSource } from '@/features/run-input/RunInputDialog';
import { runInputLabels } from '@/features/run-input/runInputLabels';

interface StartRunDialogProps {
  /** The workflow to run; null keeps the dialog closed. */
  workflowId: string | null;
  onClose: () => void;
  /** Opened from the Runs page: the new run opens with that page as its way back. */
  fromRuns?: boolean;
}

interface Loaded {
  workflowId: string;
  definition: Definition | null;
  failed: boolean;
  lastRun?: RunInputSource;
}

/**
 * A new run of a workflow's latest saved version. The Run dialog binds to that version
 * explicitly and offers the workflow's last run's input; the Backend validates the input
 * and creates the run, then the run opens in its workflow's Runs tab.
 */
export function StartRunDialog({ workflowId, onClose, fromRuns = false }: StartRunDialogProps) {
  const navigate = useNavigate();
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<unknown>(null);

  useEffect(() => {
    if (!workflowId) return;
    const controller = new AbortController();
    const { signal } = controller;
    getDefinition(workflowId, signal)
      .then((definition) => {
        setLoaded((s) =>
          s?.workflowId === workflowId
            ? { ...s, definition, failed: false }
            : { workflowId, definition, failed: false },
        );
      })
      .catch(() => {
        if (signal.aborted) return;
        setLoaded({ workflowId, definition: null, failed: true });
      });
    // The last run's input is an offer only; without it the form simply starts empty.
    listRuns({ workflowId, limit: 1 }, signal)
      .then((page) => {
        const last = page.items[0];
        return last ? getRun(last.id, signal) : null;
      })
      .then((snapshot) => {
        if (!snapshot) return;
        const lastRun: RunInputSource = {
          runId: snapshot.run.id,
          startedAt: snapshot.run.startedAt,
          input: snapshot.run.input,
        };
        setLoaded((s) =>
          s?.workflowId === workflowId
            ? { ...s, lastRun }
            : { workflowId, definition: null, failed: false, lastRun },
        );
      })
      .catch(() => undefined);
    return () => {
      controller.abort();
    };
  }, [workflowId]);

  const current = loaded?.workflowId === workflowId ? loaded : null;
  const definition = current?.definition ?? null;
  const labels = useMemo(() => runInputLabels(definition?.nodes), [definition]);

  const close = () => {
    setSubmitError(null);
    setLoaded(null);
    onClose();
  };

  return (
    <RunInputDialog
      open={workflowId !== null}
      onClose={close}
      workflowName={definition?.name}
      definitionVersion={definition?.version ?? 0}
      runInputSchema={definition?.runInputSchema}
      inputLabels={labels}
      loading={!definition && !current?.failed}
      loadFailed={current?.failed ?? false}
      lastRun={current?.lastRun}
      submitting={submitting}
      submitError={submitError}
      onSubmit={(input: JsonObject) => {
        if (!definition) return;
        setSubmitting(true);
        setSubmitError(null);
        createRun({
          workflowId: definition.workflowId,
          definitionVersion: definition.version,
          input,
        })
          .then((run) => {
            close();
            const path = `/workflows/${encodeURIComponent(run.workflowId)}/runs/${encodeURIComponent(run.id)}`;
            void navigate(fromRuns ? `${path}?from=runs` : path);
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
