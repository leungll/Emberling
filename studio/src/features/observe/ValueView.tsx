import type { ReactNode } from 'react';

import type { DetailContext } from './detail-context';
import { imagesIn, mediaItems, photoList, reviewOf } from './run-media';
import type { RunModel } from './run-model';
import type { JsonValue } from '@/api/types';
import { FactList, TechnicalDetails } from '@/components/facts/FactList';
import { JsonViewer } from '@/components/json/JsonViewer';
import type { MediaRef } from '@/components/media/media-ref';
import { Gallery } from '@/components/media/Thumbs';
import { useCopy } from '@/lib/copy';

function Sub({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex min-w-0 flex-col gap-2">
      <h3 className="m-0 text-sm font-semibold text-fg">{title}</h3>
      {children}
    </section>
  );
}

/** A media result's verdict, from its own passedCount, photoCount and notPassed fields. */
function ReviewBlock({ model, value }: { model: RunModel; value: JsonValue }) {
  const copy = useCopy();
  const m = copy.runs.media;
  const review = reviewOf(value);
  if (!review) return null;
  const photos = photoList(model);
  const notPassed = review.notPassed
    .map((id) => photos.find((p) => p.id === id))
    .filter((p): p is MediaRef => Boolean(p));
  return (
    <Sub title={m.review}>
      <FactList
        compact
        facts={[
          {
            label: m.passed,
            value: (
              <>
                <span className="tabular-nums">{review.passedCount}</span>{' '}
                <span className="text-fg-muted">{m.passedOf(review.photoCount)}</span>
              </>
            ),
          },
          {
            label: m.policy,
            value: review.policyVersion ? (
              <code className="font-mono text-xs">{review.policyVersion}</code>
            ) : (
              <span className="text-fg-muted">{copy.facts.notRecorded}</span>
            ),
          },
        ]}
      />
      {notPassed.length ? (
        <>
          <p className="m-0 text-sm font-medium text-fg-secondary">{m.didNotPass}</p>
          <Gallery items={mediaItems(model, notPassed)} />
        </>
      ) : null}
    </Sub>
  );
}

/**
 * A returned value: its images first, as thumbnails with captions, then the review a media
 * result reported, with the JSON one click away. A value without images keeps the JSON
 * viewer as its main view.
 */
export function ValueView({
  ctx,
  value,
  root,
  storageKey,
}: {
  ctx: DetailContext;
  value: JsonValue | null;
  root: string;
  storageKey: string;
}) {
  const copy = useCopy();
  const refs = imagesIn(ctx.model, value);
  if (!refs.length || value === null) return <JsonViewer value={value} root={root} />;
  return (
    <>
      <Gallery items={mediaItems(ctx.model, refs)} />
      <ReviewBlock model={ctx.model} value={value} />
      <TechnicalDetails storageKey={`json-${storageKey}`} label={copy.media.json}>
        <JsonViewer value={value} root={root} />
      </TechnicalDetails>
    </>
  );
}
