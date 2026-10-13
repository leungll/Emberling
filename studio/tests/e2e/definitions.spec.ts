import { expect, test } from '@playwright/test';

/**
 * Smoke check that the root address opens Runs and that the Workflows list renders.
 *
 * The Studio origin comes from `playwright.config.ts`'s `baseURL`, which already honours
 * `EMBERLING_E2E_STUDIO_PORT` / `EMBERLING_STUDIO_URL`; the same config's `webServer`
 * block starts Studio and waits for it, so an unreachable Studio is a real failure of the
 * harness here, never a reason to skip.
 */
test('the root opens Runs and the Workflows list renders', async ({ page }) => {
  await page.goto('/');
  await expect(page).toHaveURL(/\/runs$/);
  await expect(page.getByRole('heading', { name: 'Runs' })).toBeVisible();

  await page
    .getByRole('navigation', { name: 'Main' })
    .getByRole('link', { name: /^Workflows/ })
    .click();
  await expect(page.getByRole('heading', { name: 'Workflows' })).toBeVisible();
  await expect(page.getByRole('table')).toBeVisible();
});
