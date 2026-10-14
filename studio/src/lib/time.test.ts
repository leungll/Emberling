import { describe, expect, it } from 'vitest';

import {
  absoluteTime,
  allZones,
  bytes,
  duration,
  exactSeconds,
  exactTime,
  limitText,
  listTime,
  offsetLabel,
  offsetText,
  relativeWords,
  resolveZone,
  secondsText,
  untilText,
  zoneCity,
  zoneIsValid,
  zoneMatches,
  zoneOffset,
  zoneQuery,
} from './time';

const NOW = new Date('2026-10-08T06:32:10Z');
const ago = (ms: number) => new Date(NOW.getTime() - ms).toISOString();

describe('relativeWords', () => {
  it.each([
    [ago(10_000), 'just now'],
    [ago(44_000), 'just now'],
    [ago(46_000), '1 min ago'],
    [ago(26 * 60_000), '26 min ago'],
    [ago(59 * 60_000), '59 min ago'],
    [ago(3 * 3_600_000), '3 h ago'],
    [ago(26 * 3_600_000), '1 day ago'],
    [ago(5 * 86_400_000), '5 days ago'],
    [ago(90 * 86_400_000), '3 months ago'],
    [ago(400 * 86_400_000), '1 year ago'],
    [ago(-2 * 60_000), 'in 2 min'],
    [null, '—'],
    ['not a time', '—'],
  ])('%s reads %s', (iso, expected) => {
    expect(relativeWords(iso, NOW)).toBe(expected);
  });
});

describe('absoluteTime and listTime', () => {
  it.each([
    ['UTC', '2026-10-08 06:32'],
    ['Asia/Shanghai', '2026-10-08 14:32'],
    ['America/Los_Angeles', '2026-10-07 23:32'],
    ['Asia/Kolkata', '2026-10-08 12:02'],
  ])('shows an absolute time in %s as %s', (zone, expected) => {
    expect(absoluteTime(NOW.toISOString(), zone)).toBe(expected);
  });

  it('uses 00 for midnight, never 24', () => {
    expect(absoluteTime('2026-10-08T16:00:00Z', 'Asia/Shanghai')).toBe('2026-10-09 00:00');
  });

  it('is relative within 24 hours and absolute beyond, in the same zone', () => {
    expect(listTime(ago(26 * 60_000), NOW, 'UTC')).toBe('26 min ago');
    expect(listTime(ago(23.4 * 3_600_000), NOW, 'UTC')).toBe('23 h ago');
    expect(listTime(ago(24 * 3_600_000 + 1000), NOW, 'UTC')).toBe('2026-10-07 06:32');
    expect(listTime(ago(24 * 3_600_000 + 1000), NOW, 'Asia/Shanghai')).toBe('2026-10-07 14:32');
    expect(listTime(undefined, NOW, 'UTC')).toBe('—');
  });
});

describe('exactTime', () => {
  it('carries seconds, the zone and the relative form', () => {
    expect(exactTime(ago(26 * 60_000), NOW, 'Asia/Shanghai')).toBe(
      '2026-10-08 14:06:10, Shanghai time (UTC+8). 26 min ago.',
    );
    expect(exactTime(ago(26 * 60_000), NOW, 'UTC')).toBe('2026-10-08 06:06:10, UTC. 26 min ago.');
    expect(exactTime(ago(5000), NOW, 'America/Los_Angeles')).toBe(
      '2026-10-07 23:32:05, Los Angeles time (UTC−7). Just now.',
    );
  });

  it('is empty for a missing value', () => {
    expect(exactTime(null, NOW, 'UTC')).toBe('');
    expect(exactSeconds(undefined, 'UTC')).toBe('');
  });
});

