/**
 * The rows of a JSON tree, as a pure function of the value and the viewer's expand state,
 * so the viewer's keyboard model and rendering share one list.
 */
export const LONG_STRING = 200;

export interface JsonViewState {
  /** Explicit open (true) or closed (false) paths; others follow the default depth. */
  open: Record<string, boolean>;
  /** Long strings shown in full. */
  full: Record<string, boolean>;
  /** JSON-looking strings kept as text instead of opened as JSON. */
  asText: Record<string, boolean>;
}

export type JsonRowValue =
  | { type: 'object'; count: number; array: boolean; parsedFromText: boolean }
  | { type: 'string'; text: string; truncated: boolean; keptAsText: boolean }
  | { type: 'number' | 'boolean' | 'null'; text: string };

export interface JsonRow {
  path: string;
  key: string | null;
  depth: number;
  expandable: boolean;
  expanded: boolean;
  value: JsonRowValue;
}

/** Levels open by default: the root and its children. */
export const DEFAULT_OPEN_DEPTH = 2;

function isOpen(state: JsonViewState, path: string, depth: number): boolean {
  return state.open[path] ?? depth < DEFAULT_OPEN_DEPTH;
}

/** A string that holds a JSON object or array opens as JSON. */
export function parseJsonText(text: string): unknown {
  const t = text.trim();
  if (!(t.startsWith('{') || t.startsWith('['))) return undefined;
  try {
    const v: unknown = JSON.parse(t);
    return v !== null && typeof v === 'object' ? v : undefined;
  } catch {
    return undefined;
  }
}

export function childPath(parent: string, key: string | number, array: boolean): string {
  if (array) return `${parent}[${key}]`;
  const k = String(key);
  return /^[A-Za-z_$][\w$]*$/.test(k) ? `${parent}.${k}` : `${parent}[${JSON.stringify(k)}]`;
}

export function jsonRows(value: unknown, root: string, state: JsonViewState): JsonRow[] {
  const rows: JsonRow[] = [];
  const walk = (v: unknown, key: string | null, path: string, depth: number) => {
    let shown = v;
    let parsedFromText = false;
    if (typeof v === 'string' && !state.asText[path]) {
      const parsed = parseJsonText(v);
      if (parsed !== undefined) {
        shown = parsed;
        parsedFromText = true;
      }
    }
    if (shown !== null && typeof shown === 'object') {
      const array = Array.isArray(shown);
      const entries: [string, unknown][] = array
        ? (shown as unknown[]).map((x, i) => [String(i), x])
        : Object.entries(shown as Record<string, unknown>);
      const expandable = entries.length > 0;
      const expanded = expandable && isOpen(state, path, depth);
      rows.push({
        path,
        key,
        depth,
        expandable,
        expanded,
        value: { type: 'object', count: entries.length, array, parsedFromText },
      });
      if (expanded) {
        for (const [k, child] of entries) walk(child, k, childPath(path, k, array), depth + 1);
      }
      return;
    }
    let rowValue: JsonRowValue;
    if (typeof shown === 'string') {
      const truncated = shown.length > LONG_STRING && !state.full[path];
      rowValue = {
        type: 'string',
        text: truncated ? shown.slice(0, LONG_STRING) : shown,
        truncated,
        keptAsText: Boolean(state.asText[path]),
      };
    } else if (shown === null || shown === undefined) {
      rowValue = { type: 'null', text: 'null' };
    } else if (typeof shown === 'number') {
      rowValue = { type: 'number', text: String(shown) };
    } else if (typeof shown === 'boolean') {
      rowValue = { type: 'boolean', text: String(shown) };
    } else {
      rowValue = {
        type: 'string',
        text: JSON.stringify(shown),
        truncated: false,
        keptAsText: false,
      };
    }
    rows.push({ path, key, depth, expandable: false, expanded: false, value: rowValue });
  };
  walk(value, null, root, 0);
  return rows;
}
