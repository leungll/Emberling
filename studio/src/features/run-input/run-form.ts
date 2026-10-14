import { ApiRequestError } from '@/api/client';
import type { AssetRef, JsonObject, JsonSchema, JsonValue, RunInputSchema } from '@/api/types';
import { copy } from '@/lib/copy';

/**
 * The Run dialog's form as pure functions: fields read from the bound version's frozen
 * runInputSchema, the values a form holds, their client-side checks and the run input
 * they make. The checks are for responsiveness only; the Backend validates the input and
 * is the final authority.
 */
export type FieldKind = 'text' | 'number' | 'bool' | 'json' | 'asset' | 'photos';

export interface RunField {
  key: string;
  label: string;
  required: boolean;
  kind: FieldKind;
  schema: JsonSchema;
  /** Accepted image media types, from the schema's mediaType enum. */
  types?: string[];
  maxBytes?: number;
  minItems?: number;
  maxItems?: number;
}

/** Text, number and JSON fields hold their text; images hold completed uploads only. */
export type FieldValue = string | boolean | AssetRef | AssetRef[] | null;
export type FormValues = Record<string, FieldValue>;

const r = copy.runDialog;

/** "diff_text" reads "Diff text", "maxRetries" reads "Max retries". */
export function humanKey(key: string): string {
  const t = key
    .replace(/[_-]+/g, ' ')
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .toLowerCase()
    .trim();
  return t.charAt(0).toUpperCase() + t.slice(1);
}

function isAssetRefSchema(schema: JsonSchema | undefined): boolean {
  return schema?.type === 'object' && schema.properties?.assetId !== undefined;
}

/**
 * An AssetRef value carries exactly the immutable reference fields. A prior run's input
 * has already been through Backend validation, so a shape check is enough to recognise it.
 */
export function isAssetRef(value: unknown): value is AssetRef {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return false;
  const c = value as Record<string, unknown>;
  return (
    typeof c.assetId === 'string' &&
    typeof c.mediaType === 'string' &&
    typeof c.sizeBytes === 'number' &&
    typeof c.sha256 === 'string'
  );
}

function kindOf(schema: JsonSchema): FieldKind {
  if (isAssetRefSchema(schema)) return 'asset';
  if (schema.type === 'array' && isAssetRefSchema(schema.items)) return 'photos';
  if (schema.type === 'string') return 'text';
  if (schema.type === 'number' || schema.type === 'integer') return 'number';
  if (schema.type === 'boolean') return 'bool';
  // An object or array of values, or a property with no declared type.
  return schema.type === undefined ? 'text' : 'json';
}

/** The fields of a run form; labels come from the Input steps that declare them. */
export function runFields(
  schema: RunInputSchema | undefined,
  labels: Readonly<Record<string, string>>,
): RunField[] {
  const required = new Set(schema?.required ?? []);
  return Object.entries(schema?.properties ?? {}).map(([key, s]) => {
    const kind = kindOf(s);
    const item = kind === 'photos' ? s.items : s;
    const mediaType = item?.properties?.mediaType;
    const types = mediaType?.enum?.filter((t): t is string => typeof t === 'string');
    const field: RunField = {
      key,
      label: labels[key] ?? s.title ?? humanKey(key),
      required: required.has(key),
      kind,
      schema: s,
    };
    if (types && types.length > 0) field.types = types;
    const maxBytes = item?.properties?.sizeBytes?.maximum;
    if (typeof maxBytes === 'number') field.maxBytes = maxBytes;
    if (kind === 'photos') {
      if (s.minItems !== undefined) field.minItems = s.minItems;
      if (s.maxItems !== undefined) field.maxItems = s.maxItems;
    }
    return field;
  });
}

/** The form's values from a prior run's input (Run again), or empty ones. */
export function seedValues(fields: readonly RunField[], input?: JsonObject | null): FormValues {
  const values: FormValues = {};
  for (const f of fields) {
    const v = input?.[f.key];
    switch (f.kind) {
      case 'photos':
        values[f.key] = Array.isArray(v) && v.every(isAssetRef) ? (v as unknown as AssetRef[]) : [];
        break;
      case 'asset':
        values[f.key] = isAssetRef(v) ? v : null;
        break;
      case 'bool':
        values[f.key] = v === true;
        break;
      case 'number':
        values[f.key] = typeof v === 'number' ? String(v) : '';
        break;
      case 'json':
        values[f.key] = v === undefined || v === null ? '' : JSON.stringify(v, null, 2);
        break;
      case 'text':
        values[f.key] = typeof v === 'string' ? v : '';
        break;
    }
  }
  return values;
}

