import type { VariantProps } from 'class-variance-authority';
import type { HTMLAttributes } from 'react';

import { cn } from '@/lib/utils';

import { badgeVariants } from './badge-variants';

export type BadgeProps = HTMLAttributes<HTMLSpanElement> &
  VariantProps<typeof badgeVariants> & {
    /** Renders the leading status dot used by the Definitions table and Canvas. */
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
