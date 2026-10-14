/**
 * Coming back to a list from a row's page: the row the viewer opened is tinted briefly and
 * holds focus quietly, so once the tint fades it looks like every other row. The first key
 * press makes that focus visible again.
 */
import { expect, test, type Locator, type Page } from '@playwright/test';

import { RECORDED_AT, serveFixtures } from '../support/server';

const look = (row: Locator) =>
  row.evaluate((el) => {
    const s = getComputedStyle(el);
    return { background: s.backgroundColor, outline: s.outlineStyle };
  });

async function openAndReturn(page: Page, url: string, leave: RegExp) {
  await page.clock.setFixedTime(new Date(RECORDED_AT));
  await serveFixtures(page);
  await page.goto(url);
  const rows = page.locator('tbody tr[data-row]');
  await expect(rows.nth(2)).toBeVisible();
  const opened = rows.first();
  const key = await opened.getAttribute('data-row');
  await opened.locator('[data-row-primary]').click();
  await expect(page).toHaveURL(leave);
  // The address changes before the list unmounts; going back that early returns to a list
  // that never left, with the clicked link still focused by the pointer.
  await expect(rows).toHaveCount(0);
  await page.goBack();

  const returned = page.locator(`tbody tr[data-row="${key}"]`);
  // Quiet focus on the opened row shows the return was restored; the tint may be gone.
  const primary = returned.locator('[data-row-primary]');
  await expect(primary).toBeFocused();
  await expect(primary).toHaveAttribute('data-quiet-focus', '');
  // Away from every row, so hover does not shade one.
  await page.mouse.move(0, 0);
  await expect(returned).not.toHaveAttribute('data-returned', { timeout: 5_000 });
  return { rows, returned };
}

test.beforeEach(() => {
  test.skip(test.info().project.name !== 'w1440', 'the behaviour does not depend on the width');
});

test('Runs: the returned row looks like any other row once its tint fades', async ({ page }) => {
  const { rows, returned } = await openAndReturn(page, '/runs', /\/runs\/run_/);
  const plain = await look(rows.last());
  expect(await look(returned)).toEqual(plain);

  await page.keyboard.press('ArrowDown');
  const next = rows.nth(1);
  await expect(next.locator('[data-row-primary]')).toBeFocused();
  await expect.poll(async () => (await look(next)).background).not.toBe(plain.background);
  expect(await look(returned)).toEqual(plain);
});

test('Workflows: the returned row looks like any other row once its tint fades', async ({
  page,
}) => {
  const { rows, returned } = await openAndReturn(page, '/workflows', /\/workflows\/wf_/);
  const plain = await look(rows.last());
  expect(await look(returned)).toEqual(plain);

  await page.keyboard.press('ArrowDown');
  const next = rows.nth(1);
  await expect(next.locator('[data-row-primary]')).toBeFocused();
  await expect.poll(async () => (await look(next)).outline).not.toBe('none');
});
