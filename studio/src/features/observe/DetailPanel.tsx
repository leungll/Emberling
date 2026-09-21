import { useEffect, useState } from 'react';

import { ApiRequestError, getAgentTrace, getNodeRunDetail } from '@/api/client';
import type {
  AgentTrace,
  AgentTurnTrace,
  NodeRun,
  NodeRunDetail,
  NodeRunStatus,
  RunEvent,
  RunSnapshot,
} from '@/api/types';
import { Badge } from '@/components/ui/badge';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Separator } from '@/components/ui/separator';
import { formatDuration, formatMillis, formatTimestamp } from '@/lib/format';
import {
  agentActionStatusVariant,
  agentTurnStatusVariant,
  nodeAttemptStatusVariant,
  nodeRunStatusLabel,
  nodeRunStatusVariant,
  toolAttemptStatusVariant,
} from '@/lib/status';

/** Registered Node Type of a MANAGED_AGENT NodeRun. Only these have an Agent Trace. */
const AGENT_NODE_TYPE = 'agent';

interface DetailPanelProps {
  snapshot: RunSnapshot;
  selectedNodeRun: NodeRun | null;
  selectedEvent: RunEvent | null;
  /** Committed Events held by the page so far, used only to find the latest one already
   * known for the selected NodeRun. This never fetches or invents an Event of its own. */
  events: RunEvent[];
}

/** NodeRun and Event detail. Every value is a server fact rendered as received. */
export function DetailPanel({
  snapshot,
  selectedNodeRun,
  selectedEvent,
  events,
}: DetailPanelProps) {
  const detail = useNodeRunDetail(snapshot.run.id, selectedNodeRun?.id, selectedNodeRun?.status);

  // Only a MANAGED_AGENT NodeRun has an Agent Trace; every other Node Type would 404.
  const agentNodeRunId =
    selectedNodeRun?.nodeType === AGENT_NODE_TYPE ? selectedNodeRun.id : undefined;
  const trace = useAgentTrace(
    snapshot.run.id,
    agentNodeRunId,
    latestAgentEventSeq(events, agentNodeRunId),
  );

  const latestEvent = selectedNodeRun
    ? events
        .filter((event) => event.nodeRunId === selectedNodeRun.id)
        .reduce<RunEvent | null>(
          (latest, event) => (!latest || event.seq > latest.seq ? event : latest),
          null,
        )
    : null;

  return (
    <aside className="flex w-96 shrink-0 flex-col">
      <div className="border-b border-[var(--border)] px-3 py-2">
        <h2 className="text-xs font-semibold uppercase tracking-wide text-[var(--muted-foreground)]">
          Detail
        </h2>
      </div>

      <ScrollArea className="flex-1 p-3 text-xs">
        {!selectedNodeRun && !selectedEvent ? <RunSummary snapshot={snapshot} /> : null}

        {selectedNodeRun ? (
          <NodeRunDetailView
            nodeRun={selectedNodeRun}
            detail={detail.value}
            detailError={detail.error}
            latestEvent={latestEvent}
          />
        ) : null}

        {agentNodeRunId ? (
          <>
            <Separator className="my-3" />
            <AgentTraceView trace={trace.value} traceError={trace.error} />
          </>
        ) : null}

        {selectedEvent ? (
          <>
            {selectedNodeRun ? <Separator className="my-3" /> : null}
            <section className="space-y-2">
              <h3 className="font-medium">Event #{selectedEvent.seq}</h3>
              <Field label="Type" value={selectedEvent.type} />
              <Field label="At" value={formatTimestamp(selectedEvent.timestamp)} />
              <Field label="NodeRun" value={selectedEvent.nodeRunId ?? '— (run level)'} />
              <JsonBlock label="Payload" value={selectedEvent.payload} />
            </section>
          </>
        ) : null}
      </ScrollArea>
    </aside>
  );
}

