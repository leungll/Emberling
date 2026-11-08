import { ApiRequestError } from '@/api/client';
import type { AssetRef, JsonObject, JsonSchema, JsonValue, RunInputSchema } from '@/api/types';
import { copy } from '@/lib/copy';
import {
  hasFieldList,
  humanKey,
  schemaFields,
  startValue,
  type SchemaField,
} from '@/lib/schema-fields';

export { humanKey };

/**
 * The Run dialog's form as pure functions: fields read from the bound version's frozen
 * runInputSchema, the values a form holds, their client-side checks and the run input
 * they make. The checks are for responsiveness only; the Backend validates the input and
 * is the final authority.
 */
export type FieldKind = 'text' | 'number' | 'bool' | 'json' | 'object' | 'asset' | 'photos';

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
  /** The sub-fields of an object field, in the order the form shows them. */
  fields?: SchemaField[];
}

/**
 * An object field shown as sub-fields: each sub-field's text or checkbox state, and the keys
 * a prior run's value had that the schema doesn't list, which are sent back unchanged.
 */
export interface ObjectDraft {
  values: Record<string, string | boolean>;
  extra: Record<string, JsonValue>;
}

/**
 * Text, number and JSON fields hold their text; images hold completed uploads only. An
 * object field holds an ObjectDraft while it shows sub-fields and its JSON text while it
 * shows JSON.
 */
export type FieldValue = string | boolean | AssetRef | AssetRef[] | ObjectDraft | null;
export type FormValues = Record<string, FieldValue>;

const r = copy.runDialog;

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
  if (schema.type === 'object' && hasFieldList(schema)) return 'object';
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
    if (kind === 'object') field.fields = schemaFields(s);
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
      case 'object':
        values[f.key] = seedObject(f.fields ?? [], isPlainObject(v) ? v : null);
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

function isPlainObject(v: unknown): v is Record<string, JsonValue> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/** Whether an object field's value is its sub-fields rather than its JSON text. */
export function isObjectDraft(v: FieldValue | undefined): v is ObjectDraft {
  if (typeof v !== 'object' || v === null || Array.isArray(v)) return false;
  const d = v as Partial<ObjectDraft>;
  return isPlainObject(d.values) && isPlainObject(d.extra);
}

/** Sub-field values from an object (a prior run's), or empty ones; a one-value choice starts chosen. */
export function seedObject(
  fields: readonly SchemaField[],
  obj: Record<string, JsonValue> | null,
): ObjectDraft {
  const values: Record<string, string | boolean> = {};
  const extra: Record<string, JsonValue> = {};
  for (const sf of fields) {
    const x = obj?.[sf.key];
    if (sf.control === 'bool') {
      values[sf.key] = x === true;
    } else if (x === undefined) {
      values[sf.key] = startValue(sf);
    } else if (typeof x === 'string' && sf.control !== 'json') {
      values[sf.key] = x;
    } else if (typeof x === 'number' && sf.control === 'number') {
      values[sf.key] = String(x);
    } else {
      values[sf.key] = JSON.stringify(x, null, sf.control === 'json' ? 2 : undefined);
    }
  }
  for (const [k, x] of Object.entries(obj ?? {})) {
    if (!fields.some((sf) => sf.key === k)) extra[k] = x;
  }
  return { values, extra };
}

/** Nothing typed or ticked: a one-value choice that starts chosen doesn't count. */
function draftIsEmpty(fields: readonly SchemaField[], d: ObjectDraft): boolean {
  if (Object.keys(d.extra).length > 0) return false;
  return fields.every((sf) => {
    const v = d.values[sf.key];
    if (sf.control === 'bool') return v !== true;
    if (sf.control === 'choice' && sf.options.length === 1) return true;
    return typeof v !== 'string' || v.trim() === '';
  });
}

/**
 * The object the sub-fields make: numbers as numbers, JSON parsed, empty optional sub-fields
 * left out. A value that doesn't read as its type stays text, so the JSON view shows it.
 */
function buildObject(fields: readonly SchemaField[], d: ObjectDraft): Record<string, JsonValue> {
  const out: Record<string, JsonValue> = { ...d.extra };
  for (const sf of fields) {
    const v = d.values[sf.key];
    if (sf.control === 'bool') {
      if (v === true || sf.required) out[sf.key] = v === true;
      continue;
    }
    const s = typeof v === 'string' ? v : '';
    if (s.trim() === '') continue;
    if (sf.control === 'number') {
      const n = Number(s.trim());
      out[sf.key] = Number.isFinite(n) ? n : s;
    } else if (sf.control === 'json') {
      try {
        out[sf.key] = JSON.parse(s) as JsonValue;
      } catch {
        out[sf.key] = s;
      }
    } else {
      out[sf.key] = s;
    }
  }
  return out;
}

