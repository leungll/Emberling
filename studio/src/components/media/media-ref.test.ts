import { describe, expect, it } from 'vitest';

import {
  collectMedia,
  mediaMeta,
  mediaRefOf,
  mediaSrc,
  mediaWord,
  resolveMediaUrl,
} from './media-ref';

const ARTIFACT_ID = `artifact_${'a'.repeat(32)}`;
const external = { source: 'EXTERNAL', uri: 'https://images.example/a.png' } as const;
const artifact = {
  source: 'ARTIFACT',
  artifact: { artifactId: ARTIFACT_ID, mediaType: 'image/png', sizeBytes: 2048, sha256: 'x' },
} as const;
const asset = {
  source: 'ASSET',
  asset: { assetId: 'asset_1', mediaType: 'image/jpeg', sizeBytes: 10, sha256: 'y' },
} as const;

describe('mediaRefOf', () => {
  it('reads every reference shape the API returns', () => {
    expect(mediaRefOf(artifact)).toEqual({
      kind: 'ARTIFACT',
      id: ARTIFACT_ID,
      mediaType: 'image/png',
      sizeBytes: 2048,
    });
    expect(mediaRefOf(asset)).toMatchObject({ kind: 'ASSET', id: 'asset_1' });
    expect(mediaRefOf(external)).toMatchObject({ kind: 'EXTERNAL', id: external.uri });
    // A photo set lists its photos by asset ID; a run input lists uploaded assets.
    expect(mediaRefOf({ photoAssetId: 'asset_2', mediaType: 'image/png' })).toMatchObject({
      kind: 'ASSET',
      id: 'asset_2',
    });
    expect(mediaRefOf({ assetId: 'asset_3', mediaType: 'image/jpeg', sizeBytes: 5 })).toEqual({
      kind: 'ASSET',
      id: 'asset_3',
      mediaType: 'image/jpeg',
      sizeBytes: 5,
    });
    expect(mediaRefOf({ artifactId: ARTIFACT_ID, mediaType: 'image/png' })).toMatchObject({
      kind: 'ARTIFACT',
    });
    expect(mediaRefOf({ photoAssetId: 'asset_2' })).toBeNull();
    expect(mediaRefOf('asset_2')).toBeNull();
  });
});

describe('collectMedia', () => {
  it('finds images at any depth, inside JSON text too, once each and in order', () => {
    const text = JSON.stringify({
      brief: 'x',
      photos: [
        { photoAssetId: 'asset_1', mediaType: 'image/png' },
        { photoAssetId: 'asset_2', mediaType: 'image/png' },
      ],
    });
    expect(collectMedia({ text }).map((r) => r.id)).toEqual(['asset_1', 'asset_2']);
    expect(collectMedia({ image: artifact, again: [artifact, asset] }).map((r) => r.id)).toEqual([
      ARTIFACT_ID,
      'asset_1',
    ]);
  });

  it('leaves out references that are not images and plain text', () => {
    expect(collectMedia({ doc: { assetId: 'asset_9', mediaType: 'application/pdf' } })).toEqual([]);
    expect(collectMedia('A plain answer.')).toEqual([]);
  });
});

describe('mediaSrc', () => {
  it('previews an outside image only from an absolute http or https address', () => {
    const ref = (uri: string) =>
      mediaSrc({ kind: 'EXTERNAL', id: uri, mediaType: null, sizeBytes: null }, 'run_1');
    expect(ref('https://images.example/a.png')).toBe('https://images.example/a.png');
    expect(ref('javascript:alert(1)')).toBeNull();
    expect(ref('data:image/png;base64,AAAA')).toBeNull();
    expect(ref('/relative.png')).toBeNull();
  });

  it('serves an asset through the asset content route', () => {
    expect(mediaSrc(mediaRefOf(asset)!, null)).toMatch(/\/assets\/asset_1\/content$/);
  });

  it('serves an artifact only through its own run and only as an image', () => {
    const ref = mediaRefOf(artifact)!;
    expect(mediaSrc(ref, 'run_1')).toMatch(/\/runs\/run_1\/artifacts\/artifact_a+\/content$/);
    expect(mediaSrc(ref, null)).toBeNull();
    expect(mediaSrc({ ...ref, mediaType: 'text/html' }, 'run_1')).toBeNull();
    expect(mediaSrc({ ...ref, id: 'artifact_../x' }, 'run_1')).toBeNull();
  });
});

describe('words', () => {
  it('names an image by where it came from, with its format and size', () => {
    expect(mediaWord(mediaRefOf(asset)!)).toBe('Uploaded image');
    expect(mediaWord(mediaRefOf(artifact)!)).toBe('Generated image');
    expect(mediaWord(mediaRefOf(external)!)).toBe('Linked image');
    expect(mediaMeta(mediaRefOf(artifact)!)).toBe('PNG, 2 KB');
    expect(mediaMeta(mediaRefOf(external)!)).toBe('');
  });
});

describe('resolveMediaUrl', () => {
  const ctx = (runInput: unknown) => ({ runId: 'run_1', runInput });
  const input = {
    task: { source: { source: 'ASSET', asset: { assetId: 'asset_src', mediaType: 'image/png' } } },
  };

  it('places a value only when the run input carries that exact asset', () => {
    const hit = resolveMediaUrl('asset_src', 'image/png', ctx(input));
    expect(hit?.url).toMatch(/\/assets\/asset_src\/content$/);
    expect(hit?.kind).toBe('image');
    expect(resolveMediaUrl('asset_other', 'image/png', ctx(input))).toBeNull();
    expect(resolveMediaUrl('asset_src', 'image/png', ctx(null))).toBeNull();
    expect(resolveMediaUrl('', 'image/png', ctx(input))).toBeNull();
  });

  it('names the kind from the declared media type', () => {
    expect(resolveMediaUrl('asset_src', 'video/mp4', ctx(input))?.kind).toBe('video');
    expect(resolveMediaUrl('asset_src', 'application/pdf', ctx(input))?.kind).toBe('other');
  });

  it('never guesses an address from the value alone', () => {
    expect(resolveMediaUrl('https://cdn.example/a.mp4', 'video/mp4', ctx(input))).toBeNull();
    expect(resolveMediaUrl('/some/path.mp4', 'video/mp4', ctx(input))).toBeNull();
  });
});
