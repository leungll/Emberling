import { describe, expect, it } from 'vitest';

import {
  buildInput,
  checkField,
  humanKey,
  jsonProblem,
  mediaList,
  parseObject,
  photoCount,
  runFields,
  runStartError,
  seedValues,
  setAtPath,
  subFieldErrors,
  switchObjectView,
  type ObjectDraft,
  type RunField,
} from './run-form';
import { ApiRequestError } from '@/api/client';
import type { RunInputSchema } from '@/api/types';

const REF = { assetId: 'asset_1', mediaType: 'image/png', sizeBytes: 120, sha256: 'abc' };

const SCHEMA: RunInputSchema = {
  type: 'object',
  properties: {
    brief: { type: 'string', title: 'Brief' },
    count: { type: 'integer' },
    loud: { type: 'boolean' },
    settings: { type: 'object' },
    reference: {
      type: 'object',
      properties: {
        assetId: { type: 'string' },
        mediaType: { type: 'string', enum: ['image/webp', 'image/png'] },
        sizeBytes: { type: 'integer', maximum: 1024 },
      },
    },
    photos: {
      type: 'array',
      minItems: 1,
      maxItems: 3,
      items: {
        type: 'object',
        properties: { assetId: { type: 'string' }, mediaType: { type: 'string' } },
      },
    },
  },
  required: ['brief', 'photos'],
} as unknown as RunInputSchema;

describe('runFields', () => {
  it('reads each property of the frozen schema into a field of the right kind', () => {
    const fields = runFields(SCHEMA, { brief: 'Creative Brief' });
    expect(fields.map((f) => [f.key, f.kind, f.required, f.label])).toEqual([
      ['brief', 'text', true, 'Creative Brief'],
      ['count', 'number', false, 'Count'],
      ['loud', 'bool', false, 'Loud'],
      ['settings', 'json', false, 'Settings'],
      ['reference', 'asset', false, 'Reference'],
      ['photos', 'photos', true, 'Photos'],
    ]);
    const reference = fields.find((f) => f.key === 'reference');
    expect(reference?.types).toEqual(['image/webp', 'image/png']);
    expect(reference?.maxBytes).toBe(1024);
    const photos = fields.find((f) => f.key === 'photos');
    expect([photos?.minItems, photos?.maxItems]).toEqual([1, 3]);
  });

  it('has no fields for a workflow without run input', () => {
    expect(runFields(undefined, {})).toEqual([]);
  });
});

describe('seedValues and buildInput', () => {
  const fields = runFields(SCHEMA, {});

  it('round-trips a prior run’s input unchanged', () => {
    const input = {
      brief: 'line one\nline two',
      count: 3,
      loud: true,
      settings: { a: 1, nested: { b: 'x' } },
      reference: REF,
      photos: [REF, { ...REF, assetId: 'asset_2' }],
    };
    expect(buildInput(fields, seedValues(fields, input))).toEqual(input);
  });

  it('leaves optional empty fields out: the Backend injects no defaults', () => {
    const values = seedValues(fields, { brief: 'b', photos: [REF] });
    expect(buildInput(fields, values)).toEqual({ brief: 'b', photos: [REF] });
  });

  it('ignores prior values of the wrong shape', () => {
    const values = seedValues(fields, { reference: { assetId: 1 }, photos: 'x', brief: 4 });
    expect(values.reference).toBeNull();
    expect(values.photos).toEqual([]);
    expect(values.brief).toBe('');
  });
});

// A task schema as a saved Definition returns it: JSONB reorders `properties`, while
// `required` keeps the author's order.
const TASK_SCHEMA: RunInputSchema = {
  type: 'object',
  properties: {
    task: {
      type: 'object',
      properties: {
        goal: { type: 'string', enum: ['DELIVER_CLIP'] },
        notes: { type: 'string' },
        loud: { type: 'boolean' },
        sourceId: { type: 'string', minLength: 1, title: 'Source video' },
        durationMs: { type: 'integer', minimum: 0 },
        requirement: { type: 'string', minLength: 1, maxLength: 2000 },
        sourceSha256: { type: 'string', minLength: 64, maxLength: 64 },
      },
      required: ['requirement', 'sourceId', 'sourceSha256', 'durationMs', 'goal'],
    },
  },
  required: ['task'],
};
const SHA = 'a'.repeat(64);
const only = (fields: readonly RunField[]): RunField => {
  const [f] = fields;
  if (!f) throw new Error('no field');
  return f;
};
const TASK = {
  requirement: 'A 30 second clip about tests',
  sourceId: 'src_1',
  sourceSha256: SHA,
  durationMs: 120000,
  goal: 'DELIVER_CLIP',
};

