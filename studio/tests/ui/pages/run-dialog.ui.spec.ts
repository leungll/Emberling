/**
 * The Run dialog over the page that opens it. The pages behind are still the legacy ones,
 * so these states are reviewed in report mode until those pages move to block mode.
 */
import { expect, test, type Page } from '@playwright/test';

import { data } from '../support/data';
import { review, type ReviewState } from '../support/review';

const dialogOpen = async (page: Page) => {
  await expect(page.getByRole('dialog')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Start run' })).toBeVisible();
};

const states: ReviewState[] = [
  {
    name: 'run-dialog-new',
    url: `/workflows/${data.completed.workflowId}?run=1`,
    mode: 'report',
    ready: dialogOpen,
    // Focus is trapped in the dialog on purpose.
    noTabWalk: true,
  },
  {
    name: 'run-dialog-photos',
    url: `/workflows/${data.photos.workflowId}?run=1`,
    mode: 'report',
    ready: dialogOpen,
    noTabWalk: true,
  },
  {
    name: 'run-dialog-errors',
    url: `/workflows/${data.photos.workflowId}?run=1`,
    mode: 'report',
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
