import { API_BASE } from '@/api/client';
import { copy } from '@/lib/copy';
import { bytes } from '@/lib/time';

/**
 * Image references as the API returns them, read into one shape: an ImageRef (`source`
 * with `asset`, `artifact` or `uri`), or a list item that names an asset or artifact with
 * its media type (a photo set's `{ photoAssetId, mediaType }`, a run input's AssetRef).
 */

export type MediaKind = 'ASSET' | 'ARTIFACT' | 'EXTERNAL';

export interface MediaRef {
  kind: MediaKind;
  /** The asset or artifact ID, or the address of an outside image. */
  id: string;
  mediaType: string | null;
  sizeBytes: number | null;
}

type Obj = Record<string, unknown>;

const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);
const str = (v: unknown): string | null => (typeof v === 'string' && v !== '' ? v : null);
const num = (v: unknown): number | null => (typeof v === 'number' ? v : null);

/** A string that holds a JSON object or list, parsed; anything else as it is. */
export function parseJsonText(v: unknown): unknown {
  if (typeof v !== 'string') return v;
  const t = v.trim();
  if ((t.startsWith('{') && t.endsWith('}')) || (t.startsWith('[') && t.endsWith(']'))) {
    try {
      return JSON.parse(t) as unknown;
    } catch {
      return v;
    }
  }
  return v;
}

export function mediaRefOf(v: unknown): MediaRef | null {
  if (!isObj(v)) return null;
  const ref = (kind: MediaKind, id: string | null, o: Obj): MediaRef | null =>
    id ? { kind, id, mediaType: str(o.mediaType), sizeBytes: num(o.sizeBytes) } : null;
  if (v.source === 'ARTIFACT' && isObj(v.artifact)) {
    return ref('ARTIFACT', str(v.artifact.artifactId), v.artifact);
  }
  if (v.source === 'ASSET' && isObj(v.asset)) return ref('ASSET', str(v.asset.assetId), v.asset);
  if (v.source === 'EXTERNAL') {
    const uri = str(v.uri);
    return uri ? { kind: 'EXTERNAL', id: uri, mediaType: str(v.mediaType), sizeBytes: null } : null;
  }
  if (!str(v.mediaType)) return null;
  if (str(v.assetId) || str(v.photoAssetId)) {
    return ref('ASSET', str(v.assetId) ?? str(v.photoAssetId), v);
  }
  if (str(v.artifactId)) return ref('ARTIFACT', str(v.artifactId), v);
  return null;
}

/** How deep a value is searched; outputs nest a list of items inside a JSON text at most. */
const MAX_DEPTH = 8;

/** Every image reference in a value, JSON text included, once each, in order. */
export function collectMedia(value: unknown): MediaRef[] {
  const out: MediaRef[] = [];
  const seen = new Set<string>();
  const walk = (raw: unknown, depth: number) => {
    if (depth > MAX_DEPTH || raw === null || raw === undefined) return;
    const v = parseJsonText(raw);
    const r = mediaRefOf(v);
    if (r) {
      if (!seen.has(r.id) && (r.mediaType ?? 'image/').startsWith('image/')) {
        seen.add(r.id);
        out.push(r);
      }
      return;
    }
    if (Array.isArray(v)) for (const x of v) walk(x, depth + 1);
    else if (isObj(v)) for (const x of Object.values(v)) walk(x, depth + 1);
  };
  walk(value, 0);
  return out;
}

/** An Execution Artifact id: "artifact_" plus the first 32 hex digits of its sha256. */
const ARTIFACT_ID = /^artifact_[0-9a-f]{32}$/;

/** The image media types the Backend saves and serves an Execution Artifact as. */
const ARTIFACT_IMAGE_TYPES: readonly string[] = ['image/png', 'image/jpeg', 'image/webp'];

/**
 * Where an image is loaded from: an uploaded asset's content route; a run artifact through
 * that run's own route, only for a well-formed ID with an image media type; an outside
 * image only from an absolute http(s) address. Anything else has no preview and is never
 * resolved through a guessed URL.
 */
export function mediaSrc(ref: MediaRef, runId: string | null): string | null {
  if (ref.kind === 'ASSET') return `${API_BASE}/assets/${encodeURIComponent(ref.id)}/content`;
  if (ref.kind === 'ARTIFACT') {
    if (!runId || !ARTIFACT_ID.test(ref.id)) return null;
    if (!ref.mediaType || !ARTIFACT_IMAGE_TYPES.includes(ref.mediaType)) return null;
    return `${API_BASE}/runs/${encodeURIComponent(runId)}/artifacts/${encodeURIComponent(ref.id)}/content`;
  }
  let parsed: URL;
  try {
    parsed = new URL(ref.id);
  } catch {
    return null;
  }
  if ((parsed.protocol !== 'http:' && parsed.protocol !== 'https:') || parsed.host === '') {
    return null;
  }
  return ref.id;
}

/** The run a media-typed value is read against: its id and its input as submitted. */
export interface MediaUrlContext {
  runId: string | null;
  /** The run's input, where an uploaded asset is named by its AssetRef. */
  runInput: unknown;
}

export type MediaKindWord = 'image' | 'video' | 'other';

export interface ResolvedMedia {
  url: string;
  /** What to render it with, from the schema's media type. */
  kind: MediaKindWord;
}

function kindOf(mediaType: string): MediaKindWord {
  if (mediaType.startsWith('image/')) return 'image';
  if (mediaType.startsWith('video/')) return 'video';
  return 'other';
}

/**
 * Where a value whose schema declares a media type (JSON Schema `contentMediaType`) loads
 * from, or null when Studio has no authoritative route for it. Only an uploaded asset
 * resolves: the value must equal, exactly, the `assetId` of an AssetRef in the run's input,
 * and it loads through the asset content route. A Provider's own id, a digest or an address
 * found in a Tool output has no URL here and shows as the reference it is; nothing is ever
 * resolved through a guessed address.
 */
export function resolveMediaUrl(
  value: string,
  mediaType: string,
  ctx: MediaUrlContext,
): ResolvedMedia | null {
  if (!value) return null;
  const found = assetRefNamed(ctx.runInput, value, 0);
  if (!found) return null;
  const url = mediaSrc(found, ctx.runId);
  return url ? { url, kind: kindOf(mediaType) } : null;
}

/** The uploaded-asset reference in a value whose id is exactly `assetId`, or null. */
function assetRefNamed(raw: unknown, assetId: string, depth: number): MediaRef | null {
  if (depth > MAX_DEPTH || raw === null || raw === undefined) return null;
  const v = parseJsonText(raw);
  const r = mediaRefOf(v);
  if (r) return r.kind === 'ASSET' && r.id === assetId ? r : null;
  const children = Array.isArray(v) ? v : isObj(v) ? Object.values(v) : [];
  for (const x of children) {
    const hit = assetRefNamed(x, assetId, depth + 1);
    if (hit) return hit;
  }
  return null;
}

/** "Uploaded image", "Generated image" or "Linked image". */
export function mediaWord(ref: MediaRef): string {
  return copy.media.kind[ref.kind];
}

/** The format and size: "PNG, 2 KB". */
export function mediaFormat(ref: MediaRef): string {
  return (ref.mediaType ?? '').replace(/^image\//, '').toUpperCase();
}

export function mediaMeta(ref: MediaRef): string {
  return [mediaFormat(ref), ref.sizeBytes !== null ? bytes(ref.sizeBytes) : '']
    .filter(Boolean)
    .join(', ');
}
