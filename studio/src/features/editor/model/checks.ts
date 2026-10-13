import type {
  Edge,
  JsonValue,
  ModelMetadata,
  Node,
  ToolMetadata,
  ValidationError,
} from '@/api/types';
import { editor as t } from '@/lib/copy/editor';
import {
  FIELD_SPEC,
  KEY_RE,
  MODEL_PARAM,
  fieldKeyFor,
  genericLabel,
  specFor,
  type SectionId,
} from './fields';
import {
  fits,
  isOutputStep,
  ordered,
  portLabel,
  portOf,
  sourcesOf,
  typeWord,
  type Catalog,
} from './steps';

/**
 * Issues that block Save and Run. Each names its step, field and panel section so the checks
 * list, the card marker and the field itself can all point at it. Studio checks what it can
 * from the registered schemas for responsiveness; the server checks the workflow again.
 */
export interface Issue {
  node: string | null;
  field: string | null;
  text: string;
  section: SectionId | 'connections' | 'step' | null;
  label: string | null;
}

export interface CheckContext {
  catalog: Catalog;
  models: ReadonlyMap<string, ModelMetadata>;
  tools: ReadonlyMap<string, ToolMetadata>;
}

/** The checks Studio can make itself; the server may find more on save. */
export function validate(
  nodes: readonly Node[],
  edges: readonly Edge[],
  ctx: CheckContext,
): Issue[] {
  const out: Issue[] = [];
  const add: Add = (n, field, text, section, label) => {
    const spec = n && field ? FIELD_SPEC[`${n.type}.${field}`] : undefined;
    out.push({
      node: n ? n.id : null,
      field,
      text,
      section: section ?? spec?.section ?? (n ? 'settings' : null),
      label: label ?? spec?.label ?? (field ? genericLabel(field) : null),
    });
  };
  const keys = new Map<string, string>();
  for (const n of ordered(nodes)) {
    const meta = ctx.catalog.get(n.type);
    if (!meta) {
      add(n, 'type', t.checks.unregistered, 'step', t.panel.stepName);
      continue;
    }
    const c = n.config;
    const props = meta.configSchema.properties ?? {};
    const req = meta.configSchema.required ?? [];
    for (const [k, s] of Object.entries(props)) {
      const v = c[k];
      const f = fieldKeyFor(n.type, k);
      const spec = specFor(n.type, k);
      const name =
        spec?.widget === 'lengthRange'
          ? k === 'minLength'
            ? t.fields.minLength
            : t.fields.maxLength
          : (spec?.label ?? genericLabel(k));
      const missing =
        v === undefined ||
        v === null ||
        v === '' ||
        (Array.isArray(v) && !v.length && req.includes(k));
      if (missing) {
        if (!req.includes(k) || k === 'required') continue;
        if (k === 'inputKey') add(n, f, t.checks.addKey);
        else if (s.type === 'string' && /modelId$/i.test(k)) add(n, f, t.checks.chooseModel);
        else add(n, f, t.checks.missing[k] ?? t.checks.enterValue(name));
        continue;
      }
      if (s.type === 'integer') {
        if (typeof v !== 'number' || Math.round(v) !== v) {
          add(n, f, t.checks.wholeNumber(name));
          continue;
        }
        if (s.minimum !== undefined && v < s.minimum) {
          if (k === 'timeoutMs') add(n, f, t.checks.timeAboveZero);
          else {
            const min = `${s.minimum.toLocaleString('en-US')}${spec?.unit === t.fields.px ? ' px' : ''}`;
            add(n, f, t.checks.atLeast(name, min));
          }
        }
      }
      if (k === 'inputKey' && typeof v === 'string') {
        if (!KEY_RE.test(v)) add(n, f, t.checks.keyFormat);
        else if (keys.has(v)) add(n, f, t.checks.keyTaken(v));
        if (!keys.has(v)) keys.set(v, n.id);
      }
      if (/modelId$/i.test(k) && s.type === 'string' && typeof v === 'string') {
        const m = ctx.models.get(v);
        const ui = meta.uiSchema.fields.find((x) => x.path === k);
        if (!m) add(n, f, t.checks.modelMissing(v));
        else if (ui?.capability && !m.capabilities.includes(ui.capability)) {
          add(
            n,
            f,
            t.checks.modelCant(
              m.displayName,
              t.checks.capability[ui.capability] ?? t.checks.capabilityOther,
            ),
          );
        }
      }
    }
    if (
      typeof c.minLength === 'number' &&
      typeof c.maxLength === 'number' &&
      c.minLength > c.maxLength
    ) {
      add(n, 'textLength', t.checks.lengthOrder);
    }
    if (n.type === 'image_input' && typeof c.maxSizeBytes === 'number' && c.maxSizeBytes < 1) {
      add(n, 'maxSizeBytes', t.checks.sizeAboveZero);
    }
    checkModelConfig(n, props, ctx, add);
    const allowed = Array.isArray(c.allowedTools) ? c.allowedTools : [];
    for (const x of allowed) {
      if (typeof x === 'string' && !ctx.tools.has(x))
        add(n, 'allowedTools', t.checks.toolMissing(x));
    }
    for (const k of ['contextSchema', 'stateSchema', 'outputSchema']) {
      if (c[k] !== undefined && c[k] !== null && props[k]) {
        const err = schemaError(c[k]);
        if (err) add(n, k, err);
      }
    }
    for (const p of meta.inputs) {
      if (p.required && !sourcesOf(nodes, edges, n.id, p.name).length) {
        add(
          n,
          'connections',
          t.checks.connect(portLabel(p), n.name),
          'connections',
          t.panel.sections.connections,
        );
      }
    }
  }
  // Links whose types don't fit, for example from an imported workflow.
  for (const e of edges) {
    const sn = nodes.find((n) => n.id === e.source);
    const tn = nodes.find((n) => n.id === e.target);
    const op = portOf(ctx.catalog, sn, 'out', e.sourceHandle);
    const ip = portOf(ctx.catalog, tn, 'in', e.targetHandle);
    if (sn && tn && op && ip && !fits(op.dataType, ip.dataType)) {
      add(
        tn,
        'connections',
        t.checks.badLink(typeWord(op.dataType), sn.name, portLabel(ip), typeWord(ip.dataType)),
        'connections',
        t.panel.sections.connections,
      );
    }
  }
  if (!nodes.some((n) => isOutputStep(ctx.catalog, n)))
    add(null, null, t.checks.noOutput, null, null);
  return out;
}

