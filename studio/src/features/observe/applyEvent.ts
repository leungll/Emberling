import type { NodeRunStatus, RunEvent, RunSnapshot, RunStatus } from '@/api/types';

/**
 * Merges one committed Event into a local copy of a Run Snapshot.
 *
 * Constraint, and the reason this file is tiny: it may only copy facts the Event payload
 * explicitly carries. It does not aggregate, derive or infer anything.
 *
 *   - `run.status` changes only when a `RUN_*` event names the new status. A
 *     `WAITING_CALLBACK` NodeRun never turns the Run into `PAUSED` here; that aggregate
 *     belongs to the Backend and arrives as `RUN_PAUSED`.
 *   - A NodeRun status changes only for the `NODE_*` events whose meaning is a status
 *     transition of exactly that NodeRun, identified by the envelope's `nodeRunId`.
 *   - Unknown NodeRun ids are ignored rather than invented. A NodeRun the client has not
 *     seen is recovered by re-reading the Snapshot, never by fabricating a row.
 *   - Event payload values are bounded summaries. Authoritative `input`, `output` and
 *     `error` come from the Snapshot and the Detail queries, so they are not written here.
 *
 * Anything this function cannot do from the Event alone stays unchanged until the next
 * Snapshot read. Being stale is acceptable; inventing Runtime state is not.
 */

/** Run-level events that name the Run status they transitioned into. */
const RUN_STATUS_BY_EVENT: Partial<Record<RunEvent['type'], RunStatus>> = {
  RUN_CREATED: 'RUNNING',
  RUN_PAUSED: 'PAUSED',
  RUN_RESUMED: 'RUNNING',
  RUN_COMPLETED: 'COMPLETED',
  RUN_FAILED: 'FAILED',
};

/**
 * Node-level events whose own meaning is a status transition of the NodeRun named by the
 * envelope. `NODE_RETRYING` and `NODE_CALLBACK_RECEIVED` are absent because neither
 * changes the NodeRun status. `AGENT_*` events are absent because the Agent NodeRun status
 * after one of them depends on the state of the Turn and Action, which would be a
 * derivation rather than a copy.
 */
const NODE_RUN_STATUS_BY_EVENT: Partial<Record<RunEvent['type'], NodeRunStatus>> = {
  NODE_READY: 'READY',
  NODE_STARTED: 'RUNNING',
  NODE_DISPATCHED: 'WAITING_CALLBACK',
  NODE_COMPLETED: 'SUCCEEDED',
  NODE_FAILED: 'FAILED',
};

export function applyEvent(snapshot: RunSnapshot, event: RunEvent): RunSnapshot {
  // Out-of-order or replayed Events never move the snapshot backwards.
  if (event.seq <= snapshot.lastSeq) return snapshot;

  const next: RunSnapshot = { ...snapshot, lastSeq: event.seq };

  const runStatus = RUN_STATUS_BY_EVENT[event.type];
  if (runStatus !== undefined) {
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
