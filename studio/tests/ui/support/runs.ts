/**
 * The run list for the UI review. The fixtures were recorded before the Backend could list
 * runs, so `GET /api/runs` is answered here from the recorded definitions and run snapshots:
 * the same fields, filters, counts and cursors as the Backend's run list.
 *
 * It also builds runs that were never recorded live: a run still waiting for a callback and
 * a run still running, each as one recorded run stood partway through, with its own events
 * up to that point. Every value comes from that run; nothing is invented except moving its
 * timestamps so it started a few minutes before the review clock.
 */
import type { Overrides, Reply } from './server';
import { RECORDED_AT, errorBody, fixture } from './server';

interface DefinitionItem {
  workflowId: string;
  name: string;
  latestVersion: number;
  lastRun: { id: string; status: string; createdAt: string } | null;
}

interface Snapshot {
  run: {
    id: string;
    workflowId: string;
    definitionVersion: number;
    status: string;
    startedAt: string;
    completedAt: string | null;
    error: { code: string } | null;
    output: unknown;
    tokenUsage: unknown;
    lastSeq: number;
  };
  nodeRuns: {
    id: string;
    nodeId: string;
    nodeType: string;
    status: string;
    startedAt: string | null;
    waitingAt: string | null;
    completedAt: string | null;
    latencyMs: number | null;
    output: unknown;
    tokenUsage: unknown;
  }[];
  lastSeq: number;
}

export interface RunItem {
  id: string;
  workflowId: string;
  workflowName: string;
  definitionVersion: number;
  latestVersion: number;
  status: string;
  createdAt: string;
  startedAt: string;
  completedAt: string | null;
  error: { code: string } | null;
  failedStep: { nodeId: string; nodeName: string } | null;
  waitingSteps: { nodeId: string; nodeName: string; waitingAt: string | null }[];
}

/** Read on first use: this module and the fixture server import each other. */
function definitions(): DefinitionItem[] {
  return (fixture('/api/definitions') as { items: DefinitionItem[] }).items;
}

function newestFirst(a: RunItem, b: RunItem): number {
  return a.startedAt < b.startedAt ? 1 : -1;
}

/** A recorded response, or null for a path that was not recorded. */
function tryFixture(path: string): unknown {
  try {
    return fixture(path);
  } catch {
    return null;
  }
}

function nodeName(workflowId: string, version: number, nodeId: string): string {
  const def = tryFixture(`/api/definitions/${workflowId}/versions/${version}`) as {
    nodes: { id: string; name: string }[];
  } | null;
  return def?.nodes.find((n) => n.id === nodeId)?.name ?? nodeId;
}

function itemOf(def: DefinitionItem, snapshot: Snapshot): RunItem {
  const r = snapshot.run;
  const failed = snapshot.nodeRuns.find((n) => n.status === 'FAILED');
  return {
    id: r.id,
    workflowId: def.workflowId,
    workflowName: def.name,
    definitionVersion: r.definitionVersion,
    latestVersion: def.latestVersion,
    status: r.status,
    createdAt: r.startedAt,
    startedAt: r.startedAt,
    completedAt: r.completedAt,
    error: r.status === 'FAILED' && r.error ? { code: r.error.code } : null,
    failedStep:
      r.status === 'FAILED' && failed
        ? {
            nodeId: failed.nodeId,
            nodeName: nodeName(r.workflowId, r.definitionVersion, failed.nodeId),
          }
        : null,
    waitingSteps:
      r.status === 'PAUSED'
        ? snapshot.nodeRuns
            .filter((n) => n.status === 'WAITING_CALLBACK')
            .map((n) => ({
              nodeId: n.nodeId,
              nodeName: nodeName(r.workflowId, r.definitionVersion, n.nodeId),
              waitingAt: n.waitingAt,
            }))
        : [],
  };
}

/** Every recorded run with its snapshot, newest first: the review's run list. */
export function recordedRuns(): RunItem[] {
  return definitions()
    .flatMap((def) => {
      if (!def.lastRun) return [];
      const snapshot = tryFixture(`/api/runs/${def.lastRun.id}`) as Snapshot | null;
      return snapshot ? [itemOf(def, snapshot)] : [];
    })
    .sort(newestFirst);
}

/**
 * The last run of every recorded definition, for paging states. Runs recorded without a
 * snapshot carry the definition list's own fields only.
 */
