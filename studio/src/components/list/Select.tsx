import { Check, ChevronDown, Search } from 'lucide-react';
import { useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';

import { controlClasses } from '@/components/ui/field';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';

export interface SelectOption<V extends string = string> {
  value: V;
  /** The display name; never a raw ID. */
  label: string;
  /** One muted line under the label, such as a model description. */
  description?: ReactNode;
  /** Extra text the search matches (an ID, an alias). */
  search?: string;
  mono?: boolean;
}

interface SelectProps<V extends string> {
  /** Names the control: the trigger reads "<label>: <selected>". */
  label: string;
  /** Hides the label from the trigger name when a visible label already names it. */
  labelledBy?: string;
  value: V;
  options: readonly SelectOption<V>[];
  onChange: (value: V) => void;
  /** A search box placeholder; with it the list is a combobox. */
  search?: string;
  noMatch?: (query: string) => string;
  size?: 'sm' | 'md';
  prefix?: ReactNode;
  /** Shown in the trigger, muted, while no option is selected. */
  placeholder?: string;
  side?: 'top' | 'bottom';
  id?: string;
  className?: string;
}

function focusOption(
  list: HTMLElement | null,
  which: 'first' | 'last' | 'selected' | number,
): void {
  if (!list) return;
  const options = Array.from(list.querySelectorAll<HTMLElement>('[role="option"]'));
  if (options.length === 0) return;
  let target: HTMLElement | undefined;
  if (which === 'first') target = options[0];
  else if (which === 'last') target = options.at(-1);
  else if (which === 'selected')
    target = options.find((o) => o.getAttribute('aria-selected') === 'true') ?? options[0];
  else target = options[Math.max(0, Math.min(options.length - 1, which))];
  target?.focus();
}

/**
 * A selector built on one listbox: the trigger shows the selected name, the list opens in a
 * popover with an optional search box. ↑/↓, Home/End and typing a letter move; Enter picks;
 * Escape closes and returns focus to the trigger. No native select.
 */
export function Select<V extends string>({
  label,
  labelledBy,
  value,
  options,
  onChange,
  search,
  noMatch,
  size = 'md',
  prefix,
  placeholder,
  side = 'bottom',
  id,
  className,
}: SelectProps<V>) {
  const copy = useCopy();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const listRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const listId = useId();
  const current = options.find((o) => o.value === value);

  const q = query.trim().toLowerCase();
  const shown = q
    ? options.filter((o) => `${o.label} ${o.search ?? ''}`.toLowerCase().includes(q))
    : options;

  // The list is one Tab stop: the selected option, or the first one shown.
  const tabStop = shown.some((o) => o.value === value) ? value : shown[0]?.value;

  const pick = (v: V) => {
    setOpen(false);
    setQuery('');
    onChange(v);
  };

  const onListKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const options = Array.from(
      listRef.current?.querySelectorAll<HTMLElement>('[role="option"]') ?? [],
    );
    const i = options.indexOf(document.activeElement as HTMLElement);
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      focusOption(listRef.current, i + 1);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      if (i <= 0 && inputRef.current) inputRef.current.focus();
      else focusOption(listRef.current, i - 1);
    } else if (e.key === 'Home') {
      e.preventDefault();
      focusOption(listRef.current, 'first');
    } else if (e.key === 'End') {
      e.preventDefault();
      focusOption(listRef.current, 'last');
    } else if (e.key.length === 1 && /\S/.test(e.key) && !e.metaKey && !e.ctrlKey && !e.altKey) {
      const ch = e.key.toLowerCase();
      const start = i + 1;
      const order = [...options.slice(start), ...options.slice(0, start)];
      const hit = order.find((o) => (o.textContent ?? '').trim().toLowerCase().startsWith(ch));
      if (hit) {
        e.preventDefault();
        hit.focus();
      }
    }
  };

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) setQuery('');
      }}
    >
      <PopoverTrigger asChild>
        <button
          type="button"
          id={id}
          aria-haspopup="listbox"
          aria-label={labelledBy ? undefined : `${label}: ${current?.label ?? placeholder ?? ''}`}
          aria-labelledby={labelledBy ? `${labelledBy} ${id ?? ''}`.trim() : undefined}
          onKeyDown={(e) => {
            if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && !open) {
              e.preventDefault();
              setOpen(true);
            }
          }}
          className={cn(
            controlClasses,
            'inline-flex w-auto max-w-full min-w-0 items-center gap-2 text-left aria-expanded:border-focus',
            size === 'sm'
              ? 'min-h-control-sm py-1 pr-1 pl-2 text-sm tabular-nums'
              : 'min-h-control-md py-1 pr-2 pl-3',
            className,
          )}
        >
          {prefix ? <span className="text-fg-muted [&_svg]:size-icon-sm">{prefix}</span> : null}
          <span
            className={cn(
              'min-w-0 flex-1 wrap-anywhere',
              !current && placeholder && 'text-fg-placeholder',
            )}
          >
            {current?.label ?? placeholder ?? ''}
          </span>
          <ChevronDown aria-hidden="true" className="size-icon-sm shrink-0 text-fg-muted" />
        </button>
      </PopoverTrigger>
      <PopoverContent
        label={label}
        side={side}
        style={{
          minWidth: 'max(var(--radix-popover-trigger-width), 180px)',
          maxWidth: 420,
        }}
        className="w-max"
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          if (inputRef.current) inputRef.current.focus();
          else focusOption(listRef.current, 'selected');
        }}
      >
        {search ? (
          <div className="relative flex items-center p-1">
            <Search
              aria-hidden="true"
              className="pointer-events-none absolute left-4 size-icon-sm text-fg-muted"
            />
            <input
              ref={inputRef}
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  const first = shown[0];
                  if (first) {
                    e.preventDefault();
                    pick(first.value);
                  }
                } else if (e.key === 'ArrowDown') {
                  e.preventDefault();
                  focusOption(listRef.current, 'first');
                }
              }}
              placeholder={search}
              aria-label={search}
              aria-controls={listId}
              autoComplete="off"
              className={cn(controlClasses, 'h-control-md pl-8')}
            />
          </div>
        ) : null}
        <div
          ref={listRef}
          id={listId}
          role="listbox"
          aria-label={label}
          onKeyDown={onListKey}
          style={{ maxHeight: 'min(60vh, 400px)', scrollbarGutter: 'stable' }}
          className="overflow-y-auto"
        >
          {shown.length === 0 ? (
            <p style={{ maxWidth: 320 }} className="m-0 px-2 py-3 text-sm text-fg-muted">
              {noMatch ? noMatch(query.trim()) : copy.lists.noMatches}
            </p>
          ) : (
            shown.map((o) => {
              const selected = o.value === value;
              return (
                <button
                  key={o.value}
                  type="button"
                  role="option"
                  aria-selected={selected}
                  tabIndex={o.value === tabStop ? 0 : -1}
                  onClick={() => {
                    pick(o.value);
                  }}
                  style={{ gridTemplateColumns: 'var(--icon-md) minmax(0, 1fr)' }}
                  className="grid min-h-control-sm w-full items-start gap-x-2 rounded-md px-2 py-1 text-left text-sm text-fg-secondary hover:bg-surface-hover hover:text-fg focus:bg-surface-hover focus:text-fg focus-visible:-outline-offset-2 aria-selected:text-fg"
                >
                  <span className="inline-flex pt-0.5 text-accent-fg">
                    {selected ? <Check aria-hidden="true" className="size-icon-sm" /> : null}
                  </span>
                  <span className="flex min-w-0 flex-col">
                    <span className={cn('wrap-anywhere', o.mono && 'font-mono')}>{o.label}</span>
                    {o.description ? (
                      <span className="text-xs text-fg-muted">{o.description}</span>
                    ) : null}
                  </span>
                </button>
              );
            })
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
