/**
 * The pages as they stand before the redesign, reviewed in report mode: findings are
 * written to the report without failing the run. Each page moves to block mode when its
 * redesigned version lands.
 */
import { expect, test } from '@playwright/test';

import { data } from '../support/data';
import { review, type ReviewState } from '../support/review';

const states: ReviewState[] = [
  {
    name: 'legacy-definitions',
    url: '/workflows',
    mode: 'report',
    ready: async (page) => {
      await expect(page.getByRole('table')).toBeVisible();
    },
  },
  {
    name: 'legacy-editor',
    url: `/workflows/${data.completed.workflowId}`,
    mode: 'report',
    ready: async (page) => {
      await expect(page.locator('.react-flow__node').first()).toBeVisible();
    },
  },
  {
    name: 'legacy-observe-failed',
    url: `/workflows/${data.failed.workflowId}/runs/${data.failed.runId}`,
    mode: 'report',
    ready: async (page) => {
      await expect(page.locator('.react-flow__node').first()).toBeVisible();
    },
  },
];

for (const state of states) {
  test(state.name, async ({ page }, testInfo) => {
    await review(page, testInfo, state);
  });
}
