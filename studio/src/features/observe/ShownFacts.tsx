import type { AgentTraceMedia, ShownToolField } from '@/api/agent-types';
import type { JsonValue } from '@/api/types';
import { FactList } from '@/components/facts/FactList';
import { mediaKindOf } from '@/components/media/media-field';
import { resolveMediaUrl } from '@/components/media/media-ref';
import { Scalar } from '@/components/values/Scalar';
import { useCopy } from '@/lib/copy';
import { nestedSummary } from '@/lib/value-reading';

const LONG = 80;

/**
 * The members a Tool's registration declares for its run card, each under its declared
 * title, for any Tool. A value the trace left out reads as a plain "not shown" fact. A
 * string value shows as the media it names only when the trace's media list or the run
 * input serves it; otherwise it stays text, never a guessed address.
 */
export function ShownFacts({
  fields,
  media,
  runInput,
}: {
  fields: readonly ShownToolField[];
  media: AgentTraceMedia | null;
  runInput: unknown;
}) {
  const m = useCopy().runs.media;
  if (!fields.length) return null;
  const ctx = { media, runInput };
  const valueOf = (f: ShownToolField) => {
    if (f.withheld) {
      return <span className="text-fg-muted">{m.notShown[f.withheld] ?? m.notShownPlain}</span>;
    }
    const v = f.value as JsonValue | undefined;
    if (v === undefined || v === null || v === '') return null;
    if (typeof v === 'string') {
      const found = resolveMediaUrl(v, null, ctx);
      const kind = found ? mediaKindOf(found.mediaType) : 'other';
      if (found && kind === 'video') {
        return (
          <video
            controls
            preload="none"
            src={found.url}
            className="w-full max-w-full rounded-md border border-border bg-surface-inset"
          />
        );
      }
      if (found && kind === 'image') {
        return (
          <img
            src={found.url}
            alt={f.title}
            draggable={false}
            style={{ maxHeight: 240 }}
            className="max-w-full rounded-md border border-border-subtle"
          />
        );
      }
      if (v.length > LONG || v.includes('\n')) {
        return <span className="whitespace-pre-wrap">{v}</span>;
      }
      return <Scalar v={v} />;
    }
    if (typeof v === 'object') return nestedSummary(v);
    return <Scalar v={v} />;
  };
  return <FactList compact facts={fields.map((f) => ({ label: f.title, value: valueOf(f) }))} />;
}
