import { createContext } from 'react';

import { API_BASE } from '@/api/client';
import type { ExecutionFactTrace, ImageRef, ImageSource } from '@/api/types';

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

/** An Execution Artifact id: "artifact_" plus the first 32 hex digits of its sha256. */
const ARTIFACT_ID = /^artifact_[0-9a-f]{32}$/;

/** The image media types the Backend saves and serves an Execution Artifact as. */
const ARTIFACT_IMAGE_MEDIA_TYPES: readonly string[] = ['image/png', 'image/jpeg', 'image/webp'];

/**
 * The Run that ImagePreviews below it belong to. An ARTIFACT is served only through the
 * route of the Run that produced it, so without a Run there is no preview.
 */
export const PreviewRunContext = createContext<string | null>(null);

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

/**
 * The ARTIFACT image a Tool result saved, read from the execution fact that result
 * recorded: its `artifact` binding is the canonical JSON of the ArtifactRef. The binding is
 * used only when it decodes to a complete reference that names the fact's own subject, so
 * a fact about one asset never previews another.
 */
export function factArtifactImage(fact: ExecutionFactTrace): ImageRef | null {
  const raw = fact.bindings.artifact;
  if (typeof raw !== 'string') return null;
  let decoded: unknown;
  try {
    decoded = JSON.parse(raw);
  } catch {
    return null;
  }
  if (typeof decoded !== 'object' || decoded === null || Array.isArray(decoded)) return null;
  const { artifactId, mediaType, sizeBytes, sha256 } = decoded as Record<string, unknown>;
  if (
    typeof artifactId !== 'string' ||
    typeof mediaType !== 'string' ||
    typeof sizeBytes !== 'number' ||
    typeof sha256 !== 'string' ||
    artifactId !== fact.subject
  ) {
    return null;
  }
  return { source: 'ARTIFACT', artifact: { artifactId, mediaType, sizeBytes, sha256 } };
}
