import type { StatusTone } from '@/lib/status';

/** Solid colour per tone, from the shared status tokens in index.css. */
export const TONE_DOT: Record<StatusTone, string> = {
  neutral: 'bg-st-queued-dot',
  running: 'bg-st-running-dot',
  waiting: 'bg-st-waiting-dot',
  succeeded: 'bg-st-succeeded-dot',
  failed: 'bg-st-failed-dot',
};

/** Readable foreground per tone, for inline status wording next to a glyph. */
export const TONE_TEXT: Record<StatusTone, string> = {
  neutral: 'text-st-queued-fg',
  running: 'text-st-running-fg',
  waiting: 'text-st-waiting-fg',
  succeeded: 'text-st-succeeded-fg',
  failed: 'text-st-failed-fg',
};

/** Border and tinted fill per tone, for a selected card that keeps its status colour. */
export const TONE_CARD: Record<StatusTone, string> = {
  neutral: 'border-fg-muted bg-surface-hover',
  running: 'border-st-running-dot bg-st-running-bg',
  waiting: 'border-st-waiting-dot bg-st-waiting-bg',
  succeeded: 'border-st-succeeded-dot bg-st-succeeded-bg',
  failed: 'border-st-failed-dot bg-st-failed-bg',
};
