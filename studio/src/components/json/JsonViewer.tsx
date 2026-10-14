import { ChevronDown, ChevronRight, Copy } from 'lucide-react';
import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';

import { jsonRows, type JsonRow, type JsonViewState } from './json-rows';
import { Button } from '@/components/ui/button';
import { Segmented } from '@/components/ui/segmented';
import { copyText } from '@/lib/clipboard';
import { useCopy } from '@/lib/copy';
import { showToast } from '@/lib/toast';
import { cn } from '@/lib/utils';

interface JsonViewerProps {
  value: unknown;
  /** The name of the root in copied paths, such as "output". */
  root?: string;
  initialMode?: 'tree' | 'raw';
  className?: string;
}

const VALUE_CLASS = {
  number: 'text-accent-fg',
  boolean: 'text-accent-fg',
  null: 'text-accent-fg',
} as const;

/**
 * Structured data in a tree, two levels open, with a Tree | Raw switch, Copy path and Copy
 * JSON. Strings over 200 characters stop at 200 behind "Show all"; a string that holds JSON
 * opens as JSON with "Show as text". ↑/↓ move, →/← open and close, Enter toggles.
 */
export function JsonViewer({
  value,
  root = 'value',
  initialMode = 'tree',
  className,
}: JsonViewerProps) {
  const copy = useCopy();
  const j = copy.json;
  const [mode, setMode] = useState<'tree' | 'raw'>(initialMode);
  const [state, setState] = useState<JsonViewState>({ open: {}, full: {}, asText: {} });
  const [focus, setFocus] = useState<string | null>(null);
  // A row to focus once the next render has placed it (after a toggle or an arrow key).
  const pendingFocus = useRef<string | null>(null);
  const treeRef = useRef<HTMLUListElement>(null);

  const rows = useMemo(() => jsonRows(value, root, state), [value, root, state]);
  const active = rows.some((r) => r.path === focus) ? focus : (rows[0]?.path ?? null);
  const raw = useMemo(() => JSON.stringify(value, null, 2), [value]);

  useEffect(() => {
    const path = pendingFocus.current;
    if (!path) return;
    pendingFocus.current = null;
    treeRef.current?.querySelector<HTMLElement>(`[data-path="${CSS.escape(path)}"]`)?.focus();
  }, [rows, active]);

  const toggle = (path: string, open: boolean) => {
    setState((s) => ({ ...s, open: { ...s.open, [path]: open } }));
    setFocus(path);
    pendingFocus.current = path;
  };
  const flag = (which: 'full' | 'asText', path: string) => {
    setState((s) => ({ ...s, [which]: { ...s[which], [path]: !s[which][path] } }));
    setFocus(path);
    pendingFocus.current = path;
  };
  const move = (row: JsonRow | undefined) => {
    if (!row) return;
    setFocus(row.path);
    pendingFocus.current = row.path;
  };

  const onKey = (e: KeyboardEvent<HTMLLIElement>, row: JsonRow, i: number) => {
    if (e.target !== e.currentTarget) return;
    const go = (fn: () => void) => {
      e.preventDefault();
      fn();
    };
    if (e.key === 'ArrowDown') go(() => move(rows[i + 1]));
    else if (e.key === 'ArrowUp') go(() => move(rows[i - 1]));
    else if (e.key === 'Home') go(() => move(rows[0]));
    else if (e.key === 'End') go(() => move(rows.at(-1)));
    else if (e.key === 'ArrowRight')
      go(() => {
        if (row.expandable && !row.expanded) toggle(row.path, true);
        else if (row.expanded) move(rows[i + 1]);
      });
    else if (e.key === 'ArrowLeft')
      go(() => {
        if (row.expanded) toggle(row.path, false);
        else
          move(
            rows
              .slice(0, i)
              .reverse()
              .find((r) => r.depth < row.depth),
          );
      });
    else if ((e.key === 'Enter' || e.key === ' ') && row.expandable)
      go(() => toggle(row.path, !row.expanded));
  };

  const copyPath = async () => {
    if (!active) return;
    if (await copyText(active)) showToast(j.copiedPath(active), { done: true });
    else showToast(copy.ids.copyFailed);
  };
  const copyJson = async () => {
    if (await copyText(raw)) showToast(j.copiedJson, { done: true });
    else showToast(copy.ids.copyFailed);
  };

  return (
    <div className={cn('flex min-w-0 flex-col gap-2', className)}>
      <div className="flex items-center gap-1">
        <Segmented
          label={j.showAs}
          value={mode}
          onValueChange={setMode}
          options={[
            { value: 'tree', label: j.tree },
            { value: 'raw', label: j.raw },
          ]}
        />
        <span className="flex-1" />
        {mode === 'tree' ? (
          <Button variant="ghost" size="sm" onClick={() => void copyPath()}>
            <Copy aria-hidden="true" />
            {j.copyPath}
          </Button>
        ) : null}
        <Button variant="ghost" size="sm" onClick={() => void copyJson()}>
          <Copy aria-hidden="true" />
          {j.copyJson}
        </Button>
      </div>
      {mode === 'raw' ? (
        <pre
          data-raw
          tabIndex={0}
          style={{ maxHeight: 480 }}
          className="m-0 overflow-auto rounded-lg border border-border-subtle bg-surface-inset p-3 font-mono text-xs whitespace-pre-wrap wrap-anywhere text-fg"
        >
          {raw}
        </pre>
      ) : (
        <ul
          ref={treeRef}
          role="tree"
          data-raw
          aria-label={j.treeLabel}
          style={{ maxHeight: 480 }}
          className="m-0 list-none overflow-auto rounded-lg border border-border-subtle bg-surface-inset py-1 font-mono text-xs text-fg"
        >
          {rows.map((row, i) => (
            <li
              key={row.path}
              role="treeitem"
              aria-level={row.depth + 1}
              aria-expanded={row.expandable ? row.expanded : undefined}
              aria-selected={row.path === active}
              data-path={row.path}
              tabIndex={row.path === active ? 0 : -1}
              onKeyDown={(e) => {
                onKey(e, row, i);
              }}
              onClick={(e) => {
                if ((e.target as HTMLElement).closest('button, a')) return;
                if (row.expandable) toggle(row.path, !row.expanded);
                else setFocus(row.path);
              }}
              className={cn(
                'flex min-h-control-xs flex-wrap items-center gap-1.5 py-1 pr-3 pl-2 hover:bg-surface-hover focus-visible:-outline-offset-2',
                row.expandable ? 'cursor-pointer' : 'cursor-default',
              )}
            >
              <span
                aria-hidden="true"
                style={{ marginLeft: row.depth * 16 }}
                className="inline-flex w-icon-sm shrink-0 items-center justify-center text-fg-muted"
              >
                {row.expandable ? (
                  row.expanded ? (
                    <ChevronDown className="size-icon-xs" />
                  ) : (
                    <ChevronRight className="size-icon-xs" />
                  )
                ) : null}
              </span>
              {row.key === null ? null : <span className="text-fg-secondary">{row.key}:</span>}
              <RowValue row={row} onFlag={flag} />
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function RowValue({
  row,
  onFlag,
}: {
  row: JsonRow;
  onFlag: (which: 'full' | 'asText', path: string) => void;
}) {
  const j = useCopy().json;
  const v = row.value;
  const act = 'h-control-xs font-sans text-xs';
  if (v.type === 'object') {
    return (
      <>
        <span className="font-sans text-fg-muted">
          {v.array ? j.items(v.count) : j.keys(v.count)}
        </span>
        {v.parsedFromText ? (
          <Button
            variant="link"
            size="sm"
            className={act}
            onClick={() => onFlag('asText', row.path)}
          >
            {j.showAsText}
          </Button>
        ) : null}
      </>
    );
  }
  if (v.type === 'string') {
    return (
      <>
        <span className="min-w-0 flex-1 whitespace-pre-wrap wrap-anywhere text-fg">{v.text}</span>
        {v.truncated ? (
          <Button variant="link" size="sm" className={act} onClick={() => onFlag('full', row.path)}>
            {j.showAll}
          </Button>
        ) : null}
        {v.keptAsText ? (
          <Button
            variant="link"
            size="sm"
            className={act}
            onClick={() => onFlag('asText', row.path)}
          >
            {j.showAsJson}
          </Button>
        ) : null}
      </>
    );
  }
  return <span className={VALUE_CLASS[v.type]}>{v.text}</span>;
}
