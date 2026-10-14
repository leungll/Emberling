import type { ReactNode } from 'react';

import type { DetailContext } from './detail-context';
import { cardInfo, type CallSaved } from './card-info';
import {
  factImage,
  imageLabel,
  imagesIn,
  mediaItems,
  photoList,
  reviewOf,
  savedImageOf,
} from './run-media';
import type { RunModel, Step } from './run-model';
import { Words } from './Words';
import type { JsonValue } from '@/api/types';
import { FactList, TechnicalDetails } from '@/components/facts/FactList';
import { JsonViewer } from '@/components/json/JsonViewer';
import type { MediaRef } from '@/components/media/media-ref';
import { Gallery, ThumbSet } from '@/components/media/Thumbs';
import { copy as words, useCopy } from '@/lib/copy';

function Sub({ title, count, children }: { title: string; count?: number; children: ReactNode }) {
  return (
    <section className="flex min-w-0 flex-col gap-2">
      <h3 className="m-0 flex items-center gap-1.5 text-sm font-semibold text-fg">
        {title}
        {count !== undefined ? (
          <span className="font-normal text-fg-muted tabular-nums">{count}</span>
        ) : null}
      </h3>
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

/** A step's images in its Overview: the same ones its card shows, larger. */
export function StepImages({ ctx, step }: { ctx: DetailContext; step: Step }) {
  const copy = useCopy();
  const m = copy.runs.media;
  const info = cardInfo(ctx.model, step);
  if (!info.refs.length) return null;
  const title = info.refsLabel ?? (step.node.type === 'media_brief' ? m.photos : m.images);
  return (
    <Sub title={title} count={info.refs.length}>
      <Gallery items={mediaItems(ctx.model, info.refs)} />
    </Sub>
  );
}

/**
 * A tool call's Result tab. The trace does not carry a call's result yet; what it does
 * carry is shown: what the call saved (the image it generated, a review or test verdict).
 */
export function CallResultView({ ctx, saved }: { ctx: DetailContext; saved: CallSaved }) {
  const copy = useCopy();
  const m = copy.runs.media;
  const f = copy.runs.details.facts2;
  const { facts, result } = saved;
  const images = facts.flatMap((x) => {
    const r = factImage(x);
    return r ? [r] : [];
  });
  const rest = facts.filter((x) => x.factType !== 'image_generated');
  return (
    <>
      {result ? (
        <p className="m-0 text-md text-fg">
          <Words text={m.callResult(result.text)} tools={result.tools} />
        </p>
      ) : null}
      <Gallery items={mediaItems(ctx.model, images)} />
      {rest.length ? (
        <Sub title={m.savedResults}>
          <ul className="m-0 flex list-none flex-col p-0">
            {rest.map((x) => {
              const img = savedImageOf(ctx.model, x);
              return (
                <li
                  key={x.id}
                  className="flex min-h-row-dense flex-wrap items-center gap-2 py-1 text-sm text-fg"
                >
                  {img ? <ThumbSet items={mediaItems(ctx.model, [img])} size="sm" /> : null}
                  <span>{factLabel(x.factType)}</span>
                  {img ? <span className="text-fg-muted">{imageLabel(ctx.model, img)}</span> : null}
                  {x.verdict === true ? (
                    <span className="text-st-succeeded-fg">{f.passed}</span>
                  ) : x.verdict === false ? (
                    <span className="text-st-failed-fg">{f.failed}</span>
                  ) : null}
                </li>
              );
            })}
          </ul>
        </Sub>
      ) : null}
      <p className="m-0 text-sm text-fg-muted">{m.callResultGap}</p>
      {facts.length ? (
        <TechnicalDetails storageKey="json-call" label={copy.media.json}>
          <JsonViewer value={facts} root="savedResults" />
        </TechnicalDetails>
      ) : null}
    </>
  );
}

function factLabel(type: string): string {
  return (words.factLabel as Record<string, string | undefined>)[type] ?? type.replace(/_/g, ' ');
}
