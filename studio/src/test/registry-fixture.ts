import type {
  Edge,
  JsonObject,
  JsonSchema,
  ModelMetadata,
  Node,
  NodeMetadata,
  PortDataType,
  ToolMetadata,
  UiSchemaField,
} from '@/api/types';

/** A compact copy of the registered step types, models and tools, for the Editor's tests. */

type PortSpec = [name: string, type: PortDataType, required?: boolean];

function meta(
  type: string,
  displayName: string,
  category: string,
  inputs: PortSpec[],
  outputs: PortSpec[],
  properties: Record<string, JsonSchema>,
  required: string[] = [],
  ui: Partial<UiSchemaField>[] = [],
): NodeMetadata {
  return {
    type,
    displayName,
    category,
    executionKind: 'SYNC',
    inputs: inputs.map(([name, dataType, req = true]) => ({ name, dataType, required: req })),
    outputs: outputs.map(([name, dataType]) => ({ name, dataType, required: true })),
    configSchema: { type: 'object', properties, required },
    uiSchema: {
      fields: ui.map((f, i) => ({
        path: f.path ?? '',
        order: i,
        group: 'BASIC',
        widget: f.widget ?? 'DEFAULT',
        ...f,
      })),
    },
    sideEffect: { kind: 'NONE', idempotency: 'SAFE' },
    factInputs: [],
  };
}

const str: JsonSchema = { type: 'string' };
const int = (minimum?: number): JsonSchema => ({
  type: 'integer',
  ...(minimum === undefined ? {} : { minimum }),
});

export const NODE_TYPES: NodeMetadata[] = [
  meta(
    'agent',
    'Agent',
    'Agent',
    [['input', 'text']],
    [['text', 'text']],
    {
      instructions: str,
      modelId: str,
      modelConfig: { type: 'object' },
      allowedTools: { type: 'array', items: str },
      contextSchema: { type: 'object' },
      stateSchema: { type: 'object' },
      outputSchema: { type: 'object' },
      maxTurns: int(1),
      timeoutMs: int(1),
      maxGenerationCalls: int(1),
      imageModelId: str,
    },
    ['modelId', 'maxTurns', 'timeoutMs'],
    [
      { path: 'modelId', widget: 'MODEL_SELECTOR', capability: 'structured_decision' },
      { path: 'imageModelId', widget: 'MODEL_SELECTOR', capability: 'reference_image_generation' },
    ],
  ),
  meta(
    'image_generation',
    'Image Generation',
    'Prompt & Model',
    [
      ['prompt', 'text'],
      ['reference', 'image', false],
    ],
    [['image', 'image']],
    { modelId: str, width: int(256) },
    ['modelId', 'width'],
    [{ path: 'modelId', widget: 'MODEL_SELECTOR', capability: 'image_generation' }],
  ),
  meta(
    'image_input',
    'Image Input',
    'Input',
    [],
    [['image', 'image']],
    {
      inputKey: str,
      label: str,
      required: { type: 'boolean' },
      acceptedMediaTypes: { type: 'array', items: str },
      maxSizeBytes: int(1),
    },
    ['inputKey', 'required'],
  ),
  meta(
    'media_brief',
    'Media Brief',
    'Input',
    [['brief', 'text']],
    [['text', 'text']],
    { inputKey: str },
    ['inputKey'],
  ),
  meta(
    'media_output',
    'Media Output',
    'Output',
    [
      ['image', 'image'],
      ['caption', 'text'],
    ],
    [],
    {},
  ),
  meta(
    'media_result',
    'Media Result',
    'Output',
    [
      ['text', 'text'],
      ['brief', 'text'],
    ],
    [
      ['image', 'image'],
      ['caption', 'text'],
    ],
    {},
  ),
  meta(
    'prompt_template',
    'Prompt Template',
    'Prompt & Model',
    [['text', 'text']],
    [['text', 'text']],
    { template: str },
    ['template'],
  ),
  meta(
    'text_generation',
    'Text Generation',
    'Prompt & Model',
    [['prompt', 'text']],
    [['text', 'text']],
    { modelId: str, modelConfig: { type: 'object' }, systemPrompt: str },
    ['modelId'],
    [{ path: 'modelId', widget: 'MODEL_SELECTOR', capability: 'text_generation' }],
  ),
  meta(
    'text_input',
    'Text Input',
    'Input',
    [],
    [['text', 'text']],
    {
      inputKey: str,
      label: str,
      required: { type: 'boolean' },
      minLength: int(0),
      maxLength: int(1),
    },
    ['inputKey', 'required'],
  ),
  meta('text_output', 'Text Output', 'Output', [['text', 'text']], [], {}),
];

export const MODELS: ModelMetadata[] = [
  {
    id: 'image-model-v1',
    displayName: 'Mock Image Model v1',
    capabilities: ['image_generation', 'reference_image_generation'],
    configSchema: {
      type: 'object',
      additionalProperties: false,
      properties: { temperature: { type: 'number', minimum: 0, maximum: 2, default: 0.2 } },
    },
  },
  {
    id: 'text-model-v1',
    displayName: 'Mock Text Model v1',
    capabilities: ['text_generation', 'structured_decision'],
    configSchema: {
      type: 'object',
      additionalProperties: false,
      properties: {
        script: { type: 'string', enum: ['photo-set', 'software-delivery'] },
        temperature: { type: 'number', minimum: 0, maximum: 2, default: 0.2 },
      },
    },
  },
];

const tool = (
  name: string,
  description: string,
  extra: Partial<ToolMetadata> = {},
): ToolMetadata => ({
  name,
  description,
  inputSchema: { type: 'object' },
  outputSchema: { type: 'object' },
  sideEffect: { kind: 'NONE', idempotency: 'SAFE' },
  executionKind: 'SYNC',
  requires: [],
  countsTowardGenerationLimit: false,
  usesImageModel: false,
  ...extra,
});

export const TOOLS: ToolMetadata[] = [
  tool('lookup', 'Read a deterministic record'),
  tool('generate_image', 'Generate an image asset', {
    countsTowardGenerationLimit: true,
    usesImageModel: true,
  }),
];

export function step(
  id: string,
  type: string,
  x: number,
  y: number,
  config: JsonObject = {},
  name?: string,
): Node {
  const m = NODE_TYPES.find((n) => n.type === type);
  return { id, type, name: name ?? m?.displayName ?? type, position: { x, y }, config };
}

export function link(
  id: string,
  source: string,
  sourceHandle: string,
  target: string,
  targetHandle: string,
): Edge {
  return { id, source, sourceHandle, target, targetHandle };
}
