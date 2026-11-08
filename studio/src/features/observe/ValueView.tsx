import type { ReactNode } from 'react';

import type { DetailContext } from './detail-context';
import { cardInfo, type CallArguments, type CallSaved } from './card-info';
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
import { ShownFacts } from './ShownFacts';
import { Words } from './Words';
import type { JsonValue, ToolReviewTrace, ToolSettingsTrace, WithheldToolField } from '@/api/types';
import { FactList, TechnicalDetails } from '@/components/facts/FactList';
import { JsonViewer } from '@/components/json/JsonViewer';
import { MediaRow } from '@/components/media/MediaValue';
import { mediaTypeOf, type MediaField } from '@/components/media/media-field';
import {
  resolveMediaUrl,
  type MediaRef,
  type MediaResolutionContext,
} from '@/components/media/media-ref';
import { useMediaSource } from '@/components/media/media-source';
import { Gallery, ThumbSet } from '@/components/media/Thumbs';
import { humanize } from '@/lib/value-reading';
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

/** How deep a value is searched for served media: a task object inside the run input. */
const SERVED_DEPTH = 2;

/**
 * The string members of a value that the served media lists or a schema would place as
 * media, each titled by its key; a nested object is searched one level down.
 */
function servedMedia(value: JsonValue | null, source: MediaResolutionContext): MediaField[] {
  const out: MediaField[] = [];
  const walk = (v: JsonValue | null, depth: number) => {
    if (typeof v !== 'object' || v === null || Array.isArray(v) || depth > SERVED_DEPTH) return;
    for (const [key, member] of Object.entries(v)) {
      const mediaType = mediaTypeOf(member, undefined, source);
      if (mediaType && typeof member === 'string' && resolveMediaUrl(member, mediaType, source)) {
        out.push({ key, title: humanize(key), value: member, mediaType });
      } else walk(member, depth + 1);
    }
  };
  walk(value, 0);
  return out;
}

/**
 * A returned value: its images first, as thumbnails with captions, the media the run serves
 * for its values, then the review a media result reported, with the JSON one click away. A
 * value without media keeps the JSON viewer as its main view.
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
  const served = servedMedia(value, useMediaSource());
  if ((!refs.length && !served.length) || value === null) {
    return <JsonViewer value={value} root={root} />;
  }
  return (
    <>
      <Gallery items={mediaItems(ctx.model, refs)} />
      {served.map((f) => (
        <MediaRow key={f.key} media={f} />
      ))}
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

/** Why a member the trace left out is missing, in a sentence. */
function WithheldNotes({ withheld }: { withheld: readonly WithheldToolField[] }) {
  const m = useCopy().runs.media;
  if (!withheld.length) return null;
  return (
    <>
      {withheld.map((w) => {
        const what = m.withheldField[w.field] ?? m.withheldOther(w.field);
        const say = m.withheld[w.reason];
        return (
          <p key={w.field} className="m-0 text-sm text-fg-secondary">
            {say ? say(what) : what}
          </p>
        );
      })}
    </>
  );
}

/** The generation settings a generate_image call asked for, as the trace returns them. */
function SettingsBlock({ settings }: { settings: ToolSettingsTrace }) {
  const m = useCopy().runs.media;
  return (
    <Sub title={m.settings}>
      <FactList
        compact
        facts={[
          settings.prompt !== undefined && {
            label: m.prompt,
            value: <span className="whitespace-pre-wrap">{settings.prompt}</span>,
            note: settings.promptTruncated ? m.promptCut : undefined,
          },
          settings.aspectRatio !== undefined && {
            label: m.aspectRatio,
            value: settings.aspectRatio,
          },
          settings.resolution !== undefined && { label: m.resolution, value: settings.resolution },
        ]}
      />
    </Sub>
  );
}

/** A review's verdict and the reasons it gave. */
function ReviewVerdict({ review }: { review: ToolReviewTrace }) {
  const m = useCopy().runs.media;
  const reasons = review.reasons ?? [];
  return (
    <Sub title={m.review}>
      <FactList
        compact
        facts={[
          {
            label: m.reviewVerdict,
            value: review.passed ? (
              <span className="text-st-succeeded-fg">{m.reviewPassed}</span>
            ) : (
              <span className="text-st-failed-fg">{m.reviewFailed}</span>
            ),
          },
        ]}
      />
      {reasons.length ? (
        <>
          <p className="m-0 text-sm font-medium text-fg-secondary">{m.reviewReasons}</p>
          <ul className="m-0 flex list-disc flex-col gap-1 pl-5 text-sm text-fg">
            {reasons.map((reason, i) => (
              <li key={i}>{reason}</li>
            ))}
          </ul>
        </>
      ) : null}
    </Sub>
  );
}

/**
 * A tool call's Arguments tab. The members the Tool declares for its run card come first,
 * for any Tool; without a declaration the arguments the trace projects by name: the photo
 * a generate_image call works on (placed by the position the trace returns) and the
 * settings it asked for, and a deploy's target.
 */
export function CallArgumentsView({ ctx, args }: { ctx: DetailContext; args: CallArguments }) {
  const copy = useCopy();
  const m = copy.runs.media;
  const { photo, settings, target, withheld, shown, media } = args;
  if (shown.length) {
    return <ShownFacts fields={shown} media={media} runInput={ctx.model.snapshot.run.input} />;
  }
  const ref = photo ? photoList(ctx.model).find((p) => p.id === photo.photoAssetId) : undefined;
  const json: Record<string, JsonValue> = {};
  if (photo) json.photoAssetId = photo.photoAssetId;
  return (
    <>
      {photo ? (
        <Sub
          title={
            photo.position !== undefined && photo.count !== undefined
              ? m.photoOf(photo.position, photo.count)
              : m.photoArg
          }
        >
          {ref ? (
            <Gallery items={mediaItems(ctx.model, [ref])} />
          ) : (
            <p className="m-0 text-sm text-fg-secondary">{m.notInPhotoSet(photo.photoAssetId)}</p>
          )}
        </Sub>
      ) : null}
      {settings ? <SettingsBlock settings={settings} /> : null}
      {target ? (
        <Sub title={m.target}>
          <p className="m-0 text-sm text-fg">{target}</p>
        </Sub>
      ) : null}
      <WithheldNotes withheld={withheld} />
      {photo ? (
        <TechnicalDetails storageKey="json-args" label={copy.media.json}>
          <JsonViewer value={json} root="arguments" />
        </TechnicalDetails>
      ) : null}
    </>
  );
}

/**
 * A tool call's Result tab: the result members the Tool declares for its run card, else the
 * result facts the trace projects by name (a video, a review's verdict and reasons), and
 * what the call saved (the image it generated, a review or test verdict).
 */
export function CallResultView({ ctx, saved }: { ctx: DetailContext; saved: CallSaved }) {
  const copy = useCopy();
  const m = copy.runs.media;
  const f = copy.runs.details.facts2;
  const { facts, result, review, videoUrl, withheld, shown, media } = saved;
  const declared = shown.length > 0;
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
      {declared ? (
        <ShownFacts fields={shown} media={media} runInput={ctx.model.snapshot.run.input} />
      ) : null}
      {videoUrl && !declared ? (
        <Sub title={m.video}>
          <video
            controls
            preload="none"
            src={videoUrl}
            className="w-full max-w-full rounded-md border border-border bg-surface-inset"
          />
        </Sub>
      ) : null}
      {review && !declared ? <ReviewVerdict review={review} /> : null}
      {declared ? null : <WithheldNotes withheld={withheld} />}
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
