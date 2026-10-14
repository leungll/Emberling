import { API_BASE } from '@/api/client';
import type { AssetRef, ImageRef, JsonObject, Node, NodeRun } from '@/api/types';

/** Step types that show an image area on their card. */
export const IMAGE_TYPES: readonly string[] = [
  'image_input',
  'media_brief',
  'image_generation',
  'media_result',
  'media_output',
];

/** Step types that ask for images when a run starts, so images can be staged on them. */
export const STAGE_TYPES: readonly string[] = ['image_input', 'media_brief'];

/** One image to preview: a source URL when one is allowed, and how to name it. */
export interface PreviewImage {
  key: string;
  src: string | null;
  name: string | null;
  mediaType: string;
}

const isObject = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v);

function isImageRef(v: unknown): v is ImageRef {
  return (
    isObject(v) && (v.source === 'ASSET' || v.source === 'ARTIFACT' || v.source === 'EXTERNAL')
  );
}

export function isAssetRef(v: unknown): v is AssetRef {
  return isObject(v) && typeof v.assetId === 'string' && typeof v.mediaType === 'string';
}

/** Image references at the top level, in a list, or one property deep (a port map). */
export function collectImages(value: unknown): (ImageRef | AssetRef)[] {
  if (isImageRef(value) || isAssetRef(value)) return [value];
  if (Array.isArray(value))
    return value.flatMap((v) => (isImageRef(v) || isAssetRef(v) ? [v] : []));
  if (isObject(value))
    return Object.values(value).flatMap((v) =>
      Array.isArray(v) ? collectImages(v) : isImageRef(v) || isAssetRef(v) ? [v] : [],
    );
  return [];
}

const ARTIFACT_ID = /^artifact_[0-9a-f]{32}$/;
const ARTIFACT_TYPES: readonly string[] = ['image/png', 'image/jpeg', 'image/webp'];

export function assetUrl(assetId: string): string {
  return `${API_BASE}/assets/${encodeURIComponent(assetId)}/content`;
}

/**
 * Where an image may be previewed from: an uploaded asset's content route, a run's artifact
 * route for a well-formed image artifact, or an absolute http(s) URL. Anything else has no
 * preview and is never resolved through a guessed URL.
 */
export function previewOf(ref: ImageRef | AssetRef, runId: string | null, i: number): PreviewImage {
  if (isAssetRef(ref))
    return { key: ref.assetId, src: assetUrl(ref.assetId), name: null, mediaType: ref.mediaType };
  if (ref.source === 'ASSET' && ref.asset) {
    return {
      key: ref.asset.assetId,
      src: assetUrl(ref.asset.assetId),
      name: null,
      mediaType: ref.asset.mediaType,
    };
  }
  if (ref.source === 'ARTIFACT' && ref.artifact) {
    const a = ref.artifact;
    const ok = runId && ARTIFACT_ID.test(a.artifactId) && ARTIFACT_TYPES.includes(a.mediaType);
    return {
      key: a.artifactId,
      src: ok
        ? `${API_BASE}/runs/${encodeURIComponent(runId)}/artifacts/${encodeURIComponent(a.artifactId)}/content`
        : null,
      name: null,
      mediaType: a.mediaType,
    };
  }
  let src: string | null = null;
  try {
    const u = new URL(ref.uri ?? '');
    if ((u.protocol === 'http:' || u.protocol === 'https:') && u.host) src = ref.uri ?? null;
  } catch {
    src = null;
  }
  return { key: `${ref.uri ?? ''}#${i}`, src, name: null, mediaType: ref.mediaType ?? '' };
}

/** The images the last run produced for (or received at) a step. */
export function lastRunImages(
  node: Node,
  nr: NodeRun | undefined,
  runId: string | null,
  runInput: JsonObject | null,
): PreviewImage[] {
  if (!nr) return [];
  let refs: (ImageRef | AssetRef)[];
  if (node.type === 'media_brief') {
    refs = collectImages(nr.output);
    const key = node.config.inputKey;
    if (!refs.length && runInput && typeof key === 'string') refs = collectImages(runInput[key]);
  } else if (node.type === 'image_input') {
    refs = collectImages(nr.output);
    const key = node.config.inputKey;
    if (!refs.length && runInput && typeof key === 'string') refs = collectImages(runInput[key]);
  } else {
    const out = nr.output;
    refs = collectImages(isObject(out) && 'image' in out ? out.image : out);
  }
  return refs.map((r, i) => previewOf(r, runId, i));
}

/** Images staged on a step for the next run started from this page, kept for this browser tab. */
export interface StagedImage {
  ref: AssetRef;
  name: string;
}

export type Staged = Record<string, StagedImage[]>;

export const stagedKey = (workflowId: string): string => `emb.editor.staged:${workflowId}`;

/** The run input the staged images fill: each staging step's input key gets its images. */
export function stagedInput(nodes: readonly Node[], staged: Staged): JsonObject {
  const out: JsonObject = {};
  for (const n of nodes) {
    const key = n.config.inputKey;
    const items = staged[n.id];
    if (typeof key !== 'string' || !items?.length) continue;
    const refs = items.map((s) => ({ ...s.ref }));
    out[key] = n.type === 'media_brief' ? refs : (refs[0] ?? null);
  }
  return out;
}
