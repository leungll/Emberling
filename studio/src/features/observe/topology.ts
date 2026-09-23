import type { Definition } from '@/api/types';

/**
 * Definition nodes in topological order, ties kept in Definition order. This orders the
 * immutable bound Definition for display only; it reads no NodeRun and decides nothing
 * about execution. A node on a cycle (which validation rejects) is appended in
 * Definition order rather than dropped.
 */
export function topologicalNodeOrder(definition: Definition): Definition['nodes'] {
  const indegree = new Map(definition.nodes.map((node) => [node.id, 0]));
  const next = new Map<string, string[]>();
  for (const edge of definition.edges) {
    if (!indegree.has(edge.source) || !indegree.has(edge.target)) continue;
    indegree.set(edge.target, (indegree.get(edge.target) ?? 0) + 1);
    next.set(edge.source, [...(next.get(edge.source) ?? []), edge.target]);
  }
  const ordered: Definition['nodes'] = [];
  const placed = new Set<string>();
  let progress = true;
  while (progress) {
    progress = false;
    for (const node of definition.nodes) {
      if (placed.has(node.id) || (indegree.get(node.id) ?? 0) > 0) continue;
      ordered.push(node);
      placed.add(node.id);
      progress = true;
      for (const target of next.get(node.id) ?? []) {
        indegree.set(target, (indegree.get(target) ?? 0) - 1);
      }
      break;
    }
  }
  return [...ordered, ...definition.nodes.filter((node) => !placed.has(node.id))];
}
