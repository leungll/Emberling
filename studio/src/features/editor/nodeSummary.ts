import type { ExecutionKind, JsonObject, ModelMetadata, NodeMetadata } from '@/api/types';

/**
 * One-line, factual node card summaries (04 §2.5). Each summary reads only the node's own
 * registered NodeMetadata (ports, category, uiSchema) and its own Definition-node config;
 * nothing here is a marketing description or an invented capability. A generic
 * "ExecutionKind · SideEffect" line was tried and rejected (it told a reader nothing about
 * what the node actually does), so this derives the summary from the same facts the
 * Properties panel edits instead.
 */

export function executionKindLabel(kind: ExecutionKind): string {
  switch (kind) {
    case 'SYNC':
      return 'Sync';
    case 'ASYNC':
      return 'Async';
    case 'MANAGED_AGENT':
      return 'Managed agent';
    default:
      return kind;
  }
}

function modelLabel(modelId: unknown, models: ModelMetadata[]): string {
  if (typeof modelId !== 'string' || modelId === '') return 'No model selected';
  return models.find((model) => model.id === modelId)?.displayName ?? modelId;
}

/**
 * Input Nodes (Text Input, Image Input) share the same `inputKey`/`required` config shape
 * (backend/internal/nodes/textinput, imageinput). The card's type is already the eyebrow
 * (RegisteredNode); this line names the Run input field it binds, whether a Run must
 * supply it, and the accepted type (04 §2.5), read off the registered output ports.
 */
function inputSummary(metadata: NodeMetadata, config: JsonObject): string {
  const inputKey = typeof config.inputKey === 'string' && config.inputKey ? config.inputKey : '—';
  const required = config.required === true;
  const summary = `${inputKey} · ${required ? 'required' : 'optional'}`;
  const accepted = portTypeList(metadata.outputs);
  return accepted ? `${summary} · ${accepted}` : summary;
}

/**
 * Text/Media Output take no config (backend/internal/nodes/textoutput, mediaoutput): their
 * complete logical result becomes the Run's output.
 */
function outputSummary(): string {
  return 'Run output';
}

/**
 * A Model call node (any node whose uiSchema names a MODEL_SELECTOR field, i.e. Text
 * Generation and Image Generation) summarises the resolved model, one representative
 * MODEL_PARAMETERS value if the config sets one (e.g. Image Generation's `width`), and the
 * Sync/Async mode — the three facts 04 §2.5 calls out, read generically off the Registry's
 * own uiSchema groups rather than a per-node-type field name.
 */
function generationSummary(
  metadata: NodeMetadata,
  config: JsonObject,
  models: ModelMetadata[],
  modelField: string,
): string {
  const parts = [modelLabel(config[modelField], models)];
  const specField = metadata.uiSchema.fields.find((field) => field.group === 'MODEL_PARAMETERS');
  if (specField) {
    const value = config[specField.path];
    // Name the parameter with its config key ("width 1024") so a bare number is not
    // mistaken for some other fact.
    if (typeof value === 'string' || typeof value === 'number') {
      parts.push(`${specField.path} ${String(value)}`);
    }
  }
  parts.push(executionKindLabel(metadata.executionKind));
  return parts.join(' · ');
}

/**
 * Agent nodes (backend/internal/nodes/agent) summarise the resolved model, how many Tools
 * the Definition allows it, and its Max Turns limit — the four facts 04 §2.5 calls out for
 * an Agent card (name is already the card's bold title).
 */
function agentSummary(config: JsonObject, models: ModelMetadata[]): string {
  const parts = [modelLabel(config.modelId, models)];
  const toolCount = Array.isArray(config.allowedTools) ? config.allowedTools.length : 0;
  parts.push(`${toolCount} allowed tool${toolCount === 1 ? '' : 's'}`);
  if (typeof config.maxTurns === 'number') parts.push(`max ${config.maxTurns} turns`);
  return parts.join(' · ');
}

function portTypeList(ports: NodeMetadata['inputs']): string {
  const unique = [...new Set(ports.map((p) => p.dataType))];
  return unique.join('/');
}

/** Port-flow fallback for a node with no more specific summary rule (e.g. Prompt Template). */
function portFlowSummary(metadata: NodeMetadata): string {
  const inputs = portTypeList(metadata.inputs);
  const outputs = portTypeList(metadata.outputs);
  if (!inputs && !outputs) return 'no ports';
  if (!inputs) return `→ ${outputs}`;
  if (!outputs) return `${inputs} →`;
  return `${inputs} → ${outputs}`;
}

/**
 * Card summary line for one Definition node. `config` is the node's own persisted config
 * (empty object for a node not yet configured); `models` is the registered Model list used
 * to resolve a `modelId` to its display name.
 */
export function nodeSummaryLine(
  metadata: NodeMetadata,
  config: JsonObject = {},
  models: ModelMetadata[] = [],
): string {
  if (metadata.category === 'Input') return inputSummary(metadata, config);
  if (metadata.category === 'Output') return outputSummary();
  if (metadata.category === 'Agent') return agentSummary(config, models);

  const modelField = metadata.uiSchema.fields.find((field) => field.widget === 'MODEL_SELECTOR');
  if (modelField) return generationSummary(metadata, config, models, modelField.path);

  return portFlowSummary(metadata);
}

/**
 * Palette summary for a registered Node Type (no Definition node, so no config yet). It
 * states what kind of Runtime work the type performs, from NodeMetadata alone, instead of
 * the per-node summary's config-dependent placeholders ("No model selected").
 */
export function paletteSummaryLine(metadata: NodeMetadata): string {
  if (metadata.category === 'Input') {
    const outputs = portTypeList(metadata.outputs);
    return outputs ? `${outputs} source` : 'Run input';
  }
  if (metadata.category === 'Output') return 'Run output';
  if (metadata.category === 'Agent') return 'Model · Tools · managed loop';
  const hasModel = metadata.uiSchema.fields.some((field) => field.widget === 'MODEL_SELECTOR');
  if (hasModel) return `Model call · ${executionKindLabel(metadata.executionKind)}`;
  return portFlowSummary(metadata);
}

export interface RuntimeSemanticsCounts {
  sync: number;
  async: number;
  agent: number;
}

export function runtimeSemanticsCounts(nodeTypes: NodeMetadata[]): RuntimeSemanticsCounts {
  return nodeTypes.reduce<RuntimeSemanticsCounts>(
    (acc, metadata) => {
      if (metadata.executionKind === 'SYNC') acc.sync += 1;
      else if (metadata.executionKind === 'ASYNC') acc.async += 1;
      else if (metadata.executionKind === 'MANAGED_AGENT') acc.agent += 1;
      return acc;
    },
    { sync: 0, async: 0, agent: 0 },
  );
}
