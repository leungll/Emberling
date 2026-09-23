import { describe, expect, it, vi } from 'vitest';

import { EVENT_TYPES, subscribeRunEvents, type EventSourceLike } from './sse';
import type { EventType, RunEvent } from './types';

const CONNECTING = 0;
const OPEN = 1;
const CLOSED = 2;

/**
 * Behaves like the browser EventSource for the frames the Backend writes
 * (`id: N\nevent: <TYPE>\ndata: ...`): a named frame is dispatched ONLY to listeners
 * registered for that type through addEventListener, never to `onmessage`.
 */
class FakeEventStream implements EventSourceLike {
  readyState = CONNECTING;
  onopen: ((event: Event) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  closed = false;
  private readonly listeners = new Map<string, Array<(event: MessageEvent) => void>>();

  constructor(readonly url: string) {}

  addEventListener(type: string, listener: (event: MessageEvent) => void): void {
    const list = this.listeners.get(type) ?? [];
    list.push(listener);
    this.listeners.set(type, list);
  }

  close(): void {
    this.closed = true;
    this.readyState = CLOSED;
  }

  open(): void {
    this.readyState = OPEN;
    this.onopen?.(new Event('open'));
  }

  /** Simulates one committed Event arriving as a named SSE frame. */
  emit(event: RunEvent): void {
    this.emitRaw(event.type, JSON.stringify(event), String(event.seq));
  }

  emitRaw(type: string, data: string, lastEventId = ''): void {
    const message = new MessageEvent(type, { data, lastEventId });
    if (type === 'message') this.onmessage?.(message);
    for (const listener of this.listeners.get(type) ?? []) listener(message);
  }

  fail(readyState: number): void {
    this.readyState = readyState;
    this.onerror?.(new Event('error'));
  }
}

function event(seq: number, type: EventType = 'NODE_COMPLETED'): RunEvent {
  return {
    id: `evt_${seq}`,
    runId: 'run_123',
    nodeRunId: 'nr_123',
    type,
    seq,
    timestamp: '2026-08-03T12:00:04Z',
    payload: {},
  };
}

function subscribe(afterSeq = 0) {
  const received: RunEvent[] = [];
  const onOpen = vi.fn();
  const onError = vi.fn();
  let stream!: FakeEventStream;

  const subscription = subscribeRunEvents('run_123', {
    afterSeq,
    onEvent: (e) => received.push(e),
    onOpen,
    onError,
    eventSourceFactory: (url) => {
      stream = new FakeEventStream(url);
      return stream;
    },
  });

  return { received, stream, subscription, onOpen, onError };
}

describe('subscribeRunEvents', () => {
  it('opens the stream at the snapshot cursor', () => {
    const { stream, subscription } = subscribe(12);
    expect(stream.url).toBe('/api/runs/run_123/events?afterSeq=12');
    subscription.close();
  });

  it('delivers every MVP Event type sent as a named frame', () => {
    const { received, stream, subscription } = subscribe(0);

    EVENT_TYPES.forEach((type, index) => stream.emit(event(index + 1, type)));

    expect(received.map((e) => e.type)).toEqual([...EVENT_TYPES]);
    expect(EVENT_TYPES).toHaveLength(23);
    subscription.close();
  });

  it('does not treat an unnamed message frame as an Event', () => {
    const { received, stream, subscription } = subscribe(0);

    stream.emitRaw('message', JSON.stringify(event(1)), '1');

    expect(received).toHaveLength(0);
    subscription.close();
  });

  it('drops duplicate seq', () => {
    const { received, stream, subscription } = subscribe(0);

    stream.emit(event(1));
    stream.emit(event(2));
    // A reconnect window or a repeated notification replays an already delivered Event.
    stream.emit(event(2));
    stream.emit(event(3));

    expect(received.map((e) => e.seq)).toEqual([1, 2, 3]);
    subscription.close();
  });

  it('drops events at or below the initial afterSeq', () => {
    const { received, stream, subscription } = subscribe(12);

    stream.emit(event(11));
    stream.emit(event(12));
    stream.emit(event(13));

    expect(received.map((e) => e.seq)).toEqual([13]);
    subscription.close();
  });

  it('delivers a forward jump so the caller can detect the gap', () => {
    const { received, stream, subscription } = subscribe(0);

    stream.emit(event(1));
    stream.emit(event(5));
    stream.emit(event(4));

    expect(received.map((e) => e.seq)).toEqual([1, 5]);
    expect(subscription.lastDeliveredSeq()).toBe(5);
    subscription.close();
  });

  it('keeps the cursor when a frame does not parse', () => {
    const { received, stream, subscription } = subscribe(0);

    stream.emit(event(1));
    stream.emitRaw('NODE_COMPLETED', 'not json', '2');
    stream.emit(event(2));

    expect(received.map((e) => e.seq)).toEqual([1, 2]);
    expect(subscription.lastDeliveredSeq()).toBe(2);
    subscription.close();
  });

  it('stops delivering and closes the underlying stream on close', () => {
    const { received, stream, subscription } = subscribe(0);

    stream.emit(event(1));
    subscription.close();
    stream.emit(event(2));

    expect(stream.closed).toBe(true);
    expect(received.map((e) => e.seq)).toEqual([1]);
  });

  it('surfaces open', () => {
    const { stream, subscription, onOpen } = subscribe(0);

    stream.open();

    expect(onOpen).toHaveBeenCalledTimes(1);
    subscription.close();
  });

  it('reports whether the browser will reconnect on its own after an error', () => {
    const { stream, subscription, onError, received } = subscribe(0);

    // CONNECTING: the browser retries itself and sends Last-Event-ID.
    stream.fail(CONNECTING);
    // CLOSED: the browser gave up (e.g. a proxy 5xx); only the caller can reconnect.
    stream.fail(CLOSED);

    expect(onError.mock.calls.map(([info]) => info)).toEqual([
      { reconnecting: true },
      { reconnecting: false },
    ]);
    expect(received).toHaveLength(0);
    subscription.close();
  });

  it('forwards nothing after close', () => {
    const { stream, subscription, onError, onOpen } = subscribe(0);

    subscription.close();
    stream.onopen?.(new Event('open'));
    stream.onerror?.(new Event('error'));

    expect(onOpen).not.toHaveBeenCalled();
    expect(onError).not.toHaveBeenCalled();
  });
});
