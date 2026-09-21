import type { JsonSchema, UiSchema, UiSchemaField } from '@/api/types';

export type FieldWidget = 'text' | 'textarea' | 'number' | 'checkbox' | 'select';

export interface ResolvedField {
  path: string;
  schema: JsonSchema;
  label: string;
  required: boolean;
  widget: FieldWidget;
  group: string;
  enumValues: string[];
}

const DEFAULT_GROUP = 'Basic';

function humanize(path: string): string {
  const last = path.split('.').pop() ?? path;
  return last
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/[_-]+/g, ' ')
    .replace(/^./, (c) => c.toUpperCase());
}

/**
 * Chooses the control for one property.
 *
 * `configSchema` decides the value contract, `uiSchema` only decides presentation. A
 * uiSchema widget may therefore refine the control (TEXTAREA instead of a single line)
 * but never override the schema's type, enum or required set.
 */
function resolveWidget(schema: JsonSchema, uiWidget: string | undefined): FieldWidget {
  if (schema.enum && schema.enum.length > 0) return 'select';

  const widget = (uiWidget ?? '').toUpperCase();
  if (widget === 'TEXTAREA' || widget === 'PROMPT') return 'textarea';

  switch (schema.type) {
    case 'boolean':
      return 'checkbox';
    case 'number':
    case 'integer':
      return 'number';
    default:
      return 'text';
  }
}

/**
 * Orders the Properties Panel fields.
 *
 * Fields named by `uiSchema` come first in `order`. Anything the uiSchema omits keeps its
 * schema order and lands at the end of the Basic group.
 */
export function resolveFields(
  configSchema: JsonSchema | undefined,
  uiSchema: UiSchema | undefined,
): ResolvedField[] {
  const properties = configSchema?.properties ?? {};
  const required = new Set(configSchema?.required ?? []);

  const uiByPath = new Map<string, UiSchemaField>();
  for (const field of uiSchema?.fields ?? []) {
    uiByPath.set(field.path, field);
  }

  const described: { field: ResolvedField; order: number; declared: boolean }[] = [];
  let fallbackOrder = 0;

  for (const [path, schema] of Object.entries(properties)) {
    const ui = uiByPath.get(path);
    const enumValues = (schema.enum ?? []).map((value) => String(value));

    described.push({
      declared: ui !== undefined,
      order: ui?.order ?? fallbackOrder++,
      field: {
        path,
        schema,
        label: schema.title ?? humanize(path),
        required: required.has(path),
        widget: resolveWidget(schema, ui?.widget),
        group: ui?.group ?? DEFAULT_GROUP,
        enumValues,
      },
    });
  }

  return described
    .sort((a, b) => {
      if (a.declared !== b.declared) return a.declared ? -1 : 1;
      return a.order - b.order;
    })
    .map((entry) => entry.field);
}

export function groupFields(fields: ResolvedField[]): [string, ResolvedField[]][] {
  const groups = new Map<string, ResolvedField[]>();
  for (const field of fields) {
    const existing = groups.get(field.group);
    if (existing) existing.push(field);
    else groups.set(field.group, [field]);
  }
  return [...groups.entries()];
}