describe('offsets and zones', () => {
  it.each([
    [0, 'UTC+0'],
    [480, 'UTC+8'],
    [-420, 'UTC−7'],
    [330, 'UTC+5:30'],
    [345, 'UTC+5:45'],
    [-570, 'UTC−9:30'],
  ])('%i minutes read %s', (minutes, expected) => {
    expect(offsetLabel(minutes)).toBe(expected);
  });

  it('reads the offset of a zone at an instant, including daylight saving', () => {
    expect(zoneOffset('Asia/Shanghai', NOW)).toBe('UTC+8');
    expect(zoneOffset('America/Los_Angeles', new Date('2026-01-15T00:00:00Z'))).toBe('UTC−8');
    expect(zoneOffset('America/Los_Angeles', NOW)).toBe('UTC−7');
  });

  it.each([
    ['America/Los_Angeles', 'Los Angeles'],
    ['Asia/Calcutta', 'Kolkata'],
    ['UTC', 'UTC'],
  ])('names %s by its city %s', (zone, city) => {
    expect(zoneCity(zone)).toBe(city);
  });

  it('resolves the preference and rejects unknown zones', () => {
    expect(resolveZone('local', 'Europe/Berlin')).toBe('Europe/Berlin');
    expect(resolveZone('UTC', 'Europe/Berlin')).toBe('UTC');
    expect(zoneIsValid('Asia/Shanghai')).toBe(true);
    expect(zoneIsValid('Mars/Olympus')).toBe(false);
  });

  it('finds zones by city, ID or offset, with a typed minus sign', () => {
    const zones = allZones(NOW, [
      'Asia/Shanghai',
      'America/Los_Angeles',
      'Asia/Kolkata',
      'Etc/GMT+1',
      'UTC',
    ]);
    expect(zones.map((z) => z.id)).toEqual([
      'America/Los_Angeles',
      'Asia/Kolkata',
      'Asia/Shanghai',
    ]);
    const find = (q: string) =>
      zones.filter((z) => zoneMatches(z.search, zoneQuery(q))).map((z) => z.city);
    expect(find('+8')).toEqual(['Shanghai']);
    expect(find('−7')).toEqual(['Los Angeles']);
    expect(find('5:30')).toEqual(['Kolkata']);
    expect(find('los angeles')).toEqual(['Los Angeles']);
    expect(find('asia/')).toEqual(['Kolkata', 'Shanghai']);
    expect(find('')).toHaveLength(3);
  });
});

describe('durations', () => {
  it.each([
    [0.4, '<1 ms'],
    [850, '850 ms'],
    [4200, '4.2 s'],
    [4000, '4 s'],
    [22_612, '22.6 s'],
    [64_000, '1 min 04 s'],
    [7_380_000, '2 h 03 min'],
    [-5, '<1 ms'],
    [null, ''],
  ])('%s ms reads %s', (ms, expected) => {
    expect(duration(ms)).toBe(expected);
  });

  it('gives the exact value in seconds for tooltips', () => {
    expect(secondsText(22_612)).toBe('22.612 s');
  });

  it.each([
    [0, '+0 ms'],
    [-3, '+0 ms'],
    [3200, '+3.2 s'],
    [64_000, '+1 min 04 s'],
  ])('offset %i reads %s', (ms, expected) => {
    expect(offsetText(ms)).toBe(expected);
  });

  it('counts down to a deadline and up after it', () => {
    expect(untilText(new Date(NOW.getTime() + 240_000).toISOString(), NOW)).toBe('in 4 min 00 s');
    expect(untilText(new Date(NOW.getTime() - 12_000).toISOString(), NOW)).toBe('12 s ago');
  });

  it.each([
    [0, '0 B'],
    [512, '512 B'],
    [1229, '1.2 KB'],
    [3_565_158, '3.4 MB'],
    [20 * 1024 * 1024, '20 MB'],
  ])('%i bytes read %s', (n, expected) => {
    expect(bytes(n)).toBe(expected);
  });

  it.each([
    [120_000, '2 min'],
    [90_000, '1.5 min'],
    [20_000, '20 s'],
    [1500, '1500 ms'],
  ])('a %i ms limit reads %s', (ms, expected) => {
    expect(limitText(ms)).toBe(expected);
  });
});
