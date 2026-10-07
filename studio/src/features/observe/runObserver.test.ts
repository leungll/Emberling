import { describe, expect, it } from 'vitest';

import { observeRun, type ObservedRun, type RunObserverDeps } from './runObserver';
import type { RunEventSubscription, SubscribeRunEventsOptions } from '@/api/sse';
import type {
  EventType,
  JsonObject,
  NodeRun,
  NodeRunStatus,
  RunEvent,
  RunSnapshot,
  RunStatus,
} from '@/api/types';

function nodeRun(id: string, status: NodeRunStatus, nodeType = 'image_generation'): NodeRun {
  return {
    id,
    runId: 'run_1',
    nodeId: id.replace('nr_', 'node_'),
    nodeType,
    status,
    input: null,
    output: null,
    error: null,
    readyAt: '2026-08-03T12:00:01Z',
    startedAt: null,
    waitingAt: null,
    completedAt: null,
    latencyMs: null,
    tokenUsage: null,
  };
}

/** One SSE connection. `deliver()` behaves like one iteration of the server's cursor loop. */
class FakeStream implements RunEventSubscription {
  closed = false;
  cursor: number;

  constructor(
    private readonly server: FakeServer,
    readonly options: SubscribeRunEventsOptions,
  ) {
    this.cursor = options.afterSeq;
  }

  /** Sends every committed Event with seq > cursor, in order, like stream.go. */
  deliver(): void {
    for (const e of this.server.log.filter((x) => x.seq > this.cursor)) {
      this.send(e);
    }
  }

  /** Sends one frame verbatim, including replays and out-of-order frames. */
  send(e: RunEvent): void {
    if (this.closed) return;
    this.cursor = Math.max(this.cursor, e.seq);
    this.options.onEvent(e);
  }

  open(): void {
    this.options.onOpen?.();
  }

  fail(reconnecting: boolean): void {
    this.options.onError?.({ reconnecting });
  }

  close(): void {
    this.closed = true;
  }

  lastDeliveredSeq(): number {
    return this.cursor;
  }
}

/** Committed facts of one Run plus the read endpoints Studio uses. */
class FakeServer {
  runStatus: RunStatus = 'RUNNING';
  nodeRuns: NodeRun[] = [];
  log: RunEvent[] = [];
  streams: FakeStream[] = [];
  getRunCalls = 0;
  listEventsCalls: { afterSeq: number; limit: number }[] = [];
  /** When set, getRun captures the facts at call time but resolves only once released. */
  holdSnapshot: Promise<void> | null = null;
  onListEvents: (() => void) | null = null;
  timers: { callback: () => void; ms: number; cleared: boolean }[] = [];

  commit(type: EventType, nodeRunId: string | null = null, payload: JsonObject = {}): RunEvent {
    const e: RunEvent = {
      id: `evt_${this.log.length + 1}`,
      runId: 'run_1',
      nodeRunId,
      type,
      seq: this.log.length + 1,
      timestamp: '2026-08-03T12:00:04Z',
      payload,
    };
    this.log.push(e);
    return e;
  }

  /** A Run status transition as the Backend commits it: status and `{from, to}` Event. */
  transition(type: EventType, to: RunStatus): RunEvent {
    const from = this.runStatus;
    this.runStatus = to;
    return this.commit(type, null, { from, to });
  }

  setNodeRun(id: string, status: NodeRunStatus): void {
    this.nodeRuns = this.nodeRuns.map((n) => (n.id === id ? { ...n, status } : n));
  }

  snapshot(): RunSnapshot {
    return structuredClone({
      run: {
        id: 'run_1',
        workflowId: 'wf_1',
        definitionVersion: 1,
        status: this.runStatus,
        input: {},
        output: null,
        error: null,
      },
      nodeRuns: this.nodeRuns,
      lastSeq: this.log.length,
    });
  }

  get stream(): FakeStream {
    const last = this.streams[this.streams.length - 1];
    if (!last) throw new Error('no stream opened');
    return last;
  }

  get pendingTimers() {
    return this.timers.filter((t) => !t.cleared);
  }

  fireTimer(): void {
    const timer = this.pendingTimers[0];
    if (!timer) throw new Error('no pending timer');
    timer.cleared = true;
    timer.callback();
  }

