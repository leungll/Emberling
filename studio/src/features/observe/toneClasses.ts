import type { StatusTone } from '@/lib/status';

/** Solid colour per tone, from the shared 04 §4 status tokens in index.css. */
export const TONE_DOT: Record<StatusTone, string> = {
  neutral: 'bg-[var(--status-neutral-dot)]',
  running: 'bg-[var(--status-running-dot)]',
  waiting: 'bg-[var(--status-waiting-dot)]',
  succeeded: 'bg-[var(--status-succeeded-dot)]',
  failed: 'bg-[var(--status-failed-dot)]',
};

/** Readable foreground per tone, for inline status wording next to a glyph. */
export const TONE_TEXT: Record<StatusTone, string> = {
  neutral: 'text-[var(--status-neutral-fg)]',
  running: 'text-[var(--status-running-fg)]',
  waiting: 'text-[var(--status-waiting-fg)]',
  succeeded: 'text-[var(--status-succeeded-fg)]',
  failed: 'text-[var(--status-failed-fg)]',
};

/** Border and tinted fill per tone, for a selected card that keeps its status colour. */
export const TONE_CARD: Record<StatusTone, string> = {
  neutral: 'border-[var(--muted-foreground)] bg-[var(--accent)]',
  running: 'border-[var(--status-running-dot)] bg-[var(--status-running-bg)]',
  waiting: 'border-[var(--status-waiting-dot)] bg-[var(--status-waiting-bg)]',
  succeeded: 'border-[var(--status-succeeded-dot)] bg-[var(--status-succeeded-bg)]',
  failed: 'border-[var(--status-failed-dot)] bg-[var(--status-failed-bg)]',
};
