import os from 'node:os';
import path from 'node:path';

/**
 * The one description of the e2e stack: ports, URLs, the Backend's environment and the
 * Backend module directory. `playwright.config.ts` starts the stack from it, and
 * `backend-process.ts` restarts the Backend from the very same values, so a restarted
 * Backend can never drift from the one Playwright launched.
 *
 * Every port and the database name are overridable so a workstation already running the
 * standard stack on the standard ports (another Definition-authoring session, `make dev`,
 * ...) can point this suite at an entirely separate Backend/Mock Provider/Studio/database
 * without colliding. CI and an otherwise-idle workstation both get the same defaults.
 */
export const BACKEND_PORT = process.env.EMBERLING_E2E_BACKEND_PORT ?? '8080';
export const MOCKPROVIDER_PORT = process.env.EMBERLING_E2E_MOCKPROVIDER_PORT ?? '9101';
export const STUDIO_PORT = process.env.EMBERLING_E2E_STUDIO_PORT ?? '5173';
// The default matches the no-Docker local PostgreSQL setup
// (PostgreSQL 18 on 127.0.0.1:55432); CI overrides it to the
// job-scoped `postgres` service instead.
export const DATABASE_URL =
  process.env.EMBERLING_E2E_DATABASE_URL ??
  'postgres://emberling@127.0.0.1:55432/emberling_e2e?sslmode=disable';

export const BACKEND_URL = `http://localhost:${BACKEND_PORT}`;
export const MOCKPROVIDER_URL = `http://localhost:${MOCKPROVIDER_PORT}`;
export const STUDIO_URL = process.env.EMBERLING_STUDIO_URL ?? `http://localhost:${STUDIO_PORT}`;

// Reused across processes: unlike the model provider's base URL under Docker Compose
// (deploy/compose.yaml's documented gap), Backend, Mock Provider and the browser all
// resolve "localhost" identically here, so the image URI a completed Image Generation
// NodeRun returns is reachable from the same browser that renders its preview.
export const ASSET_STORAGE_ROOT = path.join(os.tmpdir(), 'emberling-e2e-assets');

/** `backend/`, the Go module every Backend process (initial or restarted) runs from. */
export const BACKEND_DIR = path.resolve(import.meta.dirname, '..', '..', '..', 'backend');

/** The Backend's full environment; `config.Load` requires every key except HTTP_ADDR. */
export const BACKEND_ENV: Readonly<Record<string, string>> = {
  EMBERLING_HTTP_ADDR: `:${BACKEND_PORT}`,
  EMBERLING_DATABASE_URL: DATABASE_URL,
  EMBERLING_DATABASE_MAX_CONNS: '10',
  EMBERLING_ASSET_STORAGE_ROOT: ASSET_STORAGE_ROOT,
  // 16 MiB: one reference image per upload; generic file upload is out of MVP scope
  // (matches deploy/compose.yaml's backend service).
  EMBERLING_ASSET_MAX_UPLOAD_BYTES: '16777216',
  // Backend dispatch and the browser's own preview requests both resolve "localhost" the
  // same way, which is exactly the ambiguity Docker Compose cannot resolve with a single
  // value (see deploy/compose.yaml's top-of-file comment).
  EMBERLING_MODEL_PROVIDER_BASE_URL: MOCKPROVIDER_URL,
  // A placeholder: the Mock Provider checks no credential, and this value never reaches
  // a Definition, an Event, Trace or log (CLAUDE.md's Persistence rules).
  EMBERLING_MODEL_PROVIDER_API_KEY: 'e2e-mock-provider-key',
  EMBERLING_CALLBACK_BASE_URL: BACKEND_URL,
  EMBERLING_CALLBACK_SIGNING_SECRET: 'e2e-callback-signing-secret',
  EMBERLING_RECONCILE_INTERVAL: '5s',
  EMBERLING_RECONCILE_BATCH_SIZE: '100',
  EMBERLING_PENDING_CALLBACK_TTL: '24h',
  EMBERLING_PENDING_CALLBACK_MAX_PAYLOAD_BYTES: '262144',
  EMBERLING_TRACE_MAX_FIELD_BYTES: '8192',
  EMBERLING_TRACE_MAX_RESPONSE_BYTES: '1048576',
};

/** Polls `url` until it answers 2xx, or throws with the last failure after `deadlineMs`. */
export async function waitForReady(url: string, label: string, deadlineMs: number): Promise<void> {
  const start = Date.now();
  let lastError: unknown;
  while (Date.now() - start < deadlineMs) {
    try {
      const response = await fetch(url, { signal: AbortSignal.timeout(2000) });
      if (response.ok) return;
      lastError = new Error(`${label} answered ${response.status}`);
    } catch (error) {
      lastError = error;
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error(`${label} did not become ready within ${deadlineMs}ms: ${String(lastError)}`);
}
