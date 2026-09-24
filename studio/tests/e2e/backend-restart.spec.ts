import { readFile } from 'node:fs/promises';

import { expect, test, type Page } from '@playwright/test';

import {
  buildBackendBinary,
  killBackend,
  startBackend,
  type BackendProcess,
} from './backend-process';
import { GENERATED_DEFINITIONS_PATH, type GeneratedDefinitions } from './global-setup';
import { BACKEND_URL } from './stack';

/**
 * Backend restart while observing a suspended Run (docs/09 §1 items 3 and 11; 04-ux.md
 * §6 items 10 and 11).
 *
 * The Run input `mock:delay:20000` makes the Mock Provider deliver its callback 20s after
 * dispatch (`backend/internal/adapters/mocktask/adapter.go`), which is the window this
 * test restarts the Backend in. The Mock Provider never retries a callback, so the
 * restart must be complete before that timer fires; `buildBackendBinary` runs before the
 * Run is created, keeping compilation out of the window, and the restart itself takes
 * well under the margin. Nothing here posts the callback: the Provider does, to the
 * relaunched Backend, exactly as a real Provider would after an Emberling crash.
 *
 * This spec runs in its own Playwright project after every other spec has finished
 * (playwright.config.ts): it kills the Backend they all share.
 */

const STREAM_PATH = /\/api\/runs\/[^/]+\/events\?/;

/** `seq` of every Event Timeline entry, in rendered order (EventTimeline.tsx's "seq N"). */
async function renderedSeqs(page: Page): Promise<number[]> {
  const names = await page.getByRole('button', { name: /· seq \d+/ }).allInnerTexts();
  return names.map((text) => Number(/· seq (\d+)/.exec(text)?.[1]));
}

let binary: string;
let restarted: BackendProcess | null = null;

test.beforeAll(async () => {
  test.setTimeout(180_000);
  binary = await buildBackendBinary();
});

test.afterAll(async () => {
  await restarted?.stop();
});

test('Observe reconnects after a Backend restart and shows the resumed Run completing without duplicated Events', async ({
  page,
}) => {
  test.setTimeout(120_000);
  const generated = JSON.parse(
    await readFile(GENERATED_DEFINITIONS_PATH, 'utf-8'),
  ) as GeneratedDefinitions;
  const { workflowId } = generated.aigcMedia;

  await page.goto(`/studio/${workflowId}`);

  const runButton = page.getByRole('button', { name: 'Run', exact: true });
  await expect(runButton).toBeEnabled();
  await runButton.click();

  const dialog = page.getByRole('dialog', { name: 'Run workflow' });
  await dialog.getByLabel('Prompt').fill('mock:delay:20000');
  await dialog.getByRole('button', { name: 'Create run' }).click();

  await page.waitForURL(/\/runs\//);
  const runId = /\/runs\/([^/?#]+)/.exec(page.url())?.[1];
  expect(runId).toBeTruthy();

  const status = page.getByTestId('run-status');
  await expect(status).toHaveText('Waiting for external result', { timeout: 15_000 });
  const imageNodeButton = page.getByRole('button', { name: /node_image/ });
  await expect(imageNodeButton).toContainText('Waiting for callback');

  // The stream is open at this point; the Timeline shows the Events committed so far.
  const seqsBeforeRestart = await renderedSeqs(page);
  expect(seqsBeforeRestart.length).toBeGreaterThan(0);

  // --- restart: same database, new process. The page is left exactly as it is.
  await killBackend();
  // A 200 `text/event-stream` response can only come from a live Backend, so one arriving
  // after the kill proves Observe reopened its SSE stream (runObserver.ts's reconnect,
  // from the last applied seq) rather than merely retrying into a dead proxy.
  const reconnected = page.waitForResponse(
    (response) =>
      STREAM_PATH.test(response.url()) &&
      response.status() === 200 &&
      (response.headers()['content-type'] ?? '').includes('text/event-stream'),
    { timeout: 60_000 },
  );
  restarted = await startBackend(binary);
  await reconnected;

  // Item 11: after the restart the page still observes the original Run, which the
  // Provider's callback resumes on the new process, through to COMPLETED.
  await expect(status).toHaveText('Completed', { timeout: 45_000 });
  // The Run Rail lists only RUNNING / WAITING NodeRuns, so the finished node is read from
  // the read-only Canvas.
  await expect(page.locator('.react-flow__node[data-id="node_image"]')).toContainText('Succeeded');

  // Item 10 / docs/09 §1 item 11: no Event is missing, out of order or applied twice.
  // The Timeline is contiguous from seq 1 and matches the Backend's committed history.
  await expect(page.getByRole('button', { name: /Run completed/ })).toHaveCount(1);
  const seqsAfterRestart = await renderedSeqs(page);
  expect(seqsAfterRestart.length).toBeGreaterThan(seqsBeforeRestart.length);
  expect(seqsAfterRestart).toEqual(seqsAfterRestart.map((_, index) => index + 1));

  const response = await fetch(`${BACKEND_URL}/api/runs/${runId}/events?afterSeq=0&limit=200`);
  expect(response.ok).toBe(true);
  const committed = (await response.json()) as { items: { seq: number }[] };
  expect(seqsAfterRestart).toEqual(committed.items.map((event) => event.seq));
});
