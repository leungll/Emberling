import { readFile } from 'node:fs/promises';

import { expect, test } from '@playwright/test';

import { GENERATED_DEFINITIONS_PATH, type GeneratedDefinitions } from './global-setup';

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
  await page.goto(`/studio/${workflowId}`);

  const runButton = page.getByRole('button', { name: 'Run', exact: true });
  await expect(runButton).toBeEnabled();
  await runButton.click();

  const dialog = page.getByRole('dialog', { name: 'Run workflow' });
  await expect(dialog).toBeVisible();

  // The Run Input Dialog's field label is the Text Input node's configured `label`
  // ("Document"), not its `inputKey` ("document") - RunInputDialog.tsx reads it through
  // runInputLabels from the Definition's Input nodes, and the key stays the submitted field
  // name. Item 2: the actual input is written to the Run, never to the Definition.
  await dialog.getByLabel('Document').fill('Emberling keeps every execution fact in PostgreSQL.');
  await dialog.getByRole('button', { name: 'Create run' }).click();

  // Item 4: Run creation switches Studio from edit to observe mode.
  await page.waitForURL(/\/runs\//);

  // Item 5: observe mode hides the Palette and edit forms; item 6: the bound Definition
  // version is shown throughout (asserted via the header's "v<version>" text below).
  await expect(page.getByText(`${workflowId} · v`)).toBeVisible();

  // Observe must not render the Palette or the node config/edit form.
  // NodePalette.tsx and PropertiesPanel.tsx render "NODE REGISTRY" and "PROPERTIES ·
  // DEFINITION" headings respectively and are only ever mounted by EditPage, never by
  // ObservePage - so their absence here proves the edit-only surfaces were never rendered,
  // not merely hidden by CSS.
  await expect(page.getByText('NODE REGISTRY')).toHaveCount(0);
  await expect(page.getByText('PROPERTIES · DEFINITION')).toHaveCount(0);

  const status = page.getByTestId('run-status');
  await expect(status).toHaveText('Completed', { timeout: 30_000 });

  // Item 7: Canvas/Trace/Detail render server facts; the Timeline is exactly the committed
  // Event list, in order, never reordered or synthesised (EventTimeline.tsx).
  await expect(page.getByRole('button', { name: /Run created/ })).toBeVisible();
  await expect(page.getByRole('button', { name: /Run completed/ })).toBeVisible();

  // Detail shows the Run's output (RunSummary's "Output" JsonBlock, visible by default
  // when no NodeRun or Event is selected).
  await expect(page.getByText('Output', { exact: true })).toBeVisible();
  const outputBlock = page.locator('pre').last();
  await expect(outputBlock).not.toHaveText('null');
});
