/**
 * The Editor, reviewed in block mode: the overview, step settings for each kind of step,
 * the checks, quick add, the tool picker, a multi-step selection, the zoom levels, a new
 * workflow, the leave guard, and the not-found and load-error states.
 */
import { expect, test, type Page } from '@playwright/test';

import { data } from '../support/data';
import { review, type ReviewState } from '../support/review';
import { errorBody } from '../support/server';

const canvasReady = async (page: Page) => {
  await expect(page.locator('[data-step-card]').first()).toBeVisible();
  await expect(page.getByRole('complementary', { name: 'Side panel' })).toBeVisible();
};

const card = (page: Page, name: string) =>
  page.locator('[data-step-card]').filter({ hasText: name }).first();

const selectStep = (name: string) => async (page: Page) => {
  await card(page, name).locator('[data-card-head]').click();
  await expect(page.getByRole('textbox', { name: 'Step name' })).toHaveValue(name);
};

const editor = `/workflows/${data.completed.workflowId}`;
const photos = `/workflows/${data.photos.workflowId}`;

const states: ReviewState[] = [
  { name: 'editor-overview', url: editor, mode: 'block', ready: canvasReady },
  {
    name: 'editor-agent',
    url: editor,
    mode: 'block',
    ready: canvasReady,
    act: selectStep('Delivery Agent'),
  },
  {
    name: 'editor-agent-sections-open',
    url: editor,
    mode: 'block',
    ready: canvasReady,
    act: async (page) => {
      await selectStep('Delivery Agent')(page);
      await page.getByRole('button', { name: /Data formats/ }).click();
      await page.getByRole('button', { name: /Model behavior/ }).click();
      await expect(page.locator('[data-field="modelConfig"]')).toBeVisible();
    },
  },
  {
    name: 'editor-input',
    url: editor,
    mode: 'block',
    ready: canvasReady,
    act: selectStep('Delivery Task'),
  },
  {
    name: 'editor-output',
    url: editor,
    mode: 'block',
    ready: canvasReady,
    act: selectStep('Deployment Result'),
  },
  {
    name: 'editor-photos',
    url: photos,
    mode: 'block',
    ready: canvasReady,
  },
  {
    name: 'editor-tool-picker',
    url: editor,
    mode: 'block',
    ready: canvasReady,
    act: async (page) => {
      await selectStep('Delivery Agent')(page);
      await page.getByRole('button', { name: 'Add tool' }).click();
      await expect(page.getByRole('dialog', { name: 'Add tools' })).toBeVisible();
    },
    noTabWalk: true,
  },
  {
    name: 'editor-issues',
    url: editor,
    mode: 'block',
    ready: canvasReady,
    act: async (page) => {
      await selectStep('Delivery Agent')(page);
      // Deleting the agent leaves the result step's input unconnected: an issue.
      await page.keyboard.press('Delete');
      await page.getByRole('button', { name: /^\d+ issues?$/ }).click();
      await expect(page.getByRole('dialog', { name: /issue/ })).toBeVisible();
    },
    noTabWalk: true,
  },
  {
    name: 'editor-quick-add',
    url: editor,
    mode: 'block',
    ready: canvasReady,
    act: async (page) => {
      await page.locator('.react-flow__pane').dblclick({ position: { x: 120, y: 520 } });
      await expect(page.getByRole('combobox', { name: /Search steps/ })).toBeVisible();
    },
    noTabWalk: true,
  },
  {
    name: 'editor-multi-select',
    url: editor,
    mode: 'block',
    ready: canvasReady,
    act: async (page) => {
      await page.locator('.react-flow__pane').click({ position: { x: 40, y: 40 } });
      await page.keyboard.press('ControlOrMeta+a');
      await expect(page.getByRole('heading', { name: '3 steps selected' })).toBeVisible();
    },
  },
  {
    name: 'editor-zoomed-out',
    url: editor,
    mode: 'block',
    ready: canvasReady,
    act: async (page) => {
      const out = page.getByRole('button', { name: 'Zoom out' });
      for (let i = 0; i < 4; i += 1) await out.click();
      await expect(page.locator('[data-zoom-level="far"]').first()).toBeVisible();
    },
  },
  {
    name: 'editor-new',
    url: '/workflows/new',
    mode: 'block',
    ready: async (page) => {
      await expect(page.getByRole('button', { name: 'Add first step' })).toBeVisible();
    },
  },
  {
    name: 'editor-leave-guard',
    url: editor,
    mode: 'block',
    ready: canvasReady,
    act: async (page) => {
      await page.getByRole('textbox', { name: 'Workflow name' }).fill('Software Delivery, edited');
      await page
        .getByRole('link', { name: /Workflows/ })
        .first()
        .click();
      await expect(page.getByRole('dialog', { name: 'Leave without saving?' })).toBeVisible();
    },
    noTabWalk: true,
  },
  {
    name: 'editor-not-found',
    url: `/workflows/${data.missingWorkflowId}`,
    mode: 'block',
    ready: async (page) => {
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    },
    skip: ['console'],
    allowUnexpected: /\/api\/definitions\/wf_0+$/,
  },
  {
    name: 'editor-load-error',
    url: editor,
    mode: 'block',
    overrides: {
      [`/api/definitions/${data.completed.workflowId}`]: {
        status: 503,
        body: errorBody('DEPENDENCY_UNAVAILABLE', 'database unavailable'),
      },
    },
    ready: async (page) => {
      await expect(page.getByRole('alert')).toBeVisible();
    },
    skip: ['console'],
  },
];

for (const state of states) {
  test(state.name, async ({ page }, testInfo) => {
    await review(page, testInfo, state);
  });
}
