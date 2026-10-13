import type { ReactNode } from 'react';

import { cn } from '@/lib/utils';

/** Uppercase section label shared by every Observe card (the mocks' "RUN SUMMARY" style). */
export function SectionLabel({ children, className }: { children: ReactNode; className?: string }) {
  return <h2 className={cn('text-xs font-semibold text-fg-muted', className)}>{children}</h2>;
}

/** Rounded dark card with a label row, as laid out in the Observe mocks. */
export function Panel({
  title,
  meta,
  children,
  className,
  bodyClassName,
  testId,
}: {
  title: ReactNode;
  meta?: ReactNode;
  children: ReactNode;
  className?: string;
  bodyClassName?: string;
  testId?: string;
}) {
  return (
    <section
      data-testid={testId}
      className={cn(
        'flex min-h-0 min-w-0 flex-col rounded-xl border border-border bg-surface',
        className,
      )}
    >
      <div className="flex shrink-0 items-center justify-between gap-3 px-5 pt-4 pb-3">
        <SectionLabel>{title}</SectionLabel>
        {meta ? <div className="text-sm text-fg-muted">{meta}</div> : null}
      </div>
      <div className={cn('min-h-0 flex-1', bodyClassName)}>{children}</div>
    </section>
  );
}
