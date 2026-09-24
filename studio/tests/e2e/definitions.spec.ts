import { expect, test } from '@playwright/test';

/**
 * Smoke check that the Definitions route renders.
 *
 * The Studio origin comes from `playwright.config.ts`'s `baseURL`, which already honours
 * `EMBERLING_E2E_STUDIO_PORT` / `EMBERLING_STUDIO_URL`; the same config's `webServer`
 * block starts Studio and waits for it, so an unreachable Studio is a real failure of the
 * harness here, never a reason to skip.
 */
test('definitions route renders', async ({ page }) => {
  await page.goto('/');

  await expect(page.getByRole('heading', { name: 'Definitions' })).toBeVisible();
});
