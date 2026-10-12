import { useContext } from 'react';

import {
  PreviewRunContext,
  artifactPreviewUri,
  assetPreviewUri,
  externalPreviewUri,
} from './imageRefs';
import type { ImageRef } from '@/api/types';

/**
 * `<img>` preview plus the raw reference text: an image shows both a preview and its raw
 * reference. EXTERNAL renders the given http(s) URI directly, never through a proxy; ASSET
 * reuses the existing `/assets/{id}/content` route; ARTIFACT uses the content route of the
 * Run being observed, the only Run allowed to read it. A reference no route serves shows
 * only its raw text — never a guessed or invented URL.
 */
export function ImagePreview({ image }: { image: ImageRef }) {
  const runId = useContext(PreviewRunContext);
  const src =
    image.source === 'EXTERNAL'
      ? externalPreviewUri(image.uri)
      : image.source === 'ASSET'
        ? assetPreviewUri(image)
        : image.source === 'ARTIFACT'
          ? artifactPreviewUri(image, runId)
          : null;
  const reference =
    image.source === 'EXTERNAL'
      ? image.uri
      : image.source === 'ASSET'
        ? image.asset?.assetId
        : image.artifact?.artifactId;

  return (
    <div className="space-y-1">
      {src ? (
        // Non-empty alt text: an ImageRef preview is content, not decoration, and keeps
        // the "img" role for assistive tech and tests alike.
        // A fixed preview box: a tiny image (a 16x16 mock output) is scaled up to stay
        // visible, a large one is scaled down, and both keep their aspect ratio on a
        // checkerboard so transparent pixels read as such.
        <div
          data-testid="image-preview-box"
          className="flex h-40 w-40 items-center justify-center overflow-hidden rounded-lg border border-[var(--border)] bg-[var(--muted)] bg-[repeating-conic-gradient(var(--accent)_0%_25%,transparent_0%_50%)] bg-[length:16px_16px]"
        >
          <img
            src={src}
            alt={`${image.source} image preview`}
            referrerPolicy="no-referrer"
            className="h-full w-full object-contain"
          />
        </div>
      ) : null}
      <p className="text-[13px] text-[var(--muted-foreground)] [overflow-wrap:anywhere]">
        {image.source} · {reference ?? '—'}
      </p>
    </div>
  );
}
