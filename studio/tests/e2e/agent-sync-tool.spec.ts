import { readFile } from 'node:fs/promises';

import { expect, test } from '@playwright/test';

import { GENERATED_DEFINITIONS_PATH, type GeneratedDefinitions } from './global-setup';

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

  await page.goto(`/studio/${workflowId}`);

  const runButton = page.getByRole('button', { name: 'Run', exact: true });
  await expect(runButton).toBeEnabled();
  await runButton.click();

  const dialog = page.getByRole('dialog', { name: 'Run workflow' });
  await dialog.getByLabel('Question').fill('mock:tool-call:lookup:{"key":"ember"}');
  await dialog.getByRole('button', { name: 'Create run' }).click();

  await page.waitForURL(/\/runs\//);

  const status = page.getByTestId('run-status');
  await expect(status).toHaveText('Completed', { timeout: 30_000 });
  await expect(page.getByRole('button', { name: /Run completed/ })).toBeVisible();

  // The Run Rail lists only RUNNING / WAITING NodeRuns; a finished node is selected on the
  // read-only Canvas (ObserveCanvas.tsx maps the Definition node id to its NodeRun).
  const agentNode = page.locator('.react-flow__node[data-id="node_agent"]');
  await expect(agentNode).toContainText('Succeeded');
  await agentNode.click();

  // AgentTraceView's own heading, not the Canvas chip that also reads "Agent".
  await expect(page.getByRole('heading', { name: 'Agent', exact: true })).toBeVisible();

  // Item 12: one card per persisted Turn (AgentTurnCard), each printing the Decision the
  // Backend committed, the Action it ran and that Action's Tool Attempts - none derived.
  const turns = page.getByTestId('agent-turn');
  await expect(turns).toHaveCount(2);

  const turn1 = turns.filter({ has: page.getByText('Turn 1', { exact: true }) });
  await expect(turn1).toContainText('Turn 1');
  await expect(turn1).toContainText('TOOL_CALL');
  await expect(turn1.getByText('Action TOOL_CALL')).toBeVisible();
  await expect(turn1.getByText(/^lookup #1$/)).toBeVisible();
  // Exactly the Action's status badge and the `lookup` Attempt's: the Tool ran to
  // completion synchronously, with no callback and no second Attempt.
  await expect(turn1.getByText('SUCCEEDED', { exact: true })).toHaveCount(2);

  const turn2 = turns.filter({ has: page.getByText('Turn 2', { exact: true }) });
  await expect(turn2).toContainText('Turn 2');
  await expect(turn2).toContainText('FINAL');
  await expect(turn2.getByText('Action FINAL')).toBeVisible();
  await expect(turn2.getByText(/^lookup #\d+$/)).toHaveCount(0);

  // The Agent Run's termination as the Backend recorded it.
  await expect(page.getByText('FINAL_RESPONSE')).toBeVisible();
});
