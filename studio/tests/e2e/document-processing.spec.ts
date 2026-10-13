import { readFile } from 'node:fs/promises';

import { expect, test } from '@playwright/test';

import { GENERATED_DEFINITIONS_PATH, type GeneratedDefinitions } from './global-setup';
import { details, eventList, listedSeqs, runStatus } from './run-view';

/**
 * Document Processing: a fully synchronous Run. Every Node in
 * this Definition is SYNC, so the Run is expected to reach COMPLETED without ever passing
 * through WAITING_CALLBACK - this is the scenario's own acceptance shape, not a shortcut
 * taken by the test.
 */
test('Document Processing completes synchronously and shows its output', async ({ page }) => {
  const generated = JSON.parse(
    await readFile(GENERATED_DEFINITIONS_PATH, 'utf-8'),
  ) as GeneratedDefinitions;
  const { workflowId } = generated.documentProcessing;

  // Definition creation happened in global-setup via a direct POST (not through the UI);
  // this is the first UI interaction, matching "start a Run from Studio's Run Input
  // Dialog" (item 1: build and run a scenario from Definitions/Studio).
  await page.goto(`/workflows/${workflowId}`);

  const runButton = page.getByRole('button', { name: 'Run', exact: true });
  await expect(runButton).toBeEnabled();
  await runButton.click();

  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();

  // The Run Input Dialog's field label is the Text Input node's configured `label`
  // ("Document"), not its `inputKey` ("document") - RunInputDialog.tsx reads it through
  // runInputLabels from the Definition's Input nodes, and the key stays the submitted field
  // name. Item 2: the actual input is written to the Run, never to the Definition.
  await dialog.getByLabel('Document').fill('Emberling keeps every execution fact in PostgreSQL.');
  await dialog.getByRole('button', { name: 'Start run' }).click();

  // Run creation opens the new run on the workflow's Runs tab.
  await page.waitForURL(/\/runs\//);

  // The run view has no step palette or edit form: those belong to the Editor only.
  await expect(page.getByText('NODE REGISTRY')).toHaveCount(0);
  await expect(page.getByText('PROPERTIES, DEFINITION')).toHaveCount(0);

  await expect(runStatus(page)).toHaveText('Completed', { timeout: 30_000 });
  // The run is bound to one Definition version, shown in its overview.
  await expect(details(page).getByText('Workflow version', { exact: true })).toBeVisible();

  // The Events view is exactly the committed Event list, in order.
  const events = await eventList(page);
  const seqs = await listedSeqs(events);
  expect(seqs).toEqual(seqs.map((_, index) => index + 1));
  await expect(events.getByRole('row', { name: /Run started/ })).toHaveCount(1);
  await expect(events.getByRole('row', { name: /Run completed/ })).toHaveCount(1);

  // The run's output, formed once its Output step succeeded.
  await details(page).getByRole('tab', { name: 'Output' }).click();
  const output = details(page).getByRole('tabpanel');
  await expect(output.getByText('No output.')).toHaveCount(0);
  await expect(output).toContainText(/\S/);
});