export function everyRun(): RunItem[] {
  const recorded = recordedRuns();
  return definitions()
    .flatMap((def): RunItem[] => {
      if (!def.lastRun) return [];
      const known = recorded.find((r) => r.id === def.lastRun?.id);
      if (known) return [known];
      return [
        {
          id: def.lastRun.id,
          workflowId: def.workflowId,
          workflowName: def.name,
          definitionVersion: 1,
          latestVersion: def.latestVersion,
          status: def.lastRun.status,
          createdAt: def.lastRun.createdAt,
          startedAt: def.lastRun.createdAt,
          completedAt: null,
          error: null,
          failedStep: null,
          waitingSteps: [],
        },
      ];
    })
    .sort(newestFirst);
}

const STATUS = new Set(['RUNNING', 'PAUSED', 'COMPLETED', 'FAILED']);

function filtersOf(url: URL): string {
  const q = url.searchParams;
  return ['workflowId', 'status', 'startedAfter', 'code'].map((k) => q.get(k) ?? '').join('|');
}

/**
 * `GET /api/runs` over the given runs: workflow, status, start and code filters, counts that
 * ignore the status filter and paging, and opaque cursors bound to the filters.
 */
export function runList(items: readonly RunItem[]): (url: URL) => Reply {
  return (url) => {
    const q = url.searchParams;
    const status = q.get('status');
    if (status && !STATUS.has(status)) {
      return { status: 400, body: errorBody('VALIDATION_FAILED', 'unknown status') };
    }
    const code = (q.get('code') ?? '').toLowerCase().replace(/^run_/, '');
    const after = q.get('startedAfter');
    const scoped = items.filter(
      (r) =>
        (!q.get('workflowId') || r.workflowId === q.get('workflowId')) &&
        (!after || r.startedAt >= new Date(after).toISOString()) &&
        (!code || r.id.toLowerCase().replace(/^run_/, '').startsWith(code)),
    );
    const counts = {
      all: scoped.length,
      running: scoped.filter((r) => r.status === 'RUNNING').length,
      paused: scoped.filter((r) => r.status === 'PAUSED').length,
      completed: scoped.filter((r) => r.status === 'COMPLETED').length,
      failed: scoped.filter((r) => r.status === 'FAILED').length,
    };
    const shown = status ? scoped.filter((r) => r.status === status) : scoped;
    const limit = Math.min(100, Math.max(1, Number(q.get('limit') ?? '20')));
    let start = 0;
    const cursor = q.get('cursor');
    if (cursor) {
      const [digest, at] = Buffer.from(cursor, 'base64url').toString().split('#');
      if (digest !== filtersOf(url) || !at) {
        return { status: 400, body: errorBody('VALIDATION_FAILED', 'invalid cursor') };
      }
      start = Number(at);
    }
    const page = shown.slice(start, start + limit);
    const encode = (n: number) => Buffer.from(`${filtersOf(url)}#${n}`).toString('base64url');
    return {
      status: 200,
      body: {
        items: page,
        nextCursor: start + limit < shown.length ? encode(start + limit) : null,
        prevCursor: start > 0 ? encode(Math.max(0, start - limit)) : null,
        counts,
      },
    };
  };
}

/* ---------- Runs as they stood partway through ---------------------------------------- */

const ISO = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$/;

/** Moves every timestamp in a value by `ms`. */
function shift<T>(value: T, ms: number): T {
  return JSON.parse(JSON.stringify(value), (_k, v: unknown) =>
    typeof v === 'string' && ISO.test(v) ? new Date(new Date(v).getTime() + ms).toISOString() : v,
  ) as T;
}

interface RecordedEvent {
  seq: number;
  type: string;
  nodeRunId: string | null;
  payload: Record<string, unknown> | null;
}

interface TraceTurn {
  turnNo: number;
  status: string;
  completedAt: string | null;
  action: { status: string; completedAt: string | null } | null;
  toolAttempts: { status: string; dispatchedAt: string | null; completedAt: string | null }[];
}

interface Trace {
  agentRun: Record<string, unknown>;
  turns: TraceTurn[];
  facts: { items: unknown[] };
}

export interface PartwayRun {
  runId: string;
  workflowId: string;
  item: RunItem;
  overrides: Overrides;
}

/**
 * A recorded agent run as it stood after event `cutSeq`: the run, its steps, the agent's
 * turns and its tool attempts as they were then. `state` says what the run was doing at
 * that point: waiting for the tool's callback, or running the agent's current turn. The
 * event stream stays open with nothing new, as for a run that is still going.
 */
