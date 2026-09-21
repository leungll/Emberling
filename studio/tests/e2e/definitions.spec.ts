import { expect, test } from '@playwright/test';

const BASE_URL = process.env.EMBERLING_STUDIO_URL ?? 'http://localhost:5173';

/**
 * Smoke check that the Definitions route renders.
 *
 * This is the one place where skipping is acceptable: without a served Studio there is
 * nothing to drive, and a red suite for a missing dev server would hide real failures.
 * The skip always prints why, so it can never be mistaken for a pass.
 */
async function studioIsReachable(): Promise<boolean> {
  try {
    const response = await fetch(BASE_URL, { signal: AbortSignal.timeout(2000) });
    return response.ok;
  } catch {
    return false;
  }
}

test('definitions route renders', async ({ page }) => {
  const reachable = await studioIsReachable();
  if (!reachable) console.warn(`e2e skipped: no Studio server reachable at ${BASE_URL}.`);
  test.skip(!reachable, `No Studio server reachable at ${BASE_URL}. Start it with 'pnpm dev'.`);

  await page.goto('/');

  await expect(page.getByRole('heading', { name: 'Definitions' })).toBeVisible();
});