type Add = (
  n: Node | null,
  field: string | null,
  text: string,
  section?: Issue['section'],
  label?: string | null,
) => void;

/** Model behavior is checked against the chosen model's own settings schema. */
function checkModelConfig(
  n: Node,
  props: Record<string, unknown>,
  ctx: CheckContext,
  add: Add,
): void {
  const modelId = n.config.modelId;
  const model = typeof modelId === 'string' ? ctx.models.get(modelId) : undefined;
  const mc = n.config.modelConfig;
  if (!props.modelConfig || !model || !mc || typeof mc !== 'object' || Array.isArray(mc)) return;
  const ms = model.configSchema;
  const mp = ms.properties ?? {};
  for (const [k, v] of Object.entries(mc)) {
    const s = mp[k];
    const label = MODEL_PARAM[k]?.label ?? k;
    const field = `modelConfig.${k}`;
    if (!s) {
      if (ms.additionalProperties === false)
        add(n, field, t.checks.paramUnknown(k, model.displayName), 'behavior', label);
      continue;
    }
    if (s.enum && !s.enum.includes(v)) add(n, field, t.checks.paramEnum(label), 'behavior', label);
    if (s.type === 'number' || s.type === 'integer') {
      if (typeof v !== 'number' || Number.isNaN(v))
        add(n, field, t.checks.paramNumber(label), 'behavior', label);
      else if (
        (s.minimum !== undefined && v < s.minimum) ||
        (s.maximum !== undefined && v > s.maximum)
      ) {
        add(
          n,
          field,
          t.checks.paramRange(label, s.minimum ?? -Infinity, s.maximum ?? Infinity),
          'behavior',
          label,
        );
      }
    }
  }
}

/** A data format is a JSON Schema object. Only its shape is checked here. */
export function schemaError(v: JsonValue): string {
  if (v === null || typeof v !== 'object' || Array.isArray(v)) return t.checks.notJsonObject;
  const types = ['object', 'array', 'string', 'number', 'integer', 'boolean', 'null'];
  if (v.type !== undefined && (typeof v.type !== 'string' || !types.includes(v.type))) {
    return t.checks.badSchemaType(typeof v.type === 'string' ? v.type : JSON.stringify(v.type));
  }
  if (
    v.properties !== undefined &&
    (typeof v.properties !== 'object' || v.properties === null || Array.isArray(v.properties))
  ) {
    return t.checks.badProperties;
  }
  if (v.required !== undefined && !Array.isArray(v.required)) return t.checks.badRequired;
  return '';
}

/** The step a server path such as `nodes[step_1].config/modelId` points at. */
export function nodeIdFromPath(path: string): string | null {
  const m = /^nodes\[([^\]]+)\]/.exec(path);
  return m?.[1] ?? null;
}

/** The config field of a server path, or `null` for a step-level or workflow-level path. */
export function configFieldFromPath(path: string): string | null {
  const m = /\.config[/.]([A-Za-z0-9_]+)/.exec(path);
  return m?.[1] ?? null;
}

/**
 * Server errors as issues. A server error the client checks already describe for the same
 * field is dropped, so the list never shows one problem twice.
 */
export function serverIssues(
  errors: readonly ValidationError[],
  nodes: readonly Node[],
  local: readonly Issue[],
): Issue[] {
  const out: Issue[] = [];
  for (const e of errors) {
    const nodeId = e.nodeId ?? nodeIdFromPath(e.path);
    const node = nodeId ? nodes.find((n) => n.id === nodeId) : undefined;
    const key = configFieldFromPath(e.path);
    const field = node && key ? fieldKeyFor(node.type, key) : null;
    const nodeRef = node ? node.id : null;
    if (local.some((i) => i.node === nodeRef && i.field === field && field !== null)) continue;
    const spec = node && field ? FIELD_SPEC[`${node.type}.${field}`] : undefined;
    out.push({
      node: nodeRef,
      field,
      text: e.message || t.checks.serverWhole,
      section: spec?.section ?? (node ? (field ? 'settings' : 'step') : null),
      label: spec?.label ?? (field ? genericLabel(field) : null),
    });
  }
  return out;
}

/** Issues of one step, optionally of one field (model behavior covers its parameters). */
export function issuesFor(issues: readonly Issue[], nodeId: string, field?: string): Issue[] {
  return issues.filter(
    (i) =>
      i.node === nodeId &&
      (!field ||
        i.field === field ||
        (field === 'modelConfig' && (i.field ?? '').startsWith('modelConfig.'))),
  );
}
