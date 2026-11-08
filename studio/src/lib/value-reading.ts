import type { JsonValue } from '@/api/types';
import { bytes } from '@/lib/time';

/** Curly quotes for prose typed with straight ones. */
export function typeset(text: string): string {
  return text
    .replace(/(^|[\s([])'/g, '$1\u2018')
    .replace(/'/g, '\u2019')
    .replace(/(^|[\s([])"/g, '$1\u201C')
    .replace(/"/g, '\u201D');
}

/**
 * The reading rules an Overview applies to any value, by its shape and its key alone. A
 * technical value (a digest, a prefixed id) is left to the Data tab; a label comes from the
 * schema title or the key in words; a whole number under a `…Ms` key is a duration and under a
 * `…Bytes` key a size; nested data reads as one summary line. These rules only decide how a
 * value is shown. They never route data or decide what a field means to the Runtime.
 */

type Obj = Record<string, JsonValue>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

/** "clipSha256" → "Clip sha256", "CANNOT_SATISFY" → "Cannot satisfy". */
export function humanize(key: string): string {
  const s = key
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/[_-]+/g, ' ')
    .toLowerCase()
    .trim();
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** A whole number of milliseconds as a clock: "4:27", "1:02:05", or "27 ms" under a second. */
export function msText(ms: number): string {
  if (ms < 1000) return `${String(ms)} ms`;
  const t = Math.round(ms / 1000);
  const h = Math.floor(t / 3600);
  const m = Math.floor((t % 3600) / 60);
  const sec = String(t % 60).padStart(2, '0');
  return h ? `${String(h)}:${String(m).padStart(2, '0')}:${sec}` : `${String(m)}:${sec}`;
}

/** A digest or a prefixed id: one unbroken token of 16+ characters carrying hex. */
export function isTechnical(v: JsonValue | undefined): boolean {
  if (typeof v !== 'string' || v.length < 16 || /\s/.test(v)) return false;
  return /[0-9a-f]{12,}/i.test(v) || /^[a-z]+_[0-9a-f]{8,}/.test(v);
}

/** By the `…Ms` naming convention a whole number holds milliseconds. */
export const isMsKey = (k: string | undefined): boolean => !!k && /[a-z0-9]Ms$/.test(k);

export function isDuration(k: string, v: JsonValue | undefined): boolean {
  return isMsKey(k) && typeof v === 'number' && Number.isInteger(v) && v >= 0;
}

/** By the `…Bytes` naming convention a whole number holds a size in bytes. */
export function isByteSize(k: string | undefined, v: JsonValue | undefined): boolean {
  return !!k && /[a-z0-9]Bytes$/.test(k) && typeof v === 'number' && Number.isInteger(v) && v >= 0;
}

/** An enum constant such as CANNOT_SATISFY. */
export const isEnumWord = (v: string): boolean => /^[A-Z][A-Z0-9_]+$/.test(v);

/** A short value in words: a duration or a size by its key, an enum constant, yes or no. */
export function scalarText(v: JsonValue | undefined, k?: string): string {
  if (typeof v === 'number' && isDuration(k ?? '', v)) return msText(v);
  if (typeof v === 'number' && isByteSize(k, v)) return bytes(v);
  if (typeof v === 'number') return v.toLocaleString('en-US');
  if (typeof v === 'boolean') return v ? 'Yes' : 'No';
  if (typeof v === 'string') return isEnumWord(v) ? humanize(v) : v;
  return '';
}

/** A field's label: its schema title, else its key in words without a unit suffix. */
export function fieldTitle(k: string, v: JsonValue | undefined, schemaTitle?: unknown): string {
  if (typeof schemaTitle === 'string' && schemaTitle) return schemaTitle;
  let t = humanize(k);
  if (isDuration(k, v)) t = t.replace(/ ms$/, '');
  if (isByteSize(k, v)) t = t.replace(/ bytes$/, '');
  t = t.replace(/ sha256$/i, '');
  return t;
}

/** The fields an Overview may show: present, and not technical. */
export function readableKeys(data: Obj, keys: readonly string[]): string[] {
  return keys.filter((k) => {
    const v = data[k];
    return v !== null && v !== undefined && v !== '' && !isTechnical(v);
  });
}

const count = (n: number, one: string, many: string) =>
  `${n.toLocaleString('en-US')} ${n === 1 ? one : many}`;

/** How many facts a nested object's summary line names. */
const SUMMARY_FACTS = 3;

/**
 * One line for nested data: an object names its first few short, readable facts ("Language
 * en, Line count 36, Size 3.8 KB"); a list, or an object with none, says how many it holds.
 */
export function nestedSummary(v: JsonValue): string {
  if (Array.isArray(v)) return count(v.length, 'item', 'items');
  if (!isObj(v)) return '';
  const keys = readableKeys(v, Object.keys(v));
  const facts = keys
    .filter((k) => {
      const x = v[k];
      return (
        typeof x === 'number' || typeof x === 'boolean' || (typeof x === 'string' && x.length <= 40)
      );
    })
    .slice(0, SUMMARY_FACTS)
    .map((k) => `${fieldTitle(k, v[k])} ${scalarText(v[k], k)}`);
  return facts.length ? facts.join(', ') : count(keys.length, 'field', 'fields');
}

export function deepEqual(a: JsonValue | undefined, b: JsonValue | undefined): boolean {
  if (a === b) return true;
  if (typeof a !== 'object' || typeof b !== 'object' || a === null || b === null) return false;
  if (Array.isArray(a) !== Array.isArray(b)) return false;
  const ka = Object.keys(a);
  const kb = Object.keys(b);
  if (ka.length !== kb.length) return false;
  return ka.every((k) => deepEqual((a as Obj)[k], (b as Obj)[k]));
}
