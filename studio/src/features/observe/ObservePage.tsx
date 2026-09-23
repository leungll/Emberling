import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router';

import { DetailPanel } from './DetailPanel';
import { EventTimeline } from './EventTimeline';
import { ObserveCanvas } from './ObserveCanvas';
import { ObserveHeader } from './ObserveHeader';
import { Panel, SectionLabel } from './Panel';
import { RunRail } from './RunRail';
import { RunSummaryCard, RunSummaryCompact } from './RunSummaryCard';
import { WorkflowStepper } from './WorkflowStepper';
import { useBoundDefinition } from './boundDefinition';
import { observeRun, type ObservedRun } from './runObserver';
import { ApiRequestError, createRun, getDefinitionVersion } from '@/api/client';
import type { Definition, JsonObject, RunEvent, RunInputSchema } from '@/api/types';
import { RunInputDialog } from '@/features/run-input/RunInputDialog';
import { useStudioStore } from '@/stores/studio-store';

function describeError(error: unknown, fallback: string): string {
  if (error instanceof ApiRequestError) return `${error.code}: ${error.message}`;
  if (error instanceof Error) return `${fallback}: ${error.message}`;
  return fallback;
}

/** Registered Node Type of a MANAGED_AGENT NodeRun, which gets the Agent layout (04 §3.4). */
const AGENT_NODE_TYPE = 'agent';

/**
 * Absolute Agent deadline of one Agent NodeRun, as committed in its AGENT_STARTED Event
 * payload. `undefined` when no such Event (or no deadline) has been received.
 */
function agentDeadlineOf(events: readonly RunEvent[], nodeRunId: string): string | undefined {
  const started = events.find(
    (event) => event.nodeRunId === nodeRunId && event.type === 'AGENT_STARTED',
  );
  const deadline = started?.payload.deadline;
  return typeof deadline === 'string' ? deadline : undefined;
}

/** `allowedTools` of one node in the bound Definition version, if it declares any. */
function allowedToolsOf(definition: Definition | null, nodeId: string): string[] | undefined {
  const tools = definition?.nodes.find((node) => node.id === nodeId)?.config.allowedTools;
  return Array.isArray(tools)
    ? tools.filter((tool): tool is string => typeof tool === 'string')
    : undefined;
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

  // The Run's own bound version, read once for the Canvas, node names and Agent Tools.
  const bound = useBoundDefinition(
    snapshot?.run.workflowId ?? '',
    snapshot?.run.definitionVersion ?? 0,
    snapshot === null,
  );
  const nodeNames = useMemo(
    () => new Map((bound.definition?.nodes ?? []).map((node) => [node.id, node.name])),
    [bound.definition],
  );

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
  const nodeRunNames = new Map(
    snapshot.nodeRuns.flatMap((nodeRun) => {
      const name = nodeNames.get(nodeRun.nodeId);
      return name ? [[nodeRun.id, name] as const] : [];
    }),
  );
  const agentSelected = selectedNodeRun?.nodeType === AGENT_NODE_TYPE;

  const canvas = (
    <ObserveCanvas
      workflowId={snapshot.run.workflowId}
      definitionVersion={snapshot.run.definitionVersion}
      nodeRuns={snapshot.nodeRuns}
      selectedNodeRunId={selectedNodeId}
      onSelectNodeRun={selectNode}
      bound={bound}
    />
  );
  const detail = (
    <DetailPanel
      snapshot={snapshot}
      selectedNodeRun={selectedNodeRun}
      selectedEvent={selectedEvent}
      events={events}
      nodeName={selectedNodeRun ? nodeNames.get(selectedNodeRun.nodeId) : undefined}
      allowedTools={
        selectedNodeRun && agentSelected
          ? allowedToolsOf(bound.definition, selectedNodeRun.nodeId)
          : undefined
      }
    />
  );
  const rail = (
    <RunRail
      snapshot={snapshot}
      selectedNodeRunId={selectedNodeId}
      onSelect={selectNode}
      nodeNames={nodeNames}
      layout="row"
    />
  );

  return (
    <div className="dark flex h-screen flex-col bg-[var(--background)] text-[var(--foreground)]">
      <ObserveHeader
        run={snapshot.run}
        definitionName={bound.definition?.name}
        onRunAgain={onRunAgain}
      />

      {agentSelected ? (
        // 04 §3.4 Agent view: workflow stepper and Run facts | Turn timeline | Detail, over
        // an Event stream strip.
        <>
          <main className="flex min-h-0 flex-1">
            <aside className="flex w-[360px] shrink-0 flex-col gap-4 overflow-auto border-r border-[var(--border)] p-5">
              <section className="shrink-0">
                <div className="flex items-center justify-between gap-3 pb-4">
                  <SectionLabel>Workflow · Read only</SectionLabel>
                  <span className="text-[13px] text-[var(--muted-foreground)]">
                    {snapshot.nodeRuns.length} NodeRuns
                  </span>
                </div>
                {bound.definition ? (
                  <WorkflowStepper
                    definition={bound.definition}
                    nodeRuns={snapshot.nodeRuns}
                    events={events}
                    selectedNodeRunId={selectedNodeId}
                    onSelect={selectNode}
                  />
                ) : (
                  <p className="text-sm text-[var(--muted-foreground)]">
                    {bound.error ?? 'Loading workflow…'}
                  </p>
                )}
              </section>
              <RunSummaryCompact
                snapshot={snapshot}
                nodeNames={nodeNames}
                deadline={selectedNodeRun ? agentDeadlineOf(events, selectedNodeRun.id) : undefined}
              />
            </aside>
            {detail}
          </main>
          <EventTimeline
            variant="strip"
            events={events}
            selectedSeq={selectedEventSeq}
            followLive={followLive}
            onSelect={onSelectEvent}
            onResumeLive={() => setFollowLive(true)}
            nodeRunNames={nodeRunNames}
          />
        </>
      ) : (
        // 04 §3.3 Run view: topology and Run Summary over Event Timeline and Detail.
        <main className="flex min-h-0 flex-1 flex-col gap-4 p-4">
          <div className="flex h-[400px] shrink-0 gap-4">
            <Panel
              title="Read-only Topology"
              meta={`Definition v${snapshot.run.definitionVersion}`}
              className="w-[430px] shrink-0"
              bodyClassName="px-2 pb-2"
            >
              {canvas}
            </Panel>
            <div className="flex min-w-0 flex-1">
              <RunSummaryCard
                snapshot={snapshot}
                events={events}
                followLive={followLive}
                definitionName={bound.definition?.name}
                nodeNames={nodeNames}
              >
                {rail}
              </RunSummaryCard>
            </div>
          </div>
          <div className="flex min-h-0 flex-1 gap-4">
            <section className="flex w-[490px] shrink-0 flex-col rounded-xl border border-[var(--border)] bg-[var(--card)]">
              <EventTimeline
                events={events}
                selectedSeq={selectedEventSeq}
                followLive={followLive}
                onSelect={onSelectEvent}
                onResumeLive={() => setFollowLive(true)}
                nodeRunNames={nodeRunNames}
              />
            </section>
            <section className="flex min-w-0 flex-1 rounded-xl border border-[var(--border)] bg-[var(--card)]">
              {detail}
            </section>
          </div>
        </main>
      )}

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
