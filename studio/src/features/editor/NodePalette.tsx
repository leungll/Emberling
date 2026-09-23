import { useMemo, useState } from 'react';

import { paletteSummaryLine, runtimeSemanticsCounts } from './nodeSummary';
import { nodeAccentColor } from './portStyles';
import type { NodeMetadata } from '@/api/types';
import { Input } from '@/components/ui/input';
import { ScrollArea } from '@/components/ui/scroll-area';

interface NodePaletteProps {
  nodeTypes: NodeMetadata[];
  onAdd: (metadata: NodeMetadata) => void;
  error?: string | null;
}

/** Fixed reading order for the known categories; anything else sorts after, alphabetically. */
const CATEGORY_ORDER = ['Input', 'Prompt & Model', 'Agent', 'Output'];

const TWO_COLUMN_CATEGORIES = new Set(['Input', 'Output']);

function categoryRank(category: string): number {
  const index = CATEGORY_ORDER.indexOf(category);
  return index === -1 ? CATEGORY_ORDER.length : index;
}

/**
 * The node catalogue comes from `GET /node-types`. Studio keeps no second list: a type
 * missing from the Registry simply cannot be added.
 */
export function NodePalette({ nodeTypes, onAdd, error }: NodePaletteProps) {
  const [query, setQuery] = useState('');

  const grouped = useMemo(() => {
    const needle = query.trim().toLowerCase();
    const matches = needle
      ? nodeTypes.filter(
          (n) =>
            n.displayName.toLowerCase().includes(needle) || n.type.toLowerCase().includes(needle),
        )
      : nodeTypes;

    const byCategory = new Map<string, NodeMetadata[]>();
    for (const metadata of matches) {
      const existing = byCategory.get(metadata.category);
      if (existing) existing.push(metadata);
      else byCategory.set(metadata.category, [metadata]);
    }
    return [...byCategory.entries()].sort(([a], [b]) => {
      const rank = categoryRank(a) - categoryRank(b);
      return rank !== 0 ? rank : a.localeCompare(b);
    });
  }, [nodeTypes, query]);

  const semantics = useMemo(() => runtimeSemanticsCounts(nodeTypes), [nodeTypes]);

  return (
    <aside className="flex w-[286px] shrink-0 flex-col border-r border-[var(--border)] bg-[var(--muted)]">
      <div className="border-b border-[var(--border)] p-3">
        <div className="mb-2 flex items-center justify-between">
          <h2 className="text-xs font-bold tracking-[0.08em] text-[var(--muted-foreground)]">
            NODE REGISTRY
          </h2>
          <span className="text-xs text-[var(--muted-foreground)]">
            {nodeTypes.length} registered
          </span>
        </div>
        <Input
          type="search"
          placeholder="Search nodes…"
          aria-label="Search nodes"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          className="bg-[var(--secondary)]"
        />
      </div>

      <ScrollArea className="flex-1 p-3">
        {error ? <p className="text-xs text-[var(--status-failed-fg)]">{error}</p> : null}
        {!error && nodeTypes.length === 0 ? (
          <p className="text-xs text-[var(--muted-foreground)]">No registered node types.</p>
        ) : null}

        {grouped.map(([category, items]) => (
          <section key={category} className="mb-4">
            <h3 className="mb-2 text-xs font-bold tracking-[0.08em] text-[var(--muted-foreground)]">
              {category.toUpperCase()}
            </h3>
            {/* Input and Output types are short, so they pair up two per row (04 §2.5). */}
            <ul
              className={
                TWO_COLUMN_CATEGORIES.has(category) ? 'grid grid-cols-2 gap-2' : 'space-y-2'
              }
            >
              {items.map((metadata) => (
                <li key={metadata.type}>
                  <button
                    type="button"
                    onClick={() => onAdd(metadata)}
                    className="h-full w-full rounded-[9px] border border-[var(--border)] bg-[var(--secondary)] px-3 py-2.5 text-left hover:bg-[var(--accent)]"
                  >
                    <span className="flex items-center gap-2 text-sm font-semibold">
                      <span
                        aria-hidden="true"
                        className="h-1.5 w-1.5 shrink-0 rounded-full"
                        style={{ backgroundColor: nodeAccentColor(metadata) }}
                      />
                      {metadata.displayName}
                    </span>
                    <span className="mt-0.5 block pl-3.5 text-xs text-[var(--muted-foreground)]">
                      {paletteSummaryLine(metadata)}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          </section>
        ))}
      </ScrollArea>

      <div className="border-t border-[var(--border)] p-3">
        <p className="mb-2 text-xs font-bold tracking-[0.08em] text-[var(--muted-foreground)]">
          RUNTIME SEMANTICS
        </p>
        <div className="flex flex-wrap gap-2 text-xs">
          <span className="rounded-full border border-[var(--border)] bg-[var(--secondary)] px-2.5 py-1">
            sync × {semantics.sync}
          </span>
          <span className="rounded-full border border-[var(--border)] bg-[var(--secondary)] px-2.5 py-1">
            async × {semantics.async}
          </span>
          <span className="rounded-full border border-[var(--border)] bg-[var(--secondary)] px-2.5 py-1">
            agent × {semantics.agent}
          </span>
        </div>
      </div>
    </aside>
  );
}
