import type {
  Edge,
  JsonSchema,
  JsonValue,
  ModelMetadata,
  Node,
  NodeMetadata,
  ToolMetadata,
} from '@/api/types';
import { copy } from '@/lib/copy';
import { orderedKeys, schemaFields } from '@/lib/schema-fields';
import { limitText } from '@/lib/time';
import { toolFlow } from '@/lib/agent-tools';

/**
 * The plain-language overview of a workflow: one sentence per step, what a run needs to start
 * and what it produces. Built from the saved configuration and the registered step types only;
 * the Editor's overview and the run pages show the same words.
 */

export interface SummaryDefinition {
  description?: string;
  nodes: readonly Node[];
  edges: readonly Edge[];
}

export interface SummaryRegistry {
  nodeTypes: ReadonlyMap<string, NodeMetadata>;
  models: ReadonlyMap<string, ModelMetadata>;
  /** Lets an Agent's sentence name its tools in their working order. */
  tools?: ReadonlyMap<string, ToolMetadata>;
}

const s = copy.editor.summary;

/** Steps in reading order: left to right, then top to bottom. */
export function summaryOrder(nodes: readonly Node[]): Node[] {
  return [...nodes].sort((a, b) => a.position.x - b.position.x || a.position.y - b.position.y);
}

function fieldLabel(n: Node): string {
  const label = n.config.label;
  return typeof label === 'string' && label !== '' ? label : n.name;
}

function num(v: unknown): number | null {
  return typeof v === 'number' && Number.isFinite(v) ? v : null;
}

/** "Stops after 8 turns or 2 min" for an agent's limits; `cardLimits` reads "Up to …". */
export function agentLimits(config: Node['config'], form: 'summary' | 'card' = 'summary'): string {
  const turns = num(config.maxTurns);
  const time = limitText(num(config.timeoutMs));
  const gen = num(config.maxGenerationCalls);
  return form === 'card' ? s.cardLimits(turns, time, gen) : s.limits(turns, time, gen);
}

/** One plain sentence per step, from its configuration. */
export function stepSentence(def: SummaryDefinition, n: Node, reg: SummaryRegistry): string {
  const c = n.config;
  const from = (handle?: string): string => {
    const names = def.edges
      .filter((e) => e.target === n.id && (!handle || e.targetHandle === handle))
      .map((e) => def.nodes.find((x) => x.id === e.source)?.name)
      .filter((x): x is string => !!x);
    return copy.editor.names(names);
  };
  const modelId = typeof c.modelId === 'string' ? c.modelId : '';
  const model = reg.models.get(modelId)?.displayName ?? s.aModel;
  switch (n.type) {
    case 'agent': {
      const tools = Array.isArray(c.allowedTools)
        ? c.allowedTools.filter((x): x is string => typeof x === 'string')
        : [];
      // The tools by their display names, in the order their facts allow.
      const flow = reg.tools ? toolFlow(tools, reg.tools, {}) : null;
      if (flow) {
        const labels = flow.map((x) => x.label.toLowerCase());
        return s.usesTools(model, copy.editor.names(labels), agentLimits(c));
      }
      return s.agent(model, tools, agentLimits(c));
    }
    case 'task_input': {
      // The fields a run must fill in, by their titles.
      const need = schemaFields(c.schema as JsonSchema | undefined).filter((x) => x.required);
      if (need.length)
        return s.asksFields(copy.editor.names(need.map((x) => x.label.toLowerCase())));
      return s.asks(fieldLabel(n));
    }
    case 'text_input':
    case 'image_input':
      return s.asks(fieldLabel(n));
    case 'media_brief':
      return s.brief(from('brief'));
    case 'prompt_template':
      return s.template(from('text'));
    case 'text_generation':
      return s.textGen(model);
    case 'image_generation':
      return s.imageGen(num(c.width), model);
    case 'media_result':
      return s.mediaResult(from('text'));
    case 'text_output':
    case 'media_output':
      return s.output(from());
    default:
      return reg.nodeTypes.get(n.type)?.description ?? copy.editor.purpose[n.type] ?? '';
  }
}

export interface SummaryInput {
  id: string;
  name: string;
  meta: string;
}

/** A task in a few words: how many fields it has, or that it takes any object. */
function taskKind(schema: JsonValue | undefined): string {
  const fields = orderedKeys(
    schema !== null && typeof schema === 'object' && !Array.isArray(schema) ? schema : undefined,
  );
  return fields.length ? s.taskFields(fields.length) : (s.kind.task ?? '');
}

/** What a run asks for: the input steps in reading order. */
export function summaryInputs(def: SummaryDefinition, reg: SummaryRegistry): SummaryInput[] {
  return summaryOrder(def.nodes)
    .filter((n) => reg.nodeTypes.get(n.type)?.category === 'Input')
    .map((n) => {
      const brief = n.type === 'media_brief';
      const kind = brief
        ? s.kind.photos
        : n.type === 'image_input'
          ? s.kind.image
          : n.type === 'task_input'
            ? taskKind(n.config.schema)
            : s.kind.text;
      return {
        id: n.id,
        name: brief ? n.name : fieldLabel(n),
        meta: s.meta(kind ?? '', brief || n.config.required === true),
      };
    });
}

/** What a run produces: the output steps that end the workflow, with what they take in. */
export function summaryOutputs(def: SummaryDefinition, reg: SummaryRegistry): SummaryInput[] {
  return summaryOrder(def.nodes).flatMap((n) => {
    const meta = reg.nodeTypes.get(n.type);
    if (meta?.category !== 'Output' || meta.outputs.length) return [];
    // An output fed by an Agent with a final report format produces that report.
    const report = def.edges.some((e) => {
      const src = def.nodes.find((x) => x.id === e.source);
      return e.target === n.id && src?.type === 'agent' && src.config.outputSchema !== undefined;
    });
    if (report) return [{ id: n.id, name: n.name, meta: s.agentReport }];
    const ports = meta.inputs.map((p) => (copy.editor.ports[p.name] ?? p.name).toLowerCase());
    return [{ id: n.id, name: n.name, meta: copy.editor.cap(copy.editor.names(ports)) }];
  });
}
