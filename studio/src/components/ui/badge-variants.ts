import { cva } from 'class-variance-authority';

export const badgeVariants = cva(
  'inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-sm font-semibold',
  {
    variants: {
      variant: {
        default: 'border-transparent bg-surface-hover text-fg',
        outline: 'border-border text-fg',
        // Status colours follow the shared status palette and are shared by Definitions, Canvas, Timeline and
        // Detail. Tokens are defined per theme (light/dark) in index.css.
        neutral: 'border-transparent bg-st-queued-bg text-st-queued-fg',
        running: 'border-transparent bg-st-running-bg text-st-running-fg',
        waiting: 'border-transparent bg-st-waiting-bg text-st-waiting-fg',
        succeeded: 'border-transparent bg-st-succeeded-bg text-st-succeeded-fg',
        failed: 'border-transparent bg-st-failed-bg text-st-failed-fg',
      },
    },
    defaultVariants: { variant: 'default' },
  },
);
