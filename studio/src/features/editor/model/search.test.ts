import { describe, expect, it } from 'vitest';
import { NODE_TYPES } from '@/test/registry-fixture';
import { highlight, matchesStep } from './search';

const meta = (type: string) => NODE_TYPES.find((m) => m.type === type)!;

describe('step search', () => {
  it('matches the name, the purpose line and the type key, in any case', () => {
    expect(matchesStep(meta('image_generation'), 'IMAGE')).toBe(true);
    expect(matchesStep(meta('agent'), 'tools')).toBe(true);
    expect(matchesStep(meta('text_input'), 'text input')).toBe(true);
    expect(matchesStep(meta('text_input'), 'image')).toBe(false);
    expect(matchesStep(meta('text_input'), '  ')).toBe(true);
  });

  it('marks every matched word', () => {
    expect(highlight('Image Generation', 'gen')).toEqual([
      { text: 'Image ', hit: false },
      { text: 'Gen', hit: true },
      { text: 'eration', hit: false },
    ]);
  });
});
