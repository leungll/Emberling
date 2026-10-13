/**
 * The Workflows list, reviewed in block mode: the recorded workflows, search, the tabs, the
 * row menu, the Run and New workflow dialogs, and the empty, error and loading states.
 */
import { expect, test, type Page } from '@playwright/test';

import { data } from '../support/data';
import { review, type ReviewState } from '../support/review';
import { errorBody } from '../support/server';

const listReady = async (page: Page) => {
  await expect(page.getByRole('heading', { name: 'Workflows' })).toBeVisible();
  await expect(page.locator('tbody tr[data-row]').first()).toBeVisible();
};

const firstMenu = (page: Page) =>
  page.locator('tbody tr[data-row]').first().locator('[data-row-menu]');

const states: ReviewState[] = [
  { name: 'workflows-list', url: '/workflows', mode: 'block', ready: listReady },
  {
    name: 'workflows-search',
    url: '/workflows?q=photo',
    mode: 'block',
    ready: async (page) => {
      await listReady(page);
      await expect(page.locator('tbody mark').first()).toBeVisible();
    },
  },
  {
    name: 'workflows-search-id',
    url: `/workflows?q=${data.completed.workflowId.slice(3, 9)}`,
    mode: 'block',
    ready: async (page) => {
      await listReady(page);
      await expect(page.locator('tbody mark').first()).toBeVisible();
    },
  },
  {
    name: 'workflows-no-match',
    url: '/workflows?q=zzzz',
    mode: 'block',
    ready: async (page) => {
      await expect(page.getByText('No workflows match “zzzz”.')).toBeVisible();
    },
  },
  {
    name: 'workflows-failed-tab',
    url: '/workflows?tab=failed',
    mode: 'block',
    ready: async (page) => {
      await listReady(page);
      await expect(
        page.getByRole('tab', { name: /Last run failed/, selected: true }),
      ).toBeVisible();
    },
  },
  {
    name: 'workflows-tab-empty',
    url: '/workflows?tab=never',
    mode: 'block',
    ready: async (page) => {
      await expect(page.getByText('Every workflow has run at least once.')).toBeVisible();
    },
  },
  {
    name: 'workflows-row-menu',
    url: '/workflows',
    mode: 'block',
    ready: listReady,
    act: async (page) => {
      await firstMenu(page).click();
      await expect(page.getByRole('menuitem', { name: 'Run', exact: true })).toBeVisible();
    },
    // An open menu closes when focus leaves it; the closed page has its own walk.
    noTabWalk: true,
  },
  {
    name: 'workflows-run-dialog',
    url: '/workflows',
    mode: 'block',
    ready: listReady,
    act: async (page) => {
      const row = page.locator(`tr[data-row="${data.completed.workflowId}"]`);
      await row.locator('[data-row-menu]').click();
      await page.getByRole('menuitem', { name: 'Run', exact: true }).click();
      await expect(page.getByRole('dialog')).toBeVisible();
      await expect(page.getByRole('button', { name: 'Start run' })).toBeVisible();
      await expect(page.getByText('Loading the run form…')).toHaveCount(0);
      await expect(page.getByRole('dialog').getByRole('textbox').first()).toBeFocused();
    },
    // Focus is trapped in the dialog on purpose.
    noTabWalk: true,
  },
  {
    name: 'workflows-new-dialog',
    url: '/workflows',
    mode: 'block',
    ready: listReady,
    act: async (page) => {
      await page.getByRole('banner').getByRole('button', { name: 'New workflow' }).click();
      await page.getByRole('button', { name: 'Create' }).click();
      await expect(page.getByText('Enter a name.')).toBeVisible();
    },
    noTabWalk: true,
  },
  {
    name: 'workflows-empty',
    url: '/workflows',
    mode: 'block',
    overrides: { '/api/definitions': { status: 200, body: { items: [] } } },
    ready: async (page) => {
      await expect(page.getByText('No workflows yet.')).toBeVisible();
    },
  },
  {
    name: 'workflows-error',
    url: '/workflows',
    mode: 'block',
    overrides: {
      '/api/definitions': {
        status: 503,
        body: errorBody('DEPENDENCY_UNAVAILABLE', 'PostgreSQL is unavailable'),
      },
    },
    ready: async (page) => {
      await expect(page.getByText('Couldn’t load workflows.')).toBeVisible();
    },
    // The browser logs the 503 this state asks for.
    skip: ['console'],
  },
  {
    name: 'workflows-loading',
    url: '/workflows',
    mode: 'block',
    overrides: { '/api/definitions': { hang: true } },
    ready: async (page) => {
      await expect(page.locator('[aria-busy="true"] .animate-skeleton').first()).toBeVisible();
    },
  },
];

for (const state of states) {
  test(state.name, async ({ page }, testInfo) => {
    await review(page, testInfo, state);
  });
}
