import { describe, expect, it } from 'vitest';

import { nodeSummaryLine, paletteSummaryLine } from './nodeSummary';
import { nodeAccentColor } from './portStyles';
import type { ModelMetadata, NodeMetadata } from '@/api/types';

const base = {
  configSchema: { type: 'object', properties: {} },
  sideEffect: { kind: 'NONE', idempotency: 'SAFE' },
} as const;

// Shapes mirror GET /api/node-types, including the `null` port lists and uiSchema fields
// the Backend sends for a side with nothing on it.
const textInput: NodeMetadata = {
  ...base,
  type: 'text_input',
  displayName: 'Text Input',
  category: 'Input',
  executionKind: 'SYNC',
  inputs: null,
  outputs: [{ name: 'text', dataType: 'text', required: true }],
  uiSchema: { fields: [{ path: 'inputKey', order: 1, group: 'BASIC', widget: 'DEFAULT' }] },
};

const textOutput: NodeMetadata = {
  ...base,
  type: 'text_output',
  displayName: 'Text Output',
  category: 'Output',
  executionKind: 'SYNC',
  inputs: [{ name: 'text', dataType: 'text', required: true }],
  outputs: null,
  uiSchema: { fields: null },
};

const imageGeneration: NodeMetadata = {
  ...base,
  type: 'image_generation',
  displayName: 'Image Generation',
  category: 'Prompt & Model',
  executionKind: 'ASYNC',
  inputs: [{ name: 'prompt', dataType: 'text', required: true }],
  outputs: [{ name: 'image', dataType: 'image', required: true }],
  uiSchema: {
    fields: [
      { path: 'modelId', order: 1, group: 'MODEL', widget: 'MODEL_SELECTOR' },
      { path: 'width', order: 2, group: 'MODEL_PARAMETERS', widget: 'DEFAULT' },
    ],
  },
};

const agent: NodeMetadata = {
  ...base,
  type: 'agent',
  displayName: 'Agent',
  category: 'Agent',
  executionKind: 'MANAGED_AGENT',
  inputs: [{ name: 'input', dataType: 'text', required: true }],
  outputs: [{ name: 'text', dataType: 'text', required: true }],
  uiSchema: { fields: [{ path: 'modelId', order: 1, group: 'MODEL', widget: 'MODEL_SELECTOR' }] },
};

const models: ModelMetadata[] = [
  { id: 'image-model-v1', displayName: 'Image Model v1', capabilities: [], configSchema: {} },
  { id: 'text-model-v1', displayName: 'Text Model v1', capabilities: [], configSchema: {} },
];

describe('nodeSummaryLine (04 §2.5)', () => {
  it('names the bound Run input field, whether it is required, and its accepted type for an Input node', () => {
    expect(nodeSummaryLine(textInput, { inputKey: 'brief', required: true })).toBe(
      'brief · required · text',
    );
    // The type comes from the registered output port, not from the node type's name.
    expect(
      nodeSummaryLine(
        {
          ...textInput,
          type: 'image_input',
          displayName: 'Image Input',
          outputs: [{ name: 'image', dataType: 'image', required: true }],
        },
        { inputKey: 'reference_image', required: false },
      ),
    ).toBe('reference_image · optional · image');
  });

  it('shows model, spec and sync/async mode for a generation node', () => {
    expect(
      nodeSummaryLine(imageGeneration, { modelId: 'image-model-v1', width: 1024 }, models),
    ).toBe('Image Model v1 · width 1024 · Async');
  });

  it('shows model, allowed tool count and Max Turns for an Agent', () => {
    expect(
      nodeSummaryLine(
        agent,
        { modelId: 'text-model-v1', allowedTools: ['lookup', 'render'], maxTurns: 6 },
        models,
      ),
    ).toBe('Text Model v1 · 2 allowed tools · max 6 turns');
  });

  it('summarises an Output node whose uiSchema fields are null', () => {
    expect(nodeSummaryLine(textOutput)).toBe('Run output');
  });
});

describe('paletteSummaryLine', () => {
  it('describes the type without config placeholders', () => {
    expect(paletteSummaryLine(textInput)).toBe('text source');
    expect(paletteSummaryLine(imageGeneration)).toBe('Model call · Async');
    expect(paletteSummaryLine(agent)).toBe('Model · Tools · managed loop');
    expect(paletteSummaryLine(textOutput)).toBe('Run output');
  });
});

describe('nodeAccentColor', () => {
  it('tolerates the null port lists the Backend sends', () => {
    expect(nodeAccentColor(textInput)).toBe('#a48afb');
    expect(nodeAccentColor(textOutput)).toBe('#a48afb');
    expect(nodeAccentColor({ ...textOutput, inputs: null })).toBe('#93a0b5');
  });
});
