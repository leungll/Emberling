import { useMemo, useState } from 'react';

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

  return (
    <aside className="flex w-60 shrink-0 flex-col border-r border-[var(--border)]">
      <div className="border-b border-[var(--border)] p-2">
        <h2 className="mb-2 text-xs font-semibold uppercase tracking-wide text-[var(--muted-foreground)]">
          Node Palette
        </h2>
        <Input
          type="search"
          placeholder="Search nodes"
          aria-label="Search nodes"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
      </div>

      <ScrollArea className="flex-1 p-2">
        {error ? <p className="text-xs text-red-500">{error}</p> : null}
        {!error && nodeTypes.length === 0 ? (
          <p className="text-xs text-[var(--muted-foreground)]">No registered node types.</p>
        ) : null}

        {grouped.map(([category, items]) => (
          <section key={category} className="mb-4">
            <h3 className="mb-1 text-[10px] font-semibold uppercase tracking-wide text-[var(--muted-foreground)]">
              {category}
            </h3>
            <ul className="space-y-1">
              {items.map((metadata) => (
                <li key={metadata.type}>
                  <button
                    type="button"
                    onClick={() => onAdd(metadata)}
                    className="w-full rounded border border-[var(--border)] px-2 py-1.5 text-left text-xs hover:bg-[var(--accent)]"
                  >
                    <span className="block font-medium">{metadata.displayName}</span>
                    <span className="block text-[10px] text-[var(--muted-foreground)]">
                      {metadata.executionKind}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          </section>
        ))}
      </ScrollArea>
    </aside>
  );
}
