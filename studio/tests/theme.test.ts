// @vitest-environment node
/**
 * The Tailwind theme generates utilities only from the design tokens. These tests compile
 * src/index.css with Tailwind itself and check two things: off-token utilities (the default
 * palette, sizes, shadows and spacing steps) generate nothing, and every utility-looking word
 * in the source that the stock Tailwind theme would style is also styled by ours, so no class
 * silently stops working.
 */
import { readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';

import { compile } from 'tailwindcss';
import { describe, expect, it } from 'vitest';

const SRC = path.join(import.meta.dirname, '..', 'src');
const TAILWIND = path.join(import.meta.dirname, '..', 'node_modules', 'tailwindcss');

async function compiler(css: string) {
  return compile(css, {
    base: SRC,
    loadStylesheet: (id: string, base: string) => {
      if (id === 'tailwindcss') {
        return Promise.resolve({
          path: path.join(TAILWIND, 'index.css'),
          base: TAILWIND,
          content: readFileSync(path.join(TAILWIND, 'index.css'), 'utf8'),
        });
      }
      if (id.startsWith('tailwindcss/')) {
        const file = path.join(TAILWIND, id.slice('tailwindcss/'.length));
        return Promise.resolve({ path: file, base: TAILWIND, content: readFileSync(file, 'utf8') });
      }
      // Third-party stylesheets (React Flow) contribute no utilities.
      return Promise.resolve({ path: id, base, content: '' });
    },
  });
}

/**
 * Reports which candidates produce a rule. Builds are incremental, so the output changes
 * exactly when a candidate adds something new.
 */
async function generated(css: string, candidates: string[]): Promise<Set<string>> {
  const c = await compiler(css);
  const out = new Set<string>();
  let previous = c.build([]);
  for (const candidate of candidates) {
    const next = c.build([candidate]);
    if (next !== previous) out.add(candidate);
    previous = next;
  }
  return out;
}

const themeCss = readFileSync(path.join(SRC, 'index.css'), 'utf8');

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) return sourceFiles(full);
    return /\.(ts|tsx)$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name) ? [full] : [];
  });
}

describe('token theme', () => {
  it('generates nothing outside the token set', async () => {
    const offToken = [
      'bg-red-500',
      'text-gray-700',
      'bg-white',
      'text-black',
      'text-base',
      'text-2xl',
      'p-7',
      'gap-2.5',
      'm-9',
      'rounded-2xl',
      'shadow-lg',
      'font-bold',
      'tracking-wide',
      'leading-tight',
      'max-w-md',
    ];
    expect([...(await generated(themeCss, offToken))]).toEqual([]);
  });

  it('generates the token utilities with token values', async () => {
    const c = await compiler(themeCss);
    const css = c.build(['bg-surface', 'text-sm', 'p-3', 'rounded-md', 'shadow-1', 'z-dialog']);
    expect(css).toContain('background-color: var(--surface)');
    expect(css).toContain('font-size: var(--text-sm)');
    expect(css).toContain('line-height: var(--tw-leading, var(--text-sm--line-height))');
    expect(css).toContain('padding: var(--sp-3)');
    expect(css).toContain('border-radius: var(--radius-md)');
    expect(css).toContain('z-index: var(--z-dialog)');
  });

  it('styles every utility the source uses', async () => {
    const words = new Set<string>();
    for (const file of sourceFiles(SRC)) {
      for (const word of readFileSync(file, 'utf8').split(/[\s"'`{}(),;]+/)) {
        // Utility-looking words only; prose and identifiers cannot match both themes.
        if (/^!?[a-z][a-z0-9-]*(:[a-z0-9-]+)*-[a-z0-9./[\]%#-]+$/.test(word)) words.add(word);
      }
    }
    const candidates = [...words];
    const stock = await generated("@import 'tailwindcss';", candidates);
    const ours = await generated(themeCss, [...stock]);
    expect([...stock].filter((word) => !ours.has(word)).sort()).toEqual([]);
  }, 120_000);

  it('defines every custom property that source code reads by name', () => {
    // The utility theme is a reference block: its --color-*, --text-* and other names exist
    // only while Tailwind compiles and are never emitted, so an inline style that reads one
    // resolves to nothing (an SVG stroke silently becomes none). Inline styles read the raw
    // tokens declared outside that block instead.
    const referenceBlock = /@theme inline reference \{[\s\S]*?\n\}/;
    const emitted = new Set(
      [...themeCss.replace(referenceBlock, '').matchAll(/(--[a-z0-9-]+)\s*:/g)].map(
        (m) => m[1] ?? '',
      ),
    );
    const undefinedReads = sourceFiles(SRC).flatMap((file) =>
      [...readFileSync(file, 'utf8').matchAll(/var\((--[a-z0-9-]+)/g)]
        .map((m) => m[1] ?? '')
        .filter((name) => !name.startsWith('--radix-') && !emitted.has(name))
        .map((name) => `${path.relative(SRC, file)}: ${name}`),
    );
    expect(undefinedReads).toEqual([]);
  });
});
