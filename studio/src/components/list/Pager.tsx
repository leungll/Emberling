import { ChevronLeft, ChevronRight } from 'lucide-react';
import { useEffect, useId, useRef } from 'react';

import { PAGE_SIZES, type PageSize } from './page-size';
import { Select } from './Select';
import { useCopy } from '@/lib/copy';

interface PagerProps {
  /** 1-based position of the first and last row shown. */
  from: number;
  to: number;
  /** The total when the server reports one; null shows only the range. */
  total: number | null;
  noun: readonly [string, string];
  size: PageSize;
  onSize: (size: PageSize) => void;
  hasPrevious: boolean;
  hasNext: boolean;
  onPrevious: () => void;
  onNext: () => void;
}

const BUTTON =
  'inline-flex h-control-sm min-w-control-sm items-center justify-center gap-1 rounded-md border border-transparent px-2 text-sm text-fg-secondary tabular-nums hover:enabled:bg-surface-hover hover:enabled:text-fg disabled:cursor-default disabled:text-fg-placeholder [&_svg]:size-icon-sm';

/**
 * The list footer: the range, rows per page, and Previous and Next (cursor pages, no page
 * numbers). The controls appear only when there is more than one page's worth of rows.
 * After a press focus stays on the pressed button, or moves to its partner when the pressed
 * one became unavailable.
 */
export function Pager({
  from,
  to,
  total,
  noun,
  size,
  onSize,
  hasPrevious,
  hasNext,
  onPrevious,
  onNext,
}: PagerProps) {
  const copy = useCopy();
  const sizeLabelId = useId();
  const sizeId = useId();
  const prevRef = useRef<HTMLButtonElement>(null);
  const nextRef = useRef<HTMLButtonElement>(null);
  const pressed = useRef<'prev' | 'next' | null>(null);

  useEffect(() => {
    const dir = pressed.current;
    if (!dir) return;
    pressed.current = null;
    const want = dir === 'next' ? (hasNext ? nextRef : prevRef) : hasPrevious ? prevRef : nextRef;
    want.current?.focus();
  }, [from, hasNext, hasPrevious]);

  const paged = hasPrevious || hasNext || (total ?? 0) > PAGE_SIZES[0];
  const range = copy.lists.range(from, to, total, noun);

  return (
    <div className="flex min-h-12 flex-wrap items-center justify-between gap-3 border-t border-border-subtle px-3 py-2 text-sm text-fg-muted">
      <span aria-live="polite">{range}</span>
      {paged ? (
        <div className="ml-auto flex items-center gap-4">
          <span className="inline-flex items-center gap-2">
            <span id={sizeLabelId}>{copy.lists.rowsPerPage}</span>
            <Select
              id={sizeId}
              label={copy.lists.rowsPerPage}
              labelledBy={sizeLabelId}
              size="sm"
              side="top"
              value={String(size)}
              options={PAGE_SIZES.map((s) => ({ value: String(s), label: String(s) }))}
              onChange={(v) => {
                onSize(Number(v) as PageSize);
              }}
            />
          </span>
          <nav aria-label={copy.lists.pages} className="flex items-center gap-1">
            <button
              ref={prevRef}
              type="button"
              className={BUTTON}
              disabled={!hasPrevious}
              onClick={() => {
                pressed.current = 'prev';
                onPrevious();
              }}
            >
              <ChevronLeft aria-hidden="true" />
              {copy.lists.previous}
            </button>
            <button
              ref={nextRef}
              type="button"
              className={BUTTON}
              disabled={!hasNext}
              onClick={() => {
                pressed.current = 'next';
                onNext();
              }}
            >
              {copy.lists.next}
              <ChevronRight aria-hidden="true" />
            </button>
          </nav>
        </div>
      ) : null}
    </div>
  );
}
