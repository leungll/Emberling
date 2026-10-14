import { readFile } from 'node:fs/promises';

import { expect, test } from '@playwright/test';

import { GENERATED_DEFINITIONS_PATH, type GeneratedDefinitions } from './global-setup';
import { details, eventList, runStatus, stepCard } from './run-view';

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
 * (`backend/internal/fixture/mockprovider/server.go`'s `Dispatcher.Schedule`), so this test posts
 * nothing itself and only waits on state.
 */
test('AIGC Media Generation waits for an external callback, then completes with an image preview', async ({
  page,
}) => {
  const generated = JSON.parse(
    await readFile(GENERATED_DEFINITIONS_PATH, 'utf-8'),
  ) as GeneratedDefinitions;
  const { workflowId } = generated.aigcMedia;

  await page.goto(`/workflows/${workflowId}`);

  const runButton = page.getByRole('button', { name: 'Run', exact: true });
  await expect(runButton).toBeEnabled();
  await runButton.click();

  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Prompt').fill('mock:delay:2000');
  await dialog.getByRole('button', { name: 'Start run' }).click();

  await page.waitForURL(/\/runs\//);

  const status = runStatus(page);
  // The Run's own status must not read PAUSED merely because one NodeRun is
  // WAITING_CALLBACK while others are still READY or RUNNING; the header shows the Backend's
  // status, so asserting it asserts the server's aggregation, not Studio's guess.
  await expect(status).toHaveText('Waiting', { timeout: 15_000 });

  // While PAUSED, the service, its task, and how long the step has waited are shown for
  // the waiting step, which the run opens on.
  await expect(stepCard(page, 'Image')).toHaveAccessibleName(/, Waiting for reply,/);
  await expect(page.getByText(/^Image is waiting for Mock task provider\./)).toBeVisible();
  const waiting = details(page);
  await expect(waiting.getByText('External task', { exact: true })).toBeVisible();
  await expect(waiting.getByText('Since', { exact: true })).toBeVisible();

  // The Mock Provider's own scheduled callback (not this test) resolves WAITING_CALLBACK.
  await expect(status).toHaveText('Completed', { timeout: 30_000 });
  const events = await eventList(page);
  await expect(events.getByRole('row', { name: /Run completed/ })).toHaveCount(1);

  // The step's output names the image the Provider reported; the callback payload always
  // reports an outside image (an EXTERNAL reference), previewed from its own address.
  await stepCard(page, 'Image').click();
  await details(page).getByRole('tab', { name: 'Output' }).click();
  const preview = details(page).getByRole('img', {
    name: 'Preview: Image from an outside service',
  });
  await expect(preview).toBeVisible({ timeout: 10_000 });
  await expect(preview).toHaveJSProperty('complete', true);
  const naturalWidth = await preview.evaluate((img) => (img as HTMLImageElement).naturalWidth);
  expect(naturalWidth).toBeGreaterThan(0);
});
