/**
 * Names stay readable when a page is narrower than the layout is designed for: a list
 * table's name column and the run trace's name column keep a usable width, the other
 * columns or the region's own scrolling give way, and the page itself never scrolls
 * sideways. Measured at one narrow viewport, so it runs in one project only.
 */
import { expect, test, type Locator, type Page } from '@playwright/test';

import { data } from '../support/data';
import { RECORDED_AT, serveFixtures } from '../support/server';

/** The narrowest a name column may render, in CSS pixels. */
const NAME_MIN = 120;

async function open(
  page: Page,
  url: string,
  size: { width: number; height: number },
  store: Record<string, string>,
) {
  await page.setViewportSize(size);
  await page.addInitScript((entries) => {
    for (const [k, v] of Object.entries(entries)) window.localStorage.setItem(k, v);
  }, store);
  await page.clock.setFixedTime(new Date(RECORDED_AT));
  await serveFixtures(page);
  await page.goto(url);
}

async function widths(cells: Locator): Promise<number[]> {
  return cells.evaluateAll((els) => els.map((el) => Math.round(el.getBoundingClientRect().width)));
}

async function noPageScroll(page: Page) {
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
}

test.beforeEach(() => {
  test.skip(test.info().project.name !== 'w1000', 'the narrow viewport is set by each test');
});

for (const list of [
  { name: 'Workflows', url: '/workflows' },
  { name: 'Runs', url: '/runs' },
]) {
  test(`${list.name} keeps its name column readable at 800 px with the sidebar open`, async ({
    page,
  }) => {
    await open(page, list.url, { width: 800, height: 800 }, { 'emb.sidebar': 'expanded' });
    const names = page.locator('tbody tr[data-row] > td:first-child');
    await expect(names.first()).toBeVisible();

    for (const w of await widths(names)) expect(w).toBeGreaterThanOrEqual(NAME_MIN);
    await noPageScroll(page);
  });
}

test('the trace keeps step names on a few lines beside the details', async ({ page }) => {
  // The run list closed and the trace at its narrowest share puts the trace beside the
  // details in a narrow column.
  await open(
    page,
    `/workflows/${data.photos.workflowId}/runs/${data.photos.runId}`,
    { width: 1024, height: 800 },
    { 'emb.runs.list': 'closed', 'emb.obs.split': '35' },
  );
  const rows = page.locator('[role="treegrid"] [role="row"][data-id]');
  await expect(rows.first()).toBeVisible();
  await expect(page.locator('[data-details]')).toBeVisible();

  const names = rows.locator('> [role="gridcell"]:first-child');
  for (const w of await widths(names)) expect(w).toBeGreaterThanOrEqual(NAME_MIN);
  // A name broken one letter per line makes its row many lines tall.
  const heights = await rows.evaluateAll((els) =>
    els.map((el) => Math.round(el.getBoundingClientRect().height)),
  );
  for (const h of heights) expect(h).toBeLessThanOrEqual(80);
  await noPageScroll(page);
});

test('the events log keeps its text columns readable beside the details', async ({ page }) => {
  await open(
    page,
    `/workflows/${data.photos.workflowId}/runs/${data.photos.runId}?view=events`,
    { width: 1024, height: 800 },
    { 'emb.runs.list': 'closed', 'emb.obs.split': '35' },
  );
  const rows = page.locator('[role="grid"] [role="row"][data-seq]');
  await expect(rows.first()).toBeVisible();

  const text = rows.first().locator('> [role="gridcell"]');
  const [, , event, element, summary] = await widths(text);
  for (const w of [event, element, summary]) expect(w).toBeGreaterThanOrEqual(96);
  await noPageScroll(page);
});
