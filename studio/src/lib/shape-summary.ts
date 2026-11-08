import type { JsonValue } from '@/api/types';

/**
 * One plain line for any value a port carried, by its shape alone, so the Data flow section
 * shows real data instead of a type name. It never lists an object's keys.
 */

const SHORT = 80;

const count = (n: number, one: string, many: string) =>
  `${n.toLocaleString('en-US')} ${n === 1 ? one : many}`;

/** `null` when there is nothing to say. */
export function shapeSummary(value: JsonValue | null | undefined): string | null {
  if (value === null || value === undefined) return null;
  if (typeof value === 'string')
    return value.length <= SHORT && !value.includes('\n')
      ? value
      : count(value.length, 'character', 'characters');
  if (typeof value === 'boolean') return value ? 'Yes' : 'No';
  if (typeof value === 'number') return value.toLocaleString('en-US');
  if (Array.isArray(value)) return count(value.length, 'item', 'items');
  return count(Object.keys(value).length, 'field', 'fields');
}
