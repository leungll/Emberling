import { ChevronRight, Keyboard, PanelLeft, Search } from 'lucide-react';
import { useRef, useState, type DragEvent, type ReactNode } from 'react';

import type { NodeMetadata } from '@/api/types';
import { controlClasses } from '@/components/ui/field';
import { IconButton } from '@/components/ui/button';
import { Kbd } from '@/components/ui/kbd';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import { useKeyShortcut } from '@/lib/shortcuts';
import { readString, writeString } from '@/lib/storage';
import { cn } from '@/lib/utils';
import { useShortcutsStore } from '@/stores/shortcuts-store';
import { STEP_MIME } from './canvas/canvas-view';
import { useEditor } from './editor-context';
import { highlight, matchesStep } from './model/search';
import { GROUPS, groupTypes, purposeOf, type GroupId } from './model/steps';
import { GroupIcon, StepIcon } from './StepIcon';

const GROUPS_KEY = 'emb.editor.palette.groups';

function readOpenGroups(): GroupId[] {
  const v = readString('local', GROUPS_KEY);
  if (v === null) return GROUPS.map((g) => g.id);
  return v.split(',').filter((x): x is GroupId => GROUPS.some((g) => g.id === x));
}

function Marked({ text, q }: { text: string; q: string }) {
  return (
    <>
      {highlight(text, q).map((r, i) =>
        r.hit ? (
          <mark key={i} className="bg-transparent font-semibold text-accent-fg">
            {r.text}
          </mark>
        ) : (
          r.text
        ),
      )}
    </>
  );
}

/** A step in the sidebar: drag it onto the canvas, or click to add it. */
function StepItem({
  meta,
  group,
  q,
  indent,
}: {
  meta: NodeMetadata;
  group: GroupId;
  q: string;
  indent: boolean;
}) {
  const copy = useCopy();
  const { ops } = useEditor();
  return (
    <Tooltip
      side="right"
      content={
        <>
          <span className="block font-medium">{meta.displayName}</span>
          <span className="block text-fg-secondary">{purposeOf(meta.type)}</span>
          <span className="mt-1 block text-xs text-fg-muted">{copy.editor.palette.itemTip}</span>
        </>
      }
    >
      <button
        type="button"
        draggable
        onDragStart={(e: DragEvent<HTMLButtonElement>) => {
          e.dataTransfer.setData(STEP_MIME, meta.type);
          e.dataTransfer.effectAllowed = 'copy';
        }}
        onClick={() => ops.addStep(meta.type)}
        className={cn(
          'flex min-h-control-md w-full cursor-grab items-center gap-2 rounded-md pr-2 text-left text-md text-fg-secondary hover:bg-surface-hover hover:text-fg focus-visible:bg-surface-hover active:cursor-grabbing',
          indent ? 'pl-8' : 'pl-2',
        )}
      >
        <StepIcon type={meta.type} group={group} className="size-icon-sm shrink-0 text-fg-muted" />
        <span className="min-w-0 flex-1 wrap-anywhere">
          <Marked text={meta.displayName} q={q} />
        </span>
      </button>
    </Tooltip>
  );
}

function ShortcutsEntry({ compact }: { compact: boolean }) {
  const copy = useCopy();
  const setOpen = useShortcutsStore((s) => s.setOpen);
  const button = (
    <button
      type="button"
      aria-keyshortcuts="?"
      aria-label={compact ? copy.nav.shortcuts : undefined}
      onClick={() => setOpen(true)}
      className={cn(
        'flex h-control-md items-center gap-2 rounded-md text-sm text-fg-muted hover:bg-surface-hover hover:text-fg',
        compact ? 'w-control-md justify-center' : 'w-full px-2',
      )}
    >
      <Keyboard aria-hidden="true" className="size-icon-md shrink-0" />
      {compact ? null : (
        <>
          <span className="min-w-0 flex-1 text-left wrap-anywhere">{copy.nav.shortcuts}</span>
          <Kbd>?</Kbd>
        </>
      )}
    </button>
  );
  return (
    <div
      className={cn('shrink-0 border-t border-border-subtle p-2', compact && 'flex justify-center')}
    >
      {compact ? (
        <Tooltip side="right" content={copy.shortcuts.entryTip}>
          {button}
        </Tooltip>
      ) : (
        button
      )}
    </div>
  );
}

/** A rail button whose group of steps flies out beside it, on hover or on click. */
function RailGroup({ group, items }: { group: GroupId; items: NodeMetadata[] }) {
  const copy = useCopy();
  const t = copy.editor;
  const [open, setOpen] = useState(false);
  const timer = useRef<number | undefined>(undefined);
  const hold = () => window.clearTimeout(timer.current);
  const leave = () => {
    hold();
    timer.current = window.setTimeout(() => setOpen(false), 300);
  };
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label={t.palette.railGroup(t.groups[group] ?? group)}
          onMouseEnter={() => {
            hold();
            setOpen(true);
          }}
          onMouseLeave={leave}
          className="flex size-control-md items-center justify-center rounded-md text-fg-muted hover:bg-surface-hover hover:text-fg aria-expanded:bg-surface-hover aria-expanded:text-fg"
        >
          <GroupIcon group={group} className="size-icon-md" />
        </button>
      </PopoverTrigger>
      <PopoverContent
        label={t.palette.railGroup(t.groups[group] ?? group)}
        side="bottom"
        align="start"
        style={{ width: 240 }}
      >
        <div onMouseEnter={hold} onMouseLeave={leave}>
          <div className="px-2 py-1 text-xs font-medium text-fg-muted">{t.groups[group]}</div>
          {items.map((m) => (
            <StepItem key={m.type} meta={m} group={group} q="" indent={false} />
          ))}
        </div>
      </PopoverContent>
    </Popover>
  );
}

