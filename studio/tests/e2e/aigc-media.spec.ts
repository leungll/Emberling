import { readFile } from 'node:fs/promises';

import { expect, test } from '@playwright/test';

import { GENERATED_DEFINITIONS_PATH, type GeneratedDefinitions } from './global-setup';

/**
 * AIGC Media Generation: the Image
 * Generation NodeRun must visibly pass through WAITING_CALLBACK before the Run completes.
 *
 * The Run input is the literal string `mock:delay:2000`. It reaches
 * `backend/internal/adapters/mocktask/adapter.go`'s directive parser unmodified because
 * this Definition (tests/e2e/fixtures.ts's `aigcMedia`) wires `text_input` straight to
 * `image_generation.prompt`, bypassing `prompt_template`/`text_generation` (whose default
 * `mockmodel` echo would otherwise destroy the leading "mock:" prefix). The Mock Provider
 * schedules its own callback after the delay
 * (`backend/internal/mockprovider/server.go`'s `Dispatcher.Schedule`), so this test posts
 * nothing itself and only waits on state.
 */
test('AIGC Media Generation waits for an external callback, then completes with an image preview', async ({
  page,
}) => {
  const generated = JSON.parse(
    await readFile(GENERATED_DEFINITIONS_PATH, 'utf-8'),
  ) as GeneratedDefinitions;
  const { workflowId } = generated.aigcMedia;

  await page.goto(`/studio/${workflowId}`);

  const runButton = page.getByRole('button', { name: 'Run', exact: true });
  await expect(runButton).toBeEnabled();
  await runButton.click();

  const dialog = page.getByRole('dialog', { name: 'Run workflow' });
  await dialog.getByLabel('Prompt').fill('mock:delay:2000');
  await dialog.getByRole('button', { name: 'Create run' }).click();

  await page.waitForURL(/\/runs\//);

  const status = page.getByTestId('run-status');
  // Item 8: the Run's own status must not read PAUSED merely because one NodeRun is
  // WAITING_CALLBACK while others are still READY/RUNNING; RUN_STATUS_LABELS maps PAUSED
  // to this exact string, so asserting it here is asserting the server's aggregation, not
  // Studio's guess (ObserveHeader.tsx renders `run.status` verbatim).
  await expect(status).toHaveText('Waiting for external result', { timeout: 15_000 });

  // Item 9: while PAUSED, Provider/External Task ID/waiting time/persisted state must be
  // visible for the waiting NodeRun.
  const imageNodeButton = page.getByRole('button', { name: /node_image/ });
  await expect(imageNodeButton).toBeVisible();
  await expect(imageNodeButton).toContainText('Waiting for callback');
  await imageNodeButton.click();

  // Exact match: the Timeline's NODE_DISPATCHED entry reads "Dispatched to provider",
  // which a plain substring match for "Provider" also resolves (case-insensitively).
  await expect(page.getByText('Provider', { exact: true })).toBeVisible();
  await expect(page.getByText('External Task ID')).toBeVisible();
  // The Attempts list below repeats the same provider id, so this field is not unique on
  // the page; asserting the first match is enough to prove the value renders at all.
  await expect(page.getByText(/mock-task-provider/).first()).toBeVisible();
  await expect(page.getByText('Waiting since')).toBeVisible();
  await expect(page.getByText('Runtime State')).toBeVisible();

  // The Mock Provider's own scheduled callback (not this test) resolves WAITING_CALLBACK.
  await expect(status).toHaveText('Completed', { timeout: 30_000 });
  await expect(page.getByRole('button', { name: /Run completed/ })).toBeVisible();

  // `node_image` (Image Generation) is still selected from above; DetailPanel looks it up
  // by id regardless of its current status (RunRail only filters which nodes it *lists*,
  // never what ObservePage can still select), so its NodeRunDetailView re-fetches and
  // renders the completed output - including the preview - without any further click.
  // `DetailPanel.tsx`'s `ImagePreview` renders `alt={\`${image.source} image preview\`}`;
  // the Mock Provider's callback payload always reports `domain.ImageSourceExternal`
  // (`backend/internal/adapters/mocktask/adapter.go`'s `successPayload`), so the alt text
  // is the literal string below, not a per-image path.
  const preview = page.getByAltText('EXTERNAL image preview');
  await expect(preview).toBeVisible({ timeout: 10_000 });
  await expect(preview).toHaveJSProperty('complete', true);
  const naturalWidth = await preview.evaluate((img) => (img as HTMLImageElement).naturalWidth);
  expect(naturalWidth).toBeGreaterThan(0);
});
