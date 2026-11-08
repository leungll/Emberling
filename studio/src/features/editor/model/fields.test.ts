import { describe, expect, it } from 'vitest';

import type { JsonObject } from '@/api/types';
import { step } from '@/test/registry-fixture';
import { inputExample, schemaSummary } from './fields';

// Property order as a JSONB round trip leaves it; only `required` keeps the author's order.
const TASK_SCHEMA: JsonObject = {
  properties: {
    goal: { type: 'string', enum: ['DELIVER_CLIP'] },
    sourceId: { type: 'string' },
    durationMs: { type: 'integer' },
    requirement: { type: 'string' },
    sourceSha256: { type: 'string' },
    notes: { type: 'string' },
  },
  required: ['requirement', 'sourceId', 'sourceSha256', 'durationMs', 'goal'],
};

describe('schemaSummary', () => {
  it('lists every field, required ones first in their listed order', () => {
    expect(schemaSummary(TASK_SCHEMA)).toBe(
      '6 fields: requirement, sourceId, sourceSha256, durationMs, goal, notes',
    );
  });

  it('describes a format without fields', () => {
    expect(schemaSummary(undefined)).toBe('Any shape');
    expect(schemaSummary({ type: 'array' })).toBe('A list');
  });
});

describe('inputExample', () => {
  it('fills a task from its schema, so it has the shape a run needs', () => {
    const node = step('t', 'task_input', 0, 0, { inputKey: 'task', schema: TASK_SCHEMA });
    expect(JSON.stringify(inputExample(node))).toBe(
      JSON.stringify({
        task: {
          requirement: '…',
          sourceId: '…',
          sourceSha256: '…',
          durationMs: 0,
          goal: 'DELIVER_CLIP',
        },
      }),
    );
  });

  it('gives a task without a schema as an empty object', () => {
    const node = step('t', 'task_input', 0, 0, { inputKey: 'job' });
    expect(inputExample(node)).toEqual({ job: {} });
  });

  it('gives text as text', () => {
    const node = step('t', 'text_input', 0, 0, { inputKey: 'brief' });
    expect(inputExample(node)).toEqual({ brief: '…' });
  });

  it('gives an image as a complete asset reference in an accepted format', () => {
    const node = step('i', 'image_input', 0, 0, {
      inputKey: 'reference',
      acceptedMediaTypes: ['image/webp'],
    });
    expect(inputExample(node)).toEqual({
      reference: { assetId: 'asset_…', mediaType: 'image/webp', sizeBytes: 0, sha256: '…' },
    });
    const brief = step('b', 'media_brief', 0, 0, { inputKey: 'photos' });
    expect(inputExample(brief)).toEqual({
      photos: [{ assetId: 'asset_…', mediaType: 'image/png', sizeBytes: 0, sha256: '…' }],
    });
  });
});
