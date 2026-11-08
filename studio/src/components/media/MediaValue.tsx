import { Play, Video } from 'lucide-react';
import { useState, type ReactNode } from 'react';

import { Dialog } from '@/components/ui/dialog';
import { useCopy } from '@/lib/copy';
import { THUMB, mediaKindOf, shapeOf, type MediaField, type Shape } from './media-field';
import { resolveMediaUrl } from './media-ref';
import { useMediaSource } from './media-source';

/**
 * A value whose schema declares a media type (JSON Schema `contentMediaType`), shown as that
 * media for any step and any field. Where it loads from is the resolver's decision alone; a
 * value the resolver cannot place shows as the reference it is, never through a guessed
 * address.
 */

function VideoDialog({ url, title, onClose }: { url: string; title: string; onClose: () => void }) {
  return (
    <Dialog open onClose={onClose} title={title} size="lg">
      <div className="flex items-center justify-center rounded-md bg-surface-inset">
        <video
          src={url}
          controls
          autoPlay
          style={{ maxHeight: '60vh' }}
          className="block max-w-full rounded-md"
        />
      </div>
    </Dialog>
  );
}

/** A video's still frame that plays on click, sized by width or by height. */
function VideoThumb({
  media,
  url,
  size,
  onMeta,
}: {
  media: MediaField;
  url: string;
  size: { width: number } | { height: number };
  onMeta: (shape: Shape, seconds: number) => void;
}) {
  const copy = useCopy();
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        type="button"
        aria-label={copy.media.play(media.title)}
        onClick={(e) => {
          e.stopPropagation();
          setOpen(true);
        }}
        style={size}
        className="nodrag relative block shrink-0 cursor-pointer overflow-hidden rounded-md border border-border-subtle bg-surface-inset p-0"
      >
        <video
          src={`${url}#t=1`}
          preload="metadata"
          muted
          style={size}
          className="block"
          onLoadedMetadata={(e) => {
            const v = e.currentTarget;
            onMeta(shapeOf(v.videoWidth, v.videoHeight), v.duration);
          }}
        />
        <span className="absolute inset-0 flex items-center justify-center">
          <span className="flex size-8 items-center justify-center rounded-full border border-border bg-surface text-fg shadow-1">
            <Play aria-hidden="true" className="size-icon-sm fill-current" />
          </span>
        </span>
      </button>
      {open ? (
        <VideoDialog
          url={url}
          title={media.title}
          onClose={() => {
            setOpen(false);
          }}
        />
      ) : null}
    </>
  );
}

/** A video's length and orientation, once its metadata has loaded. */
function useVideoLine(): [string | null, Shape | null, (shape: Shape, seconds: number) => void] {
  const copy = useCopy();
  const [shape, setShape] = useState<Shape | null>(null);
  const [line, setLine] = useState<string | null>(null);
  const onMeta = (s: Shape, seconds: number) => {
    setShape(s);
    setLine(copy.media.videoFacts(seconds.toFixed(1), copy.media.shape[s]));
  };
  return [line, shape, onMeta];
}

/**
 * The media itself at a fixed height, for a card: an image, or a video still that plays on
 * click with its length beside it. Nothing when the value does not resolve.
 */
export function MediaValue({
  media,
  height = 112,
  caption,
}: {
  media: MediaField;
  height?: number;
  /** A line beside the media; `''` for none, `null` for the media's own title. */
  caption?: string | null;
}) {
  const copy = useCopy();
  const resolved = resolveMediaUrl(media.value, media.mediaType, useMediaSource());
  const [line, , onMeta] = useVideoLine();
  if (!resolved) return null;
  const kind = mediaKindOf(resolved.mediaType ?? media.mediaType);
  if (kind === 'image')
    return (
      <img
        src={resolved.url}
        alt={media.title}
        draggable={false}
        style={{ height }}
        className="rounded-md border border-border-subtle"
      />
    );
  if (kind !== 'video') return null;
  return (
    <div className="flex items-center gap-3">
      <VideoThumb media={media} url={resolved.url} size={{ height }} onMeta={onMeta} />
      <span className="flex flex-col text-sm">
        {caption !== '' ? (
          <span className="font-medium text-fg">{caption ?? media.title}</span>
        ) : null}
        <span className="text-fg-muted tabular-nums">{line ?? copy.media.video}</span>
      </span>
    </div>
  );
}

/**
 * Media with the facts beside it: a fixed-width thumbnail sized by its orientation, then the
 * media line (its length and shape, or its size) and whatever facts follow, each a muted label
 * above its value. Never a thumbnail alone beside empty space. A value that does not resolve
 * renders only the facts.
 */
export function MediaRow({
  media,
  wide = false,
  children,
}: {
  media: MediaField;
  wide?: boolean;
  children?: ReactNode;
}) {
  const copy = useCopy();
  const resolved = resolveMediaUrl(media.value, media.mediaType, useMediaSource());
  const [videoLine, videoShape, onMeta] = useVideoLine();
  const [imageShape, setImageShape] = useState<Shape | null>(null);
  const [imageLine, setImageLine] = useState<string | null>(null);
  if (!resolved) return <>{children}</>;
  const kind = mediaKindOf(resolved.mediaType ?? media.mediaType);
  const shape = kind === 'video' ? videoShape : imageShape;
  const line = kind === 'video' ? videoLine : imageLine;
  const width = THUMB[wide ? 'wide' : 'narrow'][shape ?? 'horizontal'];
  const thumb =
    kind === 'video' ? (
      <VideoThumb media={media} url={resolved.url} size={{ width }} onMeta={onMeta} />
    ) : kind === 'image' ? (
      <img
        src={resolved.url}
        alt={media.title}
        draggable={false}
        style={{ width }}
        className="block shrink-0 rounded-md border border-border-subtle"
        onLoad={(e) => {
          const i = e.currentTarget;
          setImageShape(shapeOf(i.naturalWidth, i.naturalHeight));
          setImageLine(copy.media.dimensions(i.naturalWidth, i.naturalHeight));
        }}
      />
    ) : (
      <span
        style={{ width, aspectRatio: '16 / 9' }}
        className="flex shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-inset text-fg-muted"
      >
        <Video aria-hidden="true" className="size-icon-md" />
      </span>
    );
  return (
    <div className="flex items-start gap-4">
      {thumb}
      <div className="flex min-w-0 flex-1 flex-col gap-3">
        {line ? <StackedFact label={media.title} value={line} /> : null}
        {children}
      </div>
    </div>
  );
}

/** One fact as a muted label above its value. */
export function StackedFact({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5 text-sm">
      <span className="text-fg-muted">{label}</span>
      <span className="wrap-anywhere text-fg tabular-nums">{value}</span>
    </div>
  );
}
