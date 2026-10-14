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
