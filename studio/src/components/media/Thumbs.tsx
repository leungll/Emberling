import { ImageIcon } from 'lucide-react';
import { useState, type CSSProperties, type MouseEvent } from 'react';

import { Lightbox, type MediaItem } from './Lightbox';
import { mediaMeta } from './media-ref';
import { IdChip } from '@/components/id/IdChip';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';

/**
 * Thumbnails: every image shows whole (contain, never cropped) and opens the one lightbox.
 * An image that cannot be loaded shows the image icon, and the lightbox says why.
 */

export type ThumbSize = 'sm' | 'card' | 'lg';

const SIZE: Record<ThumbSize, { cls: string; style?: CSSProperties }> = {
  sm: { cls: 'size-control-xs rounded-sm' },
  card: { cls: 'size-control-lg rounded-md' },
  lg: { cls: 'rounded-md', style: { width: 128, height: 96 } },
};

function Thumb({
  item,
  size,
  tabbable,
  onOpen,
}: {
  item: MediaItem;
  size: ThumbSize;
  tabbable: boolean;
  onOpen: (e: MouseEvent) => void;
}) {
  const copy = useCopy();
  const [broken, setBroken] = useState(false);
  const meta = mediaMeta(item.ref);
  const s = SIZE[size];
  return (
    <button
      type="button"
      tabIndex={tabbable ? undefined : -1}
      aria-label={copy.media.open(meta ? `${item.title}, ${meta}` : item.title)}
      onClick={onOpen}
      style={s.style}
      className={cn(
        'inline-flex shrink-0 cursor-zoom-in items-center justify-center overflow-hidden border border-border bg-surface-inset p-0 text-fg-muted hover:border-fg-muted focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus',
        s.cls,
      )}
    >
      {item.src && !broken ? (
        <img
          src={item.src}
          alt=""
          loading="lazy"
          draggable={false}
          referrerPolicy="no-referrer"
          onError={() => {
            setBroken(true);
          }}
          className="block size-full object-contain"
        />
      ) : (
        <ImageIcon aria-hidden="true" className="size-icon-sm" />
      )}
    </button>
  );
}

/** A row of thumbnails, at most `max` of them, the rest behind a "+N" that opens the lightbox. */
export function ThumbSet({
  items,
  size = 'card',
  max,
  label,
  tabbable = true,
  className,
}: {
  items: readonly MediaItem[];
  size?: ThumbSize;
  max?: number;
  /** Names the group, such as "Images so far". */
  label?: string;
  tabbable?: boolean;
  className?: string;
}) {
  const copy = useCopy();
  const [open, setOpen] = useState<number | null>(null);
  if (!items.length) return null;
  const shown = max !== undefined ? items.slice(0, max) : items;
  const more = items.length - shown.length;
  const opener = (i: number) => (e: MouseEvent) => {
    e.stopPropagation();
    setOpen(i);
  };
  return (
    <span
      role="group"
      aria-label={label}
      className={cn('flex flex-wrap items-center gap-1', className)}
    >
      {shown.map((it, i) => (
        <Thumb
          key={`${it.ref.id}-${String(i)}`}
          item={it}
          size={size}
          tabbable={tabbable}
          onOpen={opener(i)}
        />
      ))}
      {more > 0 ? (
        <button
          type="button"
          tabIndex={tabbable ? undefined : -1}
          aria-label={copy.media.more(more)}
          onClick={opener(shown.length)}
          className={cn(
            'inline-flex shrink-0 cursor-zoom-in items-center justify-center rounded-md border border-border bg-surface px-1 text-xs text-fg-secondary hover:border-fg-muted',
            SIZE[size].cls,
          )}
        >
          {`+${String(more)}`}
        </button>
      ) : null}
      {open !== null ? (
        <Lightbox
          items={items}
          index={open}
          onClose={() => {
            setOpen(null);
          }}
        />
      ) : null}
    </span>
  );
}

/**
 * The main view of a value that holds images: each image with its caption, format and size,
 * and its asset or artifact ID.
 */
export function Gallery({ items }: { items: readonly MediaItem[] }) {
  const copy = useCopy();
  const m = copy.media;
  const [open, setOpen] = useState<number | null>(null);
  if (!items.length) return null;
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-3">
      {items.map((it, i) => {
        const meta = mediaMeta(it.ref);
        return (
          <figure
            key={`${it.ref.id}-${String(i)}`}
            style={{ width: 128 }}
            className="m-0 flex flex-col gap-1.5"
          >
            <Thumb
              item={it}
              size="lg"
              tabbable
              onOpen={() => {
                setOpen(i);
              }}
            />
            <figcaption className="flex flex-col items-start gap-0.5 text-xs text-fg-secondary">
              <span className="text-sm font-medium text-fg">{it.title}</span>
              {meta ? <span className="text-fg-muted">{meta}</span> : null}
              {it.ref.kind === 'EXTERNAL' ? (
                <code className="font-mono wrap-anywhere text-fg-muted">{it.ref.id}</code>
              ) : (
                <IdChip
                  id={it.ref.id}
                  kind={it.ref.kind === 'ARTIFACT' ? m.artifactId : m.assetId}
                  label={it.ref.kind === 'ARTIFACT' ? m.artifactIdLabel : m.assetIdLabel}
                />
              )}
            </figcaption>
          </figure>
        );
      })}
      {open !== null ? (
        <Lightbox
          items={items}
          index={open}
          onClose={() => {
            setOpen(null);
          }}
        />
      ) : null}
    </div>
  );
}