function RunSummary({ snapshot }: { snapshot: RunSnapshot }) {
  return (
    <section className="space-y-2">
      <h3 className="font-medium">Run Summary</h3>
      <Field label="Run" value={snapshot.run.id} />
      <Field label="Workflow" value={snapshot.run.workflowId} />
      <Field label="Version" value={`v${snapshot.run.definitionVersion}`} />
      <Field label="NodeRuns" value={String(snapshot.nodeRuns.length)} />
      <JsonBlock label="Input" value={snapshot.run.input} />
      {snapshot.run.output !== null ? (
        <JsonBlock label="Output" value={snapshot.run.output} />
      ) : null}
      {snapshot.run.error ? (
        <JsonBlock label="Error" value={snapshot.run.error as unknown} />
      ) : null}
    </section>
  );
}

/**
 * Fetches the NodeRun's Attempts and Callback Binding summaries when a NodeRun is selected.
 * Refetches whenever the NodeRun's own `status` changes (e.g. an SSE-driven transition out
 * of `WAITING_CALLBACK`), since that is the only signal this read-only projection needs.
 */
function useNodeRunDetail(
  runId: string,
  nodeRunId: string | undefined,
  status: NodeRunStatus | undefined,
): { value: NodeRunDetail | null; error: string | null } {
  const [state, setState] = useState<{ value: NodeRunDetail | null; error: string | null }>({
    value: null,
    error: null,
  });

  useEffect(() => {
    if (!nodeRunId) return;
    const controller = new AbortController();
    getNodeRunDetail(runId, nodeRunId, controller.signal)
      .then((detail) => setState({ value: detail, error: null }))
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setState({
          value: null,
          error: error instanceof ApiRequestError ? error.message : 'Failed to load Node detail',
        });
      });
    return () => controller.abort();
  }, [runId, nodeRunId, status]);

  // No NodeRun selected: never surface a stale fetch from a previously selected one.
  return nodeRunId ? state : { value: null, error: null };
}

/**
 * Highest `seq` among the committed `AGENT_*` Events already held for this NodeRun, or 0.
 *
 * This is only a refresh signal. Agent progress does not move the NodeRun's own status —
 * `applyEvent` deliberately copies no status from an `AGENT_*` Event — so the Agent Trace
 * needs its own trigger. The Trace itself is always re-read from the Backend; nothing in
 * an Event payload is patched into it here.
 */
function latestAgentEventSeq(events: RunEvent[], nodeRunId: string | undefined): number {
  if (!nodeRunId) return 0;
  return events.reduce(
    (latest, event) =>
      event.nodeRunId === nodeRunId && event.type.startsWith('AGENT_') && event.seq > latest
        ? event.seq
        : latest,
    0,
  );
}

/** Fetches the Agent Trace of an Agent NodeRun, re-reading it on each new `AGENT_*` Event. */
function useAgentTrace(
  runId: string,
  nodeRunId: string | undefined,
  agentEventSeq: number,
): { value: AgentTrace | null; error: string | null } {
  const [state, setState] = useState<{ value: AgentTrace | null; error: string | null }>({
    value: null,
    error: null,
  });

  useEffect(() => {
    if (!nodeRunId) return;
    const controller = new AbortController();
    getAgentTrace(runId, nodeRunId, controller.signal)
      .then((trace) => setState({ value: trace, error: null }))
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setState({
          value: null,
          error: error instanceof ApiRequestError ? error.message : 'Failed to load Agent trace',
        });
      });
    return () => controller.abort();
  }, [runId, nodeRunId, agentEventSeq]);

  return nodeRunId ? state : { value: null, error: null };
}

