import { describe, expect, it } from 'vitest';
import { cardTarget, linkPlan, linkProblem, reaches, type Graph } from './connect';
import { NODE_TYPES, link, step } from '@/test/registry-fixture';
import { catalogOf, fits } from './steps';

const catalog = catalogOf(NODE_TYPES);
const meta = (type: string) => NODE_TYPES.find((m) => m.type === type)!;

const graph: Graph = {
  nodes: [
    step('in', 'text_input', 0, 0),
    step('tpl', 'prompt_template', 336, 0),
    step('gen', 'text_generation', 672, 0),
    step('img', 'image_input', 0, 200),
    step('ig', 'image_generation', 672, 200),
    step('out', 'text_output', 1008, 0),
  ],
  edges: [link('e1', 'in', 'text', 'tpl', 'text'), link('e2', 'tpl', 'text', 'gen', 'prompt')],
};

describe('fits', () => {
  it('takes any type into an any input and otherwise needs the same type', () => {
    expect(fits('image', 'any')).toBe(true);
    expect(fits('text', 'text')).toBe(true);
    expect(fits('image', 'text')).toBe(false);
  });
});

describe('linkProblem', () => {
  it('accepts a free input of the same type', () => {
    expect(linkProblem(graph, catalog, 'gen', 'text', 'out', 'text')).toBeNull();
  });

  it('checks the reasons in a fixed order', () => {
    expect(linkProblem(graph, catalog, 'gen', 'nope', 'out', 'text')?.code).toBe('missing');
    expect(linkProblem(graph, catalog, 'tpl', 'text', 'tpl', 'text')?.code).toBe('self');
    expect(linkProblem(graph, catalog, 'in', 'text', 'tpl', 'text')?.code).toBe('dup');
    expect(linkProblem(graph, catalog, 'gen', 'text', 'tpl', 'text')?.code).toBe('cycle');
  });

  it('names the types in plain words', () => {
    const p = linkProblem(graph, catalog, 'img', 'image', 'out', 'text');
    expect(p).toEqual({ code: 'type', text: 'Image output can’t connect to a text input.' });
  });

  it('names the loop it would make', () => {
    expect(linkProblem(graph, catalog, 'gen', 'text', 'tpl', 'text')?.text).toBe(
      'This would make a loop: Prompt Template already leads to Text Generation.',
    );
  });

  it('offers the existing link when the input is busy', () => {
    const p = linkProblem(graph, catalog, 'in', 'text', 'gen', 'prompt');
    expect(p?.code).toBe('busy');
    expect(p?.edge?.id).toBe('e2');
    expect(p?.text).toBe(
      'The Prompt input of Text Generation already has a link from Prompt Template.',
    );
  });
});

describe('cardTarget', () => {
  it('picks the first free fitting input of a card', () => {
    expect(cardTarget(graph, catalog, 'img', 'image', 'ig')).toEqual({
      nid: 'ig',
      handle: 'reference',
      problem: null,
    });
  });

  it('falls back to a busy fitting input so Replace can be offered', () => {
    expect(cardTarget(graph, catalog, 'in', 'text', 'gen').problem?.code).toBe('busy');
  });

  it('explains a card without inputs or without a fitting one', () => {
    expect(cardTarget(graph, catalog, 'gen', 'text', 'in').problem?.text).toBe(
      'Text Input has no inputs.',
    );
    expect(cardTarget(graph, catalog, 'img', 'image', 'out').problem?.text).toBe(
      'Text Output has no image input.',
    );
  });
});

describe('linkPlan', () => {
  it('uses the first fitting input after an output, required first', () => {
    expect(
      linkPlan(
        meta('image_generation'),
        { kind: 'from', sid: 'in', handle: 'text', type: 'text' },
        graph,
        catalog,
      )?.inPort.name,
    ).toBe('prompt');
    expect(
      linkPlan(
        meta('image_generation'),
        { kind: 'from', sid: 'img', handle: 'image', type: 'image' },
        graph,
        catalog,
      )?.inPort.name,
    ).toBe('reference');
    expect(
      linkPlan(
        meta('text_input'),
        { kind: 'from', sid: 'in', handle: 'text', type: 'text' },
        graph,
        catalog,
      ),
    ).toBeNull();
  });

  it('needs an input for the source and an output for the target to insert into a link', () => {
    const plan = linkPlan(
      meta('prompt_template'),
      { kind: 'insert', edgeId: 'e2' },
      graph,
      catalog,
    );
    expect(plan?.inPort.name).toBe('text');
    expect(plan?.outPort?.name).toBe('text');
    expect(
      linkPlan(meta('image_generation'), { kind: 'insert', edgeId: 'e2' }, graph, catalog),
    ).toBeNull();
  });
});

describe('reaches', () => {
  it('follows links downstream only', () => {
    expect(reaches(graph.edges, 'in', 'gen')).toBe(true);
    expect(reaches(graph.edges, 'gen', 'in')).toBe(false);
  });
});
