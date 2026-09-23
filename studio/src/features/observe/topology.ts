import type { Definition } from '@/api/types';

/**
 * Definition nodes in topological order, ties kept in Definition order. This orders the
 * immutable bound Definition for display only; it reads no NodeRun and decides nothing
 * about execution. A node on a cycle (which validation rejects) is appended in
 * Definition order rather than dropped.
 */
export function topologicalNodeOrder(definition: Definition): Definition['nodes'] {
  const indegree = new Map(definition.nodes.map((node) => [node.id, 0]));
  const next = new Map<string, string[]>();
  for (const edge of definition.edges) {
    if (!indegree.has(edge.source) || !indegree.has(edge.target)) continue;
    indegree.set(edge.target, (indegree.get(edge.target) ?? 0) + 1);
    next.set(edge.source, [...(next.get(edge.source) ?? []), edge.target]);
  }
  const ordered: Definition['nodes'] = [];
  const placed = new Set<string>();
  let progress = true;
  while (progress) {
    progress = false;
    for (const node of definition.nodes) {
      if (placed.has(node.id) || (indegree.get(node.id) ?? 0) > 0) continue;
      ordered.push(node);
      placed.add(node.id);
      progress = true;
      for (const target of next.get(node.id) ?? []) {
        indegree.set(target, (indegree.get(target) ?? 0) - 1);
      }
      break;
    }
  }
  return [...ordered, ...definition.nodes.filter((node) => !placed.has(node.id))];
}

/** Compact Observe topology node footprint (see `CompactStatusNode`). */
export const COMPACT_NODE_WIDTH = 120;
export const COMPACT_NODE_HEIGHT = 64;
/** Minimum columns: three compact nodes and their gutters fit the 430px topology card at 100%. */
const COMPACT_COLUMNS = 3;
const COMPACT_GAP_X = 16;
const COMPACT_GAP_Y = 24;

export type CompactSide = 'top' | 'right' | 'bottom' | 'left';

type Point = { x: number; y: number };

function columnX(column: number): number {
  return column * (COMPACT_NODE_WIDTH + COMPACT_GAP_X);
}

function rowY(row: number): number {
  return row * (COMPACT_NODE_HEIGHT + COMPACT_GAP_Y);
}

/**
 * Display-only positions for the READ-ONLY TOPOLOGY card (04 §3.3 mock). The Definition's
 * own authored positions are for the Edit canvas's full-size cards and are left untouched.
 *
 * What is guaranteed: no two nodes overlap. When the bounded search succeeds, which it
 * does for every MVP fixture, a straight edge also never passes through a node that is
 * not one of its two ends (else the card would draw a dependency the Definition does not
 * have); the fallback layout only guarantees no overlapping nodes. A plain chain (every
 * edge joins consecutive nodes in topological order) snakes `COMPACT_COLUMNS` to a row, left to right then right to left;
 * any other DAG is layered, one row per dependency depth, on integer columns.
 *
 * Size: every layout is at most `COMPACT_COLUMNS` wide when no dependency depth holds more
 * than that many nodes (true of every MVP fixture), which fits the card's width at 100%;
 * the card grows to `compactTopologyExtent`'s height (ObserveCanvas), so such a graph
 * always opens at 100% and its 12px text stays at the floor. A wider graph is fitted, and
 * may shrink below 100%.
 */
export function compactTopologyPositions(definition: Definition): Map<string, Point> {
  const order = topologicalNodeOrder(definition);
  const index = new Map(order.map((node, i) => [node.id, i]));
  const edges = definition.edges.filter((edge) => index.has(edge.source) && index.has(edge.target));
  const isChain = edges.every(
    (edge) => (index.get(edge.target) ?? 0) - (index.get(edge.source) ?? 0) === 1,
  );
  return isChain ? snakePositions(order) : layeredPositions(order, edges);
}

/** Bounding size of a compact layout, in flow pixels. */
export function compactTopologyExtent(positions: ReadonlyMap<string, Point>): {
  width: number;
  height: number;
} {
  if (positions.size === 0) return { width: 0, height: 0 };
  const points = [...positions.values()];
  const minX = Math.min(...points.map((p) => p.x));
  const minY = Math.min(...points.map((p) => p.y));
  return {
    width: Math.max(...points.map((p) => p.x)) - minX + COMPACT_NODE_WIDTH,
    height: Math.max(...points.map((p) => p.y)) - minY + COMPACT_NODE_HEIGHT,
  };
}

