/**
 * The app shell: the list pages' sidebar (expanded and as the rail), the shortcut sheet,
 * the Backend connection states and the not-found page, reviewed in block mode.
 */
import { expect, test, type Page } from '@playwright/test';

import { data } from '../support/data';
import { review, type ReviewState } from '../support/review';

const runsReady = async (page: Page) => {
  await expect(page.getByRole('heading', { name: 'Runs' })).toBeVisible();
  await expect(page.getByRole('link', { name: /^Workflows: / })).toBeVisible();
  await expect(page.getByText('Connected')).toBeAttached();
};

const states: ReviewState[] = [
  { name: 'shell-runs', url: '/runs', mode: 'block', ready: runsReady },
  {
    // The other sidebar width: the rail at 1440 px, expanded at 1000 px.
    name: 'shell-runs-toggled',
    url: '/runs',
    mode: 'block',
    ready: runsReady,
    act: async (page) => {
      const aside = page.getByRole('complementary', { name: 'Main' });
      const before = await aside.getAttribute('data-state');
      await page.keyboard.press('BracketLeft');
      await expect(aside).not.toHaveAttribute('data-state', before ?? '');
    },
  },
  {
    name: 'shell-shortcuts',
    url: '/runs',
    mode: 'block',
    ready: runsReady,
    act: async (page) => {
      await page.keyboard.press('Shift+Slash');
      await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible();
    },
    // The open sheet traps focus until it closes; the closed page has its own walk.
    noTabWalk: true,
  },
  {
    name: 'shell-backend-starting',
    url: '/runs',
    mode: 'block',
    overrides: { '/ready': { status: 503, body: { status: 'starting' } } },
    ready: async (page) => {
      await expect(page.getByText('Backend starting')).toBeAttached();
    },
    // The browser logs the 503 this state asks for.
    skip: ['console'],
  },
  {
    name: 'shell-not-found',
    url: '/no/such/page',
    mode: 'block',
    ready: async (page) => {
      await expect(page.getByText('This page doesn’t exist.')).toBeVisible();
    },
  },
  {
    name: 'shell-run-not-found',
    url: `/runs/${data.missingRunId}`,
    mode: 'block',
    overrides: {
      [`/api/runs/${data.missingRunId}`]: {
        status: 404,
        body: { error: { code: 'RUN_NOT_FOUND', message: 'run not found' } },
      },
    },
    ready: async (page) => {
      await expect(page.getByText(/^Run .+ doesn’t exist\.$/)).toBeVisible();
    },
    // The browser logs the 404 this state asks for.
    skip: ['console'],
  },
];

for (const state of states) {
  test(state.name, async ({ page }, testInfo) => {
    await review(page, testInfo, state);
  });
}
