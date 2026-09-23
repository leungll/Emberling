import { API_BASE } from './client';
import type { EventType, RunEvent } from './types';

/**
 * Every MVP Event type. The Backend writes each Event as a NAMED frame
 * (`id: <seq>\nevent: <TYPE>\ndata: <Event JSON>`, 08 §5), and the browser dispatches a
 * named frame only to listeners registered for that name, never to `onmessage`. A Record
 * keeps this list exhaustive at compile time when `EventType` changes.
 */
const EVENT_TYPE_SET: Record<EventType, true> = {
  RUN_CREATED: true,
  RUN_PAUSED: true,
  RUN_RESUMED: true,
  RUN_COMPLETED: true,
  RUN_FAILED: true,
  NODE_READY: true,
  NODE_STARTED: true,
  NODE_RETRYING: true,
  NODE_DISPATCHED: true,
  NODE_CALLBACK_RECEIVED: true,
  NODE_COMPLETED: true,
  NODE_FAILED: true,
  AGENT_STARTED: true,
  AGENT_TURN_READY: true,
  AGENT_TURN_STARTED: true,
  AGENT_DECISION_COMMITTED: true,
  AGENT_ACTION_STARTED: true,
  AGENT_ACTION_WAITING: true,
  AGENT_ACTION_COMPLETED: true,
  AGENT_ACTION_FAILED: true,
  AGENT_STATE_UPDATED: true,
  AGENT_COMPLETED: true,
  AGENT_FAILED: true,
};

export const EVENT_TYPES: readonly EventType[] = Object.keys(EVENT_TYPE_SET) as EventType[];

/** `EventSource.CLOSED`; the browser will not reconnect by itself from this state. */
const READY_STATE_CLOSED = 2;

export interface SseErrorInfo {
  /**
   * True while the browser retries on its own (readyState CONNECTING); that retry sends
   * the last received `id:` as `Last-Event-ID`. False once the browser gave up (readyState
   * CLOSED, e.g. a non-2xx response), after which only the caller can reconnect.
   */
  reconnecting: boolean;
}

export interface SubscribeRunEventsOptions {
  /**
   * Seq of the last Event the caller already holds. Sent as `afterSeq` on the first
   * connection; the browser's own reconnects use `Last-Event-ID`, which the Backend
   * prefers over `afterSeq`.
   */
  afterSeq: number;
  onEvent: (event: RunEvent) => void;
  onOpen?: () => void;
  onError?: (info: SseErrorInfo) => void;
  /** Injected in tests. Defaults to the browser EventSource. */
  eventSourceFactory?: (url: string) => EventSourceLike;
}

/** The slice of EventSource this module uses, so tests can supply a fake. */
export interface EventSourceLike {
  readonly readyState: number;
  onopen: ((event: Event) => void) | null;
  onerror: ((event: Event) => void) | null;
  addEventListener(type: string, listener: (event: MessageEvent) => void): void;
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
 * them. It drops anything whose `seq` is not greater than the last delivered one, so a
 * replay across a reconnect is never delivered twice. It does NOT hide a forward jump:
 * gap detection and re-query belong to the caller, which knows what it has applied.
 */
export function subscribeRunEvents(
  runId: string,
  options: SubscribeRunEventsOptions,
): RunEventSubscription {
  const { afterSeq, onEvent, onOpen, onError, eventSourceFactory } = options;

  const url = `${API_BASE}/runs/${encodeURIComponent(runId)}/events?afterSeq=${afterSeq}`;
  const factory = eventSourceFactory ?? ((target: string) => new EventSource(target));
  const source = factory(url);

  let lastSeq = afterSeq;
  let closed = false;

  const onFrame = (message: MessageEvent) => {
    if (closed) return;

    let event: RunEvent;
    try {
      event = JSON.parse(String(message.data)) as RunEvent;
    } catch {
      // A frame that does not parse proves nothing about execution. Ignore it and keep
      // the cursor; the caller's gap detection re-reads anything it hid.
      return;
    }

    if (typeof event.seq !== 'number') return;
    if (event.seq <= lastSeq) return;

    lastSeq = event.seq;
    onEvent(event);
  };

  for (const type of EVENT_TYPES) source.addEventListener(type, onFrame);

  source.onopen = () => {
    if (closed) return;
    onOpen?.();
  };

  source.onerror = () => {
    if (closed) return;
    onError?.({ reconnecting: source.readyState !== READY_STATE_CLOSED });
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
