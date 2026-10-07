import { cva } from 'class-variance-authority';

export const badgeVariants = cva(
  'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[13px] font-semibold',
  {
    variants: {
      variant: {
        default: 'border-transparent bg-[var(--secondary)] text-[var(--secondary-foreground)]',
        outline: 'border-[var(--border)] text-[var(--foreground)]',
        // Status colours follow the shared status palette and are shared by Definitions, Canvas, Timeline and
        // Detail. Tokens are defined per theme (light/dark) in index.css.
        neutral: 'border-transparent bg-[var(--status-neutral-bg)] text-[var(--status-neutral-fg)]',
        running: 'border-transparent bg-[var(--status-running-bg)] text-[var(--status-running-fg)]',
        waiting: 'border-transparent bg-[var(--status-waiting-bg)] text-[var(--status-waiting-fg)]',
        succeeded:
          'border-transparent bg-[var(--status-succeeded-bg)] text-[var(--status-succeeded-fg)]',
        failed: 'border-transparent bg-[var(--status-failed-bg)] text-[var(--status-failed-fg)]',
      },
    },
    defaultVariants: { variant: 'default' },
  },
);
