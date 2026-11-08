import type { JsonValue } from '@/api/types';
import type { RunImages } from '@/components/media/value-media';
import { imagesIn, mediaItems, photoList, reviewOf } from './run-media';
import type { RunModel } from './run-model';

/** How a run's values resolve their images: the run's own records name them. */

type Obj = Record<string, JsonValue>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

const cache = new WeakMap<RunModel, RunImages>();

/** The run's images as the shared value layer reads them; one object per run model. */
export function runImagesOf(model: RunModel): RunImages {
  const hit = cache.get(model);
  if (hit) return hit;
  const made: RunImages = {
    refsIn: (v) => imagesIn(model, v),
    items: (refs) => mediaItems(model, refs),
    review: (v) => {
      if (!isObj(v)) return null;
      const r = reviewOf({ caption: v });
      if (!r) return null;
      const refs = photoList(model).filter((p) => r.notPassed.includes(p.id));
      return {
        passedCount: r.passedCount,
        photoCount: r.photoCount,
        notPassed: mediaItems(model, refs),
      };
    },
  };
  cache.set(model, made);
  return made;
}