/**
 * The Steps sidebar: search (/) and the step groups; drag a step onto the canvas or click to
 * add it. Collapsed, it is a rail of group buttons whose steps fly out beside them.
 */
export function StepsSidebar({
  collapsed,
  onCollapsed,
  onQuickAdd,
}: {
  collapsed: boolean;
  onCollapsed: (collapsed: boolean) => void;
  /** "/" with the sidebar collapsed opens quick add instead. */
  onQuickAdd: () => void;
}) {
  const copy = useCopy();
  const t = copy.editor.palette;
  const { registry } = useEditor();
  const [q, setQ] = useState('');
  const [openGroups, setOpenGroups] = useState<GroupId[]>(readOpenGroups);
  const search = useRef<HTMLInputElement>(null);

  useKeyShortcut('/', () => {
    if (collapsed) onQuickAdd();
    else search.current?.focus();
  });

  const groups = GROUPS.map((g) => ({ g, items: groupTypes(g, registry.nodeTypes) })).filter(
    (x) => x.items.length,
  );
  const toggle: ReactNode = (
    <IconButton
      label={collapsed ? t.expand : t.collapse}
      tooltip={collapsed ? t.expandTip : t.collapseTip}
      onClick={() => onCollapsed(!collapsed)}
    >
      <PanelLeft aria-hidden="true" />
    </IconButton>
  );

  if (collapsed) {
    return (
      <aside
        aria-label={t.title}
        style={{ width: 48 }}
        className="flex shrink-0 flex-col items-center border-r border-border-subtle bg-surface pt-2"
      >
        <div className="mb-1">{toggle}</div>
        <div className="flex flex-1 flex-col gap-0.5 border-t border-border-subtle pt-1">
          {groups.map(({ g, items }) => (
            <RailGroup key={g.id} group={g.id} items={items} />
          ))}
        </div>
        <ShortcutsEntry compact />
      </aside>
    );
  }

  const query = q.trim();
  const shown = groups
    .map((x) => ({ ...x, items: x.items.filter((m) => matchesStep(m, query)) }))
    .filter((x) => x.items.length);
  const setGroups = (next: GroupId[]) => {
    setOpenGroups(next);
    writeString('local', GROUPS_KEY, next.join(','));
  };

  return (
    <aside
      aria-label={t.title}
      style={{ width: 248 }}
      className="flex min-h-0 shrink-0 flex-col border-r border-border-subtle bg-surface"
    >
      <div className="flex items-center gap-2 pt-3 pr-2 pb-1 pl-4">
        <span className="flex-1 text-sm font-semibold text-fg">{t.title}</span>
        {toggle}
      </div>
      <div className="px-3 pt-1 pb-2">
        <div className="relative flex items-center">
          <Search
            aria-hidden="true"
            className="pointer-events-none absolute left-3 size-icon-sm text-fg-muted"
          />
          <input
            ref={search}
            type="search"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') {
                setQ('');
                e.currentTarget.blur();
              }
            }}
            placeholder={t.search}
            aria-label={t.search}
            aria-keyshortcuts="/"
            autoComplete="off"
            spellCheck={false}
            className={cn(controlClasses, 'peer h-control-md bg-surface-inset pr-8 pl-8 text-sm')}
          />
          <Kbd className="pointer-events-none absolute right-2 peer-focus:hidden">/</Kbd>
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-3">
        {shown.length ? (
          shown.map(({ g, items }) => {
            const open = query ? true : openGroups.includes(g.id);
            return (
              <section key={g.id}>
                <button
                  type="button"
                  aria-expanded={open}
                  onClick={() =>
                    setGroups(open ? openGroups.filter((x) => x !== g.id) : [...openGroups, g.id])
                  }
                  className="flex h-control-md w-full items-center gap-2 rounded-md px-2 text-left text-sm font-medium text-fg-secondary hover:bg-surface-hover hover:text-fg"
                >
                  <GroupIcon group={g.id} className="size-icon-sm text-fg-muted" />
                  <span className="min-w-0 flex-1">{copy.editor.groups[g.id]}</span>
                  <ChevronRight
                    aria-hidden="true"
                    className={cn(
                      'size-icon-sm text-fg-muted transition-transform duration-1',
                      open && 'rotate-90',
                    )}
                  />
                </button>
                {open ? (
                  <div className="pb-1">
                    {items.map((m) => (
                      <StepItem key={m.type} meta={m} group={g.id} q={query} indent />
                    ))}
                  </div>
                ) : null}
              </section>
            );
          })
        ) : (
          <p className="m-2 p-2 text-sm text-fg-muted">{t.noMatch(query)}</p>
        )}
      </div>
      <ShortcutsEntry compact={false} />
    </aside>
  );
}
