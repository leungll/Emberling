import type { Edge, Node, NodeMetadata, PortDataType, PortMetadata } from '@/api/types';
import { editor as t } from '@/lib/copy/editor';
import { fits, portLabel, portOf, typeWord, type Catalog } from './steps';

/** The steps and links the connect rules read. */
export interface Graph {
  nodes: readonly Node[];
  edges: readonly Edge[];
}

export type LinkProblemCode = 'missing' | 'self' | 'type' | 'dup' | 'cycle' | 'busy' | 'none';

export interface LinkProblem {
  code: LinkProblemCode;
  text: string;
  /** For `busy`: the link the input already has, which Replace swaps out. */
  edge?: Edge;
}

function nodeById(graph: Graph, id: string): Node | undefined {
  return graph.nodes.find((n) => n.id === id);
}

/** Whether following links from `from` arrives at `to`. */
export function reaches(edges: readonly Edge[], from: string, to: string): boolean {
  const seen = new Set<string>();
  const stack = [from];
  for (let id = stack.pop(); id !== undefined; id = stack.pop()) {
    if (id === to) return true;
    if (seen.has(id)) continue;
    seen.add(id);
    for (const e of edges) if (e.source === id) stack.push(e.target);
  }
  return false;
}

/**
 * Why a link from an output to an input can't be made, or `null` when it can. The checks run
 * in a fixed order so the first reason is the one that matters most. `busy` is the one the
 * user can resolve on the spot by replacing the input's current link.
 */
export function linkProblem(
  graph: Graph,
  catalog: Catalog,
  sid: string,
  sh: string,
  tid: string,
  th: string,
): LinkProblem | null {
  const sn = nodeById(graph, sid);
  const tn = nodeById(graph, tid);
  const op = portOf(catalog, sn, 'out', sh);
  const ip = portOf(catalog, tn, 'in', th);
  if (!sn || !tn || !op || !ip) return { code: 'missing', text: t.links.missing };
  if (sid === tid) return { code: 'self', text: t.links.self };
  if (!fits(op.dataType, ip.dataType)) {
    return { code: 'type', text: t.links.type(typeWord(op.dataType), typeWord(ip.dataType)) };
  }
  const same = graph.edges.some(
    (e) => e.source === sid && e.sourceHandle === sh && e.target === tid && e.targetHandle === th,
  );
  if (same) return { code: 'dup', text: t.links.dup };
  if (reaches(graph.edges, tid, sid))
    return { code: 'cycle', text: t.links.cycle(tn.name, sn.name) };
  const cur = graph.edges.find((e) => e.target === tid && e.targetHandle === th);
  if (cur) {
    const from = nodeById(graph, cur.source)?.name ?? cur.source;
    return { code: 'busy', edge: cur, text: t.links.busy(portLabel(ip), tn.name, from) };
  }
  return null;
}

export interface CardTarget {
  nid: string;
  handle?: string;
  problem: LinkProblem | null;
}

/** Dropping a link onto a whole card: its first free fitting input, else a fitting one in use. */
export function cardTarget(
  graph: Graph,
  catalog: Catalog,
  sid: string,
  sh: string,
  tid: string,
): CardTarget {
  const tn = nodeById(graph, tid);
  const meta = tn && catalog.get(tn.type);
  const op = portOf(catalog, nodeById(graph, sid), 'out', sh);
  if (sid === tid) return { nid: tid, problem: { code: 'self', text: t.links.self } };
  if (!tn || !meta || meta.inputs.length === 0) {
    return { nid: tid, problem: { code: 'none', text: t.links.noInputs(tn?.name ?? tid) } };
  }
  const all = meta.inputs.map((p) => ({
    nid: tid,
    handle: p.name,
    problem: linkProblem(graph, catalog, sid, sh, tid, p.name),
  }));
  const ok = all.find((x) => !x.problem) ?? all.find((x) => x.problem?.code === 'busy');
  if (ok) return ok;
  if (all.every((x) => x.problem?.code === 'type')) {
    return {
      nid: tid,
      problem: {
        code: 'type',
        text: t.links.noTypeInput(tn.name, typeWord(op?.dataType ?? 'any')),
      },
    };
  }
  return { nid: tid, problem: all.find((x) => x.problem?.code !== 'type')?.problem ?? null };
}

/** Where a new connected step goes: after an output, or into an existing link. */
export type LinkContext =
  | { kind: 'from'; sid: string; handle: string; type: PortDataType; at?: { x: number; y: number } }
  | { kind: 'insert'; edgeId: string };

export interface LinkPlan {
  inPort: PortMetadata;
  outPort?: PortMetadata;
}

/**
 * The ports a new step of this type would use. From an output: its first fitting input,
 * required inputs first. Into a link: an input that takes the source and an output the target takes.
 */
export function linkPlan(
  meta: NodeMetadata,
  link: LinkContext,
  graph: Graph,
  catalog: Catalog,
): LinkPlan | null {
  const ins = [...meta.inputs].sort((a, b) => Number(b.required) - Number(a.required));
  if (link.kind === 'from') {
    const inPort = ins.find((p) => fits(link.type, p.dataType));
    return inPort ? { inPort } : null;
  }
  const e = graph.edges.find((x) => x.id === link.edgeId);
  if (!e) return null;
  const op = portOf(catalog, nodeById(graph, e.source), 'out', e.sourceHandle);
  const ip = portOf(catalog, nodeById(graph, e.target), 'in', e.targetHandle);
  if (!op || !ip) return null;
  const inPort = ins.find((p) => fits(op.dataType, p.dataType));
  const outPort = meta.outputs.find((p) => fits(p.dataType, ip.dataType));
  return inPort && outPort ? { inPort, outPort } : null;
}
