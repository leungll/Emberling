import type { JsonObject, JsonSchema, JsonValue, Node, ToolMetadata } from '@/api/types';
import { editor as t } from '@/lib/copy/editor';
import { exampleObject, orderedKeys } from '@/lib/schema-fields';

/**
 * How each known config key is presented: its label, control and the side panel section it
 * lives in. The registered configSchema still decides which keys exist and what values they
 * take; a key without an entry here gets a generic control in a Settings section.
 */
export type FieldWidget =
  | 'longtext'
  | 'model'
  | 'tools'
  | 'int'
  | 'minutes'
  | 'schema'
  | 'modelConfig'
  | 'prompt'
  | 'label'
  | 'required'
  | 'lengthRange'
  | 'formats'
  | 'sizeMB'
  | 'key';

export type SectionId = keyof typeof t.panel.sections;

export interface FieldSpec {
  label: string;
  widget: FieldWidget;
  section: SectionId;
  /** Config keys the field covers when it is not a single key (a length range covers two). */
  keys?: readonly string[];
  placeholder?: string;
  hint?: string;
  help?: string;
  before?: string;
  unit?: string;
  min?: number;
  step?: number;
  optional?: boolean;
  hideLabel?: boolean;
  expand?: boolean;
  /** A schema shown as the list of fields a run fills in, not as a one-line summary. */
  fieldList?: boolean;
}

const f = t.fields;

export const FIELD_SPEC: Record<string, FieldSpec> = {
  'agent.instructions': {
    label: f.instructions,
    widget: 'longtext',
    section: 'instructions',
    placeholder: f.instructionsPlaceholder,
    expand: true,
    hideLabel: true,
  },
  'agent.modelId': { label: f.model, widget: 'model', section: 'model' },
  'agent.imageModelId': { label: f.imageModel, widget: 'model', section: 'model' },
  'agent.reviewModelId': {
    label: f.reviewModel,
    widget: 'model',
    section: 'model',
    optional: true,
  },
  'agent.allowedTools': { label: f.tools, widget: 'tools', section: 'tools' },
  'agent.maxTurns': {
    label: f.maxTurns,
    widget: 'int',
    section: 'limits',
    unit: f.turns,
    min: 1,
    hint: f.maxTurnsHint,
  },
  'agent.timeoutMs': {
    label: f.timeLimit,
    widget: 'minutes',
    section: 'limits',
    unit: f.min,
    hint: f.timeLimitHint,
  },
  'agent.maxGenerationCalls': {
    label: f.generationCalls,
    widget: 'int',
    section: 'limits',
    optional: true,
    unit: f.calls,
    min: 1,
    placeholder: f.noLimit,
  },
  'agent.contextSchema': {
    label: f.schema.contextSchema[0],
    widget: 'schema',
    section: 'formats',
    help: f.schema.contextSchema[1],
  },
  'agent.stateSchema': {
    label: f.schema.stateSchema[0],
    widget: 'schema',
    section: 'formats',
    help: f.schema.stateSchema[1],
  },
  'agent.outputSchema': {
    label: f.schema.outputSchema[0],
    widget: 'schema',
    section: 'formats',
    help: f.schema.outputSchema[1],
  },
  'agent.modelConfig': {
    label: t.panel.sections.behavior,
    widget: 'modelConfig',
    section: 'behavior',
  },
  'text_generation.modelId': { label: f.model, widget: 'model', section: 'model' },
  'text_generation.modelConfig': {
    label: t.panel.sections.behavior,
    widget: 'modelConfig',
    section: 'behavior',
  },
  'text_generation.systemPrompt': {
    label: f.systemPrompt,
    widget: 'longtext',
    section: 'systemPrompt',
    optional: true,
    hideLabel: true,
    placeholder: f.systemPromptPlaceholder,
    hint: f.systemPromptHint,
  },
  'image_generation.modelId': { label: f.model, widget: 'model', section: 'model' },
  'image_generation.width': {
    label: f.width,
    widget: 'int',
    section: 'size',
    unit: f.px,
    min: 256,
    step: 64,
  },
  'image_generation.modelConfig': {
    label: t.panel.sections.behavior,
    widget: 'modelConfig',
    section: 'behavior',
  },
  'prompt_template.template': {
    label: f.template,
    widget: 'prompt',
    section: 'template',
    hideLabel: true,
  },
  'text_input.required': { label: f.required, widget: 'required', section: 'needs' },
  'text_input.textLength': {
    label: f.textLength,
    widget: 'lengthRange',
    section: 'needs',
    keys: ['minLength', 'maxLength'],
  },
  'text_input.label': { label: f.fieldLabel, widget: 'label', section: 'api' },
  'text_input.inputKey': { label: f.key, widget: 'key', section: 'api' },
  'task_input.schema': {
    label: f.schema.taskSchema[0],
    widget: 'schema',
    section: 'needs',
    help: f.schema.taskSchema[1],
    fieldList: true,
  },
  'task_input.required': { label: f.required, widget: 'required', section: 'needs' },
  'task_input.label': { label: f.fieldLabel, widget: 'label', section: 'api' },
  'task_input.inputKey': { label: f.key, widget: 'key', section: 'api' },
  'image_input.required': { label: f.required, widget: 'required', section: 'needs' },
  'image_input.acceptedMediaTypes': {
    label: f.formats,
    widget: 'formats',
    section: 'needs',
    hint: f.formatsHint,
  },
  'image_input.maxSizeBytes': { label: f.maxSize, widget: 'sizeMB', section: 'needs' },
  'image_input.label': { label: f.fieldLabel, widget: 'label', section: 'api' },
  'image_input.inputKey': { label: f.key, widget: 'key', section: 'api' },
  'media_brief.inputKey': { label: f.key, widget: 'key', section: 'api' },
};

