import type { JsonValue } from '@/api/types';
import { MediaValue } from '@/components/media/MediaValue';
import { mediaTypeOf } from '@/components/media/media-field';
import { useMediaSource } from '@/components/media/media-source';
import { Scalar } from '@/components/values/Scalar';
import { findBySchema, schemaObj, type ReportSchema } from '@/lib/agent-report';
import { humanize } from '@/lib/value-reading';

/**
 * An Agent data format shown through what the agent actually wrote in the last run, for any
 * schema and any data. Labels come from each property's `title` and fall back to the property
 * name in plain words; a property's `description` is its tooltip. Nothing here knows a
 * particular workflow.
 */

type Obj = Record<string, JsonValue>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

export function Values({
  schema,
  data,
  nested = false,
}: {
  schema: ReportSchema | null;
  data: Obj;
  nested?: boolean;
}) {
  const source = useMediaSource();
  const keys = schema?.properties ? Object.keys(schema.properties) : Object.keys(data);
  const rows = keys.filter((k) => data[k] !== null && data[k] !== undefined);
  return (
    <dl
      className={`m-0 flex flex-col gap-2 text-sm ${nested ? 'mt-1 border-l border-border-subtle pl-3' : ''}`}
    >
      {rows.map((k) => {
        const sub = schema?.properties?.[k] ?? null;
        const v = data[k] as JsonValue;
        const mediaType = mediaTypeOf(v, sub ?? undefined, source);
        return (
          <div key={k} className="flex min-w-0 flex-col">
            <dt title={sub?.description ?? k} className="text-fg-muted">
              {sub?.title ?? humanize(k)}
            </dt>
            <dd className="m-0 min-w-0 text-fg">
              {isObj(v) ? (
                <Values schema={sub} data={v} nested />
              ) : Array.isArray(v) ? (
                <ul className="m-0 flex list-disc flex-col gap-0.5 pl-4">
                  {v.map((x, i) => (
                    <li key={String(i)}>
                      {isObj(x) ? <Values schema={null} data={x} nested /> : <Scalar v={x} />}
                    </li>
                  ))}
                </ul>
              ) : typeof v === 'string' && mediaType ? (
                <MediaValue
                  media={{
                    key: k,
                    title: sub?.title ?? humanize(k),
                    value: v,
                    mediaType,
                  }}
                  height={160}
                  caption=""
                />
              ) : (
                <Scalar v={v} k={k} />
              )}
            </dd>
          </div>
        );
      })}
    </dl>
  );
}

/** The schema's fields when there is no run data to show. */
function Fields({ schema }: { schema: ReportSchema }) {
  return (
    <ul className="m-0 flex list-none flex-col gap-1.5 border-l border-border-subtle p-0 pl-3">
      {Object.entries(schema.properties ?? {}).map(([key, p]) => (
        <li key={key} title={key} className="flex flex-col text-sm">
          <span className="text-fg-secondary">{p.title ?? humanize(key)}</span>
          {p.description ? <span className="text-fg-muted">{p.description}</span> : null}
          {p.properties ? <Fields schema={p} /> : null}
        </li>
      ))}
    </ul>
  );
}

export function AgentFormat({
  value,
  sample,
  sampleLabel,
  empty,
}: {
  value: JsonValue | undefined;
  /** What the agent wrote in the last run, anywhere inside this value. */
  sample: JsonValue | undefined;
  sampleLabel: string;
  empty: string;
}) {
  const schema = schemaObj(value);
  if (!schema) return <p className="m-0 text-sm text-fg-muted">{empty}</p>;
  const data = findBySchema(schema, sample);
  if (!data) return <Fields schema={schema} />;
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-sm text-fg-muted">{sampleLabel}</span>
      <Values schema={schema} data={data} />
    </div>
  );
}
