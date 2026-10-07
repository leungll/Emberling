import { Check, Hourglass } from 'lucide-react';

import { TONE_DOT } from './toneClasses';
import type { StatusTone } from '@/lib/status';
import { cn } from '@/lib/utils';

const TONE_RING: Record<StatusTone, string> = {
  neutral: 'border-[var(--status-neutral-dot)] text-[var(--status-neutral-fg)]',
  running: 'border-[var(--status-running-dot)] text-[var(--status-running-fg)]',
  waiting: 'border-[var(--status-waiting-dot)] text-[var(--status-waiting-fg)]',
  succeeded: 'border-[var(--status-succeeded-dot)] text-[var(--status-succeeded-fg)]',
  failed: 'border-[var(--status-failed-dot)] text-[var(--status-failed-fg)]',
};

/**
 * Plain status dot (Timeline, Turn cards). Running pulses lightly; every other tone
 * is a still dot. `data-tone` exposes the class for tests without reading colours.
 */
export function StatusDot({ tone, className }: { tone: StatusTone; className?: string }) {
  return (
    <span
      aria-hidden="true"
      data-tone={tone}
      className={cn(
        'inline-block h-3 w-3 shrink-0 rounded-full',
        TONE_DOT[tone],
        tone === 'running' && 'animate-pulse',
        className,
      )}
    />
  );
}

/**
 * Circled status mark (Run Rail): green check, amber hourglass, red exclamation, a pulsing
 * blue dot for running and a neutral dot for ready.
 */
export function StatusGlyph({ tone, className }: { tone: StatusTone; className?: string }) {
  return (
    <span
      aria-hidden="true"
      data-tone={tone}
      className={cn(
        'inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-full border-2 bg-[var(--background)]',
        TONE_RING[tone],
        className,
      )}
    >
      {tone === 'succeeded' ? <Check className="h-4 w-4" strokeWidth={3} /> : null}
      {tone === 'waiting' ? <Hourglass className="h-4 w-4" strokeWidth={2.5} /> : null}
      {tone === 'failed' ? <span className="text-[15px] leading-none font-bold">!</span> : null}
      {tone === 'running' || tone === 'neutral' ? (
        <span
          className={cn(
            'h-2.5 w-2.5 rounded-full',
            TONE_DOT[tone],
            tone === 'running' && 'animate-pulse',
          )}
        />
      ) : null}
    </span>
  );
}
