import type { Edge, JsonObject, Node, NodeMetadata, PortDataType, PortMetadata } from '@/api/types';
import { editor as t } from '@/lib/copy/editor';

/**
 * Step presentation drawn from the registered Node Types: sidebar groups, port words and
 * the defaults a newly added step starts with. Studio checks types only for responsiveness;
 * the server checks the saved workflow again.
 */

export type GroupId = 'inputs' | 'ai' | 'transform' | 'outputs';

export interface StepGroup {
  id: GroupId;
  types: readonly string[];
  categories: readonly string[];
}

export const GROUPS: readonly StepGroup[] = [
  { id: 'inputs', types: ['text_input', 'image_input', 'media_brief'], categories: ['Input'] },
  { id: 'ai', types: ['agent', 'text_generation', 'image_generation'], categories: ['Agent'] },
  { id: 'transform', types: ['prompt_template', 'media_result'], categories: ['Prompt & Model'] },
  { id: 'outputs', types: ['text_output', 'media_output'], categories: ['Output'] },
];

const LISTED = new Set(GROUPS.flatMap((g) => g.types));

/** The registered types of a group: its listed types first, then unlisted types of its categories. */
export function groupTypes(group: StepGroup, types: readonly NodeMetadata[]): NodeMetadata[] {
  const byType = new Map(types.map((m) => [m.type, m]));
  const listed = group.types.map((x) => byType.get(x)).filter((m): m is NodeMetadata => !!m);
  const extra = types.filter((m) => !LISTED.has(m.type) && group.categories.includes(m.category));
  return [...listed, ...extra];
}

/** The group a type falls in, for its icon and the rail flyouts. */
export function groupOf(meta: NodeMetadata | undefined, type: string): GroupId {
  const listed = GROUPS.find((g) => g.types.includes(type));
  if (listed) return listed.id;
  const byCat = meta && GROUPS.find((g) => g.categories.includes(meta.category));
  return byCat ? byCat.id : 'transform';
}

export function purposeOf(type: string): string {
  return t.purpose[type] ?? '';
}

export function portLabel(port: Pick<PortMetadata, 'name'>): string {
  return t.ports[port.name] ?? t.cap(port.name);
}

export function typeWord(type: string): string {
  return t.types[type] ?? type;
}

/** An `any` input takes every type; otherwise the output and input types must match. */
export function fits(outType: PortDataType, inType: PortDataType): boolean {
  return inType === 'any' || outType === inType;
}

export type Catalog = ReadonlyMap<string, NodeMetadata>;

export function catalogOf(types: readonly NodeMetadata[]): Catalog {
  return new Map(types.map((m) => [m.type, m]));
}

export function portOf(
  catalog: Catalog,
  node: Node | undefined,
  dir: 'in' | 'out',
  name: string,
): PortMetadata | undefined {
  const meta = node && catalog.get(node.type);
  if (!meta) return undefined;
  return (dir === 'in' ? meta.inputs : meta.outputs).find((p) => p.name === name);
}

export function isInputStep(catalog: Catalog, node: Node): boolean {
  return catalog.get(node.type)?.category === 'Input';
}

export function isOutputStep(catalog: Catalog, node: Node): boolean {
  return catalog.get(node.type)?.category === 'Output';
}

/** Steps in reading order: left to right, then top to bottom. */
export function ordered(nodes: readonly Node[]): Node[] {
  return [...nodes].sort((a, b) => a.position.x - b.position.x || a.position.y - b.position.y);
}

/** The steps linked into one input (or into any input when `handle` is omitted). */
export function sourcesOf(
  nodes: readonly Node[],
  edges: readonly Edge[],
  id: string,
  handle?: string,
): Node[] {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  return edges
    .filter((e) => e.target === id && (!handle || e.targetHandle === handle))
    .map((e) => byId.get(e.source))
    .filter((n): n is Node => !!n);
}

export function targetsOf(
  nodes: readonly Node[],
  edges: readonly Edge[],
  id: string,
  handle?: string,
): Node[] {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  return edges
    .filter((e) => e.source === id && (!handle || e.sourceHandle === handle))
    .map((e) => byId.get(e.target))
    .filter((n): n is Node => !!n);
}

/** The label the Run form shows for an input step: its label when set, else the step name. */
export function fieldLabel(node: Node): string {
  const label = node.config.label;
  return typeof label === 'string' && label !== '' ? label : node.name;
}

/** A label that equals the step name follows renames of the step. */
export function labelFollowsName(node: Node): boolean {
  const label = node.config.label;
  return label === undefined || label === null || label === '' || label === node.name;
}

/** A new step's name: the type's display name, numbered when that type is already on the canvas. */
export function newStepName(meta: NodeMetadata, nodes: readonly Node[]): string {
  const taken = new Set(nodes.map((n) => n.name));
  if (!taken.has(meta.displayName)) return meta.displayName;
  for (let i = 2; ; i += 1) {
    const name = `${meta.displayName} ${i}`;
    if (!taken.has(name)) return name;
  }
}

/** The first `<base><n>` not used as an inputKey by another step. */
export function uniqueKey(base: string, nodes: readonly Node[], start = 1): string {
  const taken = new Set(nodes.map((n) => n.config.inputKey).filter((k) => typeof k === 'string'));
  for (let i = start; ; i += 1) {
    const key = `${base}${i}`;
    if (!taken.has(key)) return key;
  }
}

/** The config a newly added step starts with. Only keys its configSchema declares are set. */
export function defaultConfig(meta: NodeMetadata, nodes: readonly Node[]): JsonObject {
  const props = meta.configSchema.properties ?? {};
  const config: JsonObject = {};
  const set = (key: string, value: JsonObject[string]) => {
    if (key in props) config[key] = value;
  };
  switch (meta.type) {
    case 'agent':
      set('instructions', '');
      set('allowedTools', []);
      set('maxTurns', 8);
      set('timeoutMs', 120000);
      break;
    case 'text_input':
      set('inputKey', uniqueKey('input', nodes));
      set('required', true);
      break;
    case 'image_input':
      set('inputKey', uniqueKey('image', nodes));
      set('required', true);
      set('acceptedMediaTypes', ['image/png', 'image/jpeg']);
      break;
    case 'media_brief':
      set('inputKey', uniqueKey('photos', nodes));
      break;
    case 'image_generation':
      set('width', 1024);
      break;
    case 'prompt_template':
      set('template', '{{text}}');
      break;
    default:
      for (const [key, schema] of Object.entries(props)) {
        if (schema.default !== undefined) config[key] = schema.default;
      }
  }
  return config;
}
