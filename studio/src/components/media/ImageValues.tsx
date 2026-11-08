import { ImageIcon } from 'lucide-react';
import { useState, type ReactNode } from 'react';

import { Lightbox, type MediaItem } from '@/components/media/Lightbox';
import { mediaMeta } from '@/components/media/media-ref';
import { useCopy } from '@/lib/copy';
import { StackedFact } from './MediaValue';
import { THUMB, shapeOf, type Shape } from './media-field';

/**
 * Images in an Overview, laid out like any other media. One image is a fixed-width thumbnail
 * sized by its orientation with its facts beside it; a list of images is a row of captioned
 * thumbnails. Every thumbnail opens the one lightbox.
 */

function Picture({
  item,
  width,
  onOpen,
  onShape,
}: {
  item: MediaItem;
  width: number;
  onOpen: () => void;
  onShape?: (shape: Shape, w: number, h: number) => void;
}) {
  const copy = useCopy();
  const [broken, setBroken] = useState(false);
  return (
    <button
      type="button"
      aria-label={copy.media.openImage(item.title)}
      onClick={(e) => {
        e.stopPropagation();
        onOpen();
      }}
      style={{ width }}
      className="block shrink-0 cursor-zoom-in overflow-hidden rounded-md border border-border-subtle bg-surface-inset p-0"
    >
      {item.src && !broken ? (
        <img
          src={item.src}
          alt=""
          draggable={false}
          referrerPolicy="no-referrer"
          style={{ width }}
          className="block"
          onLoad={(e) => {
            const i = e.currentTarget;
            onShape?.(shapeOf(i.naturalWidth, i.naturalHeight), i.naturalWidth, i.naturalHeight);
          }}
          onError={() => {
            setBroken(true);
          }}
        />
      ) : (
        <span
          style={{ aspectRatio: '4 / 3' }}
          className="flex w-full items-center justify-center text-fg-muted"
        >
          <ImageIcon aria-hidden="true" className="size-icon-md" />
        </span>
      )}
    </button>
  );
}

/** One image with its facts beside it: what it is, its format and size, then the rest. */
export function ImageRow({
  item,
  label,
  wide = false,
  children,
}: {
  item: MediaItem;
  label: string;
  wide?: boolean;
  children?: ReactNode;
}) {
  const copy = useCopy();
  const [shape, setShape] = useState<Shape | null>(null);
  const [dims, setDims] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const width = THUMB[wide ? 'wide' : 'narrow'][shape ?? 'vertical'];
  const meta = mediaMeta(item.ref);
  const line = [item.title, meta, item.ref.sizeBytes === null ? dims : null]
    .filter(Boolean)
    .join(', ');
  return (
    <div className="flex items-start gap-4">
      <Picture
        item={item}
        width={width}
        onOpen={() => {
          setOpen(true);
        }}
        onShape={(s, w, h) => {
          setShape(s);
          setDims(copy.media.dimensions(w, h));
        }}
      />
      <div className="flex min-w-0 flex-1 flex-col gap-3">
        <StackedFact label={label} value={line} />
        {children}
      </div>
      {open ? (
        <Lightbox
          items={[item]}
          index={0}
          onClose={() => {
            setOpen(false);
          }}
        />
      ) : null}
    </div>
  );
}

/** A titled row of captioned thumbnails; `small` for a secondary group. */
export function ImageGroup({
  title,
  items,
  small = false,
}: {
  title: string;
  items: readonly MediaItem[];
  small?: boolean;
}) {
  const [open, setOpen] = useState<number | null>(null);
  if (!items.length) return null;
  const width = small ? 72 : 112;
  return (
    <div className="flex min-w-0 flex-col gap-2 text-sm">
      <span className="text-fg-muted">{title}</span>
      <ul className="m-0 flex list-none flex-wrap gap-3 p-0">
        {items.map((it, i) => (
          <li key={`${it.ref.id}-${String(i)}`} style={{ width }} className="flex flex-col gap-1">
            <Picture
              item={it}
              width={width}
              onOpen={() => {
                setOpen(i);
              }}
            />
            <span className="text-fg-secondary">{it.title}</span>
          </li>
        ))}
      </ul>
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
