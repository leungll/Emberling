import type { JsonSchema, JsonValue } from '@/api/types';
import { copy } from '@/lib/copy';

/**
 * The fields of an object JSON Schema as a person reads them: in a stable order, labelled and
 * typed. Shared by the Run dialog, which asks for the values, and the Editor, which describes
 * the same fields.
 *
 * Saved Definitions come back from PostgreSQL JSONB, which reorders object keys (shorter keys
 * first), so the order of `properties` carries no meaning. The `required` array keeps its
 * order, so required fields come first in that order and the rest follow alphabetically.
 */
export type SchemaControl = 'text' | 'longtext' | 'choice' | 'number' | 'bool' | 'json';

export interface SchemaField {
  key: string;
  label: string;
  description?: string;
  required: boolean;
  control: SchemaControl;
  /** A whole number only. */
  integer: boolean;
  /** The values a choice offers, from the schema's string enum. */
  options: string[];
  schema: JsonSchema;
}

/** Text that may be longer than this gets a box that grows with it. */
export const LONG_TEXT_AT = 200;

const w = copy.schemaFields;

/** "diff_text" reads "Diff text", "maxRetries" reads "Max retries". */
export function humanKey(key: string): string {
  const t = key
    .replace(/[_-]+/g, ' ')
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .toLowerCase()
    .trim();
  return t.charAt(0).toUpperCase() + t.slice(1);
}

function propertiesOf(schema: JsonSchema | undefined): Record<string, JsonSchema> {
  const p = schema?.properties;
  return p && typeof p === 'object' && !Array.isArray(p) ? p : {};
}

/** An object schema (or one without a type) that names at least one property. */
export function hasFieldList(schema: JsonSchema | undefined): boolean {
  if (!schema || (schema.type !== undefined && schema.type !== 'object')) return false;
  return Object.keys(propertiesOf(schema)).length > 0;
}

/** Required keys in their listed order, then the other keys alphabetically. */
export function orderedKeys(schema: JsonSchema | undefined): string[] {
  const props = propertiesOf(schema);
  const required = Array.isArray(schema?.required) ? schema.required : [];
  const first = required.filter((k, i) => k in props && required.indexOf(k) === i);
  const rest = Object.keys(props)
    .filter((k) => !first.includes(k))
    .sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
  return [...first, ...rest];
}

function controlOf(s: JsonSchema, options: readonly string[]): SchemaControl {
  if (s.type === 'string') {
    if (options.length > 0) return 'choice';
    return typeof s.maxLength === 'number' && s.maxLength > LONG_TEXT_AT ? 'longtext' : 'text';
  }
  if (s.type === 'integer' || s.type === 'number') return 'number';
  if (s.type === 'boolean') return 'bool';
  return 'json';
}

/** The fields of an object schema, in the order the form and the field list show them. */
export function schemaFields(schema: JsonSchema | undefined): SchemaField[] {
  const props = propertiesOf(schema);
  const required = new Set(Array.isArray(schema?.required) ? schema.required : []);
  return orderedKeys(schema).map((key) => {
    const s = props[key] ?? {};
    const enumValues = Array.isArray(s.enum) ? s.enum : [];
    const options =
      enumValues.length > 0 && enumValues.every((v) => typeof v === 'string') ? enumValues : [];
    const field: SchemaField = {
      key,
      label: typeof s.title === 'string' && s.title.trim() ? s.title : humanKey(key),
      required: required.has(key),
      control: controlOf(s, options),
      integer: s.type === 'integer',
      options: s.type === 'string' ? options : [],
      schema: s,
    };
    if (typeof s.description === 'string' && s.description.trim()) {
      field.description = s.description;
    }
    return field;
  });
}

/** The value a new form starts a field with: empty, unticked, or a one-value choice chosen. */
export function startValue(f: SchemaField): string | boolean {
  if (f.control === 'bool') return false;
  return f.control === 'choice' && f.options.length === 1 ? (f.options[0] ?? '') : '';
}

/** What kind of value a field takes, with its limits, in a few words. */
export function typeWord(f: SchemaField): string {
  const s = f.schema;
  switch (f.control) {
    case 'choice':
      return w.oneOf(f.options);
    case 'text':
    case 'longtext':
      return w.text(s.minLength, s.maxLength);
    case 'number':
      return w.number(f.integer, s.minimum, s.maximum);
    case 'bool':
      return w.yesNo;
    case 'json':
      return s.type === 'object'
        ? w.object
        : s.type === 'array'
          ? w.list
          : s.type === 'null'
            ? w.empty
            : w.anyValue;
  }
}

function placeholder(s: JsonSchema): JsonValue {
  if (Array.isArray(s.enum) && s.enum.length === 1) return s.enum[0] ?? null;
  switch (s.type) {
    case 'string':
      return '…';
    case 'integer':
    case 'number':
      return 0;
    case 'boolean':
      return false;
    case 'array':
      return [];
    case 'null':
      return null;
    case 'object':
      return exampleObject(s);
    default:
      return hasFieldList(s) ? exampleObject(s) : '…';
  }
}

/**
 * An object with every required field filled by a typed placeholder: "…" for text, the value
 * of a one-value choice, 0 for numbers and false for yes/no. Apart from the placeholder
 * values it has the shape the schema asks for.
 */
export function exampleObject(schema: JsonSchema | undefined): Record<string, JsonValue> {
  const props = propertiesOf(schema);
  const out: Record<string, JsonValue> = {};
  for (const f of schemaFields(schema)) {
    if (f.required) out[f.key] = placeholder(props[f.key] ?? {});
  }
  return out;
}
