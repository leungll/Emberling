import { ChevronLeft, ChevronRight } from 'lucide-react';
import { useEffect, useState } from 'react';

import { mediaFormat, type MediaRef } from './media-ref';
import { FactList } from '@/components/facts/FactList';
import { IdChip } from '@/components/id/IdChip';
import { StateBlock } from '@/components/state/StateBlock';
import { IconButton } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { useCopy } from '@/lib/copy';
import { bytes } from '@/lib/time';

/** One image to show: its reference, where it loads from (null: nowhere) and its caption. */
export interface MediaItem {
  ref: MediaRef;
  src: string | null;
  title: string;
}

/**
 * The one lightbox: an image of a set at full size with its type, size and reference.
 * ← and → step through the set, Escape closes, focus returns to the thumbnail.
 */
export function Lightbox({
  items,
  index,
  onClose,
}: {
  items: readonly MediaItem[];
  index: number;
  onClose: () => void;
}) {
  const copy = useCopy();
  const m = copy.media;
  const [at, setAt] = useState(Math.max(0, Math.min(index, items.length - 1)));
  const [failed, setFailed] = useState<ReadonlySet<string>>(new Set());
  const many = items.length > 1;
  const step = (d: number) => {
    setAt((i) => Math.max(0, Math.min(items.length - 1, i + d)));
  };

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'ArrowRight' || e.key === 'ArrowLeft') {
        e.preventDefault();
        setAt((i) =>
          Math.max(0, Math.min(items.length - 1, i + (e.key === 'ArrowRight' ? 1 : -1))),
        );
      }
    };
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('keydown', onKey);
    };
  }, [items.length]);

  const it = items[at];
  if (!it) return null;
  const format = mediaFormat(it.ref);
  const shown = it.src && !failed.has(it.src) ? it.src : null;
  return (
    <Dialog
      open
      onClose={onClose}
      title={it.title}
      description={many ? m.position(at + 1, items.length) : undefined}
      size="lg"
    >
      <div className="flex items-center gap-2">
        {many ? (
          <IconButton
            label={m.previous}
            unavailableReason={at === 0 ? m.previous : undefined}
            onClick={() => {
              step(-1);
            }}
          >
            <ChevronLeft aria-hidden="true" className="size-icon-md" />
          </IconButton>
        ) : null}
        <figure
          style={{ minHeight: 240 }}
          className="m-0 flex min-w-0 flex-1 items-center justify-center rounded-md border border-border-subtle bg-surface-inset"
        >
          {shown ? (
            <img
              key={shown}
              src={shown}
              alt={many ? m.imageOf(it.title, at + 1, items.length) : it.title}
              referrerPolicy="no-referrer"
              onError={() => {
                setFailed((f) => new Set(f).add(shown));
              }}
              style={{ maxHeight: '60vh' }}
              className="block max-w-full object-contain"
            />
          ) : (
            <StateBlock kind="error" title={m.unavailable} body={m.unavailableBody} />
          )}
        </figure>
        {many ? (
          <IconButton
            label={m.next}
            data-dialog-primary
            unavailableReason={at === items.length - 1 ? m.next : undefined}
            onClick={() => {
              step(1);
            }}
          >
            <ChevronRight aria-hidden="true" className="size-icon-md" />
          </IconButton>
        ) : null}
      </div>
      <div className="mt-3">
        <FactList
          compact
          facts={[
            format ? { label: m.type, value: format } : null,
            it.ref.sizeBytes !== null ? { label: m.size, value: bytes(it.ref.sizeBytes) } : null,
            {
              label: m.reference,
              value:
                it.ref.kind === 'EXTERNAL' ? (
                  <code className="font-mono text-xs wrap-anywhere">{it.ref.id}</code>
                ) : (
                  <IdChip id={it.ref.id} label={m.referenceLabel} />
                ),
            },
          ]}
        />
      </div>
    </Dialog>
  );
}
