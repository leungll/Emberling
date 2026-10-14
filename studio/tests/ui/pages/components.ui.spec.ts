/**
 * The shared components, in the development gallery, reviewed in block mode: each overlay
 * is reviewed open as well as closed.
 */
import { expect, test, type Page } from '@playwright/test';

import { review, type ReviewState } from '../support/review';

const GALLERY = '/__ui';
const ready = async (page: Page) => {
  await expect(page.getByRole('heading', { name: 'Components' })).toBeVisible();
};

const states: ReviewState[] = [
  { name: 'components', url: GALLERY, mode: 'block', ready },
  {
    name: 'components-menu',
    url: GALLERY,
    mode: 'block',
    ready,
    act: async (page) => {
      await page.getByRole('button', { name: 'Actions for Release checker' }).click();
      await expect(page.getByRole('menu')).toBeVisible();
    },
    // An open menu holds focus until it closes; the closed page has its own walk.
    noTabWalk: true,
  },
  {
    name: 'components-select',
    url: GALLERY,
    mode: 'block',
    ready,
    act: async (page) => {
      await page.getByRole('button', { name: 'Model: Claude Opus' }).click();
      await expect(page.getByRole('listbox')).toBeVisible();
    },
    noTabWalk: true,
  },
  {
    name: 'components-time-zone',
    url: GALLERY,
    mode: 'block',
    ready,
    act: async (page) => {
      await page.getByRole('button', { name: /Change time zone/ }).click();
      await expect(page.getByRole('listbox', { name: 'Time zones' })).toBeVisible();
    },
    noTabWalk: true,
  },
  {
    name: 'components-dialog',
    url: GALLERY,
    mode: 'block',
    ready,
    act: async (page) => {
      await page.getByRole('button', { name: 'Open dialog' }).click();
      await expect(page.getByRole('dialog', { name: 'Run workflow' })).toBeVisible();
    },
    noTabWalk: true,
  },
  {
    name: 'components-shortcuts',
    url: GALLERY,
    mode: 'block',
    ready,
    act: async (page) => {
      await page.keyboard.press('?');
      await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible();
    },
    noTabWalk: true,
  },
  {
    name: 'components-toast',
    url: GALLERY,
    mode: 'block',
    ready,
    act: async (page) => {
      await page.getByRole('button', { name: 'Toast with action' }).click();
      // Radix also announces the text through a short-lived hidden copy; wait on the toast.
      await expect(page.getByRole('listitem').filter({ hasText: 'Step deleted.' })).toBeVisible();
    },
  },
];

for (const state of states) {
  test(state.name, async ({ page }, testInfo) => {
    await review(page, testInfo, state);
  });
}
