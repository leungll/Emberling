import { readFile } from 'node:fs/promises';

import { expect, test } from '@playwright/test';

import { GENERATED_DEFINITIONS_PATH, type GeneratedDefinitions } from './global-setup';
import { details, expandedTrace, runStatus, stepCard } from './run-view';

/**
 * Agent with an asynchronous Tool. The Mock Model tool-call directive
 * (`backend/internal/adapters/mockmodel/provider.go`'s `resolveToolCallDirective`) extends
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

  await page.goto(`/workflows/${workflowId}`);

  const runButton = page.getByRole('button', { name: 'Run', exact: true });
  await expect(runButton).toBeEnabled();
  await runButton.click();

  const dialog = page.getByRole('dialog');
  // The Tool call argument's own "key" is, in turn, `remote_lookup`'s own directive syntax
  // (`backend/internal/tools/remotelookup/tool.go`'s `buildRequest`): a bare key dispatches
  // and resolves near-instantly, too fast for this test to reliably observe WAITING_CALLBACK
  // at all, so `mock:delay:2000` is used here for the same reason aigc-media.spec.ts uses it
  // on Image Generation - a real, ~2s, observable callback window.
  await dialog
    .getByLabel('Question')
    .fill('mock:tool-call:remote_lookup:{"key":"mock:delay:2000"}');
  await dialog.getByRole('button', { name: 'Start run' }).click();

  await page.waitForURL(/\/runs\//);

  const status = runStatus(page);
  // While the Agent's Tool Attempt is dispatched and unresolved, the Run reads PAUSED, the
  // same as for any other waiting step: the Agent step is not a special case for the Run's
  // status, which is the Backend's.
  await expect(status).toHaveText('Waiting', { timeout: 15_000 });
  await expect(stepCard(page, 'Answering Agent')).toHaveAccessibleName(/, Waiting for reply,/);
  await expect(
    page.getByText('Answering Agent is waiting for Mock task provider.', { exact: true }),
  ).toBeVisible();
  // The run opens on the wait: the dispatched remote_lookup call.
  await expect(details(page).getByText('remote_lookup').first()).toBeVisible();

  // The Mock Provider's own scheduled callback (not this test) resolves the Tool Attempt,
  // which resumes the Agent to Turn 2 and on to FINAL; the Run completes.
  await expect(status).toHaveText('Completed', { timeout: 30_000 });

  // The trace shows both Turns (TOOL_CALL then FINAL), the wait the callback ended, and the
  // termination, each as the Backend recorded it.
  const trace = await expandedTrace(page);
  await expect(trace.getByRole('row', { name: /Turn \d+: / })).toHaveCount(2);
  await expect(trace.getByRole('row', { name: /Turn 1: called remote_lookup/ })).toBeVisible();
  await expect(trace.getByRole('row', { name: /Waited for / })).toBeVisible();
  await expect(trace.getByRole('row', { name: /Turn 2: finished/ })).toBeVisible();
  await stepCard(page, 'Answering Agent').click();
  await expect(details(page).getByText('Finished with an answer')).toBeVisible();
});
