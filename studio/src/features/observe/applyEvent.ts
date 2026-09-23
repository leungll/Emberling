import type { NodeRunStatus, RunEvent, RunSnapshot, RunStatus } from '@/api/types';

/**
 * Merges one committed Event into a local copy of a Run Snapshot, and decides when the
 * Event alone is not enough so the Snapshot must be re-read.
 *
 * Rule (04 §0, 04 §6 item 7, 08 §5): every status shown in Observe is a server fact.
 * Studio never derives Runtime status; it copies a status only when the Event states it,
 * and otherwise re-reads the Snapshot:
 *
 *   - `run.status` comes from a Run status Event's payload `to` (05 §2.3: Run status
 *     Events carry the previous and next status). A `WAITING_CALLBACK` NodeRun never
 *     turns the Run into `PAUSED` here; that aggregate arrives as `RUN_PAUSED`.
 *   - A NodeRun status changes only for the `NODE_*` Events that the Runtime commits in
 *     the same transaction as exactly that transition of the NodeRun named by the
 *     envelope (06 §1.4 and §1.6: `NODE_READY` with a new READY NodeRun, `NODE_STARTED`
 *     with the claim to RUNNING, `NODE_DISPATCHED` with WAITING_CALLBACK, `NODE_COMPLETED`
 *     with SUCCEEDED, `NODE_FAILED` with FAILED).
 *   - `AGENT_*` Events do not state the Agent NodeRun status: after `AGENT_ACTION_WAITING`
 *     it may or may not be WAITING_CALLBACK, and after an async Tool resume it returns to
 *     RUNNING without any `NODE_*` Event (04 §3.2). Those statuses come from a re-read.
 *   - An Event for a NodeRun the view has not seen triggers a re-read; no row is invented.
 *   - Event payloads are bounded summaries. Authoritative `input`, `output` and `error`
 *     come from the Snapshot and the Detail queries, so they are not written here.
 */

const RUN_STATUSES: ReadonlySet<string> = new Set<RunStatus>([
  'RUNNING',
  'PAUSED',
  'COMPLETED',
  'FAILED',
]);

const NODE_RUN_STATUS_BY_EVENT: Partial<Record<RunEvent['type'], NodeRunStatus>> = {
  NODE_READY: 'READY',
  NODE_STARTED: 'RUNNING',
  NODE_DISPATCHED: 'WAITING_CALLBACK',
  NODE_COMPLETED: 'SUCCEEDED',
  NODE_FAILED: 'FAILED',
};

/** The Run status a Run status Event states in its payload, or null if it states none. */
function statedRunStatus(event: RunEvent): RunStatus | null {
  if (!event.type.startsWith('RUN_') || event.type === 'RUN_CREATED') return null;
  const to = event.payload.to;
  return typeof to === 'string' && RUN_STATUSES.has(to) ? (to as RunStatus) : null;
}

export function isTerminalRunStatus(status: RunStatus): boolean {
  return status === 'COMPLETED' || status === 'FAILED';
}

export function applyEvent(snapshot: RunSnapshot, event: RunEvent): RunSnapshot {
  // Out-of-order or replayed Events never move the snapshot backwards.
  if (event.seq <= snapshot.lastSeq) return snapshot;

  const next: RunSnapshot = { ...snapshot, lastSeq: event.seq };

  const runStatus = statedRunStatus(event);
  if (runStatus !== null) {
    next.run = { ...next.run, status: runStatus };
    return next;
  }

  const nodeRunStatus = NODE_RUN_STATUS_BY_EVENT[event.type];
  if (nodeRunStatus === undefined || event.nodeRunId === null) return next;

  const targetId = event.nodeRunId;
  next.nodeRuns = next.nodeRuns.map((nodeRun) =>
    nodeRun.id === targetId ? { ...nodeRun, status: nodeRunStatus } : nodeRun,
  );

  return next;
}

/**
 * True when showing `event` correctly needs facts the Event does not carry, so the
 * caller must re-read the Snapshot. `view` is the Snapshot before the Event is applied.
 */
export function needsSnapshotRead(view: RunSnapshot, event: RunEvent): boolean {
  if (event.nodeRunId !== null && !view.nodeRuns.some((n) => n.id === event.nodeRunId)) {
    return true;
  }
  if (event.type.startsWith('AGENT_')) return true;
  if (event.type.startsWith('RUN_') && event.type !== 'RUN_CREATED') {
    const status = statedRunStatus(event);
    // Terminal: `run.output`, `run.error` and `completedAt` are Snapshot facts.
    return status === null || isTerminalRunStatus(status);
  }
  return false;
}
