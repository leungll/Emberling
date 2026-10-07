import type { ReactNode } from 'react';

import { cn } from '@/lib/utils';

/** Uppercase section label shared by every Observe card (the mocks' "RUN SUMMARY" style). */
export function SectionLabel({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <h2
      className={cn(
        'text-xs font-bold tracking-[0.08em] text-[var(--muted-foreground)] uppercase',
        className,
      )}
    >
      {children}
    </h2>
  );
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
        'flex min-h-0 min-w-0 flex-col rounded-xl border border-[var(--border)] bg-[var(--card)]',
        className,
      )}
    >
      <div className="flex shrink-0 items-center justify-between gap-3 px-5 pt-4 pb-3">
        <SectionLabel>{title}</SectionLabel>
        {meta ? <div className="text-[13px] text-[var(--muted-foreground)]">{meta}</div> : null}
      </div>
      <div className={cn('min-h-0 flex-1', bodyClassName)}>{children}</div>
    </section>
  );
}
