import type { Edge, JsonObject, Node } from '@/api/types';
import { snap } from './layout';
import type { Catalog } from './steps';

/**
 * Copied steps with the links between them. Kept in localStorage so a copy from one workflow
 * pastes into another; positions are relative to the copied group's top-left corner.
 */
export interface Clip {
  stamp: number;
  from: string;
  x0: number;
  y0: number;
  nodes: { ref: string; type: string; name: string; config: JsonObject; dx: number; dy: number }[];
  edges: Pick<Edge, 'source' | 'sourceHandle' | 'target' | 'targetHandle'>[];
}

export const CLIP_KEY = 'emb.editor.clipboard';

const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;

export function makeClip(
  ids: readonly string[],
  nodes: readonly Node[],
  edges: readonly Edge[],
  places: Record<string, { x: number; y: number }>,
  from: string,
  stamp: number,
): Clip | null {
  const picked = ids.flatMap((id) => {
    const n = nodes.find((x) => x.id === id);
    const p = places[id];
    return n && p ? [{ n, p }] : [];
  });
  if (!picked.length) return null;
  const x0 = Math.min(...picked.map(({ p }) => p.x));
  const y0 = Math.min(...picked.map(({ p }) => p.y));
  const set = new Set(picked.map(({ n }) => n.id));
  return {
    stamp,
    from,
    x0,
    y0,
    nodes: picked.map(({ n, p }) => ({
      ref: n.id,
      type: n.type,
      name: n.name,
      config: clone(n.config),
      dx: p.x - x0,
      dy: p.y - y0,
    })),
    edges: edges
      .filter((e) => set.has(e.source) && set.has(e.target))
      .map(({ source, sourceHandle, target, targetHandle }) => ({
        source,
        sourceHandle,
        target,
        targetHandle,
      })),
  };
}

export function parseClip(raw: string | null): Clip | null {
  if (!raw) return null;
  try {
    const c = JSON.parse(raw) as Partial<Clip> | null;
    if (!c || !Array.isArray(c.nodes) || !c.nodes.length || !Array.isArray(c.edges)) return null;
    if (typeof c.x0 !== 'number' || typeof c.y0 !== 'number') return null;
    return c as Clip;
  } catch {
    return null;
  }
}

/** A key based on `key` + "Copy" that no step in `taken` uses yet. */
export function copyKey(key: string, taken: Set<string>): string {
  let k = `${key}Copy`;
  for (let i = 2; taken.has(k); i += 1) k = `${key}Copy${i}`;
  taken.add(k);
  return k;
}

export interface Pasted {
  nodes: Node[];
  edges: Edge[];
  /** Steps whose type isn't registered here; they are skipped. */
  skipped: number;
}

/**
 * New steps and links from a clip, placed with their top-left at `at`. Names gain " copy",
 * an inputKey gains a unique "Copy" suffix, a label equal to the old name follows the new one,
 * and the links between copied steps are remapped onto the new ids.
 */
export function pasteClip(
  clip: Clip,
  at: { x: number; y: number },
  existing: readonly Node[],
  catalog: Catalog,
  newNodeId: (type: string) => string,
  newEdgeId: () => string,
): Pasted {
  const known = clip.nodes.filter((c) => catalog.has(c.type));
  const keys = new Set(
    existing.map((n) => n.config.inputKey).filter((k): k is string => typeof k === 'string'),
  );
  const map = new Map<string, string>();
  const nodes = known.map((c): Node => {
    const id = newNodeId(c.type);
    map.set(c.ref, id);
    const name = `${c.name} copy`;
    const config = clone(c.config);
    if (typeof config.inputKey === 'string' && config.inputKey)
      config.inputKey = copyKey(config.inputKey, keys);
    if (config.label === c.name) config.label = name;
    return {
      id,
      type: c.type,
      name,
      position: { x: snap(at.x + c.dx), y: snap(at.y + c.dy) },
      config,
    };
  });
  const edges = clip.edges.flatMap((e): Edge[] => {
    const source = map.get(e.source);
    const target = map.get(e.target);
    if (!source || !target) return [];
    return [
      {
        id: newEdgeId(),
        source,
        sourceHandle: e.sourceHandle,
        target,
        targetHandle: e.targetHandle,
      },
    ];
  });
  return { nodes, edges, skipped: clip.nodes.length - known.length };
}

/** Where a paste goes without a pointer: 32 px right and down per paste of the same copy. */
export function pasteOrigin(clip: Clip, round: number): { x: number; y: number } {
  return { x: clip.x0 + 32 * round, y: clip.y0 + 32 * round };
}
