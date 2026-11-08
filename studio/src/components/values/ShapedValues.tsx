import type { ReactNode } from 'react';

import type { JsonValue } from '@/api/types';
import { FactList } from '@/components/facts/FactList';
import { ImageGroup, ImageRow } from '@/components/media/ImageValues';
import { MediaRow, MediaValue, StackedFact } from '@/components/media/MediaValue';
import type { MediaField } from '@/components/media/media-field';
import { resolveMediaUrl } from '@/components/media/media-ref';
import { useMediaSource } from '@/components/media/media-source';
import { asData, imagesOf, reviewIn, useRunImages } from '@/components/media/value-media';
import { useCopy } from '@/lib/copy';
import { fieldTitle, nestedSummary, readableKeys, typeset } from '@/lib/value-reading';
import { Scalar } from './Scalar';
import { READABLE } from './value-layout';

type Obj = Record<string, JsonValue>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

const LONG = 80;
const isLong = (v: JsonValue) => typeof v === 'string' && (v.length > LONG || v.includes('\n'));

/**
 * Any object laid out by shape. Media comes first as a thumbnail with the short facts beside
 * it, long text follows full width as titled paragraphs, and nested data reads as one summary
 * line. Technical values (digests, prefixed ids) and a field already shown as media are left
 * out; they stay in the Data tab. A media field the resolver cannot place keeps its place as a
 * fact, shown as the reference it is. `compact`, for a card, keeps the media and long text
 * and lists short values only when there is nothing else to show.
 */