function snakePositions(order: Definition['nodes']): Map<string, Point> {
  const positions = new Map<string, Point>();
  order.forEach((node, i) => {
    const row = Math.floor(i / COMPACT_COLUMNS);
    const offset = i % COMPACT_COLUMNS;
    const column = row % 2 === 0 ? offset : COMPACT_COLUMNS - 1 - offset;
    positions.set(node.id, { x: columnX(column), y: rowY(row) });
  });
  return positions;
}

/** Extra columns the layered search may add beyond the widest row before giving up. */
const MAX_EXTRA_COLUMNS = 2;
/** Bound on placement attempts per column count; the MVP graphs need a handful. */
const MAX_SEARCH_STEPS = 20000;
/** Clearance an edge keeps from a foreign node's border, in flow pixels. */
const EDGE_CLEARANCE = 4;

function layeredPositions(
  order: Definition['nodes'],
  edges: Definition['edges'],
): Map<string, Point> {
  const parents = new Map<string, string[]>();
  for (const edge of edges) {
    parents.set(edge.target, [...(parents.get(edge.target) ?? []), edge.source]);
  }
  // Longest-path depth; `order` is topological, so every parent is seen first. A node on a
  // cycle (rejected by validation) simply reads its unplaced parents as depth 0.
  const depth = new Map<string, number>();
  for (const node of order) {
    const parentDepths = (parents.get(node.id) ?? []).map((id) => depth.get(id) ?? 0);
    depth.set(node.id, parentDepths.length > 0 ? Math.max(...parentDepths) + 1 : 0);
  }
  const levels: Definition['nodes'][] = [];
  for (const node of order) {
    const level = depth.get(node.id) ?? 0;
    (levels[level] ??= []).push(node);
  }
  const rows = levels.map((level) => level ?? []);
  const widest = Math.max(COMPACT_COLUMNS, ...rows.map((level) => level.length));

  for (let columns = widest; columns <= widest + MAX_EXTRA_COLUMNS; columns++) {
    const placed = searchColumns(rows, parents, columns, true);
    if (placed) return toPositions(rows, placed);
  }
  // No crossing-free placement within the bounds: fall back to the preferred columns.
  return toPositions(rows, searchColumns(rows, parents, widest, false) ?? new Map());
}

function toPositions(
  rows: Definition['nodes'][],
  column: ReadonlyMap<string, number>,
): Map<string, Point> {
  const positions = new Map<string, Point>();
  rows.forEach((level, row) => {
    for (const node of level) {
      positions.set(node.id, { x: columnX(column.get(node.id) ?? 0), y: rowY(row) });
    }
  });
  return positions;
}

/**
 * Depth-first search for one integer column per node, row by row. Each node tries free
 * columns nearest its preferred one first, ties to the left, so the result is deterministic. With
 * `avoidCrossings`, a column is rejected when an edge from a parent would pass through an
 * already placed node; rows are placed top-down and edges only run downward, so every
 * node an incoming edge could cross is already placed when that edge is checked, and no
 * later node can land on it. Returns null when no placement exists within the step bound.
 */
