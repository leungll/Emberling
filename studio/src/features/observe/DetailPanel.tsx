import { useEffect, useState, type ReactNode } from 'react';

import { SectionLabel } from './Panel';
import { StatusDot } from './StatusGlyph';
import { API_BASE, ApiRequestError, getAgentTrace, getNodeRunDetail } from '@/api/client';
import type {
  AgentTrace,
  AgentTurnTrace,
  ImageRef,
  ImageSource,
  NodeRun,
  NodeRunDetail,
  NodeRunStatus,
  RunEvent,
  RunSnapshot,
  ToolAttemptTrace,
} from '@/api/types';
import { Badge } from '@/components/ui/badge';
import { formatDuration, formatMillis, formatTimestamp } from '@/lib/format';
import {
  agentActionStatusVariant,
  agentTurnStatusTone,
  agentTurnStatusVariant,
  nodeAttemptStatusVariant,
  nodeRunStatusLabel,
  nodeRunStatusVariant,
  toolAttemptStatusVariant,
} from '@/lib/status';
import { cn } from '@/lib/utils';

/** Registered Node Type of a MANAGED_AGENT NodeRun. Only these have an Agent Trace. */
const AGENT_NODE_TYPE = 'agent';

interface DetailPanelProps {
  snapshot: RunSnapshot;
  selectedNodeRun: NodeRun | null;
  selectedEvent: RunEvent | null;
  /** Committed Events held by the page so far, used only to find the latest one already
   * known for the selected NodeRun. This never fetches or invents an Event of its own. */
  events: RunEvent[];
  /** Display name of the selected NodeRun's node in the bound Definition version. */
  nodeName?: string;
  /**
   * `allowedTools` of the selected Agent node in the Run's bound, immutable Definition
   * version. Every Turn of that Agent Run chooses among exactly these Tools, so they are
   * the candidate Tools shown per Turn; nothing is inferred from the Trace.
   */
  allowedTools?: readonly string[];
}

