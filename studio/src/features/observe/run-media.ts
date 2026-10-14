import type { ExecutionFactTrace, JsonValue } from '@/api/types';
import type { MediaItem } from '@/components/media/Lightbox';
import {
  collectMedia,
  mediaRefOf,
  mediaSrc,
  mediaWord,
  parseJsonText,
  type MediaRef,
} from '@/components/media/media-ref';
import { copy } from '@/lib/copy';
import type { RunModel, Step } from './run-model';

/**
 * The images of one run, read from returned records only: the photo list the photo set
 * step returned, the image references in inputs and outputs, and the images the Agent's
 * saved results (`image_generated`) describe. Nothing here counts or compares records; a
 * position ("Photo 2") is an index into that one returned list.
 */

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

function outputText(step: Step | undefined): unknown {
  const out = step?.nodeRun?.output;
  return isObj(out) ? out.text : undefined;
}

const photoCache = new WeakMap<RunModel, MediaRef[]>();
const savedCache = new WeakMap<RunModel, Map<string, SavedImage>>();

/** The photos the run's photo set step returned, in order. */
export function photoList(model: RunModel): MediaRef[] {
  const hit = photoCache.get(model);
  if (hit) return hit;
  const brief = model.steps.find((s) => s.node.type === 'media_brief' && s.nodeRun?.output);
  const v = parseJsonText(outputText(brief));
  const list = isObj(v) && Array.isArray(v.photos) ? collectMedia(v.photos) : [];
  photoCache.set(model, list);
  return list;
}

/** Which photo of the set an asset is (from 1), or null when the set does not list it. */
export function photoNo(model: RunModel, assetId: unknown): number | null {
  const i = photoList(model).findIndex((p) => p.id === assetId);
  return i < 0 ? null : i + 1;
}

/** The image an `image_generated` result saved: the artifact it binds. */
export function factImage(fact: ExecutionFactTrace): MediaRef | null {
  const artifact = parseJsonText(fact.bindings.artifact);
  return isObj(artifact) ? mediaRefOf({ source: 'ARTIFACT', artifact }) : null;
}

interface SavedImage {
  ref: MediaRef;
  photoAssetId: unknown;
}

/** Every image the run's saved results describe, by artifact ID. */
function savedImages(model: RunModel): Map<string, SavedImage> {
  const hit = savedCache.get(model);
  if (hit) return hit;
  const out = new Map<string, SavedImage>();
  savedCache.set(model, out);
  for (const s of model.steps) {
    for (const f of s.trace?.facts.items ?? []) {
      const ref = factImage(f);
      if (ref) out.set(ref.id, { ref, photoAssetId: f.bindings.photoAssetId });
    }
  }
  return out;
}

/** The image a saved result is about: the one it saved, or the image its subject names. */
export function savedImageOf(model: RunModel, fact: ExecutionFactTrace): MediaRef | null {
  return factImage(fact) ?? savedImages(model).get(fact.subject)?.ref ?? null;
}

/**
 * The images in a value: its image references, then artifact IDs the run saved as images
 * (an Agent answers with artifact IDs alone; their media type comes from the saved result).
 */
export function imagesIn(model: RunModel, value: JsonValue | null | undefined): MediaRef[] {
  const refs = collectMedia(value);
  const seen = new Set(refs.map((r) => r.id));
  const saved = savedImages(model);
  if (!saved.size) return refs;
  const walk = (raw: unknown, depth: number) => {
    if (depth > 8 || raw === null || raw === undefined) return;
    const v = parseJsonText(raw);
    if (typeof v === 'string') {
      const hit = saved.get(v);
      if (hit && !seen.has(v)) {
        seen.add(v);
        refs.push(hit.ref);
      }
    } else if (Array.isArray(v)) for (const x of v) walk(x, depth + 1);
    else if (isObj(v)) for (const x of Object.values(v)) walk(x, depth + 1);
  };
  walk(value, 0);
  return refs;
}

/** "Photo 2", "Generated from photo 2", or what kind of image it is. */
export function imageLabel(model: RunModel, ref: MediaRef): string {
  const m = copy.runs.media;
  if (ref.kind === 'ASSET') {
    const n = photoNo(model, ref.id);
    if (n !== null) return m.photo(n);
  }
  const saved = savedImages(model).get(ref.id);
  const from = saved ? photoNo(model, saved.photoAssetId) : null;
  return from !== null ? m.generatedFrom(from) : mediaWord(ref);
}

export interface Review {
  passedCount: number;
  photoCount: number;
  notPassed: string[];
  policyVersion: string | null;
}

/**
 * The review verdict a media result step returned in its caption: its own passedCount,
 * photoCount and notPassed. Studio shows these fields; it never counts reviews itself.
 */
export function reviewOf(value: JsonValue | null | undefined): Review | null {
  if (!isObj(value)) return null;
  const c = parseJsonText(value.caption);
  if (!isObj(c) || typeof c.passedCount !== 'number' || typeof c.photoCount !== 'number') {
    return null;
  }
  return {
    passedCount: c.passedCount,
    photoCount: c.photoCount,
    notPassed: Array.isArray(c.notPassed)
      ? c.notPassed.filter((x): x is string => typeof x === 'string')
      : [],
    policyVersion: typeof c.policyVersion === 'string' ? c.policyVersion : null,
  };
}

/** The run's images as lightbox items: where each loads from, and its caption. */
export function mediaItems(model: RunModel, refs: readonly MediaRef[]): MediaItem[] {
  const runId = model.snapshot.run.id;
  return refs.map((ref) => ({ ref, src: mediaSrc(ref, runId), title: imageLabel(model, ref) }));
}
