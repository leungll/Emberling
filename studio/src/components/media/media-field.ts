import type { JsonValue } from '@/api/types';
import { resolveMediaUrl, type MediaResolutionContext } from './media-ref';

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

/** The first field of `data` whose schema declares a media type and that resolves. */
export function findMedia(
  schema: MediaSchema | null,
  data: Record<string, JsonValue> | null,
  ctx: MediaResolutionContext,
): MediaField | null {
  if (!schema?.properties || !data) return null;
  for (const [key, sub] of Object.entries(schema.properties)) {
    const v = data[key];
    if (
      typeof sub.contentMediaType === 'string' &&
      typeof v === 'string' &&
      resolveMediaUrl(v, sub.contentMediaType, ctx)
    )
      return { key, title: sub.title ?? key, value: v, mediaType: sub.contentMediaType };
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
