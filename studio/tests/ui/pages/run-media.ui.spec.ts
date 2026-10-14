/**
 * Images in a run: the run graph's cards say what each step did and show its images, any
 * thumbnail opens the one lightbox, and the details show images before their JSON.
 */
import { expect, test, type Page } from '@playwright/test';

import { data } from '../support/data';
import { photoRun, photoRunOverrides } from '../support/photo-run';
import { review, type ReviewState } from '../support/review';
import { partwayRun, runList, withRuns } from '../support/runs';

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

/** The photo set run as it stood while its agent was generating an image. */
const generating = partwayRun(data.photos.runId, 24, 'running', 2);

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
  {
    name: 'run-media-generating',
    url: runPath(generating),
    mode: 'block',
    overrides: { '/api/runs': runList(withRuns([generating.item])), ...generating.overrides },
    ready: async (page) => {
      await expect(page.getByRole('treegrid')).toBeVisible();
      await expect(card(page, 'node_agent')).toContainText('Calling generate_image');
      await expect(card(page, 'node_agent')).toContainText('Turn 3');
      await expect(card(page, 'node_result')).toContainText('Not started');
    },
    act: async (page) => {
      // Selecting an earlier step stops following; the header offers to follow again.
      await card(page, 'node_photos').click();
      await expect(page.getByRole('button', { name: 'Follow latest step' })).toBeVisible();
    },
  },
];

for (const state of states) {
  test(state.name, async ({ page }, testInfo) => {
    await review(page, testInfo, state);
  });
}
