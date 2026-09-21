import { cva, type VariantProps } from 'class-variance-authority';
import type { HTMLAttributes } from 'react';

import { cn } from '@/lib/utils';

const badgeVariants = cva(
  'inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs font-medium',
  {
    variants: {
      variant: {
        default: 'border-transparent bg-[var(--secondary)] text-[var(--secondary-foreground)]',
        outline: 'border-[var(--border)] text-[var(--foreground)]',
        // Status colours follow 04 §4 and are shared by Canvas, Timeline and Detail.
        neutral: 'border-[var(--border)] bg-transparent text-[var(--muted-foreground)]',
        running: 'border-sky-500/40 bg-sky-500/10 text-sky-500',
        waiting: 'border-amber-500/40 bg-amber-500/10 text-amber-500',
        succeeded: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-500',
        failed: 'border-red-500/40 bg-red-500/10 text-red-500',
      },
    },
    defaultVariants: { variant: 'default' },
  },
);

export type BadgeProps = HTMLAttributes<HTMLSpanElement> & VariantProps<typeof badgeVariants>;

export function Badge({ className, variant, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ variant }), className)} {...props} />;
}

export { badgeVariants };
