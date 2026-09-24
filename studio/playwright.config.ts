import { defineConfig, devices } from '@playwright/test';

import {
  BACKEND_ENV,
  BACKEND_URL,
  MOCKPROVIDER_PORT,
  MOCKPROVIDER_URL,
  STUDIO_PORT,
  STUDIO_URL,
} from './tests/e2e/stack';

// Ports, URLs and the Backend environment live in tests/e2e/stack.ts, shared with the
// restart helper so a restarted Backend is launched exactly like this one.
process.env.EMBERLING_E2E_BACKEND_URL = BACKEND_URL;
process.env.EMBERLING_E2E_MOCKPROVIDER_URL = MOCKPROVIDER_URL;

// The restart scenario kills and relaunches the Backend every other spec talks to, so it
// runs in its own project after the `chromium` project has finished (never beside it).
const BACKEND_RESTART_SPEC = /backend-restart\.spec\.ts$/;

export default defineConfig({
  testDir: './tests/e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [['list'], ['html', { outputFolder: 'playwright-report', open: 'never' }]],
  // Resolved relative to this config file; `type: "module"` (package.json) rules out
  // `require.resolve`.
  globalSetup: './tests/e2e/global-setup.ts',
  use: {
    baseURL: STUDIO_URL,
    trace: 'on-first-retry',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
      testIgnore: BACKEND_RESTART_SPEC,
    },
    {
      name: 'chromium-backend-restart',
      use: { ...devices['Desktop Chrome'] },
      testMatch: BACKEND_RESTART_SPEC,
      dependencies: ['chromium'],
    },
  ],
  webServer: [
    {
      command: 'go run ./cmd/mockprovider',
      cwd: '../backend',
      url: `${MOCKPROVIDER_URL}/healthz`,
      env: { MOCKPROVIDER_ADDR: `:${MOCKPROVIDER_PORT}` },
      reuseExistingServer: !process.env.CI,
      timeout: 60_000,
      stdout: 'pipe',
      stderr: 'pipe',
    },
    {
      command: 'go run ./cmd/emberling',
      cwd: '../backend',
      url: `${BACKEND_URL}/ready`,
      env: { ...BACKEND_ENV },
      reuseExistingServer: !process.env.CI,
      timeout: 60_000,
      stdout: 'pipe',
      stderr: 'pipe',
    },
    {
      // `vite` (not `preview`) locally: it starts in well under a second and needs no
      // build step, which keeps `pnpm test-e2e` fast to iterate on. deploy/Dockerfile.studio
      // covers the `vite preview` / production-build path this same `preview.proxy` config
      // also serves (studio/vite.config.ts).
      command: `pnpm exec vite --port ${STUDIO_PORT} --strictPort`,
      cwd: '.',
      url: STUDIO_URL,
      env: { EMBERLING_BACKEND_ORIGIN: BACKEND_URL },
      reuseExistingServer: !process.env.CI,
      timeout: 30_000,
      stdout: 'pipe',
      stderr: 'pipe',
    },
  ],
});
