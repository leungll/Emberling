// @vitest-environment node
/**
 * Examples for the style, copy and overlay rules in eslint.config.js: each snippet is linted
 * as if it lived at the given path, with only the syntax rules switched on.
 */
import { ESLint, type Linter } from 'eslint';
import tseslint from 'typescript-eslint';
import { describe, expect, it } from 'vitest';

// @ts-expect-error -- eslint.config.js is plain JavaScript without a declaration file.
import rawConfig from '../eslint.config.js';

const syntaxBlocks = (rawConfig as Linter.Config[]).filter(
  (block) => block.rules && 'no-restricted-syntax' in block.rules,
);

const eslint = new ESLint({
  cwd: new URL('..', import.meta.url).pathname,
  overrideConfigFile: true,
  overrideConfig: [
    {
      files: ['**/*.{ts,tsx}'],
      languageOptions: {
        parser: tseslint.parser,
        parserOptions: { ecmaFeatures: { jsx: true } },
      },
    },
    ...syntaxBlocks,
  ],
});

async function messages(code: string, filePath: string): Promise<string[]> {
  const [result] = await eslint.lintText(code, { filePath });
  return (result?.messages ?? []).map((m) => m.message);
}

const COPY = /copy module/;

describe('copy rule', () => {
  it.each([
    ['visible text', 'export const A = () => <p>Run failed</p>;'],
    ['an aria-label', 'export const A = () => <button aria-label="Close" />;'],
    ['a placeholder', 'export const A = () => <input placeholder="Search runs" />;'],
  ])('rejects %s written into a shared component', async (_, code) => {
    expect(await messages(code, 'src/components/status/Example.tsx')).toEqual([
      expect.stringMatching(COPY),
    ]);
  });

  it.each([
    ['text from the copy table', 'export const A = ({ c }: { c: { x: string } }) => <p>{c.x}</p>;'],
    ['punctuation only', 'export const A = () => <p>: –</p>;'],
    ['a class name', 'export const A = () => <p className="text-fg-muted" />;'],
  ])('accepts %s', async (_, code) => {
    expect(await messages(code, 'src/components/status/Example.tsx')).toEqual([]);
  });

  it('leaves tests and the overlay primitives alone', async () => {
    const code = 'export const A = () => <p>Run failed</p>;';
    expect(await messages(code, 'src/components/status/Example.test.tsx')).toEqual([]);
    expect(await messages(code, 'src/components/ui/Example.tsx')).toEqual([]);
  });
});

describe('style rule', () => {
  it.each([
    ['an arbitrary value', 'export const c = "w-[430px]";', /arbitrary/],
    ['a hex colour', 'export const c = "#ff0000";', /Colours come from the tokens/],
    ['a palette class', 'export const c = "text-red-500";', /default Tailwind palette/],
    ['a numeric layer', 'export const c = "z-50";', /layer utility/],
    ['dot-joined facts', 'export const c = "Failed · 2 s";', /' · '/],
  ])('rejects %s anywhere in src', async (_, code, message) => {
    expect(await messages(code, 'src/lib/example.ts')).toEqual([expect.stringMatching(message)]);
  });

  it.each([
    ['token utilities', 'export const c = "bg-surface text-fg-muted z-dialog";'],
    ['a data variant', 'export const c = "data-[state=open]:bg-surface";'],
    ['an arbitrary child variant', 'export const c = "[&_svg]:size-icon-sm";'],
  ])('accepts %s', async (_, code) => {
    expect(await messages(code, 'src/lib/example.ts')).toEqual([]);
  });

  it('still applies in shared components, where the copy rule is added', async () => {
    expect(await messages('export const c = "w-[430px]";', 'src/components/x/Example.tsx')).toEqual(
      [expect.stringMatching(/arbitrary/)],
    );
  });
});

describe('overlay role rule', () => {
  it('rejects a hand-made dialog or menu in a feature', async () => {
    const code = 'export const A = () => <div role="dialog" />;';
    expect(await messages(code, 'src/features/runs/Example.tsx')).toEqual([
      expect.stringMatching(/src\/components\/ui/),
    ]);
    expect(await messages(code, 'src/components/ui/Example.tsx')).toEqual([]);
  });
});
