import { API_BASE } from '@/api/client';
import type { ImageRef, ImageSource } from '@/api/types';

const IMAGE_SOURCES: readonly ImageSource[] = ['ASSET', 'ARTIFACT', 'EXTERNAL'];

/** An image reference, told apart by its `source` discriminant. */
export function isImageRefValue(value: unknown): value is ImageRef {
  return (
    typeof value === 'object' &&
    value !== null &&
    !Array.isArray(value) &&
    IMAGE_SOURCES.includes((value as { source?: unknown }).source as ImageSource)
  );
}

/**
 * Finds ImageRef-shaped values at the top level or one port deep, singly or as a list. NodeRun
 * input and output are maps keyed by port name (`{ image: <ImageRef> }`, `{ photos: [...] }`),
 * so one level is enough; arbitrary JSON depth is never searched for a match.
 */
export function collectImageRefs(value: unknown): ImageRef[] {
  if (isImageRefValue(value)) return [value];
  if (typeof value === 'object' && value !== null && !Array.isArray(value)) {
    return Object.values(value).flatMap((v: unknown) =>
      isImageRefValue(v) ? [v] : Array.isArray(v) ? v.filter(isImageRefValue) : [],
    );
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

/** An Execution Artifact id: "artifact_" plus the first 32 hex digits of its sha256. */
const ARTIFACT_ID = /^artifact_[0-9a-f]{32}$/;

/** The image media types the Backend saves and serves an Execution Artifact as. */
const ARTIFACT_IMAGE_MEDIA_TYPES: readonly string[] = ['image/png', 'image/jpeg', 'image/webp'];

/**
 * The Run-scoped route that serves an ARTIFACT's verified content, or null. Only a
 * well-formed artifact id with an image media type is previewed; anything else is shown
 * as reference text, never resolved through a guessed URL.
 */
export function artifactPreviewUri(image: ImageRef, runId: string | null): string | null {
  const artifact = image.artifact;
  if (!runId || !artifact) return null;
  if (typeof artifact.artifactId !== 'string' || !ARTIFACT_ID.test(artifact.artifactId)) {
    return null;
  }
  if (!ARTIFACT_IMAGE_MEDIA_TYPES.includes(artifact.mediaType)) return null;
  return `${API_BASE}/runs/${encodeURIComponent(runId)}/artifacts/${encodeURIComponent(artifact.artifactId)}/content`;
}
