import type { NodeMetadata } from '@/api/types';
import { purposeOf } from './steps';

/** Folds case and curly quotes so “Image” and "image" match the same query. */
export function fold(s: string): string {
  return s.toLowerCase().replace(/[’‘]/g, "'").replace(/[“”]/g, '"').trim();
}

/**
 * Whether a step type matches a search: every word of the query appears in its name,
 * its purpose line or its type key.
 */
export function matchesStep(meta: NodeMetadata, query: string): boolean {
  const words = fold(query).split(/\s+/).filter(Boolean);
  if (!words.length) return true;
  const hay = fold(`${meta.displayName} ${purposeOf(meta.type)} ${meta.type.replace(/_/g, ' ')}`);
  return words.every((w) => hay.includes(w));
}

export interface Highlight {
  text: string;
  hit: boolean;
}

/** Splits `text` into runs, marking where the query's words appear. */
export function highlight(text: string, query: string): Highlight[] {
  const words = fold(query).split(/\s+/).filter(Boolean);
  if (!words.length) return [{ text, hit: false }];
  const lower = text.toLowerCase();
  const mark = new Array<boolean>(text.length).fill(false);
  for (const w of words) {
    for (let i = lower.indexOf(w); i >= 0; i = lower.indexOf(w, i + 1)) {
      for (let k = i; k < i + w.length; k += 1) mark[k] = true;
    }
  }
  const out: Highlight[] = [];
  let start = 0;
  for (let i = 1; i <= text.length; i += 1) {
    if (i === text.length || mark[i] !== mark[start]) {
      out.push({ text: text.slice(start, i), hit: mark[start] ?? false });
      start = i;
    }
  }
  return out;
}
