import { defineConfig, devices } from '@playwright/test';

// The UI review suite: Studio against recorded Backend responses, at the two widths the
// layout is designed for. It needs no Backend; Vite serves Studio and every Backend
// request is answered from tests/ui/fixtures/recorded.
const PORT = 5175;

export default defineConfig({
  testDir: './tests/ui',
  testMatch: /\.ui\.spec\.ts$/,
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  reporter: [['list']],
  outputDir: './tests/ui/report/test-results',
  use: {
    baseURL: `http://localhost:${PORT}`,
    locale: 'en-US',
    timezoneId: 'Asia/Shanghai',
    colorScheme: 'light',
    reducedMotion: 'reduce',
    deviceScaleFactor: 1,
  },
  projects: [
    // Self-tests of the checks and the fixtures; they do not depend on the width.
    { name: 'checks', use: { ...devices['Desktop Chrome'] }, testMatch: /checks\.ui\.spec\.ts$/ },
    {
      name: 'w1000',
      testMatch: /pages\/.*\.ui\.spec\.ts$/,
      use: {
        ...devices['Desktop Chrome'],
        viewport: { width: 1000, height: 800 },
        deviceScaleFactor: 1,
      },
    },
    {
      name: 'w1440',
      testMatch: /pages\/.*\.ui\.spec\.ts$/,
      use: {
        ...devices['Desktop Chrome'],
        viewport: { width: 1440, height: 900 },
        deviceScaleFactor: 1,
      },
    },
  ],
  webServer: {
    // The proxy target is never reached: the review answers every Backend request itself.
    command: `pnpm exec vite --port ${PORT} --strictPort`,
    url: `http://localhost:${PORT}`,
    env: { EMBERLING_BACKEND_ORIGIN: 'http://127.0.0.1:9' },
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
});
