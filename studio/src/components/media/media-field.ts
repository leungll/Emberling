import type { JsonValue } from '@/api/types';
import { humanize } from '@/lib/value-reading';
import { resolveMediaUrl, tracedMediaType, type MediaResolutionContext } from './media-ref';

/**
 * A value whose schema declares a media type (JSON Schema `contentMediaType`), and the shape
 * a thumbnail takes. Where media loads from is the resolver's decision alone.
 */

export interface MediaField {
  key: string;
  title: string;
  value: string;
  mediaType: string;
}

export interface MediaSchema {
  properties?: Record<string, MediaSchema>;
  title?: string;
  contentMediaType?: string;
}

/**
 * The media type a string value is read as: the one its schema declares, else the one the
 * Trace media list gives a reference equal to it; null when it is neither.
 */
export function mediaTypeOf(
  value: JsonValue | undefined,
  schema: MediaSchema | undefined,
  ctx: MediaResolutionContext,
): string | null {
  if (typeof value !== 'string') return null;
  if (typeof schema?.contentMediaType === 'string') return schema.contentMediaType;
  return tracedMediaType(value, ctx);
}

/**
 * The first field of `data` that is media and resolves: its schema declares a media type,
 * or the Trace media list names its value. Schema order first, then the rest of the data.
 */
export function findMedia(
  schema: MediaSchema | null,
  data: Record<string, JsonValue> | null,
  ctx: MediaResolutionContext,
): MediaField | null {
  if (!data) return null;
  const props = schema?.properties ?? {};
  const keys = [...Object.keys(props), ...Object.keys(data).filter((k) => !(k in props))];
  for (const key of keys) {
    const v = data[key];
    const mediaType = mediaTypeOf(v, props[key], ctx);
    if (mediaType && typeof v === 'string' && resolveMediaUrl(v, mediaType, ctx)) {
      return { key, title: props[key]?.title ?? humanize(key), value: v, mediaType };
    }
  }
  return null;
}

export type MediaKindWord = 'image' | 'video' | 'other';

/** What renders a media type: an image, a video, or neither. */
export function mediaKindOf(mediaType: string | null): MediaKindWord {
  if (mediaType?.startsWith('image/')) return 'image';
  if (mediaType?.startsWith('video/')) return 'video';
  return 'other';
}

export type Shape = 'vertical' | 'horizontal' | 'square';

export const shapeOf = (w: number, h: number): Shape =>
  h > w ? 'vertical' : h < w ? 'horizontal' : 'square';

/** Thumbnail widths by orientation; a wide container may show it larger. */
export const THUMB: Record<'narrow' | 'wide', Record<Shape, number>> = {
  narrow: { vertical: 112, horizontal: 200, square: 140 },
  wide: { vertical: 160, horizontal: 320, square: 200 },
};
