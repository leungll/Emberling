import { describe, expect, it } from 'vitest';

import {
  exampleObject,
  hasFieldList,
  humanKey,
  orderedKeys,
  schemaFields,
  typeWord,
} from './schema-fields';
import type { JsonSchema } from '@/api/types';

// The key order a PostgreSQL JSONB round trip gives: shorter keys first. Only `required`
// keeps the author's order.
const TASK: JsonSchema = {
  type: 'object',
  properties: {
    goal: {
      type: 'string',
      enum: ['DELIVER_CLIP'],
      title: 'Goal',
      description: 'What counts as done.',
    },
    sourceId: { type: 'string', minLength: 1, title: 'Source video' },
    durationMs: { type: 'integer', minimum: 0 },
    requirement: { type: 'string', minLength: 1, maxLength: 2000, title: 'Requirement' },
    sourceSha256: { type: 'string', minLength: 64, maxLength: 64 },
    loud: { type: 'boolean' },
    extra: { type: 'object' },
    alpha: { type: 'number' },
  },
  required: ['requirement', 'sourceId', 'sourceSha256', 'durationMs', 'goal'],
};

describe('orderedKeys', () => {
  it('puts required keys in their listed order, then the rest alphabetically', () => {
    expect(orderedKeys(TASK)).toEqual([
      'requirement',
      'sourceId',
      'sourceSha256',
      'durationMs',
      'goal',
      'alpha',
      'extra',
      'loud',
    ]);
  });

  it('skips required names that have no property and works without required', () => {
    expect(orderedKeys({ properties: { b: {}, a: {} }, required: ['zz', 'b'] })).toEqual([
      'b',
      'a',
    ]);
    expect(orderedKeys({ properties: { b: {}, a: {} } })).toEqual(['a', 'b']);
    expect(orderedKeys(undefined)).toEqual([]);
  });
});

describe('schemaFields', () => {
  const fields = schemaFields(TASK);
  const byKey = (k: string) => fields.find((f) => f.key === k);

  it('labels a field by its title, otherwise by its key', () => {
    expect(byKey('sourceId')?.label).toBe('Source video');
    expect(byKey('sourceSha256')?.label).toBe('Source sha256');
    expect(byKey('goal')?.description).toBe('What counts as done.');
  });

  it('chooses a control by type', () => {
    expect(fields.map((f) => [f.key, f.control, f.required])).toEqual([
      ['requirement', 'longtext', true],
      ['sourceId', 'text', true],
      ['sourceSha256', 'text', true],
      ['durationMs', 'number', true],
      ['goal', 'choice', true],
      ['alpha', 'number', false],
      ['extra', 'json', false],
      ['loud', 'bool', false],
    ]);
    expect(byKey('durationMs')?.integer).toBe(true);
    expect(byKey('alpha')?.integer).toBe(false);
    expect(byKey('goal')?.options).toEqual(['DELIVER_CLIP']);
  });

  it('has a field list only for an object schema with properties', () => {
    expect(hasFieldList(TASK)).toBe(true);
    expect(hasFieldList({ properties: { a: {} } })).toBe(true);
    expect(hasFieldList({ type: 'object' })).toBe(false);
    expect(hasFieldList({ type: 'string' })).toBe(false);
    expect(hasFieldList(undefined)).toBe(false);
  });
});

describe('typeWord', () => {
  const fields = schemaFields(TASK);
  const word = (k: string) => {
    const f = fields.find((x) => x.key === k);
    if (!f) throw new Error(k);
    return typeWord(f);
  };

  it('says what kind of value a field takes, with its limits', () => {
    expect(word('requirement')).toBe('Text, up to 2,000 characters');
    expect(word('sourceId')).toBe('Text');
    expect(word('sourceSha256')).toBe('Text, exactly 64 characters');
    expect(word('durationMs')).toBe('Whole number, 0 or more');
    expect(word('goal')).toBe('One of: DELIVER_CLIP');
    expect(word('alpha')).toBe('Number');
    expect(word('loud')).toBe('Yes/no');
    expect(word('extra')).toBe('JSON object');
  });
});

describe('exampleObject', () => {
  it('fills every required field with a typed placeholder, in field order', () => {
    const example = exampleObject(TASK);
    expect(example).toEqual({
      requirement: '…',
      sourceId: '…',
      sourceSha256: '…',
      durationMs: 0,
      goal: 'DELIVER_CLIP',
    });
    expect(Object.keys(example)).toEqual([
      'requirement',
      'sourceId',
      'sourceSha256',
      'durationMs',
      'goal',
    ]);
  });

  it('uses false for a yes/no field and nests required objects', () => {
    expect(
      exampleObject({
        properties: {
          on: { type: 'boolean' },
          inner: { type: 'object', properties: { n: { type: 'number' } }, required: ['n'] },
          list: { type: 'array' },
        },
        required: ['on', 'inner', 'list'],
      }),
    ).toEqual({ on: false, inner: { n: 0 }, list: [] });
    expect(exampleObject(undefined)).toEqual({});
  });
});

describe('humanKey', () => {
  it('turns a key into a label', () => {
    expect(humanKey('diff_text')).toBe('Diff text');
    expect(humanKey('maxRetries')).toBe('Max retries');
  });
});
