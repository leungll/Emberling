import { execFile, spawn, type ChildProcess } from 'node:child_process';
import { openSync } from 'node:fs';
import { mkdir } from 'node:fs/promises';
import path from 'node:path';
import { promisify } from 'node:util';

import { BACKEND_DIR, BACKEND_ENV, BACKEND_PORT, BACKEND_URL, waitForReady } from './stack';

const execFileAsync = promisify(execFile);

/**
 * Restarting the Backend under Playwright.
 *
 * `playwright.config.ts`'s `webServer` launches the Backend as `go run ./cmd/emberling`
 * and only ever kills it at the end of the whole run: it exposes no restart. This module
 * fills that gap for the restart scenario (docs/09 §1 item 3, 04-ux.md §6 item 11) by
 * finding the Backend that is listening on `BACKEND_PORT`, killing it, and starting a
 * fresh process from `BACKEND_ENV` - the same environment the config launched it with -
 * against the same database. PostgreSQL is the authority for execution facts (invariant
 * #1), so nothing but the process is replaced.
 *
 * The replacement runs a prebuilt binary rather than `go run`: compiling inside the
 * restart window would make the Backend's downtime depend on the Go build cache.
 */

const GENERATED_DIR = path.join(import.meta.dirname, '.generated');
const BACKEND_BINARY = path.join(GENERATED_DIR, 'emberling');
const BACKEND_LOG = path.join(GENERATED_DIR, 'backend-restart.log');

/** Compiles `cmd/emberling` once; the binary lives under the gitignored `.generated/`. */
export async function buildBackendBinary(): Promise<string> {
  await mkdir(GENERATED_DIR, { recursive: true });
  await execFileAsync('go', ['build', '-o', BACKEND_BINARY, './cmd/emberling'], {
    cwd: BACKEND_DIR,
    env: process.env,
  });
  return BACKEND_BINARY;
}

/** PIDs of every process listening on TCP `port` (usually one; IPv4 and IPv6 share it). */
async function listenerPids(port: string): Promise<number[]> {
  try {
    const { stdout } = await execFileAsync('lsof', ['-nP', '-t', `-iTCP:${port}`, '-sTCP:LISTEN']);
    return [
      ...new Set(
        stdout
          .split('\n')
          .filter(Boolean)
          .map((line) => Number(line)),
      ),
    ];
  } catch (error) {
    // lsof exits 1 when nothing listens; any other failure is a real harness error.
    if ((error as { code?: unknown }).code === 1) return [];
    throw error;
  }
}

/**
 * Kills the Backend listening on `BACKEND_PORT` and resolves once the port is free.
 *
 * SIGKILL, deliberately: a crash is the recovery case the Runtime promises to survive
 * (invariant #6, "never make process memory the recovery source"), and a graceful stop
 * would first wait `defaultShutdownTimeout` for the browser's open SSE stream to drain.
 */
export async function killBackend(): Promise<void> {
  const pids = await listenerPids(BACKEND_PORT);
  if (pids.length === 0) {
    throw new Error(`no process is listening on the Backend port ${BACKEND_PORT}`);
  }
  for (const pid of pids) process.kill(pid, 'SIGKILL');

  const deadline = Date.now() + 10_000;
  while ((await listenerPids(BACKEND_PORT)).length > 0) {
    if (Date.now() > deadline) {
      throw new Error(`Backend pid(s) ${pids.join(',')} still listen on ${BACKEND_PORT}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}

export interface BackendProcess {
  /** Stops the process; safe to call more than once. */
  stop(): Promise<void>;
}

/**
 * Starts `binary` with the config's Backend environment and waits for its full startup
 * gate (`/ready`: migrations, Registries, Asset storage, and the first Reconciler scan).
 * Its logs go to `.generated/backend-restart.log` instead of a pipe, so a chatty Backend
 * can never block on a pipe nobody drains.
 */
export async function startBackend(binary: string): Promise<BackendProcess> {
  const log = openSync(BACKEND_LOG, 'a');
  const child: ChildProcess = spawn(binary, [], {
    cwd: BACKEND_DIR,
    env: { ...process.env, ...BACKEND_ENV },
    stdio: ['ignore', log, log],
  });
  const exited = new Promise<void>((resolve) => child.once('exit', () => resolve()));

  try {
    await waitForReady(`${BACKEND_URL}/ready`, 'restarted backend /ready', 60_000);
  } catch (error) {
    child.kill('SIGKILL');
    throw error;
  }

  return {
    async stop() {
      if (child.exitCode !== null || child.signalCode !== null) return;
      child.kill('SIGKILL');
      await exited;
    },
  };
}
