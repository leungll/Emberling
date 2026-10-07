import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { EventTimeline } from './EventTimeline';
import type { EventType, JsonObject, RunEvent } from '@/api/types';

// No fixture here carries a callback token, signing secret or credential: the Event
// payload contract has none, and the timeline only renders an allowlist of fields anyway.
function event(seq: number, type: EventType, payload: JsonObject = {}): RunEvent {
  return {
    id: `evt_${seq}`,
    runId: 'run_1',
    nodeRunId: type.startsWith('RUN_') ? null : 'nr_image',
    type,
    seq,
    timestamp: '2026-08-03T12:00:03Z',
    payload,
  };
}

function renderTimeline(events: RunEvent[]) {
  return render(
    <EventTimeline
      events={events}
      selectedSeq={null}
      followLive
      onSelect={() => {}}
      onResumeLive={() => {}}
    />,
  );
}

describe('EventTimeline — async Node events', () => {
  it('timeline labels NODE_DISPATCHED and NODE_CALLBACK_RECEIVED with their payload fields', () => {
    renderTimeline([
      event(6, 'NODE_DISPATCHED', { attemptNo: 1, callbackBindingId: 'cb_1' }),
      event(7, 'NODE_CALLBACK_RECEIVED', { attemptNo: 1, callbackBindingId: 'cb_1' }),
    ]);

    expect(screen.getByText('Dispatched to provider')).toBeInTheDocument();
    expect(screen.getByText('Callback received')).toBeInTheDocument();
    expect(screen.getAllByText('attempt 1')).toHaveLength(2);
    expect(screen.getAllByText('binding cb_1')).toHaveLength(2);
  });

  it('labels RUN_PAUSED and RUN_RESUMED with the Run status transition the Backend reported', () => {
    renderTimeline([
      event(8, 'RUN_PAUSED', { from: 'RUNNING', to: 'PAUSED' }),
      event(9, 'RUN_RESUMED', { from: 'PAUSED', to: 'RUNNING' }),
    ]);

    expect(screen.getByText('Run paused')).toBeInTheDocument();
    expect(screen.getByText('Run resumed')).toBeInTheDocument();
    expect(screen.getByText('RUNNING → PAUSED')).toBeInTheDocument();
    expect(screen.getByText('PAUSED → RUNNING')).toBeInTheDocument();
  });

  it('NODE_COMPLETED shows completionSource CALLBACK', () => {
    renderTimeline([
      event(10, 'NODE_COMPLETED', { latencyMs: 4200, completionSource: 'CALLBACK' }),
    ]);

    expect(screen.getByText('Node completed')).toBeInTheDocument();
    expect(screen.getByText('via CALLBACK')).toBeInTheDocument();
  });

  it('NODE_COMPLETED shows completionSource PROVIDER_POLL and NODE_FAILED its failureSource', () => {
    renderTimeline([
      event(11, 'NODE_COMPLETED', { completionSource: 'PROVIDER_POLL' }),
      event(12, 'NODE_FAILED', { attemptNo: 2, failureSource: 'TIMEOUT' }),
    ]);

    expect(screen.getByText('via PROVIDER_POLL')).toBeInTheDocument();
    expect(screen.getByText('Node failed')).toBeInTheDocument();
    expect(screen.getByText('via TIMEOUT')).toBeInTheDocument();
  });

  it('renders no payload field outside the bounded allowlist', () => {
    // Defence in depth: whatever else a payload grows, the timeline can only print the
    // fields named in the allowlist, so an unexpected key is never shown by accident.
    renderTimeline([
      event(13, 'NODE_DISPATCHED', { attemptNo: 1, unlistedField: 'must-not-render' }),
    ]);

    expect(screen.getByText('attempt 1')).toBeInTheDocument();
    expect(screen.queryByText(/must-not-render/)).not.toBeInTheDocument();
  });
});

describe('EventTimeline — status dots', () => {
  it('gives each entry the dot colour of its own Event type, in seq order', () => {
    const { container } = renderTimeline([
      event(7, 'NODE_DISPATCHED'),
      event(1, 'RUN_CREATED'),
      event(6, 'NODE_STARTED'),
      event(9, 'NODE_FAILED'),
    ]);

    const tones = [...container.querySelectorAll('li [data-tone]')].map((dot) =>
      dot.getAttribute('data-tone'),
    );
    expect(tones).toEqual(['succeeded', 'running', 'waiting', 'failed']);
    // Only the running dot pulses.
    expect(container.querySelector('[data-tone="running"]')).toHaveClass('animate-pulse');
    expect(container.querySelector('[data-tone="waiting"]')).not.toHaveClass('animate-pulse');
  });

  it('marks the selected Event and offers Resume Live when not following', () => {
    render(
      <EventTimeline
        events={[event(1, 'RUN_CREATED'), event(7, 'NODE_DISPATCHED')]}
        selectedSeq={7}
        followLive={false}
        onSelect={() => {}}
        onResumeLive={() => {}}
      />,
    );

    expect(screen.getByText('12:00:03Z · seq 7')).toBeInTheDocument();
    expect(screen.getByText('selected')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Resume Live' })).toBeInTheDocument();
  });
});
