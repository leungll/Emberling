import { API_BASE } from './client';
import type { RunEvent } from './types';

export interface SubscribeRunEventsOptions {
  /**
   * Seq of the last Event the caller already holds, normally the Snapshot's `lastSeq`.
   * The Backend replays everything with a greater seq.
   */
  afterSeq: number;
  onEvent: (event: RunEvent) => void;
  onError?: (error: Event) => void;
  /** Injected in tests. Defaults to the browser EventSource. */
  eventSourceFactory?: (url: string) => EventSourceLike;
}

/** The slice of EventSource this module uses, so tests can supply a fake. */
export interface EventSourceLike {
  onmessage: ((event: MessageEvent) => void) | null;
  onerror: ((event: Event) => void) | null;
  close(): void;
}

export interface RunEventSubscription {
  close(): void;
  /** Highest seq delivered to the caller so far. Exposed for diagnostics and tests. */
  lastDeliveredSeq(): number;
}

/**
 * Subscribes to the committed Event stream of one Run.
 *
 * The Backend is the only source of Events; this function neither creates nor reorders
 * them. Reconnection is handled by the browser, which resends the last received event id
 * as `Last-Event-ID`. Because a reconnect window or a duplicated notification can replay
 * an already delivered Event, this function drops anything whose `seq` is not strictly
 * greater than the last delivered one, so the caller only ever sees ascending seq.
 */
export function subscribeRunEvents(
  runId: string,
  options: SubscribeRunEventsOptions,
): RunEventSubscription {
  const { afterSeq, onEvent, onError, eventSourceFactory } = options;

  const url = `${API_BASE}/runs/${encodeURIComponent(runId)}/events?afterSeq=${afterSeq}`;
  const factory = eventSourceFactory ?? ((target: string) => new EventSource(target));
  const source = factory(url);

  let lastSeq = afterSeq;
  let closed = false;

  source.onmessage = (message: MessageEvent) => {
    if (closed) return;

    let event: RunEvent;
    try {
      event = JSON.parse(String(message.data)) as RunEvent;
    } catch {
      // A frame that does not parse proves nothing about execution. Ignore it and keep
      // the cursor, so the next query re-reads from the last confirmed seq.
      return;
    }

    if (typeof event.seq !== 'number') return;
    if (event.seq <= lastSeq) return;

    lastSeq = event.seq;
    onEvent(event);
  };

  source.onerror = (error: Event) => {
    if (closed) return;
    onError?.(error);
  };

  return {
    close() {
      closed = true;
      source.close();
    },
    lastDeliveredSeq() {
      return lastSeq;
    },
  };
}
