import { cva, type VariantProps } from 'class-variance-authority';
import type { HTMLAttributes } from 'react';

import { cn } from '@/lib/utils';

const badgeVariants = cva(
  'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[13px] font-semibold',
  {
    variants: {
      variant: {
        default: 'border-transparent bg-[var(--secondary)] text-[var(--secondary-foreground)]',
        outline: 'border-[var(--border)] text-[var(--foreground)]',
        // Status colours follow 04 §4 and are shared by Definitions, Canvas, Timeline and
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

export type BadgeProps = HTMLAttributes<HTMLSpanElement> &
  VariantProps<typeof badgeVariants> & {
    /** Renders the leading status dot used by the Definitions table and Canvas (04 §4). */
    dot?: boolean;
  };

export function Badge({ className, variant, dot, children, ...props }: BadgeProps) {
  return (
    <span className={cn(badgeVariants({ variant }), className)} {...props}>
      {dot ? (
        <span
          aria-hidden="true"
          className="h-[5px] w-[5px] shrink-0 rounded-full bg-current opacity-90"
        />
      ) : null}
      {children}
    </span>
  );
}

export { badgeVariants };