/** NodeRun and Event detail. Every value is a server fact rendered as received. */
export function DetailPanel({
  snapshot,
  selectedNodeRun,
  selectedEvent,
  events,
  nodeName,
  allowedTools,
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

  const eventView = selectedEvent ? <EventDetail event={selectedEvent} /> : null;

  // 04 §3.4 Agent view: Turn timeline beside the NodeRun detail.
  if (selectedNodeRun && agentNodeRunId) {
    return (
      <div className="grid min-h-0 min-w-0 flex-1 grid-cols-[minmax(0,1fr)_minmax(0,440px)]">
        <section className="flex min-h-0 min-w-0 flex-col border-r border-[var(--border)]">
          <div className="flex shrink-0 items-center justify-between px-6 pt-5 pb-3">
            <SectionLabel>Agent Execution · Turn Timeline</SectionLabel>
            <span className="text-[13px] text-[var(--muted-foreground)] tabular-nums">
              {trace.value ? `turn ${trace.value.agentRun.currentTurnNo}` : null}
            </span>
          </div>
          <div className="min-h-0 flex-1 overflow-auto px-6 pb-6">
            {trace.error ? (
              <p className="text-sm text-[var(--destructive)]">{trace.error}</p>
            ) : null}
            {trace.value ? (
              <ol className="space-y-4">
                {trace.value.turns.map((turn) => (
                  <AgentTurnCard key={turn.id} turn={turn} allowedTools={allowedTools} />
                ))}
              </ol>
            ) : null}
          </div>
        </section>

        <aside
          data-testid="detail-panel"
          className="min-h-0 min-w-0 overflow-auto px-6 pt-5 pb-6 text-[14px]"
        >
          <SectionLabel>Detail · Agent NodeRun</SectionLabel>
          <div className="mt-3 space-y-5">
            <NodeRunHeading nodeRun={selectedNodeRun} nodeName={nodeName} />
            {selectedNodeRun.status === 'WAITING_CALLBACK' ? (
              <AgentWaiting
                nodeRun={selectedNodeRun}
                trace={trace.value}
                latestEvent={latestEvent}
              />
            ) : null}
            <AgentRunSummary trace={trace.value} />
            <NodeRunFacts nodeRun={selectedNodeRun} compact />
            <NodeRunValues nodeRun={selectedNodeRun} />
            {detail.error ? <p className="text-[var(--destructive)]">{detail.error}</p> : null}
            {eventView}
          </div>
        </aside>
      </div>
    );
  }

  return (
    <aside data-testid="detail-panel" className="flex min-h-0 min-w-0 flex-1 flex-col text-[14px]">
      <div className="shrink-0 px-6 pt-4 pb-2">
        <SectionLabel>
          {selectedNodeRun ? 'Node Run Detail' : selectedEvent ? 'Event Detail' : 'Run Detail'}
        </SectionLabel>
      </div>

      <div className="min-h-0 flex-1 space-y-5 overflow-auto px-6 pb-6">
        {!selectedNodeRun && !selectedEvent ? <RunValues snapshot={snapshot} /> : null}

        {selectedNodeRun ? (
          <NodeRunDetailView
            nodeRun={selectedNodeRun}
            nodeName={nodeName}
            detail={detail.value}
            detailError={detail.error}
            latestEvent={latestEvent}
          />
        ) : null}

        {eventView}
      </div>
    </aside>
  );
}

function EventDetail({ event }: { event: RunEvent }) {
  return (
    <section className="space-y-2 rounded-xl border border-[var(--border)] px-5 py-4">
      <h3 className="text-[15px] font-semibold">Event #{event.seq}</h3>
      <Field label="Type" value={event.type} />
      <Field label="At" value={formatTimestamp(event.timestamp)} />
      <Field label="NodeRun" value={event.nodeRunId ?? '— (run level)'} />
      <JsonBlock label="Payload" value={event.payload} />
    </section>
  );
}

function RunValues({ snapshot }: { snapshot: RunSnapshot }) {
  return (
    <section className="space-y-4">
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

function NodeRunHeading({ nodeRun, nodeName }: { nodeRun: NodeRun; nodeName?: string }) {
  return (
    <div className="flex items-start justify-between gap-3">
      <div className="min-w-0">
        <h3 className="truncate text-[22px] leading-tight font-bold">
          {nodeName ?? nodeRun.nodeId}
        </h3>
        <p className="mt-1 truncate text-[13px] text-[var(--muted-foreground)]">
          {nodeName ? `${nodeRun.nodeId} · ` : ''}
          {nodeRun.nodeType} · {nodeRun.id}
        </p>
      </div>
      {/* The raw NodeRun status sits beside its 04 §4 wording. */}
      <Badge
        variant={nodeRunStatusVariant(nodeRun.status)}
        className="shrink-0 px-4 py-1.5"
        title={nodeRun.status}
      >
        {nodeRunStatusLabel(nodeRun.status)}
      </Badge>
    </div>
  );
}

function NodeRunFacts({ nodeRun, compact }: { nodeRun: NodeRun; compact?: boolean }) {
  if (compact) {
    return (
      <div className="space-y-2">
        <Field label="Ready at" value={formatTimestamp(nodeRun.readyAt)} />
        <Field label="Started at" value={formatTimestamp(nodeRun.startedAt)} />
        <Field label="Completed at" value={formatTimestamp(nodeRun.completedAt)} />
        <Field label="Latency" value={formatMillis(nodeRun.latencyMs)} />
        {nodeRun.tokenUsage ? (
          <Field label="Tokens" value={String(nodeRun.tokenUsage.totalTokens)} />
        ) : null}
      </div>
    );
  }
  return (
    <div className="grid grid-cols-3 gap-x-6 gap-y-3">
      <Stat label="Ready at" value={formatTimestamp(nodeRun.readyAt)} />
      <Stat label="Started at" value={formatTimestamp(nodeRun.startedAt)} />
      <Stat label="Completed at" value={formatTimestamp(nodeRun.completedAt)} />
      <Stat label="Latency" value={formatMillis(nodeRun.latencyMs)} />
      {nodeRun.tokenUsage ? (
        <Stat label="Tokens" value={String(nodeRun.tokenUsage.totalTokens)} />
      ) : null}
    </div>
  );
}

function NodeRunValues({ nodeRun }: { nodeRun: NodeRun }) {
  return (
    <>
      {nodeRun.input !== null ? <JsonBlock label="Input" value={nodeRun.input} /> : null}
      {nodeRun.output !== null ? <JsonBlock label="Output" value={nodeRun.output} /> : null}
      {nodeRun.error ? <JsonBlock label="Error" value={nodeRun.error as unknown} /> : null}
    </>
  );
}

/** When the NodeRun started waiting and for how long; "—" until the Backend reports it. */
function waitingSince(nodeRun: NodeRun): string {
  return nodeRun.waitingAt
    ? `${formatTimestamp(nodeRun.waitingAt)} · ${formatDuration(nodeRun.waitingAt)}`
    : '—';
}

function latestEventText(latestEvent: RunEvent | null): string {
  return latestEvent ? `${latestEvent.type} · seq ${latestEvent.seq}` : '—';
}

/** Amber WAITING DIAGNOSTICS block of a WAITING_CALLBACK NodeRun (04 §3.2). */
function WaitingDiagnostics({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section
      data-testid="waiting-diagnostics"
      className="rounded-xl border border-[var(--status-waiting-dot)] bg-[var(--status-waiting-bg)] px-5 py-4"
    >
      <SectionLabel>{title}</SectionLabel>
      <div className="mt-3">{children}</div>
    </section>
  );
}

function NodeRunDetailView({
  nodeRun,
  nodeName,
  detail,
  detailError,
  latestEvent,
}: {
  nodeRun: NodeRun;
  nodeName?: string;
  detail: NodeRunDetail | null;
  detailError: string | null;
  latestEvent: RunEvent | null;
}) {
  const lastAttempt = detail?.attempts[detail.attempts.length - 1] ?? null;
  const callbackBinding = lastAttempt?.callbackBinding ?? null;

  return (
    <section className="space-y-5">
      <NodeRunHeading nodeRun={nodeRun} nodeName={nodeName} />

      {nodeRun.status === 'WAITING_CALLBACK' ? (
        <>
          {/* 04 §3.2: every waiting field is a persisted fact; a value the Backend has not
              reported yet reads "—" and is never guessed. */}
          <WaitingDiagnostics title="Waiting Diagnostics">
            <div className="grid grid-cols-2 gap-x-8 gap-y-3">
              <Field label="Provider" value={callbackBinding?.providerId ?? '—'} strong />
              <Field label="Waiting Since" value={waitingSince(nodeRun)} strong />
              <Field
                label="External Task ID"
                value={callbackBinding?.externalTaskId ?? '—'}
                strong
              />
              <Field label="Runtime State" value={lastAttempt?.status ?? '—'} strong />
              <Field label="Deadline" value={formatTimestamp(lastAttempt?.deadlineAt)} strong />
            </div>
          </WaitingDiagnostics>
          <div className="grid grid-cols-2 gap-x-6">
            <Stat label="Attempt" value={lastAttempt ? String(lastAttempt.attemptNo) : '—'} />
            <Stat label="Latest Event" value={latestEventText(latestEvent)} />
          </div>
        </>
      ) : null}

      <NodeRunFacts nodeRun={nodeRun} />
      <NodeRunValues nodeRun={nodeRun} />
      {detailError ? <p className="text-[var(--destructive)]">{detailError}</p> : null}
      {detail && detail.attempts.length > 0 ? (
        <div className="space-y-2">
          <span className="text-xs font-bold tracking-[0.08em] text-[var(--muted-foreground)] uppercase">
            Attempts
          </span>
          <ul className="space-y-2">
            {detail.attempts.map((attempt) => (
              <li
                key={attempt.id}
                className="rounded-lg border border-[var(--border)] bg-[var(--muted)] px-4 py-2.5"
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="font-semibold">#{attempt.attemptNo}</span>
                  <Badge variant={nodeAttemptStatusVariant(attempt.status)}>{attempt.status}</Badge>
                </div>
                {/* Dispatch facts belong to the Attempt that produced them: a retry keeps
                    the earlier Attempt and its own Binding in this list, while a
                    synchronous Attempt has no Binding and shows no Binding rows at all. */}
                {attempt.callbackBinding ? (
                  <div className="mt-2 space-y-1 text-[14px]">
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
 * The Tool Attempt an Agent NodeRun is waiting on: the latest one the Backend reports as
 * DISPATCHED. A MANAGED_AGENT NodeRun has no Node Attempts, so its callback facts live on
 * the Tool Attempt; none is picked when the Trace reports no dispatched Attempt.
 */
function dispatchedToolAttempt(trace: AgentTrace | null): ToolAttemptTrace | null {
  if (!trace) return null;
  for (const turn of [...trace.turns].reverse()) {
    const attempt = [...turn.toolAttempts].reverse().find((a) => a.status === 'DISPATCHED');
    if (attempt) return attempt;
  }
  return null;
}

function AgentWaiting({
  nodeRun,
  trace,
  latestEvent,
}: {
  nodeRun: NodeRun;
  trace: AgentTrace | null;
  latestEvent: RunEvent | null;
}) {
  const attempt = dispatchedToolAttempt(trace);
  return (
    <WaitingDiagnostics title="Waiting">
      <div className="space-y-2">
        <Field label="Provider" value={attempt?.callbackBinding?.providerId ?? '—'} strong />
        <Field
          label="External Task ID"
          value={attempt?.callbackBinding?.externalTaskId ?? '—'}
          strong
        />
        <Field
          label="Tool Attempt"
          value={attempt ? `${attempt.toolName} · attempt ${attempt.attemptNo}` : '—'}
          strong
        />
        <Field label="Waiting Since" value={waitingSince(nodeRun)} strong />
        {/* Same source as the Run view's Runtime State: the waited-on Attempt's status. */}
        <Field label="Runtime State" value={attempt?.status ?? '—'} strong />
        <Field label="Latest Event" value={latestEventText(latestEvent)} strong />
      </div>
    </WaitingDiagnostics>
  );
}

/**
 * Agent Run summary, printed as received: Studio derives no Agent status of its own, and
 * an Agent Run the Backend has not terminated simply has no `termination` to show.
 */
function AgentRunSummary({ trace }: { trace: AgentTrace | null }) {
  return (
    <section className="space-y-2 rounded-xl border border-[var(--border)] bg-[var(--muted)] px-5 py-4">
      <h3 className="text-xs font-bold tracking-[0.08em] text-[var(--muted-foreground)] uppercase">
        Agent
      </h3>
      {trace ? (
        <>
          <Field label="Agent Run" value={trace.agentRun.id} />
          {/* `running` is not a status: it is how an absent server-side termination reads. */}
          <Field label="Termination" value={trace.agentRun.termination ?? 'running'} />
          <Field label="Turns" value={String(trace.turns.length)} />
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
        </>
      ) : null}
    </section>
  );
}

/** One persisted Turn. A Turn that has not committed a Decision shows none; it is never
 * guessed from the Action or the Tool Attempts below it. */
function AgentTurnCard({
  turn,
  allowedTools,
}: {
  turn: AgentTurnTrace;
  allowedTools?: readonly string[];
}) {
  const tone = agentTurnStatusTone(turn.status);
  return (
    <li
      data-testid="agent-turn"
      className="space-y-3 rounded-xl border border-[var(--border)] bg-[var(--muted)] px-5 py-4 text-[14px]"
    >
      <div className="flex items-center justify-between gap-3">
        <span className="flex items-center gap-3">
          <StatusDot tone={tone} />
          <span className="text-[16px] font-bold">Turn {turn.turnNo}</span>
        </span>
        <Badge variant={agentTurnStatusVariant(turn.status)}>{turn.status}</Badge>
      </div>

      {allowedTools ? (
        <div data-testid="candidate-tools" className="flex flex-wrap items-center gap-2">
          <span className="text-[13px] text-[var(--muted-foreground)]">Candidate tools</span>
          {allowedTools.length === 0 ? (
            <span className="text-[14px] text-[var(--muted-foreground)]">none</span>
          ) : (
            allowedTools.map((tool) => (
              <span
                key={tool}
                className="rounded-md border border-[var(--border)] bg-[var(--card)] px-2 py-0.5 text-[14px] font-medium"
              >
                {tool}
              </span>
            ))
          )}
        </div>
      ) : null}

      {turn.decision ? (
        <div className="grid grid-cols-3 gap-x-4">
          <Stat label="Decision" value={turn.decision.kind} />
          <Stat label="Tool" value={turn.decision.tool ?? '—'} />
          <Stat label="State patch" value={turn.decision.hasStatePatch ? 'yes' : 'no'} />
        </div>
      ) : null}
      {turn.action ? (
        <div className="flex items-center justify-between gap-2">
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
          className={cn(
            'space-y-1.5 rounded-lg border px-4 py-3',
            attempt.status === 'DISPATCHED'
              ? 'border-[var(--status-waiting-dot)] bg-[var(--status-waiting-bg)]'
              : 'border-[var(--border)] bg-[var(--card)]',
          )}
        >
          <div className="flex items-center justify-between gap-2">
            <span className="font-semibold">
              {attempt.toolName} #{attempt.attemptNo}
            </span>
            <Badge variant={toolAttemptStatusVariant(attempt.status)}>{attempt.status}</Badge>
          </div>
          {/* A synchronous Tool Attempt has no Binding; no empty row is printed for it. */}
          {attempt.callbackBinding ? (
            <div className="space-y-1 text-[14px]">
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

/** Label/value row. The value is the label's next sibling, which tests rely on. */
function Field({ label, value, strong }: { label: string; value: string; strong?: boolean }) {
  // `strong` rows are the waiting diagnostics: a fixed label column with the value beside
  // it, as in the 04 §3.2 mock; plain rows spread label and value across the width.
  return (
    <div
      className={cn(
        'gap-4',
        strong ? 'grid grid-cols-[116px_minmax(0,1fr)]' : 'flex justify-between',
      )}
    >
      <span className="shrink-0 text-[13px] text-[var(--muted-foreground)]">{label}</span>
      <span className={cn(strong ? 'font-semibold break-words' : 'text-right break-all')}>
        {value}
      </span>
    </div>
  );
}

/** Stacked label over value, for the mocks' ATTEMPT / LATEST EVENT style facts. */
function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <span className="block text-xs font-bold tracking-[0.08em] text-[var(--muted-foreground)] uppercase">
        {label}
      </span>
      <span className="mt-1 block text-[15px] font-semibold break-all">{value}</span>
    </div>
  );
}

const IMAGE_SOURCES: readonly ImageSource[] = ['ASSET', 'ARTIFACT', 'EXTERNAL'];

/** Mirrors `backend/internal/domain/imageref.go`'s `Source` discriminant (08 §2.2). */
function isImageRefValue(value: unknown): value is ImageRef {
  return (
    typeof value === 'object' &&
    value !== null &&
    !Array.isArray(value) &&
    IMAGE_SOURCES.includes((value as { source?: unknown }).source as ImageSource)
  );
}

/**
 * Finds ImageRef-shaped values at the top level or one property deep. NodeRun input/output
 * is a raw `NodeOutput.Ports` map keyed by port name (e.g. `{ image: <ImageRef> }`), so one
 * level of nesting is enough; this does not walk arbitrary JSON depth looking for a match.
 */
function collectImageRefs(value: unknown): ImageRef[] {
  if (isImageRefValue(value)) return [value];
  if (typeof value === 'object' && value !== null && !Array.isArray(value)) {
    return Object.values(value).filter(isImageRefValue);
  }
  return [];
}

/**
 * `<img>` preview plus the raw reference text (04 §3, "图片显示预览和原始引用"). EXTERNAL
 * renders the given URI directly; ASSET reuses the existing `/assets/{id}/content` route.
 * ARTIFACT has no documented content endpoint (08), so it shows only the raw reference —
 * never a guessed or invented proxy URL.
 */
function ImagePreview({ image }: { image: ImageRef }) {
  const src =
    image.source === 'EXTERNAL' && image.uri
      ? image.uri
      : image.source === 'ASSET' && image.asset
        ? `${API_BASE}/assets/${encodeURIComponent(image.asset.assetId)}/content`
        : null;
  const reference =
    image.source === 'EXTERNAL'
      ? image.uri
      : image.source === 'ASSET'
        ? image.asset?.assetId
        : image.artifact?.artifactId;

  return (
    <div className="space-y-1">
      {src ? (
        // Non-empty alt text: an ImageRef preview is content, not decoration, and keeps
        // the "img" role for assistive tech and tests alike.
        <img
          src={src}
          alt={`${image.source} image preview`}
          className="max-h-48 max-w-full rounded-lg object-contain"
        />
      ) : null}
      <p className="break-all text-xs text-[var(--muted-foreground)]">
        {image.source} · {reference ?? '—'}
      </p>
    </div>
  );
}

/**
 * Above this serialized length a value counts as a "large field" that collapses by default
 * (04 §3, "大字段默认折叠"); everything at or under it stays visible, since only large
 * fields are documented to default-collapse — a small value like `{"brief":"x"}` must not
 * require an extra click to read.
 */
const JSON_BLOCK_COLLAPSE_THRESHOLD_CHARS = 2000;

function JsonBlock({ label, value }: { label: string; value: unknown }) {
  const images = collectImageRefs(value);
  const json = JSON.stringify(value, null, 2) ?? 'null';
  // An ImageRef preview already carries the human-readable summary (preview + raw
  // reference), so its raw JSON collapses regardless of size to avoid duplicating that
  // content in the panel; otherwise only large fields collapse.
  const collapsedByDefault = images.length > 0 || json.length > JSON_BLOCK_COLLAPSE_THRESHOLD_CHARS;
  const pre = (
    <pre className="max-h-56 overflow-auto px-4 py-3 text-[14px] leading-relaxed">{json}</pre>
  );

  return (
    <div className="space-y-2">
      <span className="block text-xs font-bold tracking-[0.08em] text-[var(--muted-foreground)] uppercase">
        {label}
      </span>
      {images.map((image, index) => (
        <ImagePreview key={index} image={image} />
      ))}
      {collapsedByDefault ? (
        <details className="rounded-lg border border-[var(--border)] bg-[var(--background)]">
          <summary className="cursor-pointer px-4 py-2 text-xs text-[var(--muted-foreground)] select-none">
            Raw JSON
          </summary>
          {pre}
        </details>
      ) : (
        <div className="rounded-lg border border-[var(--border)] bg-[var(--background)]">{pre}</div>
      )}
    </div>
  );
}
