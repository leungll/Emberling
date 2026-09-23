import { useCallback, useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router';

import { DetailPanel } from './DetailPanel';
import { EventTimeline } from './EventTimeline';
import { ObserveCanvas } from './ObserveCanvas';
import { ObserveHeader } from './ObserveHeader';
import { RunRail } from './RunRail';
import { observeRun, type ObservedRun } from './runObserver';
import { ApiRequestError, createRun, getDefinitionVersion } from '@/api/client';
import type { JsonObject, RunEvent, RunInputSchema } from '@/api/types';
import { RunInputDialog } from '@/features/run-input/RunInputDialog';
import { useStudioStore } from '@/stores/studio-store';

function describeError(error: unknown, fallback: string): string {
  if (error instanceof ApiRequestError) return `${error.code}: ${error.message}`;
  if (error instanceof Error) return `${fallback}: ${error.message}`;
  return fallback;
}

export function ObservePage() {
  const { runId = '' } = useParams();
  const navigate = useNavigate();

  const selectedNodeId = useStudioStore((s) => s.selectedNodeId);
  const selectNode = useStudioStore((s) => s.selectNode);
  const selectedEventSeq = useStudioStore((s) => s.selectedEventSeq);
  const selectEvent = useStudioStore((s) => s.selectEvent);
  const followLive = useStudioStore((s) => s.followLive);
  const setFollowLive = useStudioStore((s) => s.setFollowLive);

  const [observed, setObserved] = useState<ObservedRun>({
    snapshot: null,
    events: [],
    loadError: null,
  });
  const { snapshot, events } = observed;
  const error =
    observed.loadError === null
      ? null
      : observed.loadError instanceof ApiRequestError
        ? `${observed.loadError.code}: ${observed.loadError.message}`
        : 'Could not load run';

  // Run Again reopens the Run Input Dialog bound to THIS Run's own workflowId and
  // definitionVersion (never the latest), prefilled with this Run's own input. The
  // frozen runInputSchema for that version is fetched lazily, only when the dialog opens.
  const [runAgainOpen, setRunAgainOpen] = useState(false);
  const [runAgainSchema, setRunAgainSchema] = useState<RunInputSchema | undefined>(undefined);
  const [runAgainSubmitting, setRunAgainSubmitting] = useState(false);
  const [runAgainError, setRunAgainError] = useState<string | null>(null);

  // Snapshot, history, SSE handoff, gap re-query, reconnect and terminal close all live
  // in observeRun; the page only renders what it reports.
  useEffect(() => {
    const observer = observeRun(runId, setObserved);
    return () => observer.close();
  }, [runId]);

  const onSelectEvent = useCallback(
    (event: RunEvent) => {
      selectEvent(event.seq);
      if (event.nodeRunId) selectNode(event.nodeRunId);
    },
    [selectEvent, selectNode],
  );

  const onRunAgain = useCallback(() => {
    if (!snapshot) return;
    setRunAgainError(null);
    setRunAgainOpen(true);
    setRunAgainSchema(undefined);
    // The version is frozen and immutable, so this fetch is safe to key off it alone.
    getDefinitionVersion(snapshot.run.workflowId, snapshot.run.definitionVersion)
      .then((definition) => setRunAgainSchema(definition.runInputSchema))
      .catch((error: unknown) => {
        setRunAgainError(describeError(error, 'Could not load the run input schema'));
      });
  }, [snapshot]);

  const onRunAgainSubmit = useCallback(
    (input: JsonObject) => {
      if (!snapshot) return;
      setRunAgainSubmitting(true);
      setRunAgainError(null);
      createRun({
        workflowId: snapshot.run.workflowId,
        definitionVersion: snapshot.run.definitionVersion,
        input,
      })
        .then((run) => {
          setRunAgainOpen(false);
          navigate(`/runs/${run.id}`);
        })
        .catch((error: unknown) => {
          setRunAgainError(describeError(error, 'Could not create run'));
        })
        .finally(() => setRunAgainSubmitting(false));
    },
    [snapshot, navigate],
  );

  if (error) {
    return (
      <main className="dark flex h-screen items-center justify-center bg-[var(--background)] text-[var(--foreground)]">
        <p role="alert" className="text-sm text-red-500">
          {error}
        </p>
      </main>
    );
  }

  if (!snapshot) {
    return (
      <main className="dark flex h-screen items-center justify-center bg-[var(--background)] text-[var(--foreground)]">
        <p className="text-sm text-[var(--muted-foreground)]">Loading run…</p>
      </main>
    );
  }

  const selectedNodeRun = snapshot.nodeRuns.find((n) => n.id === selectedNodeId) ?? null;
  const selectedEvent = events.find((e) => e.seq === selectedEventSeq) ?? null;

  return (
    <div className="dark flex h-screen flex-col bg-[var(--background)] text-[var(--foreground)]">
      <ObserveHeader run={snapshot.run} onRunAgain={onRunAgain} />
      <div className="flex min-h-0 flex-1">
        <RunRail snapshot={snapshot} selectedNodeRunId={selectedNodeId} onSelect={selectNode} />
        {/* docs/04-ux.md §3 names only Run Rail | Event Timeline | Detail; §1.4 says Edit
            and Observe "两个模式复用 Header 和 Canvas" with Observe's Canvas as a "只读快照、
            缩小" (read-only snapshot, shrunk). §3 does not place it, so it sits above the
            Timeline in the centre column. */}
        <div className="flex min-w-0 flex-1 flex-col">
          <ObserveCanvas
            workflowId={snapshot.run.workflowId}
            definitionVersion={snapshot.run.definitionVersion}
            nodeRuns={snapshot.nodeRuns}
            selectedNodeRunId={selectedNodeId}
            onSelectNodeRun={selectNode}
          />
          <EventTimeline
            events={events}
            selectedSeq={selectedEventSeq}
            followLive={followLive}
            onSelect={onSelectEvent}
            onResumeLive={() => setFollowLive(true)}
          />
        </div>
        <DetailPanel
          snapshot={snapshot}
          selectedNodeRun={selectedNodeRun}
          selectedEvent={selectedEvent}
          events={events}
        />
      </div>

      <RunInputDialog
        open={runAgainOpen}
        onClose={() => setRunAgainOpen(false)}
        workflowId={snapshot.run.workflowId}
        definitionVersion={snapshot.run.definitionVersion}
        runInputSchema={runAgainSchema}
        initialInput={snapshot.run.input}
        submitting={runAgainSubmitting}
        errorMessage={runAgainError}
        onSubmit={onRunAgainSubmit}
      />
    </div>
  );
}
