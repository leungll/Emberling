import { ChevronRight } from 'lucide-react';
import { useState, type ReactNode } from 'react';

import { useCopy } from '@/lib/copy';
import { readString, writeString } from '@/lib/storage';
import { cn } from '@/lib/utils';

export interface Fact {
  label: string;
  /** A missing value (null, undefined or '') reads "Not recorded". */
  value: ReactNode;
  note?: ReactNode;
}

/** Label and value rows. Facts are listed, never joined with dots. */
export function FactList({
  facts,
  compact = false,
  className,
}: {
  facts: readonly (Fact | null | false | undefined)[];
  compact?: boolean;
  className?: string;
}) {
  const copy = useCopy();
  return (
    <dl
      style={{ gridTemplateColumns: 'minmax(96px, 140px) minmax(0, 1fr)' }}
      className={cn('m-0 grid items-baseline gap-x-4', className)}
    >
      {facts
        .filter((f): f is Fact => Boolean(f))
        .map((f) => {
          const missing = f.value === null || f.value === undefined || f.value === '';
          return (
            <div key={f.label} className="contents">
              <dt className="py-1 text-sm wrap-anywhere text-fg-muted">{f.label}</dt>
              <dd
                className={cn(
                  'm-0 flex min-w-0 flex-wrap items-center gap-1.5 py-1 wrap-anywhere text-fg',
                  compact && 'text-sm',
                )}
              >
                {missing ? (
                  <span className="text-fg-muted">{copy.facts.notRecorded}</span>
                ) : (
                  f.value
                )}
                {f.note ? <span className="basis-full text-sm text-fg-muted">{f.note}</span> : null}
              </dd>
            </div>
          );
        })}
    </dl>
  );
}

/**
 * The one collapsed home for raw codes, messages and internal IDs. Whether it is open is
 * remembered per page for the session.
 */
export function TechnicalDetails({
  storageKey,
  children,
  label,
}: {
  /** Distinguishes the details blocks of one page. */
  storageKey: string;
  children: ReactNode;
  label?: string;
}) {
  const copy = useCopy();
  const key = `emb.tech:${window.location.pathname}:${storageKey}`;
  const [open, setOpen] = useState(() => readString('session', key) === '1');
  return (
    <details
      open={open}
      onToggle={(e) => {
        const next = e.currentTarget.open;
        setOpen(next);
        writeString('session', key, next ? '1' : null);
      }}
      className="group border-t border-border-subtle"
    >
      <summary className="flex h-control-md cursor-pointer list-none items-center gap-1.5 rounded-sm text-sm font-medium text-fg-muted hover:text-fg [&::-webkit-details-marker]:hidden">
        <ChevronRight
          aria-hidden="true"
          className="size-icon-sm transition-transform duration-2 group-open:rotate-90"
        />
        {label ?? copy.techDetails}
      </summary>
      <div className="flex flex-col gap-3 pb-3">{children}</div>
    </details>
  );
}