/** A JSON.parse failure as line, column and a plain reason; null when the text is valid. */
export function jsonProblem(text: string): { line: number; col: number; msg: string } | null {
  try {
    JSON.parse(text);
    return null;
  } catch (e) {
    const m = e instanceof Error ? e.message : '';
    const pos = /position (\d+)/.exec(m);
    const at = pos ? Number(pos[1]) : text.length;
    const lc = /line (\d+) column (\d+)/.exec(m);
    let line: number;
    let col: number;
    if (lc) {
      line = Number(lc[1]);
      col = Number(lc[2]);
    } else {
      const before = text.slice(0, at).split('\n');
      line = before.length;
      col = (before.at(-1)?.length ?? 0) + 1;
    }
    let why = m
      .replace(/^JSON\.parse:\s*/, '')
      .replace(/\s*in JSON at position \d+.*$/, '')
      .replace(/\s*\(line \d+ column \d+\)$/, '')
      .replace(/,? "[^"]*"\.\.\. is not valid JSON$/, '');
    if (/end of JSON input|unterminated|end of data/i.test(why)) why = r.jsonEnds;
    why = why.replace(/'([^']*)'/g, '“$1”');
    why = why.charAt(0).toLowerCase() + why.slice(1);
    return { line, col, msg: `${why.replace(/\.$/, '')}.` };
  }
}

/** A JSON object held in a text value, which the form can show as labelled fields. */
export function parseObject(text: string): Record<string, JsonValue> | null {
  const t = text.trim();
  if (!t.startsWith('{')) return null;
  try {
    const v: unknown = JSON.parse(t);
    return v !== null && typeof v === 'object' && !Array.isArray(v)
      ? (v as Record<string, JsonValue>)
      : null;
  } catch {
    return null;
  }
}

/** A copy of `obj` with one value replaced at `path`. */
export function setAtPath(
  obj: Record<string, JsonValue>,
  path: readonly string[],
  value: JsonValue,
): Record<string, JsonValue> {
  const [head, ...rest] = path;
  if (head === undefined) return obj;
  if (rest.length === 0) return { ...obj, [head]: value };
  const child = obj[head];
  const inner =
    child !== null && typeof child === 'object' && !Array.isArray(child)
      ? (child as Record<string, JsonValue>)
      : {};
  return { ...obj, [head]: setAtPath(inner, rest, value) };
}

/** The client-side check of one field; null when it passes. */
export function checkField(f: RunField, v: FieldValue): string | null {
  if (f.kind === 'photos') {
    const n = Array.isArray(v) ? v.length : 0;
    return f.minItems !== undefined && n < f.minItems ? r.photos.shortfall(f.minItems) : null;
  }
  if (f.kind === 'asset') return f.required && !v ? r.image.missing : null;
  if (f.kind === 'bool') return null;
  const s = typeof v === 'string' ? v.trim() : '';
  if (!s) return f.required ? r.isRequired(f.label) : null;
  if (f.kind === 'number') return Number.isFinite(Number(s)) ? null : r.isNumber(f.label);
  if (f.kind === 'json' || s.startsWith('{') || s.startsWith('[')) {
    const p = jsonProblem(s);
    if (p) return r.jsonAt(p.line, p.col, p.msg);
  }
  return null;
}

/** The run input: optional empty fields stay absent, since the Backend injects no defaults. */
export function buildInput(fields: readonly RunField[], values: FormValues): JsonObject {
  const input: JsonObject = {};
  for (const f of fields) {
    const v = values[f.key];
    if (f.kind === 'photos') {
      const refs = Array.isArray(v) ? v : [];
      if (refs.length > 0 || f.required) input[f.key] = refs as unknown as JsonValue[];
    } else if (f.kind === 'asset') {
      if (isAssetRef(v)) input[f.key] = v as unknown as JsonObject;
    } else if (f.kind === 'bool') {
      if (v === true || f.required) input[f.key] = v === true;
    } else {
      const s = typeof v === 'string' ? v : '';
      if (s.trim() === '') continue;
      if (f.kind === 'number') input[f.key] = Number(s);
      else if (f.kind === 'json') input[f.key] = JSON.parse(s) as JsonValue;
      else input[f.key] = s;
    }
  }
  return input;
}

const MEDIA_NAME: Record<string, string> = {
  'image/png': 'PNG',
  'image/jpeg': 'JPEG',
  'image/webp': 'WebP',
  'image/gif': 'GIF',
};

/** "PNG, JPEG or WebP", in a fixed order whatever the schema's order. */
export function mediaList(types: readonly string[]): string {
  const known = Object.keys(MEDIA_NAME).filter((t) => types.includes(t));
  const other = types.filter((t) => !(t in MEDIA_NAME));
  return r.or([...known.map((t) => MEDIA_NAME[t] ?? t), ...other]);
}

/** The count sentence of a photo set, from the schema's own limits. */
export function photoCount(n: number, min: number | undefined, max: number | undefined): string {
  const p = r.photos;
  if (max !== undefined && n >= max) return p.atLimit(n);
  if (n === 0) {
    if (min) return p.addAtLeast(min);
    return max !== undefined ? p.addUpTo(max) : p.none;
  }
  if (min && n < min) return p.shortOf(n, min);
  return max !== undefined ? p.upTo(n, max) : p.added(n);
}

/**
 * Why a run didn't start: the Backend's own message when it answered (a rejected input
 * names the field), otherwise that it didn't respond.
 */
export function runStartError(error: unknown): string {
  return error instanceof ApiRequestError && error.message ? error.message : r.submitFailed;
}
