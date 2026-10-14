/**
 * Images in a run: the run graph's cards say what each step did and show its images, any
 * thumbnail opens the one lightbox, and the details show images before their JSON.
 */
import { expect, test, type Page } from '@playwright/test';

import { photoRun, photoRunOverrides } from '../support/photo-run';
import { review, type ReviewState } from '../support/review';

const runPath = (run: { workflowId: string; runId: string }, search = '') =>
  `/workflows/${run.workflowId}/runs/${run.runId}${search}`;

const card = (page: Page, nodeId: string) => page.locator(`[data-gid="${nodeId}"]`);

const graphReady = async (page: Page) => {
  await expect(page.getByRole('treegrid')).toBeVisible();
  await expect(card(page, 'node_output')).toBeVisible();
};

/** The details pane, opening it as a drawer where the logs are narrow. */
async function details(page: Page, rowName: RegExp, tab: string) {
  const selected = page.getByRole('tab', { name: tab, selected: true });
  if (!(await selected.isVisible())) {
    await page.getByRole('row', { name: rowName }).first().click();
    await page.getByRole('tab', { name: tab }).click();
  }
  await expect(selected).toBeVisible();
}

const states: ReviewState[] = [
  {
    name: 'run-media-cards',
    url: runPath(photoRun),
    mode: 'block',
    overrides: photoRunOverrides(),
    ready: async (page) => {
      await graphReady(page);
      await expect(card(page, 'node_brief')).toContainText('Boxed collectible figure.');
      await expect(card(page, 'node_photos')).toContainText('3 photos');
      await expect(card(page, 'node_photos').getByRole('button', { name: /^Open/ })).toHaveCount(3);
      await expect(card(page, 'node_agent')).toContainText('Answered with 3 images');
      await expect(card(page, 'node_agent')).toContainText('Finished in turn 9');
      await expect(card(page, 'node_result')).toContainText('2 of 3 photos passed review');
      await expect(card(page, 'node_output')).toContainText('Final image, JPEG');
      await expect(card(page, 'node_output').getByRole('button', { name: /^Open/ })).toHaveCount(1);
    },
  },
  {
    name: 'run-media-lightbox',
    url: runPath(photoRun, '?tab=input'),
    mode: 'block',
    overrides: photoRunOverrides(),
    ready: graphReady,
    act: async (page) => {
      await details(page, /^Completed Run\b/, 'Input');
      await page.getByRole('tabpanel').getByRole('button', { name: /^Open/ }).first().click();
      const box = page.getByRole('dialog');
      await expect(box).toContainText('Photo 1');
      await expect(box).toContainText('1 of 3');
      await page.keyboard.press('ArrowRight');
      await expect(box).toContainText('Photo 2');
      await expect(box).toContainText('2 of 3');
    },
    // Focus stays inside the open lightbox on purpose.
    noTabWalk: true,
  },
  {
    name: 'run-media-step-output',
    url: runPath(photoRun, '?el=node_result&tab=output'),
    mode: 'block',
    overrides: photoRunOverrides(),
    ready: graphReady,
    act: async (page) => {
      await details(page, /Media Result/, 'Output');
      const panel = page.getByRole('tabpanel');
      await expect(panel.getByRole('button', { name: /^Open/ }).first()).toBeVisible();
      await expect(panel).toContainText('Generated from photo 1');
      await expect(panel).toContainText('Review');
      await expect(panel).toContainText('2 of 3 photos');
      await expect(panel).toContainText('Did not pass');
      // The JSON is one click away, collapsed.
      const json = panel.locator('details', { hasText: 'JSON' });
      await expect(json).not.toHaveAttribute('open');
    },
  },
  {
    name: 'run-media-run-input',
    url: runPath(photoRun, '?tab=input'),
    mode: 'block',
    overrides: photoRunOverrides(),
    ready: graphReady,
    act: async (page) => {
      await details(page, /^Completed Run\b/, 'Input');
      const panel = page.getByRole('tabpanel');
      await expect(panel.getByRole('button', { name: /^Open/ })).toHaveCount(3);
      await expect(panel).toContainText('Photo 1');
      await expect(panel).toContainText('Photo 3');
    },
  },
];

for (const state of states) {
  test(state.name, async ({ page }, testInfo) => {
    await review(page, testInfo, state);
  });
}
