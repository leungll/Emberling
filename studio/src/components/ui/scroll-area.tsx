import type { HTMLAttributes } from 'react';

import { cn } from '@/lib/utils';

/** Minimal scroll container. The MVP needs overflow, not a custom scrollbar. */
export function ScrollArea({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('overflow-auto', className)} {...props} />;
}