function searchColumns(
  rows: Definition['nodes'][],
  parents: ReadonlyMap<string, string[]>,
  columns: number,
  avoidCrossings: boolean,
): Map<string, number> | null {
  const items = rows.flatMap((level, row) =>
    level.map((node) => ({ id: node.id, row, size: level.length })),
  );
  const rowOf = new Map(items.map((item) => [item.id, item.row]));
  const column = new Map<string, number>();
  const used = rows.map(() => new Set<number>());
  let steps = 0;

  const crosses = (id: string, row: number, c: number): boolean => {
    const to = { x: columnX(c) + COMPACT_NODE_WIDTH / 2, y: rowY(row) };
    for (const parent of parents.get(id) ?? []) {
      const pc = column.get(parent);
      const pr = rowOf.get(parent);
      if (pc === undefined || pr === undefined) continue;
      const from = { x: columnX(pc) + COMPACT_NODE_WIDTH / 2, y: rowY(pr) + COMPACT_NODE_HEIGHT };
      for (const [other, oc] of column) {
        if (other === parent) continue;
        const or = rowOf.get(other) ?? 0;
        if (or <= pr || or >= row) continue;
        if (segmentHitsNode(from, to, columnX(oc), rowY(or))) return true;
      }
    }
    return false;
  };

  const place = (k: number): boolean => {
    if (k === items.length) return true;
    if (++steps > MAX_SEARCH_STEPS) return false;
    const { id, row, size } = items[k]!;
    const meanParentColumn = (nodeId: string): number | undefined => {
      const placed = (parents.get(nodeId) ?? []).flatMap((p) => {
        const c = column.get(p);
        return c === undefined ? [] : [c];
      });
      return placed.length > 0 ? placed.reduce((a, b) => a + b, 0) / placed.length : undefined;
    };
    // A lone node sits under its parents (centred when it has none); several nodes spread
    // across the width, ranked by where their parents sit so edges do not cross needlessly.
    let preferred: number;
    if (size === 1) {
      preferred = meanParentColumn(id) ?? (columns - 1) / 2;
    } else {
      const ranked = rows[row]!.map((node, i) => ({
        id: node.id,
        i,
        key: meanParentColumn(node.id) ?? i,
      })).sort((a, b) => a.key - b.key || a.i - b.i);
      const rank = ranked.findIndex((entry) => entry.id === id);
      preferred = (rank * (columns - 1)) / (size - 1);
    }
    const candidates = Array.from({ length: columns }, (_, c) => c)
      .filter((c) => !used[row]!.has(c))
      .sort((a, b) => Math.abs(a - preferred) - Math.abs(b - preferred) || a - b);
    for (const c of candidates) {
      if (avoidCrossings && crosses(id, row, c)) continue;
      column.set(id, c);
      used[row]!.add(c);
      if (place(k + 1)) return true;
      column.delete(id);
      used[row]!.delete(c);
      if (steps > MAX_SEARCH_STEPS) return false;
    }
    return false;
  };

  return place(0) ? column : null;
}

/** Whether the segment from `a` to `b` enters a compact node at (x, y), with clearance. */
function segmentHitsNode(a: Point, b: Point, x: number, y: number): boolean {
  // Liang-Barsky clip against the node rectangle grown by the clearance.
  const left = x - EDGE_CLEARANCE;
  const right = x + COMPACT_NODE_WIDTH + EDGE_CLEARANCE;
  const top = y - EDGE_CLEARANCE;
  const bottom = y + COMPACT_NODE_HEIGHT + EDGE_CLEARANCE;
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  let t0 = 0;
  let t1 = 1;
  const checks: [number, number][] = [
    [-dx, a.x - left],
    [dx, right - a.x],
    [-dy, a.y - top],
    [dy, bottom - a.y],
  ];
  for (const [p, q] of checks) {
    if (p === 0) {
      if (q < 0) return false;
      continue;
    }
    const t = q / p;
    if (p < 0) {
      if (t > t1) return false;
      if (t > t0) t0 = t;
    } else {
      if (t < t0) return false;
      if (t < t1) t1 = t;
    }
  }
  return t0 < t1;
}

/**
 * Which sides a straight edge leaves its source and enters its target by: sideways within
 * a row, vertically between rows.
 */
export function compactEdgeSides(
  source: { x: number; y: number },
  target: { x: number; y: number },
): { from: CompactSide; to: CompactSide } {
  const dx = target.x - source.x;
  const dy = target.y - source.y;
  if (Math.abs(dy) < COMPACT_NODE_HEIGHT / 2) {
    return dx >= 0 ? { from: 'right', to: 'left' } : { from: 'left', to: 'right' };
  }
  return dy > 0 ? { from: 'bottom', to: 'top' } : { from: 'top', to: 'bottom' };
}

/** Handle id an Observe edge names for one side of a compact node. */
export function compactHandleId(kind: 'source' | 'target', side: CompactSide): string {
  return `${kind}-${side}`;
}
