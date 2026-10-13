import { Check, Hourglass } from 'lucide-react';

import { TONE_DOT } from './toneClasses';
import type { StatusTone } from '@/lib/status';
import { cn } from '@/lib/utils';

const TONE_RING: Record<StatusTone, string> = {
  neutral: 'border-st-queued-dot text-st-queued-fg',
  running: 'border-st-running-dot text-st-running-fg',
  waiting: 'border-st-waiting-dot text-st-waiting-fg',
  succeeded: 'border-st-succeeded-dot text-st-succeeded-fg',
  failed: 'border-st-failed-dot text-st-failed-fg',
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
        'inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-full border-2 bg-bg',
        TONE_RING[tone],
        className,
      )}
    >
      {tone === 'succeeded' ? <Check className="h-4 w-4" strokeWidth={3} /> : null}
      {tone === 'waiting' ? <Hourglass className="h-4 w-4" strokeWidth={2.5} /> : null}
      {tone === 'failed' ? <span className="text-md font-semibold">!</span> : null}
      {tone === 'running' || tone === 'neutral' ? (
        <span
          className={cn(
            'h-2 w-2 rounded-full',
            TONE_DOT[tone],
            tone === 'running' && 'animate-pulse',
          )}
        />
      ) : null}
    </span>
  );
}