describe('object fields', () => {
  const fields = runFields(TASK_SCHEMA, { task: 'Editing Task' });
  const task = only(fields);

  it('reads an object with properties as labelled sub-fields, required ones first', () => {
    expect([task.kind, task.label, task.required]).toEqual(['object', 'Editing Task', true]);
    expect(task.fields?.map((f) => [f.key, f.label, f.control, f.required])).toEqual([
      ['requirement', 'Requirement', 'longtext', true],
      ['sourceId', 'Source video', 'text', true],
      ['sourceSha256', 'Source sha256', 'text', true],
      ['durationMs', 'Duration ms', 'number', true],
      ['goal', 'Goal', 'choice', true],
      ['loud', 'Loud', 'bool', false],
      ['notes', 'Notes', 'text', false],
    ]);
  });

  it('starts a first run with a one-value choice chosen and the rest empty', () => {
    const draft = seedValues(fields, null).task as ObjectDraft;
    expect(draft.values).toEqual({
      requirement: '',
      sourceId: '',
      sourceSha256: '',
      durationMs: '',
      goal: 'DELIVER_CLIP',
      loud: false,
      notes: '',
    });
  });

  it('builds typed values and leaves empty optional sub-fields out', () => {
    const draft = seedValues(fields, null).task as ObjectDraft;
    const values = {
      task: {
        ...draft,
        values: {
          ...draft.values,
          requirement: TASK.requirement,
          sourceId: 'src_1',
          sourceSha256: SHA,
          durationMs: ' 120000 ',
        },
      },
    };
    expect(checkField(task, values.task)).toBeNull();
    const input = buildInput(fields, values);
    expect(input).toEqual({ task: TASK });
    expect(typeof (input.task as Record<string, unknown>).durationMs).toBe('number');
  });

  it('round-trips a prior run’s input, keeping keys the schema doesn’t list', () => {
    const prior = { task: { ...TASK, loud: true, notes: 'n', unknown: { a: 1 } } };
    expect(buildInput(fields, seedValues(fields, prior))).toEqual(prior);
  });

  it('checks each sub-field and names it', () => {
    const draft = seedValues(fields, null).task as ObjectDraft;
    const with_ = (values: Record<string, string | boolean>): ObjectDraft => ({
      ...draft,
      values: { ...draft.values, ...TASK, durationMs: '1', ...values },
    });
    expect(subFieldErrors(task, draft)).toMatchObject({
      requirement: 'Requirement is required.',
      sourceId: 'Source video is required.',
    });
    expect(checkField(task, draft)).toBe('Requirement is required.');
    expect(subFieldErrors(task, with_({ durationMs: 'x' }))).toEqual({
      durationMs: 'Duration ms must be a number.',
    });
    expect(subFieldErrors(task, with_({ durationMs: '1.5' }))).toEqual({
      durationMs: 'Duration ms must be a whole number.',
    });
    expect(subFieldErrors(task, with_({ sourceSha256: 'abc' }))).toEqual({
      sourceSha256: 'Source sha256 must be exactly 64 characters.',
    });
    expect(subFieldErrors(task, with_({ goal: 'OTHER' }))).toEqual({
      goal: 'Goal must be one of: DELIVER_CLIP.',
    });
    expect(subFieldErrors(task, with_({}))).toEqual({});
  });

  it('leaves an optional object out while nothing is filled in', () => {
    const optional = runFields({ ...TASK_SCHEMA, required: [] }, {});
    const f = only(optional);
    const values = seedValues(optional, null);
    expect(checkField(f, values.task ?? null)).toBeNull();
    expect(buildInput(optional, values)).toEqual({});
  });

  it('switches to JSON and back without losing the value', () => {
    const draft = seedValues(fields, { task: TASK }).task as ObjectDraft;
    const text = switchObjectView(task, draft, 'json');
    expect(typeof text).toBe('string');
    expect(JSON.parse(text as string)).toEqual(TASK);
    expect(buildInput(fields, { task: text })).toEqual({ task: TASK });
    const back = switchObjectView(task, text, 'fields');
    expect(back).toEqual(draft);
    expect(switchObjectView(task, '[1]', 'fields')).toBeNull();
  });

  it('checks the JSON view as JSON', () => {
    expect(checkField(task, '{"a": }')).toMatch(/^Line 1, column \d+: /);
    expect(checkField(task, '')).toBe('Editing Task is required.');
  });
});

