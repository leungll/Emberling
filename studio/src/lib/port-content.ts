import type { Edge, JsonValue, Node } from '@/api/types';
import { copy } from '@/lib/copy';

/**
 * Ports named by what flows through them. An Agent hands back its final report on its output,
 * so that port reads "Final report" (or the title its declared report format gives it) rather
 * than its data type, and so does every input it feeds. Works for any Agent and any step that
 * follows it.
 */

const isObj = (v: unknown): v is Record<string, JsonValue> =>
  typeof v === 'object' && v !== null && !Array.isArray(v);

/** The report format of an Agent's output, or null when it hands back plain text. */
export function reportSchemaOf(node: Node | undefined): Record<string, JsonValue> | null {
  if (node?.type !== 'agent') return null;
  const s = node.config.outputSchema;
  return isObj(s) && isObj(s.properties) ? s : null;
}

/** Every Agent hands back its final report on its output; a declared format may title it. */
export function reportTitle(node: Node | undefined): string | null {
  if (node?.type !== 'agent') return null;
  const s = reportSchemaOf(node);
  return s && typeof s.title === 'string' && s.title.trim() ? s.title : copy.editor.reportPort;
}

/** The Agent whose report enters `nodeId` at `port`, if any. */
export function reportSource(
  nodes: readonly Node[],
  edges: readonly Edge[],
  nodeId: string,
  port?: string,
): Node | null {
  for (const e of edges) {
    if (e.target !== nodeId || (port && e.targetHandle !== port)) continue;
    const src = nodes.find((n) => n.id === e.source);
    if (src?.type === 'agent') return src;
  }
  return null;
}

/** The label a port shows: what flows through it when that is known, else null. */
export function contentLabel(
  nodes: readonly Node[],
  edges: readonly Edge[],
  node: Node,
  side: 'in' | 'out',
  port: string,
): string | null {
  if (side === 'out') return reportTitle(node);
  return reportTitle(reportSource(nodes, edges, node.id, port) ?? undefined);
}