/** The spec that covers a config key of a type, directly or through a multi-key field. */
export function specFor(type: string, key: string): FieldSpec | undefined {
  const direct = FIELD_SPEC[`${type}.${key}`];
  if (direct) return direct;
  for (const [k, spec] of Object.entries(FIELD_SPEC)) {
    if (k.startsWith(`${type}.`) && spec.keys?.includes(key)) return spec;
  }
  return undefined;
}

/** The field id a config key belongs to (a range's two keys share one field). */
export function fieldKeyFor(type: string, key: string): string {
  if (FIELD_SPEC[`${type}.${key}`]) return key;
  for (const [k, spec] of Object.entries(FIELD_SPEC)) {
    if (k.startsWith(`${type}.`) && spec.keys?.includes(key)) return k.slice(type.length + 1);
  }
  return key;
}

/** The fields of a type in one section, in the table's order. */
export function fieldsIn(type: string, section: SectionId): string[] {
  return Object.entries(FIELD_SPEC)
    .filter(([k, spec]) => k.startsWith(`${type}.`) && spec.section === section)
    .map(([k]) => k.slice(type.length + 1));
}

/** A readable label for a config key that has no spec. */
export function genericLabel(key: string): string {
  return t.cap(
    key
      .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
      .replace(/[_-]+/g, ' ')
      .toLowerCase(),
  );
}

export const FORMAT_TYPES: readonly (readonly [string, string])[] = [
  ['image/png', 'PNG'],
  ['image/jpeg', 'JPEG'],
  ['image/webp', 'WebP'],
  ['image/gif', 'GIF'],
];

export const MB = 1048576;

export const KEY_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** Model parameter names in words; the model registry carries none yet. */
export const MODEL_PARAM: Record<string, { label: string; help: string; mockOnly?: boolean }> = {
  temperature: { label: f.temperature, help: f.temperatureHelp },
  script: { label: f.mockScenario, help: f.mockScenarioHelp, mockOnly: true },
};

