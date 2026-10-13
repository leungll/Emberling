/**
 * Records the UI review fixtures from a running local Backend.
 *
 *   (cd studio && node tests/ui/record-fixtures.ts) [--backend http://localhost:8080]
 *     [--workflows wf_a,wf_b]
 *
 * The recorder only reads. Every request goes through `get()`, which refuses any method
 * other than GET, so recording can never create, change or start anything on the stack
 * it reads from. Seed the stack separately (for example with `make demo`) before
 * recording.
 *
 * It writes one JSON file per response into fixtures/recorded/ and an index that maps each
 * request path to its file; tests/ui/support/server.ts serves exactly these responses. A
 * secret scan runs over everything before a file is written, and the recorder stops if
 * any response carries a credential, a callback token or an internal storage key.
 */
import { mkdir, readdir, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';

import { findSecrets } from './support/secret-scan.ts';

interface DefinitionListItem {
  workflowId: string;
  name: string;
  latestVersion: number;
  updatedAt: string;
  lastRun: { id: string; status: string; createdAt: string } | null;
}

interface RunSnapshot {
  run: { id: string; workflowId: string; definitionVersion: number };
  nodeRuns: { id: string; nodeType: string }[];
  lastSeq: number;
}

const OUT_DIR = path.join(import.meta.dirname, 'fixtures', 'recorded');
/** How many distinct workflows (by name) are recorded in depth when none are named. */
const DEFAULT_DEPTH = 8;
const EVENT_PAGE = 500;

function arg(name: string): string | undefined {
  const i = process.argv.indexOf(`--${name}`);
  return i >= 0 ? process.argv[i + 1] : undefined;
}

const BACKEND = (
  arg('backend') ??
  process.env.EMBERLING_RECORD_BACKEND_URL ??
  'http://localhost:8080'
).replace(/\/$/, '');

const index: Record<string, { file: string; status: number }> = {};
let fileNo = 0;

async function get(pathname: string, method = 'GET'): Promise<{ status: number; body: unknown }> {
  if (method !== 'GET') {
    throw new Error(`record-fixtures only reads: refusing ${method} ${pathname}`);
  }
  const response = await fetch(`${BACKEND}${pathname}`, { method });
  const text = await response.text();
  const body: unknown = text ? JSON.parse(text) : null;
  return { status: response.status, body };
}

async function record(pathname: string): Promise<unknown> {
  const { status, body } = await get(pathname);
  const secrets = findSecrets(body);
  if (secrets.length > 0) {
    throw new Error(`${pathname} carries values that must not be recorded: ${secrets.join(', ')}`);
  }
  fileNo += 1;
  const file = `${String(fileNo).padStart(3, '0')}-${pathname
    .replace(/^\/(api\/)?/, '')
    .replace(/[^a-zA-Z0-9]+/g, '-')
    .slice(0, 80)}.json`;
  await writeFile(path.join(OUT_DIR, file), `${JSON.stringify(body, null, 2)}\n`);
  index[pathname] = { file, status };
  return body;
}

async function recordRun(runId: string): Promise<void> {
  const snapshot = (await record(`/api/runs/${runId}`)) as RunSnapshot;
  const events: unknown[] = [];
  let after = 0;
  for (;;) {
    const page = await get(`/api/runs/${runId}/events?afterSeq=${after}&limit=${EVENT_PAGE}`);
    const items = (page.body as { items: { seq: number }[] }).items;
    events.push(...items);
    const last = items.at(-1);
    if (!last || items.length < EVENT_PAGE) break;
    after = last.seq;
  }
  const secrets = findSecrets(events);
  if (secrets.length > 0) throw new Error(`events of ${runId} carry ${secrets.join(', ')}`);
  fileNo += 1;
  const file = `${String(fileNo).padStart(3, '0')}-runs-${runId}-events.json`;
  await writeFile(path.join(OUT_DIR, file), `${JSON.stringify({ items: events }, null, 2)}\n`);
  // The fixture server pages this list by afterSeq and limit, like the Backend does.
  index[`/api/runs/${runId}/events`] = { file, status: 200 };

  for (const nodeRun of snapshot.nodeRuns) {
    await record(`/api/runs/${runId}/nodes/${nodeRun.id}`);
    if (nodeRun.nodeType === 'agent') await record(`/api/runs/${runId}/nodes/${nodeRun.id}/agent`);
  }
  const { workflowId, definitionVersion } = snapshot.run;
  const versionPath = `/api/definitions/${workflowId}/versions/${definitionVersion}`;
  if (!index[versionPath]) await record(versionPath);
}

async function main(): Promise<void> {
  await mkdir(OUT_DIR, { recursive: true });
  for (const entry of await readdir(OUT_DIR)) await rm(path.join(OUT_DIR, entry));

  const ready = await get('/ready');
  index['/ready'] = { file: '', status: ready.status };

  const list = (await record('/api/definitions')) as { items: DefinitionListItem[] };
  await record('/api/node-types');
  await record('/api/models');
  await record('/api/tools');

  const named = arg('workflows')?.split(',').filter(Boolean);
  const seen = new Set<string>();
  const chosen = named
    ? list.items.filter((item) => named.includes(item.workflowId))
    : list.items.filter((item) => {
        if (seen.has(item.name) || seen.size >= DEFAULT_DEPTH) return false;
        seen.add(item.name);
        return true;
      });

  for (const item of chosen) {
    await record(`/api/definitions/${item.workflowId}`);
    if (item.lastRun) await recordRun(item.lastRun.id);
  }

  await writeFile(
    path.join(OUT_DIR, 'index.json'),
    `${JSON.stringify({ recordedAt: new Date().toISOString(), paths: index }, null, 2)}\n`,
  );
  console.log(`recorded ${Object.keys(index).length} responses from ${BACKEND}`);
}

await main();
