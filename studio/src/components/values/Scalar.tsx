import type { JsonValue } from '@/api/types';
import { bytes } from '@/lib/time';
import { useCopy } from '@/lib/copy';
import { humanize, isByteSize, isEnumWord, isMsKey, msText } from '@/lib/value-reading';

/** How many characters of a content digest or long identifier a value shows. */
const DIGEST_HEAD = 12;

/**
 * One short value in words: a duration or a size by its key's convention, a digest by its
 * start (the whole value in the tooltip), an enum constant in words, yes or no.
 */
export function Scalar({ v, k }: { v: JsonValue; k?: string }) {
  const copy = useCopy();
  if (typeof v === 'number' && Number.isInteger(v) && v >= 0 && isMsKey(k))
    return <span>{msText(v)}</span>;
  if (typeof v === 'number' && isByteSize(k, v)) return <span>{bytes(v)}</span>;
  if (typeof v === 'string') {
    if (/^[0-9a-f]{32,}$/i.test(v))
      return (
        <span title={v} className="font-mono">
          {v.slice(0, DIGEST_HEAD)}…
        </span>
      );
    if (isEnumWord(v)) return <span>{humanize(v)}</span>;
    return <span>{v}</span>;
  }
  if (typeof v === 'boolean') return <span>{v ? copy.values.yes : copy.values.no}</span>;
  if (typeof v === 'number') return <span>{v.toLocaleString('en-US')}</span>;
  return <span>{JSON.stringify(v)}</span>;
}
