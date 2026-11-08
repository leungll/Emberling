import type { JsonValue, Node } from '@/api/types';

type Obj = Record<string, JsonValue>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

/**
 * The task object a run gave a task step, or null when the run has none. A task step's
 * `inputKey` names the run input field it reads.
 */
export function taskValues(node: Node, runInput: JsonValue | null | undefined): Obj | null {
  const key = node.config.inputKey;
  if (typeof key !== 'string' || !isObj(runInput)) return null;
  const v = runInput[key];
  return isObj(v) && Object.keys(v).length ? v : null;
}
