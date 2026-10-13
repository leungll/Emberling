/**
 * Self-tests for the review checks: each check must fire on a page that breaks its rule
 * and stay quiet on one that keeps it. Runs on blank pages, with no Studio involved.
 */
import { expect, test, type Page } from '@playwright/test';

import { describeFocus, inspectPage, type CheckName } from './support/checks/in-page';
import { findSecrets } from './support/secret-scan';
import { fixture } from './support/server';
import { readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';

const TOKENS = `
  :root { --fg: #18212f; --surface: #ffffff; --bg: #f7f7f8; --border: #e3e6eb;
          --shadow-1: 0 4px 12px -2px rgba(16,24,40,.12), 0 1px 3px rgba(16,24,40,.06); }
  p { margin: 0; }
  body { margin: 0; font: 13px/18px system-ui; color: var(--fg); background: var(--bg); }
`;

async function checks(page: Page, body: string): Promise<CheckName[]> {
  await page.setContent(`<style>${TOKENS}</style>${body}`);
  const { violations } = await page.evaluate(inspectPage, {});
  return [...new Set(violations.map((v) => v.check))];
}

test.describe('review checks', () => {
  test('a page built from tokens passes', async ({ page }) => {
    expect(
      await checks(
        page,
        `<main style="padding:16px"><p>Runs</p>
         <button style="height:32px;padding:0 12px;border:1px solid var(--border);background:var(--surface);border-radius:6px;font:inherit;color:var(--fg)">Open</button></main>`,
      ),
    ).toEqual([]);
  });

  test('off-scale colour, size, space and radius are reported', async ({ page }) => {
    expect(await checks(page, '<p style="color:#123456">Run</p>')).toEqual(['tokens']);
    expect(await checks(page, '<p style="font-size:15px">Run</p>')).toEqual(['tokens']);
    expect(await checks(page, '<p style="padding:5px">Run</p>')).toEqual(['tokens']);
    expect(await checks(page, '<div style="border-radius:3px;height:8px"></div>')).toEqual([
      'tokens',
    ]);
  });

  test('truncated and clipped text is reported', async ({ page }) => {
    expect(
      await checks(
        page,
        '<p style="width:40px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">A long workflow name</p>',
      ),
    ).toContain('overflow');
    expect(await checks(page, '<div style="width:2000px;height:4px"></div>')).toContain('overflow');
  });

  test('dot-joined facts, raw enums and broken values are reported', async ({ page }) => {
    expect(await checks(page, '<p>Completed · 4 steps</p>')).toEqual(['copy']);
    expect(await checks(page, '<p>WAITING_CALLBACK</p>')).toEqual(['copy']);
    expect(await checks(page, '<p>Started undefined</p>')).toEqual(['copy']);
    expect(await checks(page, '<code>WAITING_CALLBACK</code>')).toEqual([]);
  });

  test('low contrast and small targets are reported', async ({ page }) => {
    expect(await checks(page, '<p style="color:var(--border)">Run</p>')).toEqual(['contrast']);
    expect(
      await checks(
        page,
        '<button style="height:16px;width:16px;padding:0;border:0;background:var(--surface)" aria-label="x"></button>',
      ),
    ).toContain('target-size');
  });

  test('focus rings drawn as outline or ring shadow are recognised', async ({ page }) => {
    await page.setContent(`<style>${TOKENS}
      .o:focus-visible { outline: 2px solid var(--fg); }
      .r:focus-visible { outline: none; box-shadow: 0 0 0 2px var(--fg); }
      .n:focus-visible { outline: none; }</style>
      <button class="o">One</button><button class="r">Two</button><button class="n">Three</button>`);
    const rings: boolean[] = [];
    for (let i = 0; i < 3; i += 1) {
      await page.keyboard.press('Tab');
      rings.push((await page.evaluate(describeFocus))?.ring ?? false);
    }
    expect(rings).toEqual([true, true, false]);
  });

  test('an exemption covers the element and records its reason', async ({ page }) => {
    await page.setContent(
      `<style>${TOKENS}</style><p data-ui-exempt="tokens:image-preview" style="color:#123456">Run</p>`,
    );
    const result = await page.evaluate(inspectPage, {});
    expect(result.violations).toEqual([]);
    expect(result.exemptions[0]).toContain('image-preview');
  });
});

test.describe('recorded fixtures', () => {
  test('carry no credentials, callback tokens or internal storage keys', () => {
    const dir = path.join(import.meta.dirname, 'fixtures', 'recorded');
    const found = readdirSync(dir).flatMap((file) =>
      findSecrets(JSON.parse(readFileSync(path.join(dir, file), 'utf8')), file),
    );
    expect(found).toEqual([]);
  });

  test('the secret scan recognises what it must refuse', () => {
    expect(findSecrets({ headers: { authorization: 'Bearer abcdefghijkl' } })).toHaveLength(2);
    expect(findSecrets({ url: 'http://x/callback?token=abcdefghijk' })).toHaveLength(1);
    expect(findSecrets(fixture('/api/definitions'))).toEqual([]);
  });
});
