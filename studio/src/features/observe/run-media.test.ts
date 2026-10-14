import { describe, expect, it } from 'vitest';

import { imageLabel, imagesIn, photoList, reviewOf, savedImageOf } from './run-media';
import { PHOTO_RUN, recordedModel, recordedParts } from '@/test/recorded-run';

const m = recordedModel(recordedParts(PHOTO_RUN.dir, PHOTO_RUN.runId));
const step = (type: string) => {
  const s = m.steps.find((x) => x.node.type === type);
  if (!s?.nodeRun) throw new Error(`no ${type} step`);
  return s;
};

describe('photoList', () => {
  it('is the one list of photos the photo set step returned', () => {
    expect(photoList(m).map((r) => r.id)).toEqual([
      'asset_1163e40e4d624c0fbed64bbaf782a349',
      'asset_33551a30dfeb4fb7960909e5c680b4be',
      'asset_2141b4e978c9458f874c7ec6dbd8daea',
    ]);
  });
});

describe('imagesIn', () => {
  it('reads image references, and artifact IDs the run saved as generated images', () => {
    const agent = step('agent').nodeRun;
    const refs = imagesIn(m, agent?.output);
    expect(refs.map((r) => [r.kind, r.id])).toEqual([
      ['ARTIFACT', 'artifact_a09efbd58fef361792747db896e5a04f'],
      ['ARTIFACT', 'artifact_2bafb85f8945b42083a3d0afba07d752'],
      ['ARTIFACT', 'artifact_057235642ebba33207e13d6dd79caf96'],
    ]);
    expect(refs[0]?.mediaType).toBe('image/jpeg');
  });

  it('finds the uploaded photos in the run input', () => {
    expect(imagesIn(m, m.snapshot.run.input).map((r) => r.kind)).toEqual([
      'ASSET',
      'ASSET',
      'ASSET',
    ]);
  });
});

describe('imageLabel', () => {
  it('says which photo of the set an image is, or which photo it was made from', () => {
    const [p1, p2] = photoList(m);
    expect(p1 && imageLabel(m, p1)).toBe('Photo 1');
    expect(p2 && imageLabel(m, p2)).toBe('Photo 2');
    const [made] = imagesIn(m, step('agent').nodeRun?.output);
    expect(made && imageLabel(m, made)).toBe('Generated from photo 1');
  });
});

describe('savedImageOf', () => {
  it('finds the image a saved result is about', () => {
    const facts = step('agent').trace?.facts.items ?? [];
    const review = facts.find((f) => f.factType === 'asset_reviewed');
    expect(review && savedImageOf(m, review)?.id).toBe(review?.subject);
  });
});

describe('reviewOf', () => {
  it('reads the verdict counts the media result step returned, never counting reviews', () => {
    const r = reviewOf(step('media_result').nodeRun?.output);
    expect(r).toEqual({
      passedCount: 2,
      photoCount: 3,
      notPassed: ['asset_1163e40e4d624c0fbed64bbaf782a349'],
      policyVersion: 'mock-review-policy-v1',
    });
    expect(reviewOf({ caption: 'A plain caption' })).toBeNull();
    expect(reviewOf(null)).toBeNull();
  });
});
