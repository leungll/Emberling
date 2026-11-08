import { createContext, useContext } from 'react';

import type { JsonValue } from '@/api/types';
import type { MediaItem } from '@/components/media/Lightbox';
import {
  collectMedia,
  mediaRefOf,
  mediaSrc,
  mediaWord,
  parseJsonText,
  type MediaRef,
} from '@/components/media/media-ref';
import { readableKeys } from '@/lib/value-reading';

/**
 * Which values of any object are images, read through the image references Studio already
 * understands (an ImageRef, an asset or artifact reference) and, when a run is shown, the
 * images that run's records describe. Nothing here knows a particular workflow.
 */

type Obj = Record<string, JsonValue>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

export interface ValueReview {
  passedCount: number;
  photoCount: number;
  /** The items that did not pass, as the run lists them. */
  notPassed: MediaItem[];
}

/** How a run resolves images and review verdicts in its values; the run page provides one. */
export interface RunImages {
  /** Every image reference in a value, JSON text included, plus the ids the run saved as images. */
  refsIn(v: JsonValue | undefined): MediaRef[];
  /** Where each image loads from and what to call it. */
  items(refs: readonly MediaRef[]): MediaItem[];
  /** A review verdict a value returns, or null. */
  review(v: JsonValue | undefined): ValueReview | null;
}

/** The run whose images an Overview shows; without it only full references resolve. */
export const RunImagesContext = createContext<RunImages | null>(null);

export function useRunImages(): RunImages | null {
  return useContext(RunImagesContext);
}

/** A value as data: JSON text parsed, anything else as it is. */
export function asData(v: JsonValue | undefined): JsonValue | undefined {
  return parseJsonText(v) as JsonValue | undefined;
}

/** Items for references outside a run: assets and outside addresses load, artifacts do not. */
function plainItems(refs: readonly MediaRef[]): MediaItem[] {
  return refs.map((ref) => ({ ref, src: mediaSrc(ref, null), title: mediaWord(ref) }));
}

/** An item that only points at an image: a reference, or ids and media facts alone. */
function onlyPoints(v: JsonValue | undefined): boolean {
  if (typeof v === 'string') return true;
  if (mediaRefOf(v)) return true;
  return (
    isObj(v) && readableKeys(v, Object.keys(v)).every((k) => k === 'mediaType' || k === 'sizeBytes')
  );
}

export interface ValueImages {
  items: MediaItem[];
  /** A list of images rather than one. */
  list: boolean;
}

/**
 * The images a value is: one image reference, or a list whose every item names one image and
 * nothing readable besides. A value that merely contains an image somewhere is not one.
 */
export function imagesOf(run: RunImages | null, raw: JsonValue | undefined): ValueImages | null {
  const v = asData(raw);
  const refsIn = (x: JsonValue | undefined) => (run ? run.refsIn(x) : collectMedia(x));
  const toItems = (refs: MediaRef[]) => (run ? run.items(refs) : plainItems(refs));
  if (Array.isArray(v)) {
    if (!v.length || !v.every(onlyPoints)) return null;
    const per = v.map((x) => refsIn(x));
    if (!per.every((r) => r.length === 1)) return null;
    return { items: toItems(per.flat()), list: true };
  }
  if (!onlyPoints(v)) return null;
  const refs = refsIn(v);
  return refs.length === 1 ? { items: toItems(refs), list: false } : null;
}

/** A review verdict a value returns: how many passed of how many, and which did not. */
export function reviewIn(run: RunImages | null, raw: JsonValue | undefined): ValueReview | null {
  return run ? run.review(asData(raw)) : null;
}
