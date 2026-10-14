import { readJson, writeJson } from '@/lib/storage';

/**
 * The sidebar's Recent workflows: the ones this viewer opened (localStorage `emb.recent`,
 * most recent first), then the most recently updated ones, one per name, five at most.
 */
export const RECENT_KEY = 'emb.recent';
const KEEP = 20;
export const RECENT_MAX = 5;

function isIdList(v: unknown): v is string[] {
  return Array.isArray(v) && v.every((x) => typeof x === 'string');
}

export function readRecent(): string[] {
  return readJson<string[]>('local', RECENT_KEY, [], isIdList);
}

/** The Editor and a workflow's Runs tab note the workflow the viewer opened. */
export function noteRecent(workflowId: string): void {
  if (!workflowId) return;
  const list = readRecent().filter((id) => id !== workflowId);
  list.unshift(workflowId);
  writeJson('local', RECENT_KEY, list.slice(0, KEEP));
}

interface RecentCandidate {
  workflowId: string;
  name: string;
  updatedAt: string;
}

export function recentWorkflows<T extends RecentCandidate>(
  items: readonly T[],
  visited: readonly string[],
  max = RECENT_MAX,
): T[] {
  const byId = new Map(items.map((d) => [d.workflowId, d]));
  const seen = new Set<string>();
  const out: T[] = [];
  const add = (d: T | undefined) => {
    if (!d || seen.has(d.name) || out.length >= max) return;
    seen.add(d.name);
    out.push(d);
  };
  for (const id of visited) add(byId.get(id));
  const byUpdate = [...items].sort((a, b) =>
    a.updatedAt < b.updatedAt ? 1 : a.updatedAt > b.updatedAt ? -1 : 0,
  );
  for (const d of byUpdate) add(d);
  return out;
}
