import { readFile } from 'node:fs/promises';

import { expect, test } from '@playwright/test';

import { GENERATED_DEFINITIONS_PATH, type GeneratedDefinitions } from './global-setup';
import { details, eventList, expandedTrace, runStatus, stepCard } from './run-view';

/**
 * Agent with a synchronous Tool: the
 * Tool Loop runs at least two Turns, the first executing the Tool and the second
 * producing FINAL. `mock:tool-call:lookup:<json-arguments>` makes the in-process Mock
 * Model Provider commit a TOOL_CALL Decision for the built-in `lookup` Tool on Turn 1
 * (`backend/internal/adapters/mockmodel/provider.go`'s `resolveToolCallDirective`); the
 * Tool answers in the same call, so its Attempt and the Action reach SUCCEEDED with no
 * callback, and Turn 2's request already carries the "tool" message, which the Provider
 * treats as the directive answered and returns FINAL.
 */
test('Agent with a synchronous Tool runs the Tool on Turn 1, answers FINAL on Turn 2 and completes', async ({
  page,
}) => {
  const generated = JSON.parse(
    await readFile(GENERATED_DEFINITIONS_PATH, 'utf-8'),
  ) as GeneratedDefinitions;
  const { workflowId } = generated.agentSyncLookup;

  await page.goto(`/workflows/${workflowId}`);

  const runButton = page.getByRole('button', { name: 'Run', exact: true });
  await expect(runButton).toBeEnabled();
  await runButton.click();

  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Question').fill('mock:tool-call:lookup:{"key":"ember"}');
  await dialog.getByRole('button', { name: 'Start run' }).click();

  await page.waitForURL(/\/runs\//);

  await expect(runStatus(page)).toHaveText('Completed', { timeout: 30_000 });
  await expect(stepCard(page, 'Answering Agent')).toHaveAccessibleName(/, Succeeded,/);

  // One trace row per persisted Turn, each named by the Decision the Backend committed and
  // the Action it ran: Turn 1 called the Tool, Turn 2 finished.
  const trace = await expandedTrace(page);
  await expect(trace.getByRole('row', { name: /Turn \d+: / })).toHaveCount(2);
  await expect(trace.getByRole('row', { name: /Turn 1: called lookup/ })).toBeVisible();
  await expect(trace.getByRole('row', { name: /Turn 2: finished/ })).toBeVisible();
  // The Tool answered in the same call: no wait for an outside service.
  await expect(trace.getByRole('row', { name: /Wait(ed|ing) for/ })).toHaveCount(0);

  // The Agent Run's termination as the Backend recorded it.
  await stepCard(page, 'Answering Agent').click();
  await expect(details(page).getByText('Finished with an answer')).toBeVisible();

  const events = await eventList(page);
  await expect(events.getByRole('row', { name: /Run completed/ })).toHaveCount(1);
});
