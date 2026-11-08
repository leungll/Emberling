import type { Node, ToolMetadata } from '@/api/types';
import { copy } from '@/lib/copy';
import { humanize } from '@/lib/value-reading';

/**
 * An Agent's tools as ordered steps, read from Registry metadata alone: readers first, then
 * tools that establish facts, then tools that need them. Every label, gate and retry count
 * comes from the registered tool and the node's configuration, so this reads the same for any
 * Agent and any set of tools.
 */

export interface ToolStep {
  tool: ToolMetadata;
  label: string;
  /** Failed results of the tool's own fact type the Agent may collect before it must stop. */
  fixes: number | null;
  /** Labels of what must have passed before this tool runs: the tool that produces each fact. */
  after: string[];
  /** True when the tool reports back later rather than in the same call. */
  later: boolean;
}

/** A tool's display name: its registered title when it has one, else its name in words. */
export function toolTitle(t: ToolMetadata): string {
  return t.title ?? humanize(t.name);
}

export function retriesText(n: number): string {
  return copy.editor.guide.retriesText(n);
}

export function allowedToolNames(node: Node): string[] {
  const v = node.config.allowedTools;
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
}

/** The per-fact-type allowance of failed results an Agent's configuration declares. */
export function maxFailedFacts(node: Node): Record<string, number> {
  const v = node.config.maxFailedFacts;
  if (!v || typeof v !== 'object' || Array.isArray(v)) return {};
  return Object.fromEntries(
    Object.entries(v).filter((e): e is [string, number] => typeof e[1] === 'number'),
  );
}

/**
 * The ordered tool steps for a list of tool names; null when a tool is not registered or
 * the list is empty, so the caller falls back to plain names.
 */
export function toolFlow(
  names: readonly string[],
  tools: ReadonlyMap<string, ToolMetadata>,
  maxFailed: Record<string, number>,
): ToolStep[] | null {
  const metas = names.map((n) => tools.get(n));
  if (!metas.length || metas.some((m) => !m)) return null;
  const list = metas as ToolMetadata[];
  const producer = (fact: string) => list.find((t) => t.produces?.factType === fact);
  const rank = (t: ToolMetadata) => (t.requires.length ? 2 : t.produces ? 1 : 0);
  return list
    .map((t, i) => ({ t, i }))
    .sort((a, b) => rank(a.t) - rank(b.t) || a.i - b.i)
    .map(({ t }) => {
      const own = t.produces?.factType;
      return {
        tool: t,
        label: toolTitle(t),
        fixes: own && typeof maxFailed[own] === 'number' ? maxFailed[own] : null,
        after: t.requires.map((r) => {
          const p = producer(r.factType);
          return p ? toolTitle(p) : humanize(r.factType);
        }),
        later: t.executionKind === 'ASYNC',
      };
    });
}

/** The ordered tool steps of an Agent node whose tools are all registered, else null. */
export function agentToolFlow(
  node: Node,
  tools: ReadonlyMap<string, ToolMetadata>,
): ToolStep[] | null {
  if (node.type !== 'agent') return null;
  return toolFlow(allowedToolNames(node), tools, maxFailedFacts(node));
}
