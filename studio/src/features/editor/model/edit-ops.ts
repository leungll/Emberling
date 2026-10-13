import type { Edge, Node, NodeMetadata } from '@/api/types';
import type { LinkContext, LinkPlan } from './connect';
import { CARD_W, GAP_X, clearBelow, snap } from './layout';
import { defaultConfig, newStepName } from './steps';

/**
 * Edits of the steps and links, as pure functions of the draft. Each returns the next nodes
 * and edges; the store records one history entry around each.
 */
export interface Graph {
  nodes: Node[];
  edges: Edge[];
}

export interface IdSource {
  node: (type: string) => string;
  edge: () => string;
}

export function newStep(
  meta: NodeMetadata,
  nodes: readonly Node[],
  id: string,
  x: number,
  y: number,
): Node {
  return {
    id,
    type: meta.type,
    name: newStepName(meta, nodes),
    position: { x: snap(x), y: snap(y) },
    config: defaultConfig(meta, nodes),
  };
}

/** A link into an input that already has one replaces it. */
export function connect(
  g: Graph,
  ids: IdSource,
  s: string,
  sh: string,
  t: string,
  th: string,
): Graph {
  const same = g.edges.some(
    (e) => e.source === s && e.sourceHandle === sh && e.target === t && e.targetHandle === th,
  );
  if (same) return g;
  const kept = g.edges.filter((e) => !(e.target === t && e.targetHandle === th));
  return {
    nodes: g.nodes,
    edges: [...kept, { id: ids.edge(), source: s, sourceHandle: sh, target: t, targetHandle: th }],
  };
}

export function deleteSteps(g: Graph, remove: readonly string[]): Graph {
  const set = new Set(remove);
  return {
    nodes: g.nodes.filter((n) => !set.has(n.id)),
    edges: g.edges.filter((e) => !set.has(e.source) && !set.has(e.target)),
  };
}

export function deleteEdge(g: Graph, id: string): Graph {
  return { nodes: g.nodes, edges: g.edges.filter((e) => e.id !== id) };
}

/**
 * Copies of steps, 32 px right and down, named "… copy". An inputKey gains "Copy" and a
 * label equal to the step name follows the new name. Links are not copied.
 */
export function duplicate(
  g: Graph,
  which: readonly string[],
  places: Record<string, { x: number; y: number }>,
  ids: IdSource,
): { graph: Graph; created: string[] } {
  const keys = new Set(
    g.nodes.map((n) => n.config.inputKey).filter((k): k is string => typeof k === 'string'),
  );
  const copies = which.flatMap((id): Node[] => {
    const n = g.nodes.find((x) => x.id === id);
    const p = places[id];
    if (!n || !p) return [];
    const name = `${n.name} copy`;
    const config = JSON.parse(JSON.stringify(n.config)) as Node['config'];
    if (typeof config.inputKey === 'string' && config.inputKey) {
      let k = `${config.inputKey}Copy`;
      for (let i = 2; keys.has(k); i += 1) k = `${config.inputKey}Copy${i}`;
      keys.add(k);
      config.inputKey = k;
    }
    if (config.label === n.name) config.label = name;
    return [{ ...n, id: ids.node(n.type), name, config, position: { x: p.x + 32, y: p.y + 32 } }];
  });
  return {
    graph: { nodes: [...g.nodes, ...copies], edges: g.edges },
    created: copies.map((c) => c.id),
  };
}

export interface LinkedPlacement {
  /** Display places of every step (top-left, height as drawn). */
  places: Record<string, { x: number; y: number; h: number }>;
  /** A port's vertical offset within its card. */
  portY: (id: string, dir: 'in' | 'out', handle: string) => number;
}

/**
 * Adds a step connected after an output, or inserted into a link. After an output it goes one
 * column right (or at the drop point); inserted, it goes right of the source and the target's
 * column and everything right of it move over by the room that is missing.
 */
export function addLinked(
  g: Graph,
  meta: NodeMetadata,
  plan: LinkPlan,
  link: LinkContext,
  at: LinkedPlacement,
  ids: IdSource,
): { graph: Graph; created: string } | null {
  let nodes = g.nodes;
  let edges = g.edges;
  let x: number;
  let anchorY: number;
  const places = { ...at.places };
  if (link.kind === 'from') {
    const sp = places[link.sid];
    if (!sp) return null;
    x = link.at ? snap(link.at.x) : snap(sp.x + CARD_W + GAP_X + 24);
    anchorY = link.at ? link.at.y : sp.y + at.portY(link.sid, 'out', link.handle);
  } else {
    const e = edges.find((x2) => x2.id === link.edgeId);
    const a = e && places[e.source];
    const b = e && places[e.target];
    if (!e || !a || !b) return null;
    x = snap(a.x + CARD_W + GAP_X);
    const need = snap(x + CARD_W + GAP_X - b.x);
    if (need > 0 && b.x > a.x) {
      nodes = nodes.map((m) => {
        const p = places[m.id];
        if (m.id === e.source || !p || p.x < b.x - 1) return m;
        places[m.id] = { ...p, x: p.x + need };
        return { ...m, position: { x: p.x + need, y: p.y } };
      });
    }
    anchorY = a.y + at.portY(e.source, 'out', e.sourceHandle);
  }
  const id = ids.node(meta.type);
  const step = newStep(meta, nodes, id, x, 0);
  // Line the new card's input up with the link, then step down past any card it would cover.
  const inY = 40;
  const y = clearBelow(id, step.position.x, snap(anchorY - inY), 120, places);
  nodes = [...nodes, { ...step, position: { x: step.position.x, y } }];
  if (link.kind === 'from') {
    edges = [
      ...edges,
      {
        id: ids.edge(),
        source: link.sid,
        sourceHandle: link.handle,
        target: id,
        targetHandle: plan.inPort.name,
      },
    ];
  } else {
    const e = edges.find((x2) => x2.id === link.edgeId);
    if (!e || !plan.outPort) return null;
    edges = [
      ...edges.filter((x2) => x2.id !== e.id),
      {
        id: ids.edge(),
        source: e.source,
        sourceHandle: e.sourceHandle,
        target: id,
        targetHandle: plan.inPort.name,
      },
      {
        id: ids.edge(),
        source: id,
        sourceHandle: plan.outPort.name,
        target: e.target,
        targetHandle: e.targetHandle,
      },
    ];
  }
  return { graph: { nodes, edges }, created: id };
}