  deps(): RunObserverDeps {
    return {
      getRun: async () => {
        this.getRunCalls += 1;
        const snap = this.snapshot();
        if (this.holdSnapshot) await this.holdSnapshot;
        return snap;
      },
      listEvents: (_runId, afterSeq, limit) => {
        this.listEventsCalls.push({ afterSeq, limit });
        this.onListEvents?.();
        return Promise.resolve(
          structuredClone(this.log.filter((e) => e.seq > afterSeq).slice(0, limit)),
        );
      },
      subscribe: (_runId, options) => {
        const stream = new FakeStream(this, options);
        this.streams.push(stream);
        return stream;
      },
      setTimer: (callback, ms) => {
        const timer = { callback, ms, cleared: false };
        this.timers.push(timer);
        return timer;
      },
      clearTimer: (handle) => {
        (handle as { cleared: boolean }).cleared = true;
      },
    };
  }
}

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
}

async function start(server: FakeServer) {
  let state: ObservedRun = { snapshot: null, events: [], loadError: null };
  const observer = observeRun('run_1', (next) => (state = next), server.deps());
  await settle();
  return {
    observer,
    get state() {
      return state;
    },
    seqs: () => state.events.map((e) => e.seq),
    status: (id: string) => state.snapshot?.nodeRuns.find((n) => n.id === id)?.status,
  };
}

/** A Run with one Image Generation NodeRun that has started. seq 1..3. */
function runningServer(): FakeServer {
  const server = new FakeServer();
  server.nodeRuns = [nodeRun('nr_image', 'READY')];
  server.commit('RUN_CREATED');
  server.commit('NODE_READY', 'nr_image');
  server.setNodeRun('nr_image', 'RUNNING');
  server.commit('NODE_STARTED', 'nr_image');
  return server;
}

describe('observeRun — Snapshot, history and SSE handoff', () => {
  it('reads the Snapshot, loads history up to lastSeq, then streams from there', async () => {
    const server = runningServer();

    const view = await start(server);

    expect(server.getRunCalls).toBe(1);
    expect(server.listEventsCalls[0]?.afterSeq).toBe(0);
    expect(view.seqs()).toEqual([1, 2, 3]);
    expect(server.streams).toHaveLength(1);
    expect(server.stream.options.afterSeq).toBe(3);
    view.observer.close();
  });

  it('pages history in bounded, seq-ordered requests', async () => {
    const server = new FakeServer();
    for (let i = 0; i < 450; i += 1) server.commit('NODE_RETRYING');

    const view = await start(server);

    expect(server.listEventsCalls.map((c) => c.afterSeq)).toEqual([0, 200, 400]);
    expect(server.listEventsCalls.every((c) => c.limit === 200)).toBe(true);
    expect(view.seqs()).toEqual(Array.from({ length: 450 }, (_, i) => i + 1));
    view.observer.close();
  });

  it('window 1: Events committed after the Snapshot are applied once, in seq order', async () => {
    const server = runningServer();
    // Commits land after the Snapshot read but before history and SSE are open.
    server.onListEvents = () => {
      server.onListEvents = null;
      server.setNodeRun('nr_image', 'WAITING_CALLBACK');
      server.commit('NODE_DISPATCHED', 'nr_image');
    };

    const view = await start(server);
    // The first SSE query replays from its cursor; a proxy replay repeats seq 3 and 4.
    server.stream.send(server.log[2]!);
    server.stream.send(server.log[3]!);
    server.transition('RUN_PAUSED', 'PAUSED');
    server.stream.deliver();

    expect(view.seqs()).toEqual([1, 2, 3, 4, 5]);
    expect(view.status('nr_image')).toBe('WAITING_CALLBACK');
    expect(view.state.snapshot?.run.status).toBe('PAUSED');
    view.observer.close();
  });

  it('window 2: after a transport error, re-reads and reconnects from the last applied seq', async () => {
    const server = runningServer();
    const view = await start(server);
    server.stream.open();
    const first = server.stream;

    // The Backend restarts: the stream dies for good while seq 4 and 5 commit.
    first.fail(false);
    server.setNodeRun('nr_image', 'WAITING_CALLBACK');
    server.commit('NODE_DISPATCHED', 'nr_image');
    server.transition('RUN_PAUSED', 'PAUSED');
    await settle();

    expect(first.closed).toBe(true);
    expect(server.pendingTimers.map((t) => t.ms)).toEqual([1000]);

    const readsBefore = server.getRunCalls;
    server.fireTimer();
    await settle();

    expect(server.getRunCalls).toBeGreaterThan(readsBefore);
    expect(server.listEventsCalls.at(-1)?.afterSeq).toBe(3);
    expect(server.streams).toHaveLength(2);
    expect(server.stream.options.afterSeq).toBe(5);
    expect(view.seqs()).toEqual([1, 2, 3, 4, 5]);
    expect(view.status('nr_image')).toBe('WAITING_CALLBACK');
    expect(view.state.snapshot?.run.status).toBe('PAUSED');

    // Replay across the reconnect is dropped; the next Event is applied once.
    server.stream.send(server.log[4]!);
    server.setNodeRun('nr_image', 'SUCCEEDED');
    server.commit('NODE_COMPLETED', 'nr_image');
    server.stream.deliver();
    expect(view.seqs()).toEqual([1, 2, 3, 4, 5, 6]);
    expect(view.status('nr_image')).toBe('SUCCEEDED');
    view.observer.close();
  });

  it('backs off between failed reconnects, bounded, and resets after an open', async () => {
    const server = runningServer();
    const view = await start(server);

    const delays: number[] = [];
    for (let i = 0; i < 8; i += 1) {
      server.stream.fail(false);
      await settle();
      delays.push(server.pendingTimers[0]!.ms);
      server.fireTimer();
      await settle();
    }
    expect(delays).toEqual([1000, 2000, 4000, 8000, 16000, 30000, 30000, 30000]);

    server.stream.open();
    server.stream.fail(false);
    await settle();
    expect(server.pendingTimers.map((t) => t.ms)).toEqual([1000]);
    view.observer.close();
    expect(server.pendingTimers).toHaveLength(0);
  });

  it('lets the browser reconnect itself with Last-Event-ID and still re-reads', async () => {
    const server = runningServer();
    const view = await start(server);
    const reads = server.getRunCalls;

    server.stream.fail(true);
    await settle();

    expect(server.getRunCalls).toBe(reads + 1);
    expect(server.stream.closed).toBe(false);
    expect(server.streams).toHaveLength(1);
    expect(server.pendingTimers).toHaveLength(0);
    view.observer.close();
  });
});

