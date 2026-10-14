/**
 * A photo set run recorded from a real Backend with GET requests only: a media brief with
 * three uploaded photos, an Agent that generated an image for each and reviewed them, a
 * Media Result that reports two of three passed, and a Media Output. Its images are run
 * artifacts, which the main recording predates, so it lives in its own fixture directory
 * and is served through overrides. Image bytes are not recorded; the fixture server answers
 * every content route with a small grey picture.
 */
import { readFileSync } from 'node:fs';
import path from 'node:path';

import { runList, withRuns, type RunItem } from './runs';
import type { Overrides } from './server';

const DIR = path.join(import.meta.dirname, '..', 'fixtures', 'photo-run');

interface PhotoIndex {
  runId: string;
  paths: Record<string, string>;
}

const index = JSON.parse(readFileSync(path.join(DIR, 'index.json'), 'utf8')) as PhotoIndex;

function read(pathname: string): unknown {
  const file = index.paths[pathname];
  if (!file) throw new Error(`no photo run fixture for ${pathname}`);
  return JSON.parse(readFileSync(path.join(DIR, file), 'utf8'));
}

interface Snapshot {
  run: {
    id: string;
    workflowId: string;
    definitionVersion: number;
    status: string;
    startedAt: string;
    completedAt: string | null;
  };
}

const snapshot = read(`/api/runs/${index.runId}`) as Snapshot;
const definition = read(`/api/definitions/${snapshot.run.workflowId}`) as {
  name: string;
  version: number;
};

export const photoRun = { workflowId: snapshot.run.workflowId, runId: index.runId };

const item: RunItem = {
  id: snapshot.run.id,
  workflowId: snapshot.run.workflowId,
  workflowName: definition.name,
  definitionVersion: snapshot.run.definitionVersion,
  latestVersion: definition.version,
  status: snapshot.run.status,
  createdAt: snapshot.run.startedAt,
  startedAt: snapshot.run.startedAt,
  completedAt: snapshot.run.completedAt,
  error: null,
  failedStep: null,
  waitingSteps: [],
};

/** Every recorded response of the photo run, and the run list with it in. */
export function photoRunOverrides(): Overrides {
  const out: Overrides = { '/api/runs': runList(withRuns([item])) };
  for (const pathname of Object.keys(index.paths)) {
    if (pathname.endsWith('/events')) {
      const items = (read(pathname) as { items: { seq: number }[] }).items;
      out[pathname] = (url) => {
        const after = Number(url.searchParams.get('afterSeq') ?? '0');
        return { status: 200, body: { items: items.filter((e) => e.seq > after) } };
      };
    } else {
      out[pathname] = { status: 200, body: read(pathname) };
    }
  }
  return out;
}
