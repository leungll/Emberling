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
 * AIGC Media Generation with a Reference Image: the Run Input Dialog uploads the image through `POST /api/assets` first and
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
  // The field is named by the Image Input node's configured `label`; its accept list is
  // the node's `acceptedMediaTypes`, which the Backend re-validates on upload.
  const referenceField = dialog.getByRole('group', { name: 'Reference Image' });
  const chooser = page.waitForEvent('filechooser');
  await referenceField.getByRole('button', { name: 'Choose image' }).click();
  await (
    await chooser
  ).setFiles({ name: 'reference.png', mimeType: 'image/png', buffer: ONE_PIXEL_PNG });

  // Once uploaded, the field shows the file name and a human size, never the Asset ID.
  await expect(referenceField.getByText('reference.png, 0.1 KB')).toBeVisible({ timeout: 10_000 });
  await expect(referenceField.getByRole('button', { name: 'Replace reference.png' })).toBeVisible();
  await expect(referenceField).not.toContainText('asset_');

  // The Asset ID is the Backend's, read from the create-run payload so Observe can be
  // checked against the very same Asset.
  const createRun = page.waitForRequest(
    (request) => request.method() === 'POST' && new URL(request.url()).pathname === '/api/runs',
  );
  await dialog.getByRole('button', { name: 'Create run' }).click();
  const body = (await createRun).postDataJSON() as {
    input: { reference: { assetId: string; sizeBytes: number } };
  };
  const { assetId } = body.input.reference;
  expect(assetId).toBeTruthy();
  expect(body.input.reference.sizeBytes).toBe(ONE_PIXEL_PNG.length);
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

/**
 * A photo set is an array of image AssetRefs. The dialog uploads every selected image
 * through `POST /api/assets` and writes only the returned AssetRefs, in selection order,
 * into the `photos` input; the Media Brief NodeRun then reads that set from Run input.
 * Run Again on that Run offers the same photo set again without re-uploading it.
 */
test('Photo set upload starts the Photo Effect Template and Run Again keeps the photos', async ({
  page,
}) => {
  const generated = JSON.parse(
    await readFile(GENERATED_DEFINITIONS_PATH, 'utf-8'),
  ) as GeneratedDefinitions;
  const { workflowId } = generated.photoSet;

  await page.goto(`/studio/${workflowId}`);
  const runButton = page.getByRole('button', { name: 'Run', exact: true });
  await expect(runButton).toBeEnabled();
  await runButton.click();

  const dialog = page.getByRole('dialog', { name: 'Run workflow' });
  await dialog.getByLabel('Creative Brief').fill('a clay stop-motion look');
  // The Media Brief step declares no label, so the field is named by the step's name.
  const photoSetField = dialog.getByRole('group', { name: 'Photo Set' });
  await expect(photoSetField).toContainText('Add at least 1 photo.');
  const chooser = page.waitForEvent('filechooser');
  await photoSetField.getByRole('button', { name: 'Add photos' }).click();
  await (
    await chooser
  ).setFiles(
    [1, 2, 3].map((n) => ({
      name: `photo-${n}.png`,
      mimeType: 'image/png',
      buffer: ONE_PIXEL_PNG,
    })),
  );

  const thumbnails = dialog.getByRole('img', { name: /^Image \d$/ });
  await expect(thumbnails).toHaveCount(3, { timeout: 10_000 });
  await expect(dialog.getByText('3 photos added. Add up to 12.')).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Remove photo-2.png' })).toBeVisible();
  // Each thumbnail's title is the Backend-assigned assetId of that upload.
  const titlesOf = (images: typeof thumbnails) =>
    images.evaluateAll((imgs) => imgs.map((img) => img.getAttribute('title') ?? ''));
  const assetIds = await titlesOf(thumbnails);
  expect(new Set(assetIds).size).toBe(3);

  const createRun = page.waitForRequest(
    (request) => request.method() === 'POST' && new URL(request.url()).pathname === '/api/runs',
  );
  await dialog.getByRole('button', { name: 'Create run' }).click();
  const body = (await createRun).postDataJSON() as {
    input: { brief: string; photos: { assetId: string; mediaType: string }[] };
  };
  expect(body.input.photos.map((photo) => photo.assetId)).toEqual(assetIds);
  expect(body.input.photos.every((photo) => photo.mediaType === 'image/png')).toBe(true);
  await page.waitForURL(/\/runs\//);

  // The Backend accepted the photo set against the frozen schema and Media Brief read it.
  const photoNode = page.locator('.react-flow__node[data-id="node_photos"]');
  await expect(photoNode).toContainText('Succeeded', { timeout: 30_000 });

  await page.getByRole('button', { name: 'Run Again' }).click();
  const again = page.getByRole('dialog', { name: 'Run workflow' });
  const prefilled = again.getByRole('img', { name: /^Image \d$/ });
  await expect(prefilled).toHaveCount(3);
  expect(await titlesOf(prefilled)).toEqual(assetIds);
});