describe('observeRun — gaps', () => {
  it('stops at a seq gap, re-queries from the last applied seq and re-reads the Snapshot', async () => {
    const server = runningServer();
    const view = await start(server);
    const reads = server.getRunCalls;

    server.setNodeRun('nr_image', 'WAITING_CALLBACK');
    server.commit('NODE_DISPATCHED', 'nr_image');
    server.transition('RUN_PAUSED', 'PAUSED');
    // seq 4 is lost in transit; seq 5 arrives first.
    server.stream.send(server.log[4]!);

    expect(view.seqs()).toEqual([1, 2, 3]);

    await settle();

    expect(server.listEventsCalls.at(-1)?.afterSeq).toBe(3);
    expect(server.getRunCalls).toBe(reads + 1);
    expect(view.seqs()).toEqual([1, 2, 3, 4, 5]);
    expect(view.status('nr_image')).toBe('WAITING_CALLBACK');
    expect(view.state.snapshot?.run.status).toBe('PAUSED');
    view.observer.close();
  });
});

describe('observeRun — terminal Run', () => {
  it('closes the stream when the Run completes and does not reconnect', async () => {
    const server = runningServer();
    const view = await start(server);

    server.setNodeRun('nr_image', 'SUCCEEDED');
    server.commit('NODE_COMPLETED', 'nr_image');
    server.transition('RUN_COMPLETED', 'COMPLETED');
    server.stream.deliver();
    await settle();

    expect(view.state.snapshot?.run.status).toBe('COMPLETED');
    expect(server.stream.closed).toBe(true);

    // The server closed the connection; the browser would now retry in a loop.
    server.stream.fail(true);
    server.stream.fail(false);
    await settle();

    expect(server.streams).toHaveLength(1);
    expect(server.pendingTimers).toHaveLength(0);
    view.observer.close();
  });

  it('shows a finished Run from history without opening a stream', async () => {
    const server = runningServer();
    server.setNodeRun('nr_image', 'SUCCEEDED');
    server.commit('NODE_COMPLETED', 'nr_image');
    server.transition('RUN_COMPLETED', 'COMPLETED');

    const view = await start(server);

    expect(view.seqs()).toEqual([1, 2, 3, 4, 5]);
    expect(view.state.snapshot?.run.status).toBe('COMPLETED');
    expect(server.streams).toHaveLength(0);
    expect(server.pendingTimers).toHaveLength(0);
    view.observer.close();
  });
});

