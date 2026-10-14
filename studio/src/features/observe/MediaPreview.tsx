import {
  artifactPreviewUri,
  assetPreviewUri,
  collectImageRefs,
  externalPreviewUri,
} from './imageRefs';
import type { ImageRef, JsonValue } from '@/api/types';
import { useCopy } from '@/lib/copy';

function previewSrc(image: ImageRef, runId: string): string | null {
  if (image.source === 'EXTERNAL') return externalPreviewUri(image.uri);
  if (image.source === 'ASSET') return assetPreviewUri(image);
  return artifactPreviewUri(image, runId);
}

function referenceOf(image: ImageRef): string {
  if (image.source === 'EXTERNAL') return image.uri ?? '';
  if (image.source === 'ASSET') return image.asset?.assetId ?? '';
  return image.artifact?.artifactId ?? '';
}

/**
 * The images an input or output names, above its JSON. Each loads only from the route that
 * serves its kind of reference: an http(s) address as given, an uploaded asset's content, or
 * an artifact through this run's own route. A reference no route serves shows its text only.
 */
export function MediaPreview({
  value,
  runId,
}: {
  value: JsonValue | null | undefined;
  runId: string;
}) {
  const copy = useCopy();
  const m = copy.runs.details.media;
  const images = collectImageRefs(value);
  if (!images.length) return null;
  return (
    <section aria-label={m.label} className="mb-4 flex flex-wrap gap-3">
      {images.map((image, i) => {
        const src = previewSrc(image, runId);
        const what = m.source[image.source] ?? m.label;
        const reference = referenceOf(image);
        return (
          <figure
            key={`${reference}-${String(i)}`}
            className="m-0 flex flex-col gap-1"
            style={{ width: 160 }}
          >
            {src ? (
              <span
                style={{ width: 160, height: 160 }}
                className="flex items-center justify-center rounded-lg border border-border bg-checker"
              >
                <img
                  src={src}
                  alt={m.alt(what)}
                  referrerPolicy="no-referrer"
                  className="size-full rounded-lg object-contain"
                />
              </span>
            ) : (
              <span className="text-sm text-fg-muted">{m.noPreview}</span>
            )}
            <figcaption className="flex flex-col text-xs text-fg-secondary">
              <span>{what}</span>
              {reference ? (
                <code className="font-mono wrap-anywhere text-fg-muted">{reference}</code>
              ) : null}
            </figcaption>
          </figure>
        );
      })}
    </section>
  );
}
