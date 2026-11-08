import type { JsonValue, Node } from '@/api/types';
import { ShapedValues } from './ShapedValues';

/**
 * The values a task step received in a run, laid out by shape. The step's `schema` titles the
 * fields and may mark one as media. Works for any task schema.
 */

type Obj = Record<string, JsonValue>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

/** For a card: the media and long text, or the short values when there is nothing else. */
export function TaskValues({ node, data }: { node: Node; data: Obj }) {
  const schema = isObj(node.config.schema) ? node.config.schema : null;
  return <ShapedValues schema={schema} data={data} compact />;
}

/** For a panel: every readable value, laid out by shape. */
export function TaskInputValues({ node, data }: { node: Node; data: Obj }) {
  const schema = isObj(node.config.schema) ? node.config.schema : null;
  return <ShapedValues schema={schema} data={data} />;
}
