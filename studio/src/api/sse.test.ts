import { describe, expect, it, vi } from 'vitest';

import { subscribeRunEvents, type EventSourceLike } from './sse';
import type { RunEvent, EventType } from './types';

class FakeEventStream implements EventSourceLike {
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  closed = false;

  constructor(readonly url: string) {}

  close(): void {
    this.closed = true;
  }

  /** Simulates one committed Event arriving over the wire. */
  emit(event: RunEvent): void {
    this.onmessage?.(new MessageEvent('message', { data: JSON.stringify(event) }));
  }

  emitRaw(data: string): void {
    this.onmessage?.(new MessageEvent('message', { data }));
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
  let stream!: FakeEventStream;

  const subscription = subscribeRunEvents('run_123', {
    afterSeq,
    onEvent: (e) => received.push(e),
    eventSourceFactory: (url) => {
      stream = new FakeEventStream(url);
      return stream;
    },
  });

  return { received, stream, subscription };
}

describe('subscribeRunEvents', () => {
  it('opens the stream at the snapshot cursor', () => {
    const { stream, subscription } = subscribe(12);
    expect(stream.url).toBe('/api/runs/run_123/events?afterSeq=12');
    subscription.close();
  });

  it('drops duplicate seq', () => {
    const { received, stream, subscription } = subscribe(0);

    stream.emit(event(1));
    stream.emit(event(2));
    // A reconnect window or a repeated notification replays an already applied Event.
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

  it('delivers strictly ascending seq when the stream regresses', () => {
    const { received, stream, subscription } = subscribe(0);

    stream.emit(event(1));
    stream.emit(event(5));
    stream.emit(event(4));
    stream.emit(event(2));
    stream.emit(event(6));

    expect(received.map((e) => e.seq)).toEqual([1, 5, 6]);
    expect(subscription.lastDeliveredSeq()).toBe(6);
    subscription.close();
  });

  it('keeps the cursor when a frame does not parse', () => {
    const { received, stream, subscription } = subscribe(0);

    stream.emit(event(1));
    stream.emitRaw('not json');
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

  it('forwards transport errors without inventing an Event', () => {
    const onError = vi.fn();
    let stream!: FakeEventStream;
    const received: RunEvent[] = [];

    const subscription = subscribeRunEvents('run_123', {
      afterSeq: 0,
      onEvent: (e) => received.push(e),
      onError,
      eventSourceFactory: (url) => {
        stream = new FakeEventStream(url);
        return stream;
      },
    });

    stream.onerror?.(new Event('error'));

    expect(onError).toHaveBeenCalledTimes(1);
    expect(received).toHaveLength(0);
    subscription.close();
  });
});
