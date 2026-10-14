/**
 * A workflow's Runs tab: the workflow's runs beside the open run, with its graph, trace,
 * events and details, in every run state the server reports, reviewed in block mode.
 */
import { expect, test, type Page } from '@playwright/test';

import { data } from '../support/data';
import { partwayRun, runList, withRuns } from '../support/runs';
import { review, type ReviewState } from '../support/review';
import { errorBody } from '../support/server';

const DELIVERY = 'run_d9c23ab668c54acf9ec83c59c55ce742';
const PHOTO_LIMIT = {
  workflowId: 'wf_8c67ac3a5fcc482d8ca1bf8bdbdd732b',
  runId: 'run_f7c0377afa8541eb86fa06ee0ec41ae2',
};

const waiting = partwayRun(DELIVERY, 13, 'waiting', 4);
const live = partwayRun(DELIVERY, 19, 'running', 2);

const runPath = (run: { workflowId: string; runId: string }, search = '') =>
  `/workflows/${run.workflowId}/runs/${run.runId}${search}`;

const runReady = async (page: Page) => {
  await expect(page.getByRole('treegrid')).toBeVisible();
  await expect(page.getByRole('listbox', { name: /runs/i })).toBeVisible();
};

const states: ReviewState[] = [
  {
    name: 'workflow-runs-completed',
    url: runPath(data.completed),
    mode: 'block',
    ready: runReady,
  },
  {
    name: 'workflow-runs-failed',
    url: runPath(data.failed),
    mode: 'block',
    ready: async (page) => {
      await runReady(page);
      await expect(page.getByRole('alert')).toContainText('service call failed');
    },
  },
  {
    name: 'workflow-runs-agent-failed',
    url: runPath(PHOTO_LIMIT),
    mode: 'block',
    ready: async (page) => {
      await runReady(page);
      await expect(page.getByRole('alert')).toContainText('reached its generation limit');
    },
  },
  {
    name: 'workflow-runs-waiting',
    url: runPath(waiting),
    mode: 'block',
    overrides: { '/api/runs': runList(withRuns([waiting.item])), ...waiting.overrides },
    ready: async (page) => {
      await runReady(page);
      await expect(page.getByText(/is waiting for Sandbox runner\./)).toBeVisible();
    },
  },
  {
    name: 'workflow-runs-live',
    url: runPath(live),
    mode: 'block',
    overrides: { '/api/runs': runList(withRuns([live.item])), ...live.overrides },
    ready: runReady,
  },
  {
    name: 'workflow-runs-reconnecting',
    url: runPath(live),
    mode: 'block',
    overrides: {
      '/api/runs': runList(withRuns([live.item])),
      ...live.overrides,
      [`/api/runs/${live.runId}/events`]: (url, method) => {
        const history = live.overrides[`/api/runs/${live.runId}/events`];
        if (url.searchParams.has('limit') && typeof history === 'function')
          return history(url, method);
        return { status: 503, body: errorBody('UNAVAILABLE', 'stream unavailable') };
      },
    },
    ready: async (page) => {
      await runReady(page);
      await expect(page.getByText(/^Reconnecting\./)).toBeVisible();
    },
    // The browser logs the failed event stream this state asks for.
    skip: ['console'],
  },
  {
    name: 'workflow-runs-events',
    url: runPath(data.photos, '?view=events'),
    mode: 'block',
    ready: async (page) => {
      await expect(page.getByRole('grid')).toBeVisible();
    },
  },
  {
    name: 'workflow-runs-turns',
    url: runPath(data.photos, '?el=node_agent&tab=turns'),
    mode: 'block',
    ready: runReady,
    act: async (page) => {
      // Where the logs are narrow the details open as a drawer when a row is chosen.
      const turns = page.getByRole('tab', { name: 'Turns', selected: true });
      if (!(await turns.isVisible())) {
        await page.getByRole('row', { name: /Effect Template Agent/ }).click();
      }
      await expect(turns).toBeVisible();
      await page.getByRole('button', { name: /^Show each of the/ }).click();
      await expect(page.getByRole('button', { name: /^Turn 1\b/ })).toBeVisible();
      // The click scrolled the details; the review checks them from the top.
      await page.locator('[data-details] [role="tabpanel"]').evaluate((panel) => {
        panel.scrollTop = 0;
      });
    },
  },
  {
    name: 'workflow-runs-loading',
    url: runPath(data.completed),
    mode: 'block',
    overrides: { [`/api/runs/${data.completed.runId}`]: { hang: true } },
    ready: async (page) => {
      await expect(page.locator('[aria-busy="true"]').first()).toBeVisible();
    },
  },
  {
    name: 'workflow-runs-error',
    url: runPath(data.completed),
    mode: 'block',
    overrides: {
      [`/api/runs/${data.completed.runId}`]: {
        status: 503,
        body: errorBody('UNAVAILABLE', 'database unavailable'),
      },
    },
    ready: async (page) => {
      await expect(page.getByRole('button', { name: 'Retry' }).first()).toBeVisible();
    },
    // The browser logs the 503 this state asks for.
    skip: ['console'],
  },
  {
    name: 'workflow-runs-missing-run',
    url: runPath({ workflowId: data.completed.workflowId, runId: data.missingRunId }),
    mode: 'block',
    overrides: {
      [`/api/runs/${data.missingRunId}`]: {
        status: 404,
        body: errorBody('NOT_FOUND', 'run not found'),
      },
    },
    ready: async (page) => {
      await expect(page.getByText(/doesn’t exist/).first()).toBeVisible();
    },
    // The browser logs the 404 this state asks for.
    skip: ['console'],
  },
  {
    name: 'workflow-runs-none',
    url: `/workflows/${data.completed.workflowId}/runs`,
    mode: 'block',
    overrides: { '/api/runs': runList([]) },
    ready: async (page) => {
      await expect(page.getByText('This workflow hasn’t run yet.').first()).toBeVisible();
    },
  },
];

for (const state of states) {
  test(state.name, async ({ page }, testInfo) => {
    await review(page, testInfo, state);
  });
}
