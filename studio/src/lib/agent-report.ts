import type { Edge, JsonValue, Node, NodeRun } from '@/api/types';
import { reportSchemaOf, reportSource } from '@/lib/port-content';

/**
 * An Agent's final report as a step handed it on. The Runtime wraps a goal-driven Agent's
 * answer in a completion envelope (outcome, the agent's report, and the result of each
 * completion check); the report itself follows the Agent's declared format when it has one.
 * Generic: any Agent, any fields.
 */

type Obj = Record<string, JsonValue>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

export interface ReportSchema {
  contentMediaType?: string;
  properties?: Record<string, ReportSchema>;
  required?: string[];
  description?: string;
  title?: string;
}

/** A JSON Schema with properties, read as a report format; null for anything else. */
export function schemaObj(v: JsonValue | undefined): ReportSchema | null {
  return isObj(v) && isObj(v.properties) ? v : null;
}

/**
 * The value in `data` that follows `schema`: `data` itself, a JSON text holding it, or an
 * object nested a few levels inside (a node output keeps the agent's answer under a port or
 * wraps it in a report).
 */
export function findBySchema(
  schema: ReportSchema,
  data: JsonValue | undefined,
  depth = 0,
): Obj | null {
  if (typeof data === 'string') {
    try {
      return findBySchema(schema, JSON.parse(data) as JsonValue, depth);
    } catch {
      return null;
    }
  }
  if (!isObj(data)) return null;
  const keys = Object.keys(schema.properties ?? {});
  const need = schema.required?.length ? schema.required : keys;
  if (keys.length && need.every((k) => k in data)) return data;
  if (depth >= 3) return null;
  for (const v of Object.values(data)) {
    const hit = findBySchema(schema, v, depth + 1);
    if (hit) return hit;
  }
  return null;
}

function parse(v: JsonValue | undefined): JsonValue | undefined {
  if (typeof v !== 'string') return v;
  try {
    return JSON.parse(v) as JsonValue;
  } catch {
    return v;
  }
}

/** The completion envelope inside a node output, when there is one. */
function envelopeOf(output: JsonValue | undefined): Obj | null {
  const v = parse(isObj(output) && typeof output.text === 'string' ? output.text : output);
  return isObj(v) && Array.isArray(v.checks) && 'outcome' in v ? v : null;
}

export interface LastReport {
  schema: Obj | null;
  outcome: string | null;
  data: Obj | null;
  text: string | null;
  checks: { name: string; passed: boolean }[];
}

/** The report a step fed by an Agent showed in a run, read from the step's own output. */
export function lastReport(
  nodes: readonly Node[],
  edges: readonly Edge[],
  node: Node,
  nr: NodeRun | undefined,
): LastReport | null {
  const agent = reportSource(nodes, edges, node.id);
  return agent ? reportOf(agent, nr) : null;
}

/** An Agent step's own final report, read from its output. */
export function reportOf(agent: Node, nr: NodeRun | undefined): LastReport | null {
  if (!nr) return null;
  const schema = reportSchemaOf(agent);
  const env = envelopeOf(nr.output);
  const inner = parse(env ? env.agentReport : isObj(nr.output) ? nr.output.text : undefined);
  const data = schema ? findBySchema(schema, inner) : isObj(inner) ? inner : null;
  return {
    schema,
    outcome: env && typeof env.outcome === 'string' ? env.outcome : null,
    data,
    text: !data && typeof inner === 'string' ? inner : null,
    checks: env
      ? (env.checks as JsonValue[]).filter(isObj).map((c) => ({
          name: typeof c.name === 'string' ? c.name : JSON.stringify(c.name),
          passed: c.passed === true,
        }))
      : [],
  };
}

/** True when the report has something to show: an outcome, structured data or text. */
export function hasReport(report: LastReport | null): report is LastReport {
  return !!report && (report.outcome !== null || report.data !== null || report.text !== null);
}
