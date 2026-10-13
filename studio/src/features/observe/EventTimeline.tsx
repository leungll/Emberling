import { useEffect, useRef } from 'react';

import { SectionLabel } from './Panel';
import { StatusDot } from './StatusGlyph';
import { TONE_CARD } from './toneClasses';
import type { RunEvent } from '@/api/types';
import { Button } from '@/components/ui/button';
import { eventLabel, eventPayloadSummary, eventTone } from '@/lib/events';
import { formatClock } from '@/lib/format';
import { cn } from '@/lib/utils';

interface EventTimelineProps {
  events: RunEvent[];
  selectedSeq: number | null;
  followLive: boolean;
  onSelect: (event: RunEvent) => void;
  onResumeLive: () => void;
  /** NodeRun id → display name, so an entry can name the node it belongs to. */
  nodeRunNames?: ReadonlyMap<string, string>;
  /** `list` is the vertical Event Timeline (Run view); `strip` the Agent view's bottom row. */
  variant?: 'list' | 'strip';
}

/**
 * Committed Events ordered by `seq`. The list is exactly what the Backend sent: the
 * timeline neither reorders nor synthesises entries. The dot colour classifies each
 * Event's own type; it is never computed from other Events or NodeRuns.
 */
export function EventTimeline({
  events,
  selectedSeq,
  followLive,
  onSelect,
  onResumeLive,
  nodeRunNames,
  variant = 'list',
}: EventTimelineProps) {
  const ordered = [...events].sort((a, b) => a.seq - b.seq);
  const lastSeq = ordered.at(-1)?.seq ?? 0;
  const scroller = useRef<HTMLDivElement>(null);
  const stripTail = useRef<HTMLSpanElement>(null);

  // The Trace follows the newest Event until the user picks a historical one.
  useEffect(() => {
    const element = scroller.current;
    if (!followLive || !element) return;
    const follow = () => {
      element.scrollTop = element.scrollHeight;
      if (variant === 'strip') alignStripEnd(element, stripTail.current);
      else element.scrollLeft = element.scrollWidth;
    };
    follow();
    if (variant !== 'strip' || typeof ResizeObserver === 'undefined') return;
    // The strip's width follows the window, which changes which item would be clipped.
    const observer = new ResizeObserver(follow);
    observer.observe(element);
    return () => observer.disconnect();
  }, [followLive, lastSeq, variant]);

  const liveControl = followLive ? (
    <span className="text-sm font-semibold text-fg">Live</span>
  ) : (
    <Button variant="ghost" size="sm" className="text-sm" onClick={onResumeLive}>
      Resume Live
    </Button>
  );

  if (variant === 'strip') {
    return (
      <section className="flex h-16 shrink-0 items-center gap-6 border-t border-border bg-surface px-5">
        <SectionLabel className="shrink-0">Event Stream</SectionLabel>
        <div
          ref={scroller}
          data-testid="event-stream"
          className="relative flex min-w-0 flex-1 items-center gap-2 overflow-x-auto"
        >
          {ordered.map((event) => (
            <button
              key={event.seq}
              type="button"
              onClick={() => onSelect(event)}
              title={eventLabel(event.type)}
              className={cn(
                'flex shrink-0 items-center gap-2 rounded-md px-3 py-1.5 text-md text-fg-muted hover:bg-surface-hover',
                event.seq === selectedSeq && 'bg-surface-hover text-fg',
                event.seq === lastSeq && 'font-semibold text-fg',
              )}
            >
              <StatusDot tone={eventTone(event.type)} className="h-2 w-2" />
              <span className="tabular-nums">#{event.seq}</span>
              <span>{event.type}</span>
            </button>
          ))}
          {/* Trailing room that lets the newest items scroll far enough for the first
              visible one to start whole (see alignStripEnd). */}
          <span ref={stripTail} aria-hidden="true" className="shrink-0" />
        </div>
        <div className="shrink-0">{liveControl}</div>
      </section>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 items-center justify-between px-5 pt-4 pb-3">
        <SectionLabel>Event Timeline</SectionLabel>
        {liveControl}
      </div>

      <div ref={scroller} className="min-h-0 flex-1 overflow-auto px-4 pb-4">
        {ordered.length === 0 ? (
          <p className="px-1 text-md text-fg-muted">No events yet.</p>
        ) : (
          <ol className="relative">
            {/* The connecting rail behind the dots. */}
            <span
              aria-hidden="true"
              style={{ left: 23 }}
              className="absolute top-3 bottom-3 w-px bg-border"
            />
            {ordered.map((event) => {
              const selected = event.seq === selectedSeq;
              const nodeName = event.nodeRunId ? nodeRunNames?.get(event.nodeRunId) : undefined;
              return (
                <li key={event.seq} className="relative">
                  <button
                    type="button"
                    onClick={() => onSelect(event)}
                    className={cn(
                      'flex w-full items-start gap-4 rounded-lg border border-transparent px-4 py-3 text-left hover:bg-surface-hover',
                      selected && TONE_CARD[eventTone(event.type)],
                    )}
                  >
                    <StatusDot tone={eventTone(event.type)} className="relative mt-1" />
                    <span className="min-w-0 flex-1">
                      <span className="block text-xs text-fg-muted tabular-nums">
                        {formatClock(event.timestamp)}, seq {event.seq}
                      </span>
                      <span className="mt-0.5 flex items-center justify-between gap-2">
                        <span className="truncate text-md font-semibold">{event.type}</span>
                        {selected ? (
                          <span className="shrink-0 text-sm font-semibold">selected</span>
                        ) : null}
                      </span>
                      <span className="mt-0.5 flex flex-wrap gap-x-2 text-md text-fg-muted">
                        <span>{eventLabel(event.type)}</span>
                        {nodeName ? <span>, {nodeName}</span> : null}
                        {eventPayloadSummary(event.payload).map((field) => (
                          <span key={field}>{field}</span>
                        ))}
                      </span>
                    </span>
                  </button>
                </li>
              );
            })}
          </ol>
        )}
      </div>
    </div>
  );
}

/**
 * Scrolls the Event stream strip to its newest item without clipping the leftmost visible
 * one mid-label: when scrolling to the very end would cut an item at the left edge, the
 * strip scrolls on to the next item's start and `tail` supplies the extra trailing room.
 */
function alignStripEnd(element: HTMLDivElement, tail: HTMLSpanElement | null) {
  if (tail) tail.style.width = '0px';
  element.scrollLeft = element.scrollWidth;
  const left = element.scrollLeft;
  const items = Array.from(element.children).filter(
    (child): child is HTMLElement => child instanceof HTMLElement && child !== tail,
  );
  const clipped = items.findIndex(
    (item) => item.offsetLeft < left && item.offsetLeft + item.offsetWidth > left,
  );
  const next = clipped >= 0 ? items[clipped + 1] : undefined;
  if (!tail || !next) return;
  tail.style.width = `${next.offsetLeft - left}px`;
  element.scrollLeft = next.offsetLeft;
}
