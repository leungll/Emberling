import { describe, expect, it } from 'vitest';

import {
  deepEqual,
  fieldTitle,
  humanize,
  isTechnical,
  msText,
  nestedSummary,
  readableKeys,
  scalarText,
  typeset,
} from './value-reading';

describe('reading rules for any value', () => {
  it('turns keys and enum constants into words', () => {
    expect(humanize('clipDurationMs')).toBe('Clip duration ms');
    expect(humanize('CANNOT_SATISFY')).toBe('Cannot satisfy');
    expect(humanize('source-video')).toBe('Source video');
  });

  it('labels a field by its schema title, else its key without a unit suffix', () => {
    expect(fieldTitle('clipDurationMs', 4200, 'Length')).toBe('Length');
    expect(fieldTitle('clipDurationMs', 4200)).toBe('Clip duration');
    expect(fieldTitle('fileSizeBytes', 10)).toBe('File size');
    expect(fieldTitle('clipSha256', 'abc')).toBe('Clip');
    expect(fieldTitle('clipDurationMs', 'text')).toBe('Clip duration ms');
  });

  it('reads whole milliseconds as a clock and short values in words', () => {
    expect(msText(27)).toBe('27 ms');
    expect(msText(267_000)).toBe('4:27');
    expect(msText(3_725_000)).toBe('1:02:05');
    expect(scalarText(267_000, 'clipDurationMs')).toBe('4:27');
    expect(scalarText(2048, 'fileSizeBytes')).toBe('2 KB');
    expect(scalarText(true)).toBe('Yes');
    expect(scalarText('CANNOT_SATISFY')).toBe('Cannot satisfy');
    expect(scalarText(1234)).toBe('1,234');
  });

  it('leaves digests and prefixed ids to the Data tab', () => {
    expect(isTechnical('a'.repeat(64))).toBe(true);
    expect(isTechnical('asset_0123abcd4567')).toBe(true);
    expect(isTechnical('A short caption for the clip')).toBe(false);
    expect(isTechnical('asset_0123abcd 4567')).toBe(false);
    expect(
      readableKeys({ note: 'hi', sha: 'f'.repeat(40), empty: '', gone: null }, [
        'note',
        'sha',
        'empty',
        'gone',
      ]),
    ).toEqual(['note']);
  });

  it('summarises nested data as a few facts or a count, never a key list', () => {
    expect(
      nestedSummary({ language: 'en', lineCount: 36, sizeBytes: 3891, id: 'x'.repeat(20) }),
    ).toBe('Language en, Line count 36, Size 3.8 KB');
    expect(nestedSummary([1, 2, 3])).toBe('3 items');
    expect(nestedSummary({ long: 'z'.repeat(50), nested: { a: 1 } })).toBe('2 fields');
  });

  it('compares values deeply and sets curly quotes', () => {
    expect(deepEqual({ a: [1, { b: 2 }] }, { a: [1, { b: 2 }] })).toBe(true);
    expect(deepEqual({ a: 1 }, { a: 1, b: 2 })).toBe(false);
    expect(deepEqual([1], { 0: 1 })).toBe(false);
    expect(typeset(`He said "it's done"`)).toBe('He said “it’s done”');
  });
});
