import { ChevronDown, Play, Search } from 'lucide-react';
import { useId, useRef, useState, type KeyboardEvent } from 'react';

import type { DefinitionListItem } from '@/api/types';
import { buttonVariants } from '@/components/ui/button-variants';
import { controlClasses } from '@/components/ui/field';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';

interface RunWorkflowButtonProps {
  /** The workflows to choose from; null while they load. */
  workflows: readonly DefinitionListItem[] | null;
  onPick: (workflowId: string) => void;
}

function focusAt(list: HTMLElement | null, index: number | 'last'): void {
  const options = Array.from(list?.querySelectorAll<HTMLElement>('[role="option"]') ?? []);
  if (options.length === 0) return;
  const i =
    index === 'last' ? options.length - 1 : Math.max(0, Math.min(options.length - 1, index));
  options[i]?.focus();
}

/**
 * "Run workflow" on the Runs page: a searchable list of workflows, then the Run dialog for
 * the one picked. The list is the same listbox as the page's selectors.
 */
export function RunWorkflowButton({ workflows, onPick }: RunWorkflowButtonProps) {
  const copy = useCopy();
  const t = copy.runs.page;
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const listId = useId();

  const q = query.trim().toLowerCase();
  const shown = (workflows ?? []).filter(
    (w) => !q || `${w.name} ${w.workflowId}`.toLowerCase().includes(q),
  );

  const pick = (workflowId: string) => {
    setOpen(false);
    setQuery('');
    onPick(workflowId);
  };

  const onListKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const options = Array.from(
      listRef.current?.querySelectorAll<HTMLElement>('[role="option"]') ?? [],
    );
    const i = options.indexOf(document.activeElement as HTMLElement);
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      focusAt(listRef.current, i + 1);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      if (i <= 0) inputRef.current?.focus();
      else focusAt(listRef.current, i - 1);
    } else if (e.key === 'Home') {
      e.preventDefault();
      focusAt(listRef.current, 0);
    } else if (e.key === 'End') {
      e.preventDefault();
      focusAt(listRef.current, 'last');
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
          aria-haspopup="listbox"
          className={buttonVariants({ variant: 'primary' })}
        >
          <Play aria-hidden="true" />
          {t.runWorkflow}
          <ChevronDown aria-hidden="true" />
        </button>
      </PopoverTrigger>
      <PopoverContent
        label={t.pickWorkflow}
        align="end"
        style={{ width: 320 }}
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          inputRef.current?.focus();
        }}
      >
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
                  pick(first.workflowId);
                }
              } else if (e.key === 'ArrowDown') {
                e.preventDefault();
                focusAt(listRef.current, 0);
              }
            }}
            placeholder={t.searchWorkflows}
            aria-label={t.searchWorkflows}
            aria-controls={listId}
            autoComplete="off"
            className={cn(controlClasses, 'h-control-md pl-8')}
          />
        </div>
        <div
          ref={listRef}
          id={listId}
          role="listbox"
          aria-label={t.pickWorkflow}
          onKeyDown={onListKey}
          style={{ maxHeight: 'min(60vh, 400px)', scrollbarGutter: 'stable' }}
          className="overflow-y-auto"
        >
          {shown.length === 0 ? (
            <p className="m-0 px-2 py-3 text-sm text-fg-muted">
              {workflows === null
                ? copy.loading
                : q
                  ? t.noWorkflowMatch(query.trim())
                  : t.noWorkflows}
            </p>
          ) : (
            shown.map((w, i) => (
              <button
                key={w.workflowId}
                type="button"
                role="option"
                aria-selected={false}
                tabIndex={i === 0 ? 0 : -1}
                onClick={() => {
                  pick(w.workflowId);
                }}
                className="flex min-h-control-sm w-full items-center rounded-md px-2 py-1 text-left text-sm wrap-anywhere text-fg-secondary hover:bg-surface-hover hover:text-fg focus:bg-surface-hover focus:text-fg focus-visible:-outline-offset-2"
              >
                {w.name}
              </button>
            ))
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
