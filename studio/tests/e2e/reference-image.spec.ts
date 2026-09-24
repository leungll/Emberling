import { readFile } from 'node:fs/promises';

import { expect, test } from '@playwright/test';

import { GENERATED_DEFINITIONS_PATH, type GeneratedDefinitions } from './global-setup';

// The smallest valid PNG (1x1, RGBA): enough for the Backend to accept it as `image/png`
// and for the browser to decode the preview to a non-zero natural size.
const ONE_PIXEL_PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==',
  'base64',
);

/**
 * AIGC Media Generation with a Reference Image (docs/09 §1 item 13; 04-ux.md §6 items
 * 1, 2, 7): the Run Input Dialog uploads the image through `POST /api/assets` first and
 * only then writes the returned immutable AssetRef into the Run input (RunInputDialog.tsx;
 * the browser-local file is never enough). Observe then shows the Image Input NodeRun's
 * own output: a `source: ASSET` ImageRef, previewed through the Backend's controlled
 * `/api/assets/{id}/content` read - the same Asset, by id, that the dialog reported.
 *
 * The prompt is plain text (no `mock:` directive), so the Mock Provider's callback is
 * immediate and the Run completes on its own.
 */
test('Reference Image upload creates a Run whose Image Input output previews the uploaded Asset', async ({
  page,
}) => {
  const generated = JSON.parse(
    await readFile(GENERATED_DEFINITIONS_PATH, 'utf-8'),
  ) as GeneratedDefinitions;
  const { workflowId } = generated.referenceImage;

  await page.goto(`/studio/${workflowId}`);

  const runButton = page.getByRole('button', { name: 'Run', exact: true });
  await expect(runButton).toBeEnabled();
  await runButton.click();

  const dialog = page.getByRole('dialog', { name: 'Run workflow' });
  await dialog.getByLabel('Prompt').fill('a small ember creature');
  // The file input is labelled by the Image Input node's configured `label`; its accept
  // list is the node's `acceptedMediaTypes`, which the Backend re-validates on upload.
  await dialog.getByLabel('Reference Image').setInputFiles({
    name: 'reference.png',
    mimeType: 'image/png',
    buffer: ONE_PIXEL_PNG,
  });

  // The dialog prints "<assetId> · <sizeBytes> bytes" once the upload has completed; the
  // id is the Backend's, read here so Observe can be checked against the very same Asset.
  const uploaded = dialog.getByText(new RegExp(`^\\S+ · ${ONE_PIXEL_PNG.length} bytes$`));
  await expect(uploaded).toBeVisible({ timeout: 10_000 });
  const assetId = (await uploaded.innerText()).split(' · ')[0];
  expect(assetId).toBeTruthy();

  await dialog.getByRole('button', { name: 'Create run' }).click();
  await page.waitForURL(/\/runs\//);

  const status = page.getByTestId('run-status');
  await expect(status).toHaveText('Completed', { timeout: 30_000 });
  await expect(page.getByRole('button', { name: /Run completed/ })).toBeVisible();

  // Item 7: the Image Input NodeRun's output is a server fact - the canonical ASSET
  // ImageRef `image_input` published - rendered by DetailPanel's ImagePreview as
  // "<source> · <assetId>" beside an `<img alt="ASSET image preview">` whose src is the
  // Backend's own content route. The Run Rail lists only RUNNING / WAITING NodeRuns, so a
  // finished node is selected on the read-only Canvas (ObserveCanvas.tsx maps the
  // Definition node id to its NodeRun).
  const referenceNode = page.locator('.react-flow__node[data-id="node_reference"]');
  await expect(referenceNode).toContainText('Succeeded');
  await referenceNode.click();

  await expect(page.getByText(`ASSET · ${assetId}`)).toBeVisible();
  const preview = page.getByAltText('ASSET image preview');
  await expect(preview).toBeVisible({ timeout: 10_000 });
  await expect(preview).toHaveAttribute('src', `/api/assets/${assetId}/content`);
  await expect(preview).toHaveJSProperty('complete', true);
  const naturalWidth = await preview.evaluate((img) => (img as HTMLImageElement).naturalWidth);
  expect(naturalWidth).toBeGreaterThan(0);
});
