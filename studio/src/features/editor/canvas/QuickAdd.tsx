import { Search } from 'lucide-react';
import { useEffect, useId, useRef, useState } from 'react';

import type { NodeMetadata } from '@/api/types';
import { controlClasses } from '@/components/ui/field';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';
import { useEditor, type QuickAddRequest } from '@/features/editor/editor-context';
import { useEditorStore } from '@/features/editor/editor-store';
import { linkPlan } from '@/features/editor/model/connect';
import { matchesStep, highlight } from '@/features/editor/model/search';
import { GROUPS, groupTypes, purposeOf, typeWord } from '@/features/editor/model/steps';
import { StepIcon } from '@/features/editor/StepIcon';

const WIDTH = 340;

/**
 * Quick add: a searchable list of steps at a point. From an output or a link it lists only
 * the steps that fit there and adds the new step connected; elsewhere it adds the step at the
 * point it opened from.
 */
export function QuickAdd({ request, onClose }: { request: QuickAddRequest; onClose: () => void }) {
  const copy = useCopy();
  const t = copy.editor;
  const { registry, ops, geometry } = useEditor();
  const doc = useEditorStore((s) => s.doc);
  const [q, setQ] = useState('');
  const [active, setActive] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);
  const [back] = useState<Element | null>(() => document.activeElement);
  const listId = useId();
  const link = request.link;

  let title = '';
  if (link?.kind === 'from') title = t.quickAdd.takes(typeWord(link.type));
  if (link?.kind === 'insert') {
    const e = doc.edges.find((x) => x.id === link.edgeId);
    const name = (id: string | undefined) => doc.nodes.find((n) => n.id === id)?.name ?? '';
    title = t.quickAdd.between(name(e?.source), name(e?.target));
  }

  const groups = GROUPS.filter((g) => !request.only || g.id === request.only)
    .map((g) => ({
      g,
      items: groupTypes(g, registry.nodeTypes).filter(
        (m) => (!link || !!linkPlan(m, link, doc, registry.catalog)) && matchesStep(m, q),
      ),
    }))
    .filter((x) => x.items.length);
  const flat: NodeMetadata[] = groups.flatMap((x) => x.items);
  const current = Math.min(active, Math.max(0, flat.length - 1));

  // An added step takes focus itself; otherwise focus goes back where it was.
  const chosen = useRef(false);
  const choose = (m: NodeMetadata) => {
    chosen.current = true;
    onClose();
    if (link) ops.addLinked(m.type, link);
    else ops.addStep(m.type, request.at);
  };

  const x = request.x ?? window.innerWidth / 2 - WIDTH / 2;
  const y = request.y ?? window.innerHeight / 2 - 160;
  // Opens at its anchor and gets shorter rather than covering it, down to a usable height.
  const below = window.innerHeight - y - 16;
  const listMax =
    below >= 280 ? Math.min(420, below - 56) : Math.min(420, window.innerHeight - 200);

  useEffect(() => {
    listRef.current?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest' });
  }, [current]);

  return (
    <Popover
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <PopoverTrigger asChild>
        <span
          aria-hidden="true"
          style={{ position: 'fixed', left: x, top: y, width: 0, height: 0 }}
        />
      </PopoverTrigger>
      <PopoverContent
        label={t.quickAdd.label}
        style={{ width: WIDTH }}
        onCloseAutoFocus={(e) => {
          e.preventDefault();
          if (chosen.current) return;
          if (back instanceof HTMLElement && back.isConnected) back.focus();
          else geometry.current.focusCanvas();
        }}
      >
        <div className="relative mb-1 flex items-center">
          <Search
            aria-hidden="true"
            className="pointer-events-none absolute left-3 size-icon-sm text-fg-muted"
          />
          <input
            autoFocus
            type="search"
            role="combobox"
            aria-expanded="true"
            aria-controls={listId}
            aria-activedescendant={flat[current] ? `${listId}-${flat[current].type}` : undefined}
            aria-label={t.quickAdd.search}
            placeholder={t.quickAdd.search}
            autoComplete="off"
            spellCheck={false}
            value={q}
            onChange={(e) => {
              setQ(e.target.value);
              setActive(0);
            }}
            onKeyDown={(e) => {
              if (e.key === 'ArrowDown') {
                e.preventDefault();
                setActive(Math.min(flat.length - 1, current + 1));
              } else if (e.key === 'ArrowUp') {
                e.preventDefault();
                setActive(Math.max(0, current - 1));
              } else if (e.key === 'Enter') {
                const m = flat[current];
                if (m) {
                  e.preventDefault();
                  choose(m);
                }
              }
            }}
            className={cn(controlClasses, 'h-control-md pl-8 text-sm')}
          />
        </div>
        <div
          ref={listRef}
          id={listId}
          role="listbox"
          aria-label={t.palette.title}
          style={{ maxHeight: Math.max(160, listMax) }}
          className="overflow-y-auto"
        >
          {title ? (
            <div className="px-2 pt-2 pb-1 text-sm font-medium text-fg-secondary">{title}</div>
          ) : null}
          {groups.length ? (
            groups.map(({ g, items }) => (
              <div key={g.id} role="group" aria-label={t.groups[g.id]} className="mt-1 first:mt-0">
                <div className="px-2 pt-2 pb-1 text-xs font-medium text-fg-muted">
                  {t.groups[g.id]}
                </div>
                {items.map((m) => {
                  const on = flat[current]?.type === m.type;
                  return (
                    <div
                      key={m.type}
                      id={`${listId}-${m.type}`}
                      role="option"
                      aria-selected={on}
                      onPointerMove={() => setActive(flat.indexOf(m))}
                      onClick={() => choose(m)}
                      className={cn(
                        'flex w-full cursor-default items-center gap-2 rounded-md px-2 py-1 text-left',
                        on && 'bg-surface-hover',
                      )}
                    >
                      <span className="inline-flex size-control-sm shrink-0 items-center justify-center rounded-md bg-surface-hover text-fg-secondary">
                        <StepIcon type={m.type} group={g.id} />
                      </span>
                      <span className="flex min-w-0 flex-col">
                        <span className="text-sm font-medium text-fg">
                          {highlight(m.displayName, q).map((r, i) =>
                            r.hit ? (
                              <mark key={i} className="rounded-sm bg-surface-selected text-fg">
                                {r.text}
                              </mark>
                            ) : (
                              r.text
                            ),
                          )}
                        </span>
                        <span className="text-sm text-fg-muted">{purposeOf(m.type)}</span>
                      </span>
                    </div>
                  );
                })}
              </div>
            ))
          ) : (
            <p className="px-2 py-3 text-sm text-fg-muted">
              {q ? t.quickAdd.noMatch(q) : t.quickAdd.noFit}
            </p>
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
