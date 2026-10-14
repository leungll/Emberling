import { describe, expect, it } from 'vitest';

import { matchInId, shortCode } from './short-code';

describe('shortCode', () => {
  it.each([
    ['run_d9c23ab668c54acf9ec83c59c55ce742', 'd9c23ab6'],
    ['wf_f219e36bd99c403ba9158961cc29ac07', 'f219e36b'],
    ['agent_turn_70a478317059423c952bede24ab8adaf', '70a47831'],
    ['nr_70a478317059423c952bede24ab8adaf_2', '70a47831_2'],
    ['sha256:3f7a9c0e1d2b4a5c6e7f8a9b0c1d2e3f', '3f7a9c0e'],
    ['3f7a9c0e1d2b4a5c6e7f8a9b0c1d2e3f4a5b6c7d', '3f7a9c0e'],
    ['image-1', 'image-1'],
    ['', ''],
    [null, ''],
  ])('%s shows as %s', (id, code) => {
    expect(shortCode(id)).toBe(code);
  });
});

describe('matchInId', () => {
  const id = 'run_d9c23ab668c54acf9ec83c59c55ce742';

  it('highlights inside the code when the match is there', () => {
    expect(matchInId(id, 'C23A')).toEqual({
      where: 'code',
      before: 'd9',
      hit: 'c23a',
      after: 'b6',
    });
  });

  it('falls back to the full ID when the match is outside the code', () => {
    expect(matchInId(id, 'ce742')).toEqual({
      where: 'full',
      before: 'run_d9c23ab668c54acf9ec83c59c55',
      hit: 'ce742',
      after: '',
    });
  });

  it('returns nothing for no query or no match', () => {
    expect(matchInId(id, '')).toBeNull();
    expect(matchInId(id, 'zzz')).toBeNull();
  });
});