function NodeRunDetailView({
  nodeRun,
  detail,
  detailError,
  latestEvent,
}: {
  nodeRun: NodeRun;
  detail: NodeRunDetail | null;
  detailError: string | null;
  latestEvent: RunEvent | null;
}) {
  const lastAttempt = detail?.attempts[detail.attempts.length - 1] ?? null;
  const callbackBinding = lastAttempt?.callbackBinding ?? null;

  return (
    <section className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <h3 className="font-medium">{nodeRun.nodeId}</h3>
        <Badge variant={nodeRunStatusVariant(nodeRun.status)}>
          {nodeRunStatusLabel(nodeRun.status)}
        </Badge>
      </div>
      <Field label="NodeRun" value={nodeRun.id} />
      <Field label="Type" value={nodeRun.nodeType} />
      <Field label="Ready at" value={formatTimestamp(nodeRun.readyAt)} />
      <Field label="Started at" value={formatTimestamp(nodeRun.startedAt)} />
      {nodeRun.status === 'WAITING_CALLBACK' ? (
        <>
          <Field
            label="Waiting since"
            value={`${formatTimestamp(nodeRun.waitingAt)} (${formatDuration(nodeRun.waitingAt)})`}
          />
          <Field label="Provider" value={callbackBinding?.providerId ?? '—'} />
          <Field label="External Task ID" value={callbackBinding?.externalTaskId ?? '—'} />
          <Field label="Attempt" value={lastAttempt ? String(lastAttempt.attemptNo) : '—'} />
          <Field label="Runtime State" value={lastAttempt?.status ?? '—'} />
          <Field label="Latest Event" value={latestEvent ? latestEvent.type : '—'} />
          <Field label="Deadline" value={formatTimestamp(lastAttempt?.deadlineAt)} />
        </>
      ) : null}
      <Field label="Completed at" value={formatTimestamp(nodeRun.completedAt)} />
      <Field label="Latency" value={formatMillis(nodeRun.latencyMs)} />
      {nodeRun.tokenUsage ? (
        <Field label="Tokens" value={String(nodeRun.tokenUsage.totalTokens)} />
      ) : null}
      {nodeRun.input !== null ? <JsonBlock label="Input" value={nodeRun.input} /> : null}
      {nodeRun.output !== null ? <JsonBlock label="Output" value={nodeRun.output} /> : null}
      {nodeRun.error ? <JsonBlock label="Error" value={nodeRun.error as unknown} /> : null}
      {detailError ? <p className="text-[var(--destructive)]">{detailError}</p> : null}
      {detail && detail.attempts.length > 0 ? (
        <div className="space-y-1">
          <span className="text-[var(--muted-foreground)]">Attempts</span>
          <ul className="space-y-1">
            {detail.attempts.map((attempt) => (
              <li key={attempt.id} className="rounded border border-[var(--border)] px-2 py-1">
                <div className="flex items-center justify-between gap-2">
                  <span>#{attempt.attemptNo}</span>
                  <Badge variant={nodeAttemptStatusVariant(attempt.status)}>{attempt.status}</Badge>
                </div>
                {/* Dispatch facts belong to the Attempt that produced them: a retry keeps
                    the earlier Attempt and its own Binding in this list, while a
                    synchronous Attempt has no Binding and shows no Binding rows at all. */}
                {attempt.callbackBinding ? (
                  <div className="mt-1 space-y-0.5 text-[10px] text-[var(--muted-foreground)]">
                    <Field label="Callback provider" value={attempt.callbackBinding.providerId} />
                    <Field label="External task" value={attempt.callbackBinding.externalTaskId} />
                    <Field
                      label="Bound at"
                      value={formatTimestamp(attempt.callbackBinding.createdAt)}
                    />
                  </div>
                ) : null}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </section>
  );
}

/**
 * Agent Run summary and its Turns, in the order the Backend persisted them. Every value is
 * printed as received: Studio derives no Turn, Action or Agent status of its own, and an
 * Agent Run the Backend has not terminated simply has no `termination` to show.
 */
function AgentTraceView({
  trace,
  traceError,
}: {
  trace: AgentTrace | null;
  traceError: string | null;
}) {
  return (
    <section className="space-y-2">
      <h3 className="font-medium">Agent</h3>
      {traceError ? <p className="text-[var(--destructive)]">{traceError}</p> : null}
      {trace ? (
        <>
          <Field label="Agent Run" value={trace.agentRun.id} />
          {/* `running` is not a status: it is how an absent server-side termination reads. */}
          <Field label="Termination" value={trace.agentRun.termination ?? 'running'} />
          <Field label="Current turn" value={String(trace.agentRun.currentTurnNo)} />
          <Field label="Context version" value={String(trace.agentRun.currentContextVersion)} />
          <Field label="State version" value={String(trace.agentRun.currentStateVersion)} />
          <Field label="Agent deadline" value={formatTimestamp(trace.agentRun.deadline)} />
          {trace.agentRun.terminatedAt ? (
            <Field label="Terminated at" value={formatTimestamp(trace.agentRun.terminatedAt)} />
          ) : null}
          {trace.agentRun.error ? (
            <JsonBlock label="Agent error" value={trace.agentRun.error as unknown} />
          ) : null}
          {trace.turns.length > 0 ? (
            <div className="space-y-1">
              <span className="text-[var(--muted-foreground)]">Turns</span>
              <ul className="space-y-1">
                {trace.turns.map((turn) => (
                  <AgentTurnRow key={turn.id} turn={turn} />
                ))}
              </ul>
            </div>
          ) : null}
        </>
      ) : null}
    </section>
  );
}

function AgentTurnRow({ turn }: { turn: AgentTurnTrace }) {
  return (
    <li className="space-y-1 rounded border border-[var(--border)] px-2 py-1">
      <div className="flex items-center justify-between gap-2">
        <span>Turn {turn.turnNo}</span>
        <Badge variant={agentTurnStatusVariant(turn.status)}>{turn.status}</Badge>
      </div>
      {/* A Turn that has not committed a Decision yet shows none; it is never guessed
          from the Action or the Tool Attempts below. */}
      {turn.decision ? (
        <div className="space-y-0.5 text-[10px] text-[var(--muted-foreground)]">
          <Field label="Decision" value={turn.decision.kind} />
          <Field label="Tool" value={turn.decision.tool ?? '—'} />
          <Field label="State patch" value={turn.decision.hasStatePatch ? 'yes' : 'no'} />
        </div>
      ) : null}
      {turn.action ? (
        <div className="flex items-center justify-between gap-2 text-[10px]">
          <span className="text-[var(--muted-foreground)]">Action {turn.action.type}</span>
          <Badge variant={agentActionStatusVariant(turn.action.status)}>{turn.action.status}</Badge>
        </div>
      ) : null}
      {turn.action?.error ? (
        <JsonBlock label="Action error" value={turn.action.error as unknown} />
      ) : null}
      {turn.error ? <JsonBlock label="Turn error" value={turn.error as unknown} /> : null}
      {turn.toolAttempts.map((attempt) => (
        <div
          key={attempt.id}
          className="rounded border border-[var(--border)] px-2 py-1 text-[10px]"
        >
          <div className="flex items-center justify-between gap-2">
            <span>
              {attempt.toolName} #{attempt.attemptNo}
            </span>
            <Badge variant={toolAttemptStatusVariant(attempt.status)}>{attempt.status}</Badge>
          </div>
          {/* A synchronous Tool Attempt has no Binding; no empty row is printed for it. */}
          {attempt.callbackBinding ? (
            <div className="mt-1 space-y-0.5 text-[var(--muted-foreground)]">
              <Field label="Callback provider" value={attempt.callbackBinding.providerId} />
              <Field label="External task" value={attempt.callbackBinding.externalTaskId} />
            </div>
          ) : null}
          {attempt.error ? (
            <JsonBlock label="Tool attempt error" value={attempt.error as unknown} />
          ) : null}
        </div>
      ))}
    </li>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-3">
      <span className="text-[var(--muted-foreground)]">{label}</span>
      <span className="text-right break-all">{value}</span>
    </div>
  );
}

function JsonBlock({ label, value }: { label: string; value: unknown }) {
  return (
    <div className="space-y-1">
      <span className="text-[var(--muted-foreground)]">{label}</span>
      <pre className="max-h-48 overflow-auto rounded border border-[var(--border)] p-2 text-[10px]">
        {JSON.stringify(value, null, 2)}
      </pre>
    </div>
  );
}
