import { describe, expect, it } from 'vitest';
import { serverIssues, validate, type CheckContext } from './checks';
import { MODELS, NODE_TYPES, TOOLS, link, step } from '@/test/registry-fixture';
import { catalogOf } from './steps';

const ctx: CheckContext = {
  catalog: catalogOf(NODE_TYPES),
  models: new Map(MODELS.map((m) => [m.id, m])),
  tools: new Map(TOOLS.map((x) => [x.name, x])),
};

const texts = (nodes: Parameters<typeof validate>[0], edges: Parameters<typeof validate>[1] = []) =>
  validate(nodes, edges, ctx).map((i) => i.text);

describe('validate', () => {
  it('asks for an output step', () => {
    expect(texts([])).toEqual(['Add an output step so runs have a result.']);
  });

  it('reports required settings in plain words with their section', () => {
    const issues = validate(
      [
        step('a', 'agent', 0, 0, { maxTurns: 8, timeoutMs: 1000 }),
        step('o', 'text_output', 400, 0),
      ],
      [link('1', 'a', 'text', 'o', 'text')],
      ctx,
    );
    expect(issues.map((i) => [i.text, i.section, i.label])).toEqual([
      ['Choose a model.', 'model', 'Model'],
      ['Connect something to the Input input of Agent.', 'connections', 'Connections'],
    ]);
  });

  it('checks models against the capability the field needs', () => {
    expect(
      texts([step('g', 'image_generation', 0, 0, { modelId: 'text-model-v1', width: 1024 })]),
    ).toContain('Mock Text Model v1 can’t create images.');
    expect(texts([step('g', 'text_generation', 0, 0, { modelId: 'gone' })])).toContain(
      '“gone” isn’t available on this server. Choose another model.',
    );
  });

  it('checks numbers, keys, lengths and tools', () => {
    const t = texts([
      step('g', 'image_generation', 0, 0, { modelId: 'image-model-v1', width: 100 }),
      step('a', 'text_input', 0, 100, {
        inputKey: 'topic',
        required: true,
        minLength: 5,
        maxLength: 2,
      }),
      step('b', 'text_input', 0, 200, { inputKey: 'topic', required: true }),
      step('c', 'text_input', 0, 300, { inputKey: '1x', required: true }),
      step('d', 'agent', 0, 400, {
        modelId: 'text-model-v1',
        maxTurns: 1.5,
        timeoutMs: 0,
        allowedTools: ['nope'],
      }),
    ]);
    expect(t).toEqual(
      expect.arrayContaining([
        'Width must be at least 256 px.',
        'The minimum can’t be more than the maximum.',
        'Another input already uses “topic”.',
        'Use letters, numbers and underscores. Start with a letter.',
        'Max turns must be a whole number.',
        'Time limit must be more than 0.',
        '“nope” isn’t a tool on this server. Remove it.',
      ]),
    );
  });

  it('checks model behavior against the chosen model', () => {
    const t = texts([
      step('g', 'text_generation', 0, 0, {
        modelId: 'text-model-v1',
        modelConfig: { temperature: 3, seed: 1 },
      }),
    ]);
    expect(t).toEqual(
      expect.arrayContaining([
        'Temperature must be between 0 and 2.',
        '“seed” isn’t a setting Mock Text Model v1 accepts.',
      ]),
    );
  });

  it('flags links whose types do not fit', () => {
    const t = texts(
      [
        step('i', 'image_input', 0, 0, { inputKey: 'p', required: true }),
        step('o', 'text_output', 400, 0),
      ],
      [link('1', 'i', 'image', 'o', 'text')],
    );
    expect(t).toContain(
      'Image from Image Input can’t go into the Text input, which takes text. Remove this link.',
    );
  });

  it('flags an unregistered step type', () => {
    expect(texts([step('x', 'gone', 0, 0), step('o', 'text_output', 400, 0)])).toContain(
      'This step type isn’t registered on this server.',
    );
  });
});

describe('serverIssues', () => {
  it('maps server paths onto steps and fields and drops ones the client already shows', () => {
    const nodes = [step('g', 'text_generation', 0, 0)];
    const local = validate(nodes, [], ctx);
    const out = serverIssues(
      [
        { code: 'X', path: 'nodes[g].config/modelId', nodeId: 'g', message: 'model required' },
        { code: 'Y', path: 'nodes[g].config/systemPrompt', message: 'too long' },
        { code: 'Z', path: 'edges[0]', message: 'bad edge' },
      ],
      nodes,
      local,
    );
    expect(out.map((i) => [i.node, i.field, i.text])).toEqual([
      ['g', 'systemPrompt', 'too long'],
      [null, null, 'bad edge'],
    ]);
  });
});
