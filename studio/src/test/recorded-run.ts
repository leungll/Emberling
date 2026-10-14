// Run models built from responses recorded from a real Backend (tests/ui/fixtures), so unit
// tests read the same shapes the UI review serves.
import type {
  AgentTrace,
  Definition,
  NodeMetadata,
  NodeRunDetail,
  RunEvent,
  RunSnapshot,
} from '@/api/types';
import { buildRunModel, type RunModel } from '@/features/observe/run-model';

interface FixtureIndex {
  paths: Record<string, string | { file: string }>;
}

const FILES = import.meta.glob<unknown>('../../tests/ui/fixtures/*/*.json', {
  eager: true,
  import: 'default',
});

/** A fixture directory's responses by request path. */
function responses(dir: string): (path: string) => unknown {
  const read = (file: string): unknown => {
    const body = FILES[`../../tests/ui/fixtures/${dir}/${file}`];
    if (body === undefined) throw new Error(`no fixture ${dir}/${file}`);
    return body;
  };
  const index = read('index.json') as FixtureIndex;
  return (path) => {
    const entry = index.paths[path];
    if (!entry) return undefined;
    return read(typeof entry === 'string' ? entry : entry.file);
  };
}

/** The recorded photo set run: three photos, three generated images, one review failed. */
export const PHOTO_RUN = { dir: 'photo-run', runId: 'run_a66a823e6a9b465480f0537c925ff999' };

export function recordedParts(dir: string, runId: string) {
  const get = responses(dir);
  const snapshot = get(`/api/runs/${runId}`) as RunSnapshot;
  const run = snapshot.run;
  const definition = get(
    `/api/definitions/${run.workflowId}/versions/${String(run.definitionVersion)}`,
  ) as Definition;
  const events =
    (get(`/api/runs/${runId}/events`) as { items: RunEvent[] } | undefined)?.items ?? [];
  const details: Record<string, NodeRunDetail> = {};
  const traces: Record<string, AgentTrace> = {};
  for (const nr of snapshot.nodeRuns) {
    const detail = get(`/api/runs/${runId}/nodes/${nr.id}`) as NodeRunDetail | undefined;
    if (detail) details[nr.id] = detail;
    const trace = get(`/api/runs/${runId}/nodes/${nr.id}/agent`) as AgentTrace | undefined;
    if (trace) traces[nr.id] = trace;
  }
  const nodeTypes =
    (responses('recorded')('/api/node-types') as { items: NodeMetadata[] }).items ?? [];
  return { snapshot, definition, events, details, traces, nodeTypes };
}

export type RecordedParts = ReturnType<typeof recordedParts>;

/** A deep copy of the recorded parts, for a test that edits them into another state. */
export function recordedCopy(dir: string, runId: string): RecordedParts {
  return structuredClone(recordedParts(dir, runId));
}

export function recordedModel(parts: RecordedParts): RunModel {
  return buildRunModel(parts);
}
