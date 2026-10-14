import { describe, expect, it } from 'vitest';
import { NODE_TYPES, step } from '@/test/registry-fixture';
import { GROUPS, defaultConfig, groupTypes, newStepName, portLabel, uniqueKey } from './steps';

const meta = (type: string) => NODE_TYPES.find((m) => m.type === type)!;

describe('groups', () => {
  it('lists each registered type once, in the sidebar order', () => {
    const all = GROUPS.flatMap((g) => groupTypes(g, NODE_TYPES).map((m) => m.type));
    expect(all).toEqual([
      'text_input',
      'image_input',
      'media_brief',
      'agent',
      'text_generation',
      'image_generation',
      'prompt_template',
      'media_result',
      'text_output',
      'media_output',
    ]);
  });

  it('places an unlisted type by its category', () => {
    const extra = { ...meta('text_output'), type: 'json_output', displayName: 'JSON Output' };
    const outputs = groupTypes(GROUPS[3]!, [...NODE_TYPES, extra]).map((m) => m.type);
    expect(outputs.at(-1)).toBe('json_output');
  });
});

describe('new steps', () => {
  it('numbers a name when the type is already on the canvas', () => {
    const nodes = [step('a', 'agent', 0, 0), step('b', 'agent', 0, 0, {}, 'Agent 2')];
    expect(newStepName(meta('agent'), [])).toBe('Agent');
    expect(newStepName(meta('agent'), nodes)).toBe('Agent 3');
  });

  it('starts with the defaults its schema declares and a free input key', () => {
    const nodes = [step('a', 'text_input', 0, 0, { inputKey: 'input1' })];
    expect(defaultConfig(meta('text_input'), nodes)).toEqual({
      inputKey: 'input2',
      required: true,
    });
    expect(defaultConfig(meta('agent'), [])).toEqual({
      instructions: '',
      allowedTools: [],
      maxTurns: 8,
      timeoutMs: 120000,
    });
    expect(uniqueKey('image', [])).toBe('image1');
  });

  it('labels ports in words', () => {
    expect(portLabel({ name: 'reference' })).toBe('Reference image');
    expect(portLabel({ name: 'payload' })).toBe('Payload');
  });
});
