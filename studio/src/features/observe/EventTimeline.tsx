import type { RunEvent } from '@/api/types';
import { Button } from '@/components/ui/button';
import { ScrollArea } from '@/components/ui/scroll-area';
import { eventLabel, eventPayloadSummary } from '@/lib/events';
import { formatTimestamp } from '@/lib/format';
import { cn } from '@/lib/utils';

interface EventTimelineProps {
  events: RunEvent[];
  selectedSeq: number | null;
  followLive: boolean;
  onSelect: (event: RunEvent) => void;
  onResumeLive: () => void;
}

/**
 * Committed Events ordered by `seq`. The list is exactly what the Backend sent: the
 * timeline neither reorders nor synthesises entries.
 */
export function EventTimeline({
  events,
  selectedSeq,
  followLive,
  onSelect,
  onResumeLive,
}: EventTimelineProps) {
  const ordered = [...events].sort((a, b) => a.seq - b.seq);

  return (
    <section className="flex min-w-0 flex-1 flex-col border-r border-[var(--border)]">
      <div className="flex items-center justify-between border-b border-[var(--border)] px-3 py-2">
        <h2 className="text-xs font-semibold uppercase tracking-wide text-[var(--muted-foreground)]">
          Event Timeline
        </h2>
        {followLive ? (
          <span className="text-[10px] text-[var(--muted-foreground)]">Following live</span>
        ) : (
          <Button variant="ghost" size="sm" onClick={onResumeLive}>
            Resume Live
          </Button>
        )}
      </div>

      <ScrollArea className="flex-1">
        {ordered.length === 0 ? (
          <p className="p-3 text-xs text-[var(--muted-foreground)]">No events yet.</p>
        ) : (
          <ol className="divide-y divide-[var(--border)]">
            {ordered.map((event) => (
              <li key={event.seq}>
                <button
                  type="button"
                  onClick={() => onSelect(event)}
                  // The raw Event type stays reachable: readable wording must not hide
                  // which Event the Backend actually committed.
                  title={event.type}
                  className={cn(
                    'flex w-full items-baseline gap-3 px-3 py-1.5 text-left text-xs hover:bg-[var(--accent)]',
                    event.seq === selectedSeq && 'bg-[var(--accent)]',
                  )}
                >
                  <span className="w-10 shrink-0 tabular-nums text-[var(--muted-foreground)]">
                    #{event.seq}
                  </span>
                  <span className="font-medium">{eventLabel(event.type)}</span>
                  {eventPayloadSummary(event.payload).map((field) => (
                    <span key={field} className="text-[10px] text-[var(--muted-foreground)]">
                      {field}
                    </span>
                  ))}
                  <span className="ml-auto text-[10px] text-[var(--muted-foreground)]">
                    {formatTimestamp(event.timestamp)}
                  </span>
                </button>
              </li>
            ))}
          </ol>
        )}
      </ScrollArea>
    </section>
  );
}