describe('checkField', () => {
  const fields = runFields(SCHEMA, { brief: 'Brief' });
  const field = (key: string): RunField => {
    const found = fields.find((f) => f.key === key);
    if (!found) throw new Error(`no field ${key}`);
    return found;
  };

  it('names a missing required value', () => {
    expect(checkField(field('brief'), '')).toBe('Brief is required.');
    expect(checkField(field('brief'), '  ')).toBe('Brief is required.');
    expect(checkField(field('brief'), 'ok')).toBeNull();
  });

  it('asks for a number', () => {
    expect(checkField(field('count'), 'abc')).toBe('Count must be a number.');
    expect(checkField(field('count'), '4')).toBeNull();
    expect(checkField(field('count'), '')).toBeNull();
  });

  it('places invalid JSON by line and column', () => {
    expect(checkField(field('settings'), '{\n  "a": 1\n  "b": 2\n}')).toMatch(
      /^Line 3, column 3: .+\.$/,
    );
    // A text field that looks like JSON is checked as JSON too.
    expect(checkField(field('brief'), '{"a": }')).toMatch(
      /^Line 1, column \d+: unexpected token “}”/,
    );
    expect(checkField(field('brief'), 'just text')).toBeNull();
  });

  it('counts photos against the schema minimum', () => {
    expect(checkField(field('photos'), [])).toBe('Add at least 1 photo to start the run.');
    expect(checkField(field('photos'), [REF])).toBeNull();
  });
});

describe('jsonProblem', () => {
  it('says when the JSON ends early', () => {
    const p = jsonProblem('{"a": 1');
    expect([p?.line, p?.col]).toEqual([1, 8]);
    expect(p?.msg).toMatch(/\.$/);
    expect(jsonProblem('{"a": 1}')).toBeNull();
  });
});

describe('Fields view helpers', () => {
  it('opens only a JSON object as fields', () => {
    expect(parseObject('{"a": 1}')).toEqual({ a: 1 });
    expect(parseObject('[1]')).toBeNull();
    expect(parseObject('nope')).toBeNull();
  });

  it('sets one value deep in an object without touching the rest', () => {
    const before = { a: { b: 'x', c: 1 }, d: true };
    const after = setAtPath(before, ['a', 'b'], 'y');
    expect(after).toEqual({ a: { b: 'y', c: 1 }, d: true });
    expect(before.a.b).toBe('x');
  });

  it('turns a key into a label', () => {
    expect(humanKey('diff_text')).toBe('Diff text');
    expect(humanKey('maxRetries')).toBe('Max retries');
  });
});

describe('copy helpers', () => {
  it('names image types in a plain list', () => {
    expect(mediaList(['image/webp', 'image/png', 'image/jpeg'])).toBe('PNG, JPEG or WebP');
    expect(mediaList(['image/png'])).toBe('PNG');
  });

  it('describes a photo count against the limits', () => {
    expect(photoCount(0, 1, 3)).toBe('Add at least 1 photo.');
    expect(photoCount(2, 1, 3)).toBe('2 photos added. Add up to 3.');
    expect(photoCount(3, 1, 3)).toBe('3 photos added. That’s the limit.');
    expect(photoCount(1, 2, undefined)).toBe('1 photo added. Add at least 2.');
    expect(photoCount(0, undefined, undefined)).toBe('No photos added.');
  });
});

describe('runStartError', () => {
  it('shows the Backend’s reason when it rejected the run', () => {
    const rejected = new ApiRequestError(400, {
      error: { code: 'VALIDATION_FAILED', message: 'input.brief is required' },
    });
    expect(runStartError(rejected)).toBe('input.brief is required');
  });

  it('says the server didn’t respond when there was no answer', () => {
    expect(runStartError(new TypeError('Failed to fetch'))).toBe(
      'Couldn’t start the run. The server didn’t respond. Try again.',
    );
  });
});
