import { applyEvent, isTerminalRunStatus, needsSnapshotRead } from './applyEvent';
import { getRun, listEvents } from '@/api/client';
import {
  subscribeRunEvents,
  type RunEventSubscription,
  type SubscribeRunEventsOptions,
} from '@/api/sse';
import type { RunEvent, RunSnapshot } from '@/api/types';

/** What Observe renders: the server Snapshot plus the committed Events, both server facts. */
export interface ObservedRun {
  snapshot: RunSnapshot | null;
  /** Contiguous from seq 1, ascending, each Event exactly once. */
  events: RunEvent[];
  /** Set only when the first Snapshot read fails; later failures keep the last view. */
  loadError: unknown;
}

export interface RunObserverDeps {
  getRun: (runId: string, signal: AbortSignal) => Promise<RunSnapshot>;
  listEvents: (
    runId: string,
    afterSeq: number,
    limit: number,
    signal: AbortSignal,
  ) => Promise<RunEvent[]>;
  subscribe: (runId: string, options: SubscribeRunEventsOptions) => RunEventSubscription;
  setTimer: (callback: () => void, ms: number) => unknown;
  clearTimer: (handle: unknown) => void;
}

/** Requested page size for JSON history; the Backend caps it (08 §5). */
export const EVENT_PAGE_LIMIT = 200;
const RECONNECT_BASE_MS = 1000;
const RECONNECT_MAX_MS = 30_000;

