/**
 * The Runs page: every run of every workflow from the server's run list, with its status
 * tabs, filters, paging and every list state, reviewed in block mode.
 */
import { expect, test, type Page } from '@playwright/test';

import { everyRun, partwayRun, runList, withRuns } from '../support/runs';
import { review, type ReviewState } from '../support/review';
import { errorBody } from '../support/server';

const DELIVERY = 'run_d9c23ab668c54acf9ec83c59c55ce742';

const tableReady = async (page: Page) => {
  await expect(page.getByRole('heading', { name: 'Runs' })).toBeVisible();
  await expect(page.locator('tbody tr[data-row]').first()).toBeVisible();
};

const waiting = partwayRun(DELIVERY, 13, 'waiting', 4);
const live = partwayRun(DELIVERY, 19, 'running', 2);

const empty = {
  status: 200,
  body: {
    items: [],
    nextCursor: null,
    prevCursor: null,
    counts: { all: 0, running: 0, paused: 0, completed: 0, failed: 0 },
  },
};

const states: ReviewState[] = [
  {
    name: 'runs-list',
    url: '/runs',
    mode: 'block',
    overrides: { '/api/runs': runList(withRuns([waiting.item])) },
    ready: tableReady,
  },
  {
    name: 'runs-live',
    url: '/runs',
    mode: 'block',
    overrides: { '/api/runs': runList(withRuns([live.item])) },
    ready: tableReady,
  },
  {
    name: 'runs-failed-tab',
    url: '/runs?status=failed',
    mode: 'block',
    ready: tableReady,
  },
  {
    name: 'runs-paged',
    url: '/runs',
    mode: 'block',
    overrides: { '/api/runs': runList(everyRun()) },
    ready: async (page) => {
      await tableReady(page);
      await expect(page.getByRole('button', { name: /next/i })).toBeVisible();
    },
  },
  {
    name: 'runs-loading',
    url: '/runs',
    mode: 'block',
    overrides: { '/api/runs': { hang: true } },
    ready: async (page) => {
      await expect(page.locator('[aria-busy="true"]').first()).toBeVisible();
    },
  },
  {
    name: 'runs-error',
    url: '/runs',
    mode: 'block',
    overrides: {
      '/api/runs': { status: 503, body: errorBody('UNAVAILABLE', 'database unavailable') },
    },
    ready: async (page) => {
      await expect(page.getByRole('button', { name: 'Retry' })).toBeVisible();
    },
    // The browser logs the 503 this state asks for.
    skip: ['console'],
  },
  {
    name: 'runs-empty',
    url: '/runs',
    mode: 'block',
    overrides: { '/api/runs': empty },
    ready: async (page) => {
      await expect(page.getByText('No runs yet')).toBeVisible();
    },
  },
  {
    name: 'runs-empty-tab',
    url: '/runs?status=running',
    mode: 'block',
    ready: async (page) => {
      await expect(page.getByRole('button', { name: 'Show all runs' })).toBeVisible();
    },
  },
  {
    name: 'runs-no-match',
    url: '/runs?range=24h',
    mode: 'block',
    ready: async (page) => {
      await expect(page.getByRole('button', { name: 'Clear filters' }).first()).toBeVisible();
    },
  },
  {
    name: 'runs-run-workflow',
    url: '/runs',
    mode: 'block',
    ready: tableReady,
    act: async (page) => {
      await page.getByRole('button', { name: 'Run workflow' }).click();
      await expect(page.getByRole('listbox', { name: /workflow/i })).toBeVisible();
    },
    noTabWalk: true,
  },
];

for (const state of states) {
  test(state.name, async ({ page }, testInfo) => {
    await review(page, testInfo, state);
  });
}
