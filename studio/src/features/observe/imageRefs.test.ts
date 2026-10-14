import { describe, expect, it } from 'vitest';

import { artifactPreviewUri, collectImageRefs, externalPreviewUri } from './imageRefs';

const external = { source: 'EXTERNAL', uri: 'https://images.example/a.png' } as const;
const artifact = {
  source: 'ARTIFACT',
  artifact: {
    artifactId: `artifact_${'a'.repeat(32)}`,
    mediaType: 'image/png',
    sizeBytes: 10,
    sha256: 'x',
  },
} as const;

describe('collectImageRefs', () => {
  it('finds references at the top level or one port deep, singly or as a list', () => {
    expect(collectImageRefs(external)).toEqual([external]);
    expect(collectImageRefs({ image: external, caption: 'x' })).toEqual([external]);
    expect(collectImageRefs({ photos: [external, artifact, 'x'] })).toEqual([external, artifact]);
    expect(collectImageRefs({ deep: { image: external } })).toEqual([]);
    expect(collectImageRefs('text')).toEqual([]);
  });
});

describe('preview addresses', () => {
  it('previews an outside image only from an absolute http or https address', () => {
    expect(externalPreviewUri('https://images.example/a.png')).toBe('https://images.example/a.png');
    expect(externalPreviewUri('javascript:alert(1)')).toBeNull();
    expect(externalPreviewUri('data:image/png;base64,AAAA')).toBeNull();
    expect(externalPreviewUri('/relative.png')).toBeNull();
  });

  it('serves an artifact only through its own run and only as an image', () => {
    expect(artifactPreviewUri(artifact, 'run_1')).toMatch(
      /\/runs\/run_1\/artifacts\/artifact_a+\/content$/,
    );
    expect(artifactPreviewUri(artifact, null)).toBeNull();
    expect(
      artifactPreviewUri(
        { ...artifact, artifact: { ...artifact.artifact, mediaType: 'text/html' } },
        'run_1',
      ),
    ).toBeNull();
  });
});
