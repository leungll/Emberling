import { expect, type Locator, type Page } from '@playwright/test';

/**
 * The parts of a workflow's Runs tab the end-to-end specs read: the open run's status, its
 * graph cards, the trace, the events and the details of the selected element.
 */

/** The open run's status, as the Backend reported it. */
export function runStatus(page: Page): Locator {
  return page.getByTestId('run-status');
}

function escaped(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/** A step's card in the run graph; its name carries the step's status. */
export function stepCard(page: Page, name: string): Locator {
  return page.getByRole('treeitem', { name: new RegExp(`^${escaped(name)}, `) });
}

/** The details of the selected element. */
export function details(page: Page): Locator {
  return page.getByRole('complementary', { name: 'Details' });
}

/** The trace with every row open. */
export async function expandedTrace(page: Page): Promise<Locator> {
  await page.getByRole('button', { name: 'Trace options' }).click();
  await page.getByRole('menuitem', { name: 'Expand all' }).click();
  return page.getByRole('treegrid', { name: 'Trace' });
}

/** The Events view's list. */
export async function eventList(page: Page): Promise<Locator> {
  await page.getByRole('radio', { name: /^Events/ }).click();
  const grid = page.getByRole('grid', { name: 'Events' });
  await expect(grid).toBeVisible();
  return grid;
}

/** `seq` of every listed Event, in the order shown. */
export async function listedSeqs(grid: Locator): Promise<number[]> {
  return grid
    .locator('[data-seq]')
    .evaluateAll((rows) => rows.map((row) => Number(row.getAttribute('data-seq'))));
}

/**
 * An image in the details: its thumbnail and its full-size view in the lightbox both load
 * from `src` (when given) and decode to a real size. The lightbox is left open.
 */
export async function expectImageShown(
  page: Page,
  title: string,
  src?: string | RegExp,
): Promise<void> {
  const loaded = async (img: Locator) => {
    await expect(img).toBeVisible({ timeout: 10_000 });
    if (src) await expect(img).toHaveAttribute('src', src);
    await expect(img).toHaveJSProperty('complete', true);
    expect(await img.evaluate((el) => (el as HTMLImageElement).naturalWidth)).toBeGreaterThan(0);
  };
  const thumb = details(page).getByRole('button', {
    name: new RegExp(`^Open preview: ${escaped(title)}\\b`),
  });
  await loaded(thumb.locator('img'));
  await thumb.click();
  await loaded(page.getByRole('dialog', { name: title }).getByRole('img', { name: title }));
}