const defaultDeps: RunObserverDeps = {
  getRun: (runId, signal) => getRun(runId, signal),
  listEvents: (runId, afterSeq, limit, signal) => listEvents(runId, afterSeq, limit, signal),
  subscribe: (runId, options) => subscribeRunEvents(runId, options),
  setTimer: (callback, ms) => setTimeout(callback, ms),
  clearTimer: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

/**
 * Keeps one Run's Snapshot and Event list in sync with the Backend (04 §3.4, 08 §5).
 *
 * Pipeline: read the Snapshot, load Events up to its `lastSeq` as bounded JSON pages, then
 * open SSE at the last applied seq. Events are applied strictly in seq order: `seq` at or
 * below the last applied one is dropped, and a forward jump stops applying and re-queries
 * Events from the last applied seq together with a Snapshot re-read ("sync"). A transport
 * error also syncs; when the browser has given up, Studio reconnects from the last applied
 * seq with bounded exponential backoff. Once the Run is terminal and every Event up to the
 * Snapshot's `lastSeq` is applied, the stream is closed and never reopened: the Backend
 * closes a terminal Run's stream, and the browser would otherwise reconnect in a loop.
 *
 * The rendered Snapshot is always `base` (the newest Snapshot read) with the applied
 * Events newer than `base.lastSeq` folded on top by `applyEvent`, so a Snapshot read that
 * lands after later Events cannot move the view backwards. Which Events need a re-read is
 * decided by `needsSnapshotRead`. At most one sync runs at a time; further requests while
 * it runs coalesce into a single follow-up pass.
 */
export function observeRun(
  runId: string,
  onChange: (state: ObservedRun) => void,
  deps: RunObserverDeps = defaultDeps,
): { close(): void } {
  const controller = new AbortController();
  const signal = controller.signal;

  let base: RunSnapshot | null = null;
  let view: RunSnapshot | null = null;
  const events: RunEvent[] = [];
  let lastApplied = 0;

  let subscription: RunEventSubscription | null = null;
  let syncing = false;
  let syncAgain = false;
  let reconnectTimer: unknown = null;
  let reconnectAttempt = 0;
  let finished = false; // terminal and caught up: no stream, no reconnect

  const disposed = () => signal.aborted;

  function emit(): void {
    if (disposed()) return;
    // A fresh array per emit, so React sees a new list; appends in between stay O(1).
    onChange({ snapshot: view, events: events.slice(), loadError: null });
  }

  function adopt(snapshot: RunSnapshot): void {
    // Reads are sequential, so a later read never has a smaller lastSeq; guard anyway.
    if (base && snapshot.lastSeq < base.lastSeq) return;
    base = snapshot;
    // `events` is contiguous from seq 1, so events[i].seq === i + 1.
    view = events.slice(snapshot.lastSeq).reduce(applyEvent, snapshot);
  }

  /**
   * Applies one Event if it is the next seq. Returns false on a gap, so the caller can
   * stop; `seq <= lastApplied` is a duplicate and counts as handled.
   */
  function accept(event: RunEvent): boolean {
    if (event.seq <= lastApplied) return true;
    if (event.seq !== lastApplied + 1) return false;

    const before = view;
    events.push(event);
    lastApplied = event.seq;
    if (before && base && event.seq > base.lastSeq) {
      view = applyEvent(before, event);
      if (needsSnapshotRead(before, event)) requestSync();
    }
    return true;
  }

  function onStreamEvent(event: RunEvent): void {
    if (disposed() || finished) return;
    if (!accept(event)) {
      // 08 §5: on a gap keep the cursor and re-query; never skip the missing Event.
      requestSync();
      return;
    }
    emit();
    finishIfTerminal();
  }

  function caughtUpTerminal(): boolean {
    return (
      view !== null &&
      base !== null &&
      isTerminalRunStatus(view.run.status) &&
      lastApplied >= base.lastSeq
    );
  }

  function finishIfTerminal(): void {
    if (finished || !caughtUpTerminal()) return;
    finished = true;
    subscription?.close();
    subscription = null;
    if (reconnectTimer !== null) {
      deps.clearTimer(reconnectTimer);
      reconnectTimer = null;
    }
  }

  /** Loads Events after `lastApplied` until `target` is reached or a page brings nothing. */
  async function catchUp(target: number): Promise<void> {
    while (lastApplied < target) {
      const page = await deps.listEvents(runId, lastApplied, EVENT_PAGE_LIMIT, signal);
      if (disposed()) return;
      const before = lastApplied;
      for (const event of page) {
        // A gap inside committed history cannot be filled by asking again right now;
        // stop here and let the next sync retry from the same cursor.
        if (!accept(event)) break;
      }
      if (lastApplied === before) return;
    }
  }

  /** Re-reads the Snapshot and Events from the last applied seq. False if a read failed. */
  async function sync(): Promise<boolean> {
    if (syncing) {
      syncAgain = true;
      return true;
    }
    syncing = true;
    try {
      do {
        syncAgain = false;
        const snapshot = await deps.getRun(runId, signal);
        if (disposed()) return false;
        adopt(snapshot);
        await catchUp(snapshot.lastSeq);
        if (disposed()) return false;
        emit();
        finishIfTerminal();
      } while (syncAgain && !disposed());
      return true;
    } catch (error) {
      if (!disposed() && base === null) {
        onChange({ snapshot: null, events: [], loadError: error });
      }
      return false;
    } finally {
      syncing = false;
    }
  }

  function requestSync(): void {
    if (disposed()) return;
    void sync();
  }

  function openStream(): void {
    if (disposed() || finished || subscription) return;
    subscription = deps.subscribe(runId, {
      afterSeq: lastApplied,
      onEvent: onStreamEvent,
      onOpen: () => {
        reconnectAttempt = 0;
      },
      onError: ({ reconnecting }) => {
        if (disposed() || finished) return;
        if (reconnecting) {
          // The browser retries with Last-Event-ID; re-read what may have been missed.
          requestSync();
          return;
        }
        subscription?.close();
        subscription = null;
        scheduleReconnect();
      },
    });
  }

  function scheduleReconnect(): void {
    if (disposed() || finished || reconnectTimer !== null) return;
    const delay = Math.min(RECONNECT_BASE_MS * 2 ** reconnectAttempt, RECONNECT_MAX_MS);
    reconnectAttempt += 1;
    reconnectTimer = deps.setTimer(() => {
      reconnectTimer = null;
      void sync().then((ok) => {
        if (disposed() || finished) return;
        if (ok) openStream();
        else scheduleReconnect();
      });
    }, delay);
  }

  void sync().then((ok) => {
    if (ok) openStream();
  });

  return {
    close() {
      controller.abort();
      subscription?.close();
      subscription = null;
      if (reconnectTimer !== null) {
        deps.clearTimer(reconnectTimer);
        reconnectTimer = null;
      }
    },
  };
}
