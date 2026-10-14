/**
 * The Run dialog over the Editor that opens it, reviewed in block mode, and its text
 * fields sized to what is typed into them.
 */
import { expect, test, type Locator, type Page } from '@playwright/test';

import { data } from '../support/data';
import { review, type ReviewState } from '../support/review';
import { RECORDED_AT, serveFixtures } from '../support/server';

const dialogOpen = async (page: Page) => {
  await expect(page.getByRole('dialog')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Start run' })).toBeVisible();
};

const states: ReviewState[] = [
  {
    name: 'run-dialog-new',
    url: `/workflows/${data.completed.workflowId}?run=1`,
    mode: 'block',
    ready: dialogOpen,
    // Focus is trapped in the dialog on purpose.
    noTabWalk: true,
  },
  {
    name: 'run-dialog-photos',
    url: `/workflows/${data.photos.workflowId}?run=1`,
    mode: 'block',
    ready: dialogOpen,
    noTabWalk: true,
  },
  {
    name: 'run-dialog-errors',
    url: `/workflows/${data.photos.workflowId}?run=1`,
    mode: 'block',
    ready: dialogOpen,
    act: async (page) => {
      await page.getByRole('button', { name: 'Start run' }).click();
      await expect(page.getByRole('alert')).toBeVisible();
    },
    noTabWalk: true,
  },
];

for (const state of states) {
  test(state.name, async ({ page }, testInfo) => {
    await review(page, testInfo, state);
  });
}

const BRIEF =
  'Boxed collectible figure. Turn the person or pet in each photo into a small collectible ' +
  'figure standing inside a clear plastic blister pack, mounted on a printed card backing, ' +
  'with one small prop related to the subject beside the figure in the pack. Keep the ' +
  'subject recognizable: for people keep the face, hair and glasses; for pets keep the fur ' +
  'color and markings. Glossy molded-plastic finish, clean product-photo lighting on a ' +
  'plain backdrop. No brands, logos, text, known characters or artist styles.';

async function openBrief(page: Page): Promise<Locator> {
  await page.clock.setFixedTime(new Date(RECORDED_AT));
  await serveFixtures(page);
  await page.goto(`/workflows/${data.photos.workflowId}?run=1`);
  await dialogOpen(page);
  const brief = page.getByRole('dialog').getByRole('textbox', { name: /brief/i });
  await brief.fill('');
  return brief;
}

const measure = (field: Locator) =>
  field.evaluate((el) => ({
    client: el.clientHeight,
    scroll: el.scrollHeight,
    line: parseFloat(getComputedStyle(el).lineHeight),
  }));

test('a text field starts at about four lines and grows to show a whole brief', async ({
  page,
}) => {
  const brief = await openBrief(page);
  const empty = await measure(brief);
  expect(empty.client).toBeGreaterThanOrEqual(4 * empty.line);

  await brief.fill(BRIEF);
  const full = await measure(brief);
  expect(full.scroll).toBeLessThanOrEqual(full.client + 1);
  await expect(page.getByRole('button', { name: 'Start run' })).toBeInViewport();
});

test('a text field stops growing at its cap and the footer stays in view', async ({ page }) => {
  const brief = await openBrief(page);
  await brief.fill(Array.from({ length: 6 }, () => BRIEF).join('\n\n'));
  const capped = await measure(brief);
  expect(capped.scroll).toBeGreaterThan(capped.client + 1);
  expect(capped.client).toBeLessThanOrEqual(16 * capped.line + 16);
  await expect(page.getByRole('button', { name: 'Start run' })).toBeInViewport();
  await expect(page.getByRole('button', { name: 'Cancel' })).toBeInViewport();
});