describe('observeRun — status from server facts', () => {
  it('re-reads the Snapshot for an Event naming an unknown NodeRun', async () => {
    const server = runningServer();
    const view = await start(server);
    const reads = server.getRunCalls;

    server.setNodeRun('nr_image', 'SUCCEEDED');
    server.commit('NODE_COMPLETED', 'nr_image');
    server.nodeRuns.push(nodeRun('nr_caption', 'READY', 'caption'));
    server.commit('NODE_READY', 'nr_caption');
    server.nodeRuns.push(nodeRun('nr_agent', 'READY', 'agent'));
    server.commit('NODE_READY', 'nr_agent');
    server.stream.deliver();

    expect(view.status('nr_caption')).toBeUndefined();

    await settle();

    expect(view.status('nr_caption')).toBe('READY');
    expect(view.status('nr_agent')).toBe('READY');
    // Two unknown NodeRuns in one burst coalesce into at most one follow-up read.
    expect(server.getRunCalls - reads).toBeGreaterThanOrEqual(1);
    expect(server.getRunCalls - reads).toBeLessThanOrEqual(2);
    view.observer.close();
  });

  it('takes the Agent NodeRun status from the Snapshot around AGENT_ACTION_WAITING and resume', async () => {
    const server = new FakeServer();
    server.nodeRuns = [nodeRun('nr_agent', 'RUNNING', 'agent')];
    server.commit('RUN_CREATED');
    server.commit('NODE_READY', 'nr_agent');
    server.commit('NODE_STARTED', 'nr_agent');
    const view = await start(server);
    expect(view.status('nr_agent')).toBe('RUNNING');

    // The async Tool is dispatched: the Agent NodeRun waits and the Run pauses.
    server.setNodeRun('nr_agent', 'WAITING_CALLBACK');
    server.commit('AGENT_ACTION_WAITING', 'nr_agent');
    server.transition('RUN_PAUSED', 'PAUSED');
    server.stream.deliver();
    await settle();

    expect(view.status('nr_agent')).toBe('WAITING_CALLBACK');
    expect(view.state.snapshot?.run.status).toBe('PAUSED');

    // The callback completes the Tool Attempt: the Agent NodeRun returns to RUNNING.
    server.setNodeRun('nr_agent', 'RUNNING');
    server.commit('AGENT_ACTION_COMPLETED', 'nr_agent', { completionSource: 'CALLBACK' });
    server.transition('RUN_RESUMED', 'RUNNING');
    server.stream.deliver();
    await settle();

    expect(view.status('nr_agent')).toBe('RUNNING');
    expect(view.state.snapshot?.run.status).toBe('RUNNING');
    view.observer.close();
  });

  it('does not let an older re-read Snapshot undo Events applied after it', async () => {
    const server = new FakeServer();
    server.nodeRuns = [nodeRun('nr_agent', 'RUNNING', 'agent')];
    server.commit('RUN_CREATED');
    server.commit('NODE_STARTED', 'nr_agent');
    const view = await start(server);

    let release!: () => void;
    server.holdSnapshot = new Promise<void>((resolve) => (release = resolve));
    server.setNodeRun('nr_agent', 'WAITING_CALLBACK');
    server.commit('AGENT_ACTION_WAITING', 'nr_agent');
    server.transition('RUN_PAUSED', 'PAUSED');
    server.stream.deliver(); // AGENT_ACTION_WAITING starts a re-read at seq 4.
    await settle();

    // While that read is in flight the Run resumes.
    server.setNodeRun('nr_agent', 'RUNNING');
    server.commit('AGENT_ACTION_COMPLETED', 'nr_agent');
    server.transition('RUN_RESUMED', 'RUNNING');
    server.stream.deliver();
    expect(view.state.snapshot?.run.status).toBe('RUNNING');

    // The first read (taken at seq 4) lands; the follow-up read is still held.
    let releaseSecond!: () => void;
    const first = release;
    server.holdSnapshot = new Promise<void>((resolve) => (releaseSecond = resolve));
    first();
    await settle();

    // seq 6 stays applied on top of the older Snapshot: the Run is not shown PAUSED again.
    expect(view.state.snapshot?.run.status).toBe('RUNNING');
    expect(view.state.snapshot?.lastSeq).toBe(6);

    server.holdSnapshot = null;
    releaseSecond();
    await settle();

    expect(view.seqs()).toEqual([1, 2, 3, 4, 5, 6]);
    expect(view.status('nr_agent')).toBe('RUNNING');
    view.observer.close();
  });
});

describe('observeRun — load failure', () => {
  it('reports a failed first read and opens no stream', async () => {
    const server = new FakeServer();
    const deps = server.deps();
    deps.getRun = () => Promise.reject(new Error('boom'));
    let state: ObservedRun = { snapshot: null, events: [], loadError: null };
    const observer = observeRun('run_1', (next) => (state = next), deps);
    await settle();

    expect(state.loadError).toBeInstanceOf(Error);
    expect(server.streams).toHaveLength(0);
    observer.close();
  });
});
