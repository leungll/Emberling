import { describe, expect, it } from 'vitest';

import {
  collectMedia,
  mediaMeta,
  mediaRefOf,
  mediaSrc,
  mediaWord,
  resolveMediaUrl,
  tracedMediaType,
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
  const media = {
    items: [
      {
        ref: ARTIFACT_ID,
        url: '/api/runs/run_1/artifacts/' + ARTIFACT_ID + '/content',
        mediaType: 'image/png',
        toolAttemptId: 'ta_1',
      },
      {
        ref: 'https://media.example/v1/videos/vid_1.mp4',
        url: 'https://media.example/v1/videos/vid_1.mp4',
        mediaType: 'video/mp4',
        toolAttemptId: 'ta_2',
      },
      {
        ref: 'sha256:withheld',
        url: null,
        mediaType: 'video/mp4',
        toolAttemptId: 'ta_3',
        withheld: 'UNSAFE_URL' as const,
      },
    ],
    truncated: false,
  };
  const runInput = {
    brief: 'one warm look',
    photos: [
      { assetId: 'asset_1', mediaType: 'image/jpeg', sizeBytes: 10, sha256: 'y' },
      { assetId: 'sha256:withheld', mediaType: 'image/jpeg', sizeBytes: 10, sha256: 'z' },
    ],
  };

  it('resolves a value through the trace media item whose ref equals it', () => {
    expect(resolveMediaUrl(ARTIFACT_ID, 'image/*', { media, runInput })).toEqual({
      url: '/api/runs/run_1/artifacts/' + ARTIFACT_ID + '/content',
      mediaType: 'image/png',
    });
    expect(resolveMediaUrl('https://media.example/v1/videos/vid_1.mp4', null, { media })).toEqual({
      url: 'https://media.example/v1/videos/vid_1.mp4',
      mediaType: 'video/mp4',
    });
  });

  it('falls back to a Run input asset served by the asset content route', () => {
    expect(resolveMediaUrl('asset_1', 'image/*', { media, runInput })).toEqual({
      url: '/api/assets/asset_1/content',
      mediaType: 'image/jpeg',
    });
    // The asset's own media type is unknown: the caller's hint stands.
    expect(
      resolveMediaUrl('asset_9', 'image/*', { runInput: { photo: { assetId: 'asset_9' } } }),
    ).toEqual({ url: '/api/assets/asset_9/content', mediaType: 'image/*' });
    // A JSON text holding the input is read like the input itself.
    expect(resolveMediaUrl('asset_1', null, { runInput: JSON.stringify(runInput) })).toMatchObject({
      url: '/api/assets/asset_1/content',
    });
  });

  it('resolves nothing for a value no served fact links, and never builds a URL from it', () => {
    expect(
      resolveMediaUrl('sha256:' + 'c'.repeat(64), 'video/mp4', { media, runInput }),
    ).toBeNull();
    expect(resolveMediaUrl(ARTIFACT_ID, 'image/png', {})).toBeNull();
    expect(resolveMediaUrl('', 'image/png', { media, runInput })).toBeNull();
    expect(resolveMediaUrl('/api/assets/asset_1/content', null, { media, runInput })).toBeNull();
  });

  it('treats a trace item whose url was withheld as unresolved rather than falling back', () => {
    expect(resolveMediaUrl('sha256:withheld', 'video/mp4', { media, runInput })).toBeNull();
  });
});

describe('tracedMediaType', () => {
  const media = {
    items: [
      {
        ref: 'sha256:clip',
        url: '/api/runs/run_1/artifacts/a/content',
        mediaType: 'video/mp4',
        toolAttemptId: 'ta_1',
      },
      {
        ref: 'sha256:held',
        url: null,
        mediaType: 'video/mp4',
        toolAttemptId: 'ta_2',
        withheld: 'UNSAFE_URL' as const,
      },
      {
        ref: 'sha256:untyped',
        url: '/api/runs/run_1/artifacts/b/content',
        mediaType: null,
        toolAttemptId: 'ta_3',
      },
    ],
    truncated: false,
  };

  it('reads a value equal to a served trace reference as that media, with no schema', () => {
    expect(tracedMediaType('sha256:clip', { media })).toBe('video/mp4');
    expect(tracedMediaType('sha256:untyped', { media })).toBe('application/octet-stream');
  });

  it('reads nothing for a withheld, unlisted or empty value', () => {
    expect(tracedMediaType('sha256:held', { media })).toBeNull();
    expect(tracedMediaType('sha256:other', { media })).toBeNull();
    expect(tracedMediaType('', { media })).toBeNull();
    expect(tracedMediaType('sha256:clip', {})).toBeNull();
  });
});