export function ShapedValues({
  schema,
  data: raw,
  compact = false,
  lead = null,
  wide = false,
  omit = [],
  heading,
}: {
  schema: Obj | null;
  data: Obj;
  compact?: boolean;
  /** Shown first, full width: the report's status. */
  lead?: ReactNode;
  /** A wide container: a larger thumbnail and a readable width cap. */
  wide?: boolean;
  /** Keys the page already shows elsewhere, such as a field the request carries unchanged. */
  omit?: readonly string[];
  /** The section heading above; a long text titled the same shows without its label. */
  heading?: string;
}) {
  const copy = useCopy();
  const run = useRunImages();
  const source = useMediaSource();
  // JSON text reads as the data it holds.
  const data: Obj = Object.fromEntries(
    Object.entries(raw).map(([k, v]) => [k, asData(v) as JsonValue]),
  );
  const props = (schema?.properties ?? {}) as Record<string, Obj>;
  const all = Object.keys(props).length ? Object.keys(props) : Object.keys(data);
  const present = all.filter((k) => data[k] !== null && data[k] !== undefined && data[k] !== '');
  const title = (k: string) => fieldTitle(k, data[k], props[k]?.title);
  const mediaTypeOf = (k: string): string | null => {
    const t = props[k]?.contentMediaType;
    return typeof t === 'string' && typeof data[k] === 'string' ? t : null;
  };
  const mediaKeys = present.filter((k) => mediaTypeOf(k) !== null);
  const mediaKey = mediaKeys.find((k) =>
    resolveMediaUrl(data[k] as string, mediaTypeOf(k) ?? '', source),
  );
  const media: MediaField | null = mediaKey
    ? {
        key: mediaKey,
        title: title(mediaKey),
        value: data[mediaKey] as string,
        mediaType: mediaTypeOf(mediaKey) ?? '',
      }
    : null;
  // A media reference that does not resolve stays visible as the reference it is.
  const references = mediaKeys.filter((k) => k !== mediaKey && !omit.includes(k));
  const candidates = present.filter(
    (k) => k !== mediaKey && !omit.includes(k) && !references.includes(k),
  );
  // Images are found before the technical filter: an image may be named by an ID alone.
  const images = new Map(
    candidates.flatMap((k) => {
      const found = imagesOf(run, raw[k]);
      return found ? [[k, found] as const] : [];
    }),
  );
  const reviews = new Map(
    candidates.flatMap((k) => {
      const found = images.has(k) ? null : reviewIn(run, raw[k]);
      return found ? [[k, found] as const] : [];
    }),
  );
  const rest = readableKeys(
    data,
    candidates.filter((k) => !images.has(k) && !reviews.has(k)),
  );
  const short = rest.filter(
    (k) => !isObj(data[k]) && !Array.isArray(data[k]) && !isLong(data[k] as JsonValue),
  );
  const long = rest.filter((k) => isLong(data[k] as JsonValue));
  const nested = rest.filter((k) => isObj(data[k]) || Array.isArray(data[k]));
  const reference = (k: string): ReactNode => {
    const v = data[k];
    return (
      <code className="font-mono text-xs wrap-anywhere">
        {typeof v === 'string' ? v : JSON.stringify(v)}
      </code>
    );
  };
  if (compact) {
    return (
      <div className="flex flex-col gap-1.5">
        {media ? <MediaValue media={media} caption="" /> : null}
        {long.map((k) => (
          <div key={k} className="flex flex-col gap-0.5">
            <span className="text-sm text-fg-muted">{title(k)}</span>
            <p className="m-0 text-sm text-fg-secondary">{typeset(data[k] as string)}</p>
          </div>
        ))}
        {!media && !long.length && short.length ? (
          <dl
            style={{ gridTemplateColumns: 'auto minmax(0, 1fr)' }}
            className="m-0 grid gap-x-3 gap-y-0.5 text-sm"
          >
            {short.map((k) => (
              <div key={k} className="contents">
                <dt className="text-fg-muted">{title(k)}</dt>
                <dd className="m-0 min-w-0 wrap-anywhere text-fg-secondary">
                  <Scalar v={data[k] as JsonValue} k={k} />
                </dd>
              </div>
            ))}
          </dl>
        ) : null}
      </div>
    );
  }
  const facts: { key: string; label: string; value: ReactNode }[] = [
    ...references.map((k) => ({ key: k, label: title(k), value: reference(k) })),
    ...short.map((k) => ({
      key: k,
      label: title(k),
      value: <Scalar v={data[k] as JsonValue} k={k} />,
    })),
    ...nested.map((k) => ({ key: k, label: title(k), value: nestedSummary(data[k] as JsonValue) })),
    ...[...reviews].map(([k, r]) => ({
      key: k,
      label: copy.values.passed,
      value: copy.values.passedPhotos(r.passedCount, r.photoCount),
    })),
  ];
  const single = [...images].find(([, v]) => !v.list);
  const lists = [...images].filter(([k]) => k !== single?.[0]);
  const texts = long.map((k) => (
    <div key={k} className="flex flex-col gap-1">
      {title(k) === heading ? null : <span className="text-sm text-fg-muted">{title(k)}</span>}
      <p className="m-0 text-sm whitespace-pre-wrap text-fg">{typeset(data[k] as string)}</p>
    </div>
  ));
  const stacked = facts.map((f) => <StackedFact key={f.key} label={f.label} value={f.value} />);
  const first = single?.[1].items[0];
  return (
    <div style={wide ? { maxWidth: READABLE } : undefined} className="flex flex-col gap-4">
      {lead}
      {media ? (
        <MediaRow media={media} wide={wide}>
          {stacked}
        </MediaRow>
      ) : single && first ? (
        <ImageRow item={first} label={title(single[0])} wide={wide}>
          {stacked}
        </ImageRow>
      ) : facts.length ? (
        <FactList compact facts={facts.map((f) => ({ label: f.label, value: f.value }))} />
      ) : null}
      {lists.map(([k, v]) => (
        <ImageGroup key={k} title={title(k)} items={v.items} />
      ))}
      {[...reviews].map(([k, r]) => (
        <ImageGroup key={k} title={copy.values.didNotPass} items={r.notPassed} small />
      ))}
      {texts}
    </div>
  );
}