/** The short names of accepted image formats, such as PNG and JPEG, without repeats. */
export function formatNames(types: unknown): string[] {
  const out: string[] = [];
  for (const m of Array.isArray(types) ? types : []) {
    if (typeof m !== 'string') continue;
    const w = FORMAT_TYPES.find((f) => f[0] === m)?.[1] ?? m.replace(/^image\//, '').toUpperCase();
    if (!out.includes(w)) out.push(w);
  }
  return out;
}

/** The image tools of an agent: they run with its image model. */
export function imageToolsOf(node: Node, tools: ReadonlyMap<string, ToolMetadata>): ToolMetadata[] {
  const list = Array.isArray(node.config.allowedTools) ? node.config.allowedTools : [];
  return list
    .map((x) => (typeof x === 'string' ? tools.get(x) : undefined))
    .filter((x): x is ToolMetadata => !!x && x.usesImageModel);
}

/** The review tools of an agent: they review with its review model, or the mock policy. */
export function reviewToolsOf(
  node: Node,
  tools: ReadonlyMap<string, ToolMetadata>,
): ToolMetadata[] {
  const list = Array.isArray(node.config.allowedTools) ? node.config.allowedTools : [];
  return list
    .map((x) => (typeof x === 'string' ? tools.get(x) : undefined))
    .filter((x): x is ToolMetadata => !!x && x.usesReviewModel);
}

/** A config value as plain text: strings as they are, anything else as JSON. */
export function plainValue(v: unknown): string {
  return typeof v === 'string' ? v : (JSON.stringify(v) ?? '');
}

/** A starting point for each agent data format. */
export const SCHEMA_EXAMPLE: Record<string, JsonObject> = {
  contextSchema: {
    type: 'object',
    properties: { task: { type: 'string', description: 'What the agent should do' } },
    required: ['task'],
  },
  stateSchema: {
    type: 'object',
    properties: { notes: { type: 'array', items: { type: 'string' } } },
  },
  outputSchema: {
    type: 'object',
    properties: { summary: { type: 'string' }, passed: { type: 'boolean' } },
    required: ['summary'],
  },
};

/** A config value that is a JSON Schema object, or undefined. */
export function schemaOf(v: JsonValue | undefined): JsonSchema | undefined {
  return v !== null && typeof v === 'object' && !Array.isArray(v) ? v : undefined;
}

/**
 * A data format in a few words: all its fields, required ones first, its type, or that it
 * accepts any shape. Never shortened: a hidden field is one nobody can check.
 */
export function schemaSummary(v: JsonValue | undefined): string {
  if (v === undefined || v === null) return f.schemaAny;
  const schema = schemaOf(v);
  if (!schema) return f.schemaDefined;
  const keys = orderedKeys(schema);
  if (keys.length) return f.schemaFields(keys.length, keys.join(', '));
  if (typeof schema.type === 'string') return f.schemaType[schema.type] ?? f.schemaTypeOther;
  return f.schemaDefined;
}

/**
 * This step's part of the run input, with placeholder values in the shape the Backend
 * accepts: text as text, a task with every required field of its schema, an image as a
 * complete asset reference.
 */
export function inputExample(node: Node): JsonObject {
  const c = node.config;
  const key = typeof c.inputKey === 'string' && c.inputKey ? c.inputKey : 'key';
  const accepted = Array.isArray(c.acceptedMediaTypes) ? c.acceptedMediaTypes : [];
  const mediaType = typeof accepted[0] === 'string' ? accepted[0] : 'image/png';
  const ref: JsonObject = { assetId: 'asset_…', mediaType, sizeBytes: 0, sha256: '…' };
  switch (node.type) {
    case 'image_input':
      return { [key]: ref };
    case 'media_brief':
      return { [key]: [ref] };
    case 'task_input':
      return { [key]: exampleObject(schemaOf(c.schema)) };
    default:
      return { [key]: '…' };
  }
}
