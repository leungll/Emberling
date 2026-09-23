import os from 'node:os';
import path from 'node:path';

import { defineConfig, devices } from '@playwright/test';

// Every port and the database name are overridable so a workstation already running the
// standard stack on the standard ports (another Definition-authoring session, `make dev`,
// ...) can point this suite at an entirely separate Backend/Mock Provider/Studio/database
// without colliding. CI and an otherwise-idle workstation both get the same defaults.
const BACKEND_PORT = process.env.EMBERLING_E2E_BACKEND_PORT ?? '8080';
const MOCKPROVIDER_PORT = process.env.EMBERLING_E2E_MOCKPROVIDER_PORT ?? '9101';
const STUDIO_PORT = process.env.EMBERLING_E2E_STUDIO_PORT ?? '5173';
// The default matches the no-Docker local PostgreSQL this repository documents
// (ENGINEERING.md §1: PostgreSQL 18 on 127.0.0.1:55432); CI overrides it to the
// job-scoped `postgres` service instead.
const DATABASE_URL =
  process.env.EMBERLING_E2E_DATABASE_URL ??
  'postgres://emberling@127.0.0.1:55432/emberling_e2e?sslmode=disable';

const BACKEND_URL = `http://localhost:${BACKEND_PORT}`;
const MOCKPROVIDER_URL = `http://localhost:${MOCKPROVIDER_PORT}`;
const STUDIO_URL = process.env.EMBERLING_STUDIO_URL ?? `http://localhost:${STUDIO_PORT}`;

process.env.EMBERLING_E2E_BACKEND_URL = BACKEND_URL;
process.env.EMBERLING_E2E_MOCKPROVIDER_URL = MOCKPROVIDER_URL;

// Reused across processes: unlike the model provider's base URL under Docker Compose
// (deploy/compose.yaml's documented gap), Backend, Mock Provider and the browser all
// resolve "localhost" identically here, so the image URI a completed Image Generation
// NodeRun returns is reachable from the same browser that renders its preview.
const ASSET_STORAGE_ROOT = path.join(os.tmpdir(), 'emberling-e2e-assets');

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
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
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
      env: {
        EMBERLING_HTTP_ADDR: `:${BACKEND_PORT}`,
        EMBERLING_DATABASE_URL: DATABASE_URL,
        EMBERLING_DATABASE_MAX_CONNS: '10',
        EMBERLING_ASSET_STORAGE_ROOT: ASSET_STORAGE_ROOT,
        // 16 MiB: one reference image per upload; generic file upload is out of MVP scope
        // (matches deploy/compose.yaml's backend service).
        EMBERLING_ASSET_MAX_UPLOAD_BYTES: '16777216',
        // Backend dispatch and the browser's own preview requests both resolve
        // "localhost" the same way, which is exactly the ambiguity Docker Compose cannot
        // resolve with a single value (see deploy/compose.yaml's top-of-file comment).
        EMBERLING_MODEL_PROVIDER_BASE_URL: MOCKPROVIDER_URL,
        // A placeholder: the Mock Provider checks no credential, and this value never
        // reaches a Definition, an Event, Trace or log (CLAUDE.md's Persistence rules).
        EMBERLING_MODEL_PROVIDER_API_KEY: 'e2e-mock-provider-key',
        EMBERLING_CALLBACK_BASE_URL: BACKEND_URL,
        EMBERLING_CALLBACK_SIGNING_SECRET: 'e2e-callback-signing-secret',
        EMBERLING_RECONCILE_INTERVAL: '5s',
        EMBERLING_RECONCILE_BATCH_SIZE: '100',
        EMBERLING_PENDING_CALLBACK_TTL: '24h',
        EMBERLING_PENDING_CALLBACK_MAX_PAYLOAD_BYTES: '262144',
        EMBERLING_TRACE_MAX_FIELD_BYTES: '8192',
        EMBERLING_TRACE_MAX_RESPONSE_BYTES: '1048576',
      },
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
