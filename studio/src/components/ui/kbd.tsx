import type { ReactNode } from 'react';

import { cn } from '@/lib/utils';

/** A key name, as in tooltips and the shortcut sheet. */
export function Kbd({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <kbd
      style={{ height: 18 }}
      className={cn(
        'inline-flex items-center rounded-sm border border-border bg-surface-inset px-1 font-mono text-xs font-medium whitespace-nowrap text-fg-muted',
        className,
      )}
    >
      {children}
    </kbd>
  );
}
