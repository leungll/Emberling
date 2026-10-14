import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router';

import { runHref } from './workflow-list';
import { createRun, getDefinition, getRun } from '@/api/client';
import type { Definition, DefinitionListItem, JsonObject, RunSnapshot } from '@/api/types';
import { RunInputDialog } from '@/features/run-input/RunInputDialog';
import { runInputLabels } from '@/features/run-input/runInputLabels';

type Loaded =
  | { kind: 'loading' }
  | { kind: 'ready'; definition: Definition; lastRun: RunSnapshot | null }
  | { kind: 'failed' };

interface WorkflowRunDialogProps {
  /** The workflow whose Run was chosen from its row menu; null while closed. */
  item: DefinitionListItem | null;
  onClose: () => void;
}

/**
 * The Run dialog over the Workflows list. It binds the run to the workflow's latest saved
 * version, read fresh when the dialog opens, and offers the last run's input. Starting the
 * run opens it in the workflow's Runs tab.
 */
export function WorkflowRunDialog({ item, onClose }: WorkflowRunDialogProps) {
  const navigate = useNavigate();
  const [loaded, setLoaded] = useState<Loaded>({ kind: 'loading' });
  const [loadedFor, setLoadedFor] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<unknown>(null);

  const workflowId = item?.workflowId ?? null;
  const lastRunId = item?.lastRun?.id ?? null;

  // Each opening starts from a fresh load and a clean submit state.
  if (workflowId !== loadedFor) {
    setLoadedFor(workflowId);
    setLoaded({ kind: 'loading' });
    setSubmitting(false);
    setSubmitError(null);
  }

  useEffect(() => {
    if (!workflowId) return;
    const controller = new AbortController();
    const definition = getDefinition(workflowId, controller.signal);
    // The last run only fills "Use last run's input"; failing to read it leaves that out.
    const last = lastRunId
      ? getRun(lastRunId, controller.signal).catch(() => null)
      : Promise.resolve(null);
    Promise.all([definition, last]).then(
      ([d, r]) => {
        setLoaded({ kind: 'ready', definition: d, lastRun: r });
      },
      () => {
        if (!controller.signal.aborted) setLoaded({ kind: 'failed' });
      },
    );
    return () => {
      controller.abort();
    };
  }, [workflowId, lastRunId]);

  const definition = loaded.kind === 'ready' ? loaded.definition : undefined;
  const lastRun = loaded.kind === 'ready' ? loaded.lastRun : null;
  const labels = useMemo(() => runInputLabels(definition?.nodes), [definition]);

  const start = async (input: JsonObject) => {
    if (!definition || !workflowId) return;
    setSubmitting(true);
    setSubmitError(null);
    try {
      const created = await createRun({
        workflowId,
        definitionVersion: definition.version,
        input,
      });
      onClose();
      void navigate(runHref(workflowId, created.id));
    } catch (error) {
      setSubmitError(error);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <RunInputDialog
      open={item !== null}
      onClose={onClose}
      workflowName={item?.name}
      definitionVersion={definition?.version ?? item?.latestVersion ?? 1}
      runInputSchema={definition?.runInputSchema}
      inputLabels={labels}
      loading={loaded.kind === 'loading'}
      loadFailed={loaded.kind === 'failed'}
      lastRun={
        lastRun
          ? { runId: lastRun.run.id, startedAt: lastRun.run.startedAt, input: lastRun.run.input }
          : undefined
      }
      submitting={submitting}
      submitError={submitError}
      onSubmit={(input) => void start(input)}
    />
  );
}
