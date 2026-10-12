import { API_BASE } from '@/api/client';
import type { ImageRef, ImageSource } from '@/api/types';

const IMAGE_SOURCES: readonly ImageSource[] = ['ASSET', 'ARTIFACT', 'EXTERNAL'];

/** Mirrors `backend/internal/domain/imageref.go`'s `Source` discriminant. */
export function isImageRefValue(value: unknown): value is ImageRef {
  return (
    typeof value === 'object' &&
    value !== null &&
    !Array.isArray(value) &&
    IMAGE_SOURCES.includes((value as { source?: unknown }).source as ImageSource)
  );
}

/**
 * Finds ImageRef-shaped values at the top level or one property deep. NodeRun input/output
 * is a raw `NodeOutput.Ports` map keyed by port name (e.g. `{ image: <ImageRef> }`), so one
 * level of nesting is enough; this does not walk arbitrary JSON depth looking for a match.
 */
export function collectImageRefs(value: unknown): ImageRef[] {
  if (isImageRefValue(value)) return [value];
  if (typeof value === 'object' && value !== null && !Array.isArray(value)) {
    return Object.values(value).filter(isImageRefValue);
  }
  return [];
}

/**
 * The URI an EXTERNAL reference may be previewed from: only an absolute http or https URL,
 * the only form the Backend accepts for that branch. Anything else (a relative path, a
 * `data:` or `javascript:` URI, an unparseable string) gets no `<img>` and is shown as text
 * only, so the panel never loads or inlines content the reference was not allowed to name.
 */
export function externalPreviewUri(uri: string | undefined): string | null {
  if (typeof uri !== 'string' || uri === '') return null;
  let parsed: URL;
  try {
    parsed = new URL(uri);
  } catch {
    return null;
  }
  if ((parsed.protocol !== 'http:' && parsed.protocol !== 'https:') || parsed.host === '') {
    return null;
  }
  return uri;
}

/** The Backend route that serves an uploaded Asset's immutable content, or null. */
export function assetPreviewUri(image: ImageRef): string | null {
  const assetId = image.asset?.assetId;
  if (typeof assetId !== 'string' || assetId === '') return null;
  return `${API_BASE}/assets/${encodeURIComponent(assetId)}/content`;
}
