import type {
  JsonObject,
  JsonSchema,
  JsonValue,
  ModelMetadata,
  UiSchema,
  UiSchemaField,
} from '@/api/types';

export type FieldWidget =
  | 'text'
  | 'textarea'
  | 'number'
  | 'checkbox'
  | 'select'
  | 'model'
  | 'tools'
  | 'multiselect'
  | 'list'
  | 'json';

export interface ResolvedField {
  path: string;
  schema: JsonSchema;
  label: string;
  required: boolean;
  widget: FieldWidget;
  group: string;
  /** `enum` of the field itself (`select`) or of its array items (`multiselect`). */
  enumValues: string[];
  /** Model capability the Model Selector filters on; set only for `model`. */
  capability: string | undefined;
}

// A field the uiSchema omits keeps the ConfigSchema default control and lands at
// the end of the Basic group, so the fallback must be the same key the Backend uses.
const DEFAULT_GROUP = 'BASIC';

const GROUP_LABELS: Record<string, string> = {
  BASIC: 'Basic',
  MODEL: 'Model',
  MODEL_PARAMETERS: 'Model Parameters',
};

export function groupLabel(group: string): string {
  return GROUP_LABELS[group] ?? group;
}

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
 * but never override the schema's type, enum or required set. Every choice below comes
 * from the registered metadata; Studio keeps no per-node-type or per-field knowledge.
 */
function resolveWidget(schema: JsonSchema, uiWidget: string | undefined): FieldWidget {
  if (schema.enum && schema.enum.length > 0) return 'select';

  const widget = (uiWidget ?? '').toUpperCase();
  if (widget === 'MODEL_SELECTOR') return 'model';
  if (widget === 'TOOL_SELECTOR') return 'tools';
  if (widget === 'TEXTAREA' || widget === 'PROMPT_EDITOR') return 'textarea';

  switch (schema.type) {
    case 'boolean':
      return 'checkbox';
    case 'number':
    case 'integer':
      return 'number';
    case 'array':
      if (schema.items?.enum && schema.items.enum.length > 0) return 'multiselect';
      if (schema.items?.type === 'string') return 'list';
      return 'json';
    case 'object':
      return 'json';
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
    const widget = resolveWidget(schema, ui?.widget);
    const enumSource = widget === 'multiselect' ? schema.items?.enum : schema.enum;

    described.push({
      declared: ui !== undefined,
      order: ui?.order ?? fallbackOrder++,
      field: {
        path,
        schema,
        // Display label only; `path` (the API field) is untouched. A registered Model
        // Selector without its own title reads "Model" as in the MODEL property group,
        // decided by the uiSchema widget, never by the field's name.
        label: schema.title ?? (widget === 'model' ? 'Model' : humanize(path)),
        required: required.has(path),
        widget,
        group: ui?.group ?? DEFAULT_GROUP,
        enumValues: (enumSource ?? []).map((value) => String(value)),
        capability: widget === 'model' ? ui?.capability : undefined,
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

/**
 * Model Selector options: Model Registry entries that declare the uiSchema capability.
 * A field without a capability lists every registered model.
 */
export function modelOptions(
  models: ModelMetadata[],
  capability: string | undefined,
): ModelMetadata[] {
  if (!capability) return models;
  return models.filter((model) => model.capabilities.includes(capability));
}

/** Returns `config` with `path` set, or removed when `next` is undefined. */
export function setConfigField(
  config: JsonObject,
  path: string,
  next: JsonValue | undefined,
): JsonObject {
  const copy = { ...config };
  if (next === undefined) delete copy[path];
  else copy[path] = next;
  return copy;
}