export function partwayRun(
  runId: string,
  cutSeq: number,
  state: 'waiting' | 'running',
  startedMinutesAgo: number,
): PartwayRun {
  const snapshot = fixture(`/api/runs/${runId}`) as Snapshot;
  const events = (fixture(`/api/runs/${runId}/events`) as { items: RecordedEvent[] }).items;
  const ms =
    new Date(RECORDED_AT).getTime() -
    startedMinutesAgo * 60_000 -
    new Date(snapshot.run.startedAt).getTime();
  const kept = events.filter((e) => e.seq <= cutSeq);
  const seen = new Set(kept.map((e) => e.nodeRunId));
  const ended = new Set(
    kept
      .filter((e) => e.type === 'NODE_COMPLETED' || e.type === 'NODE_FAILED')
      .map((e) => e.nodeRunId),
  );
  const turnsSeen = new Set(
    kept.filter((e) => e.type === 'AGENT_TURN_READY').map((e) => Number(e.payload?.turnNo)),
  );
  const pending = state === 'waiting' ? 'WAITING_CALLBACK' : 'RUNNING';

  const unfinished = <N extends { id: string; startedAt: string | null; waitingAt: string | null }>(
    n: N,
  ): N => ({
    ...n,
    status: pending,
    waitingAt: state === 'waiting' ? n.waitingAt : null,
    completedAt: null,
    latencyMs: null,
    output: null,
    tokenUsage: null,
  });

  const run = {
    ...snapshot.run,
    status: state === 'waiting' ? 'PAUSED' : 'RUNNING',
    completedAt: null,
    output: null,
    tokenUsage: null,
    lastSeq: cutSeq,
  };
  const nodeRuns = snapshot.nodeRuns
    .filter((n) => seen.has(n.id))
    .map((n) => (ended.has(n.id) ? n : unfinished(n)));
  const partSnapshot = shift({ run, nodeRuns, lastSeq: cutSeq }, ms);
  const partEvents = shift(kept, ms);

  const overrides: Overrides = {
    [`/api/runs/${runId}`]: { status: 200, body: partSnapshot },
    [`/api/runs/${runId}/events`]: (url) => {
      // History pages carry a limit; the event stream does not, and it stays open.
      if (!url.searchParams.has('limit')) return { hang: true };
      const after = Number(url.searchParams.get('afterSeq') ?? '0');
      return { status: 200, body: { items: partEvents.filter((e) => e.seq > after) } };
    },
  };

  for (const n of nodeRuns) {
    const base = `/api/runs/${runId}/nodes/${n.id}`;
    const detail = tryFixture(base) as {
      nodeRun: { id: string; startedAt: string | null; waitingAt: string | null };
      attempts: { status: string; completedAt: string | null }[];
    } | null;
    if (detail) {
      const done = ended.has(n.id);
      overrides[base] = {
        status: 200,
        body: shift(
          {
            nodeRun: done ? detail.nodeRun : unfinished(detail.nodeRun),
            attempts: done
              ? detail.attempts
              : detail.attempts.map((a) => ({ ...a, status: 'STARTED', completedAt: null })),
          },
          ms,
        ),
      };
    }
    const trace = tryFixture(`${base}/agent`) as Trace | null;
    if (!trace || ended.has(n.id)) continue;
    const turns = trace.turns
      .filter((t) => turnsSeen.has(t.turnNo))
      .map((t, i, all): TraceTurn => {
        if (i < all.length - 1) return t;
        return {
          ...t,
          // A turn ends when its decision commits; its action then runs on its own.
          action: t.action ? { ...t.action, status: pending, completedAt: null } : null,
          toolAttempts: t.toolAttempts.map((a) => ({
            ...a,
            status: state === 'waiting' ? 'DISPATCHED' : 'STARTED',
            dispatchedAt: state === 'waiting' ? a.dispatchedAt : null,
            completedAt: null,
          })),
        };
      });
    overrides[`${base}/agent`] = {
      status: 200,
      body: shift(
        {
          ...trace,
          agentRun: {
            ...trace.agentRun,
            termination: null,
            terminatedAt: null,
            error: null,
            currentTurnNo: turns.length,
          },
          turns,
          facts: { ...trace.facts, items: [] },
        },
        ms,
      ),
    };
  }

  const def = definitions().find((d) => d.lastRun?.id === runId);
  if (!def) throw new Error(`no definition lists run ${runId}`);
  return {
    runId,
    workflowId: def.workflowId,
    item: itemOf(def, partSnapshot),
    overrides,
  };
}

/** The run list with `extra` runs in place of the recorded runs that share their IDs. */
export function withRuns(extra: readonly RunItem[], base: readonly RunItem[] = recordedRuns()) {
  const ids = new Set(extra.map((r) => r.id));
  return [...extra, ...base.filter((r) => !ids.has(r.id))].sort(newestFirst);
}
