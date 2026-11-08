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
  await expect(page.getByRole('treeitem').first()).toBeVisible();
  await expect(card(page, 'node_output')).toBeVisible();
};

/** The details pane on the element whose card is named, or the run when none is. */
async function details(page: Page, cardName: RegExp | null, tab: string) {
  const selected = page.getByRole('tab', { name: tab, selected: true });
  if (!(await selected.isVisible())) {
    if (cardName) await page.getByRole('treeitem', { name: cardName }).first().click();
    await page.getByRole('tab', { name: tab }).click();
  }
  await expect(selected).toBeVisible();
}

/** The details of the element the address selects. */
async function selectedDetails(page: Page, tab: string) {
  const selected = page.getByRole('tab', { name: tab, selected: true });
  if (!(await selected.isVisible())) await page.getByRole('tab', { name: tab }).click();
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
      await expect(card(page, 'node_agent')).toContainText('Answered with 3 images');
      await expect(card(page, 'node_agent')).toContainText('Finished in turn 9');
      await expect(card(page, 'node_result')).toContainText('2 of 3 photos passed review');
      await expect(card(page, 'node_output')).toContainText('Final image, JPEG');
      await expect(page.getByRole('tree').getByRole('button', { name: /^Open/ })).toHaveCount(0);
    },
  },
  {
    name: 'run-media-lightbox',
    url: runPath(photoRun, '?tab=data'),
    mode: 'block',
    overrides: photoRunOverrides(),
    ready: graphReady,
    act: async (page) => {
      await details(page, null, 'Data');
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
    name: 'run-media-lightbox-close',
    url: runPath(photoRun, '?el=node_result&tab=data'),
    mode: 'block',
    overrides: photoRunOverrides(),
    ready: graphReady,
    act: async (page) => {
      const box = page.getByRole('dialog');
      const agent = card(page, 'node_agent');
      await details(page, /Media Result/, 'Data');
      const opens = page.getByRole('tabpanel').getByRole('button', { name: /^Open/ });
      await opens.nth(1).click();
      await expect(box).toContainText(/2 of \d+/);
      // A click outside the lightbox neither closes it nor changes the selection.
      await page.mouse.click(8, 8);
      await expect(box).toBeVisible();
      await box.getByRole('button', { name: 'Close' }).click();
      await expect(box).toBeHidden();
      await expect(agent).toHaveAttribute('aria-selected', 'false');
      await opens.first().click();
      await expect(box).toBeVisible();
      await page.keyboard.press('Escape');
      await expect(box).toBeHidden();
      await expect(card(page, 'node_result')).toHaveAttribute('aria-selected', 'true');
    },
  },
  {
    name: 'run-media-step-output',
    url: runPath(photoRun, '?el=node_result&tab=data'),
    mode: 'block',
    overrides: photoRunOverrides(),
    ready: graphReady,
    act: async (page) => {
      await details(page, /Media Result/, 'Data');
      const panel = page.getByRole('tabpanel');
      await expect(panel.getByRole('button', { name: /^Open/ }).first()).toBeVisible();
      await expect(panel).toContainText('Generated from photo 1');
      await expect(panel).toContainText('Review');
      await expect(panel).toContainText('2 of 3 photos');
      await expect(panel).toContainText('Did not pass');
      // The JSON is one click away, collapsed.
      await expect(panel.locator('details[open]', { hasText: 'JSON' })).toHaveCount(0);
    },
  },
  {
    name: 'run-media-run-input',
    url: runPath(photoRun, '?tab=data'),
    mode: 'block',
    overrides: photoRunOverrides(),
    ready: graphReady,
    act: async (page) => {
      await details(page, null, 'Data');
      const panel = page.getByRole('tabpanel');
      const input = panel
        .locator('section')
        .filter({ has: page.getByRole('heading', { name: 'Input', exact: true }) });
      await expect(input.getByRole('button', { name: /^Open preview: Photo/ })).toHaveCount(3);
      await expect(panel).toContainText('Photo 1');
      await expect(panel).toContainText('Photo 3');
    },
  },
  {
    name: 'run-tool-photo-arguments',
    url: runPath(photoRun, '?el=action_ed283771e2ff4520a503ece37a1395e0&tab=data'),
    mode: 'block',
    overrides: photoRunOverrides(),
    ready: graphReady,
    act: async (page) => {
      await selectedDetails(page, 'Data');
      const panel = page.getByRole('tabpanel');
      await expect(panel).toContainText('Photo 1 of 3');
      await expect(panel.getByRole('button', { name: /^Open preview: Photo/ })).toHaveCount(1);
    },
  },
  {
    name: 'run-tool-delivery-turns',
    url: runPath(data.completed, '?el=node_agent&tab=turns'),
    mode: 'block',
    ready: async (page) => {
      await expect(page.getByRole('treeitem').first()).toBeVisible();
      await expect(card(page, 'node_agent')).toContainText('Deployed v1 to checkout in staging');
    },
    act: async (page) => {
      await selectedDetails(page, 'Turns');
      const panel = page.getByRole('tabpanel');
      await expect(panel).toContainText('Result: Tests passed (12)');
      await expect(panel).toContainText('Result: Deployed v1 to checkout in staging');
    },
  },
  {
    name: 'run-tool-test-result',
    url: runPath(data.completed, '?el=action_37d7842a349c4030b1aba2997c41da5a&tab=data'),
    mode: 'block',
    ready: async (page) => {
      await expect(page.getByRole('treeitem').first()).toBeVisible();
    },
    act: async (page) => {
      await selectedDetails(page, 'Data');
      const panel = page.getByRole('tabpanel');
      await expect(panel).toContainText('Tests passed (12).');
    },
  },
  {
    name: 'run-media-generating',
    url: runPath(generating),
    mode: 'block',
    overrides: { '/api/runs': runList(withRuns([generating.item])), ...generating.overrides },
    ready: async (page) => {
      await expect(page.getByRole('treeitem').first()).toBeVisible();
      await expect(card(page, 'node_agent')).toContainText(
        'Calling generate_image for photo 2 of 3',
      );
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
