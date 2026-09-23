import { readFile } from 'node:fs/promises';

import { expect, test } from '@playwright/test';

import { GENERATED_DEFINITIONS_PATH, type GeneratedDefinitions } from './global-setup';

/**
 * Agent with an asynchronous Tool (04-ux.md §6 items 1,2,4-7,12-16). M5 slice 5.3b
 * (`backend/internal/adapters/mockmodel/provider.go`'s `resolveToolCallDirective`) extended
 * the `mock:tool-call:<name>` directive to `mock:tool-call:<name>:<json-arguments>`, so the
 * in-process Mock Model Provider can emit a TOOL_CALL Decision with a real, schema-
 * satisfying argument set purely from a Run's own input - no test-only `Script` hook
 * required. `remote_lookup` (`backend/internal/tools/remotelookup`) requires a non-empty
 * `key`, so this is what makes the scenario reachable from Studio at all: see
 * `backend/test/contract/e2e_agent_mock_directive_test.go` for the equivalent HTTP-only
 * contract test and the exact Agent Definition (`tests/e2e/fixtures.ts`'s `agentLookup`
 * reproduces it, retargeted at `remote_lookup`).
 *
 * Turn 1 commits the TOOL_CALL and dispatches to the Mock Provider, which schedules its own
 * callback; Turn 2's request already carries a "tool" role message, so the Provider treats
 * the directive as answered and returns FINAL - two Turns total, not a repeat TOOL_CALL loop
 * (see that same function's comment on why Turn 2 cannot re-read the unchanged directive).
 */
test('Agent with an asynchronous Tool pauses at the Agent NodeRun, then resumes and completes', async ({
  page,
}) => {
  const generated = JSON.parse(
    await readFile(GENERATED_DEFINITIONS_PATH, 'utf-8'),
  ) as GeneratedDefinitions;
  const { workflowId } = generated.agentLookup;

  await page.goto(`/studio/${workflowId}`);

  const runButton = page.getByRole('button', { name: 'Run', exact: true });
  await expect(runButton).toBeEnabled();
  await runButton.click();

  const dialog = page.getByRole('dialog', { name: 'Run workflow' });
  // The Tool call argument's own "key" is, in turn, `remote_lookup`'s own directive syntax
  // (`backend/internal/tools/remotelookup/tool.go`'s `buildRequest`): a bare key dispatches
  // and resolves near-instantly, too fast for this test to reliably observe WAITING_CALLBACK
  // at all, so `mock:delay:2000` is used here for the same reason aigc-media.spec.ts uses it
  // on Image Generation - a real, ~2s, observable callback window.
  await dialog
    .getByLabel('Question')
    .fill('mock:tool-call:remote_lookup:{"key":"mock:delay:2000"}');
  await dialog.getByRole('button', { name: 'Create run' }).click();

  await page.waitForURL(/\/runs\//);

  const status = page.getByTestId('run-status');
  // Item 15: while the Agent's Tool Attempt is dispatched and unresolved, the Run reads
  // PAUSED (RUN_STATUS_LABELS -> "Waiting for external result"), same as any other
  // WAITING_CALLBACK NodeRun - the Agent Node is not a special case for Run aggregation.
  await expect(status).toHaveText('Waiting for external result', { timeout: 15_000 });

  const agentNodeButton = page.getByRole('button', { name: /node_agent/ });
  await expect(agentNodeButton).toBeVisible();
  await expect(agentNodeButton).toContainText('Waiting for callback');
  await agentNodeButton.click();

  // The Canvas node chip also reads "Agent" (its node type label), so this must be scoped
  // to AgentTraceView's own `<h3>` heading, not a plain text match.
  await expect(page.getByRole('heading', { name: 'Agent', exact: true })).toBeVisible();
  // The dispatched Tool Attempt's row prints "remote_lookup #<attemptNo>" (AgentTurnCard).
  // Anchored, since the Turn's candidate Tools and Decision also name remote_lookup.
  await expect(page.getByText(/^remote_lookup #\d+$/)).toBeVisible();

  // Item 16: the Mock Provider's own scheduled callback (not this test) resolves the Tool
  // Attempt, which resumes the Agent to Turn 2 and on to FINAL; the Run completes.
  await expect(status).toHaveText('Completed', { timeout: 30_000 });

  // Item 12: the Trace shows both Turns (TOOL_CALL then FINAL), the Tool result, and
  // termination - AgentTraceView renders every value it was given, with no Studio-side
  // aggregation of its own.
  await expect(page.getByText('Turns', { exact: true })).toBeVisible();
  await expect(page.getByText(/^Turn \d+$/)).toHaveCount(2);
  await expect(page.getByText('Termination')).toBeVisible();
});