function checkSubField(sf: SchemaField, v: string | boolean | undefined): string | null {
  if (sf.control === 'bool') return null;
  const raw = typeof v === 'string' ? v : '';
  const s = raw.trim();
  if (!s) return sf.required ? r.isRequired(sf.label) : null;
  switch (sf.control) {
    case 'number': {
      const n = Number(s);
      if (!Number.isFinite(n)) return r.isNumber(sf.label);
      return sf.integer && !Number.isInteger(n) ? r.isWhole(sf.label) : null;
    }
    case 'choice':
      return sf.options.includes(raw) ? null : r.isOneOf(sf.label, sf.options);
    case 'json': {
      const p = jsonProblem(s);
      return p ? r.jsonAt(p.line, p.col, p.msg) : null;
    }
    case 'text':
    case 'longtext': {
      const n = Array.from(raw).length;
      const { minLength: lo, maxLength: hi } = sf.schema;
      if (lo !== undefined && lo === hi && n !== lo) return r.lengthExactly(sf.label, lo);
      if (lo !== undefined && n < lo) return r.lengthAtLeast(sf.label, lo);
      if (hi !== undefined && n > hi) return r.lengthAtMost(sf.label, hi);
      return null;
    }
  }
}

/** The client-side checks of an object field's sub-fields, keyed by sub-field; empty when all pass. */
export function subFieldErrors(f: RunField, d: ObjectDraft): Record<string, string> {
  const out: Record<string, string> = {};
  const fields = f.fields ?? [];
  if (!f.required && draftIsEmpty(fields, d)) return out;
  for (const sf of fields) {
    const m = checkSubField(sf, d.values[sf.key]);
    if (m) out[sf.key] = m;
  }
  return out;
}

/**
 * An object field's value in the other view: its JSON text, or its sub-fields from that
 * text. Null when the text isn't a JSON object, so it can't be shown as sub-fields.
 */
export function switchObjectView(
  f: RunField,
  v: FieldValue | undefined,
  to: 'fields' | 'json',
): FieldValue | null {
  const fields = f.fields ?? [];
  if (to === 'json') {
    if (!isObjectDraft(v)) return typeof v === 'string' ? v : '';
    return !f.required && draftIsEmpty(fields, v)
      ? ''
      : JSON.stringify(buildObject(fields, v), null, 2);
  }
  if (isObjectDraft(v)) return v;
  const text = typeof v === 'string' ? v : '';
  if (text.trim() === '') return seedObject(fields, null);
  const obj = parseObject(text);
  return obj ? seedObject(fields, obj) : null;
}

/** The client-side check of one field; null when it passes. */
export function checkField(f: RunField, v: FieldValue): string | null {
  if (f.kind === 'object' && isObjectDraft(v)) {
    return Object.values(subFieldErrors(f, v))[0] ?? null;
  }
  if (f.kind === 'photos') {
    const n = Array.isArray(v) ? v.length : 0;
    return f.minItems !== undefined && n < f.minItems ? r.photos.shortfall(f.minItems) : null;
  }
  if (f.kind === 'asset') return f.required && !v ? r.image.missing : null;
  if (f.kind === 'bool') return null;
  const s = typeof v === 'string' ? v.trim() : '';
  if (!s) return f.required ? r.isRequired(f.label) : null;
  if (f.kind === 'number') return Number.isFinite(Number(s)) ? null : r.isNumber(f.label);
  if (f.kind === 'json' || f.kind === 'object' || s.startsWith('{') || s.startsWith('[')) {
    const p = jsonProblem(s);
    if (p) return r.jsonAt(p.line, p.col, p.msg);
  }
  if (f.kind === 'object' && !parseObject(s)) return r.isObject(f.label);
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
    } else if (f.kind === 'object' && isObjectDraft(v)) {
      const fields = f.fields ?? [];
      if (f.required || !draftIsEmpty(fields, v)) input[f.key] = buildObject(fields, v);
    } else {
      const s = typeof v === 'string' ? v : '';
      if (s.trim() === '') continue;
      if (f.kind === 'number') input[f.key] = Number(s);
      else if (f.kind === 'json' || f.kind === 'object') input[f.key] = JSON.parse(s) as JsonValue;
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
