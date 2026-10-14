import { Fragment, type ReactNode } from 'react';

import { cn } from '@/lib/utils';

/** A registered tool's name, in the code face. */
export function ToolName({ name, className }: { name: string; className?: string }) {
  return <code className={cn('font-mono text-xs text-inherit', className)}>{name}</code>;
}

function marked(text: string, query: string, key: string): ReactNode {
  if (!query) return text;
  const out: ReactNode[] = [];
  const lower = text.toLowerCase();
  let i = 0;
  let j = lower.indexOf(query, i);
  while (j >= 0) {
    if (j > i) out.push(text.slice(i, j));
    out.push(
      <mark key={`${key}-${String(j)}`} className="rounded-sm bg-surface-selected text-accent-fg">
        {text.slice(j, j + query.length)}
      </mark>,
    );
    i = j + query.length;
    j = lower.indexOf(query, i);
  }
  if (i < text.length) out.push(text.slice(i));
  return out;
}

/**
 * A sentence with the tool names in it set in the code face, and an optional search match
 * highlighted. The words stay one string in the copy table; this only marks them up.
 */
export function Words({
  text,
  tools = [],
  query = '',
}: {
  text: string;
  tools?: readonly string[];
  query?: string;
}) {
  const q = query.trim().toLowerCase();
  const names = [...new Set(tools.filter(Boolean))].sort((a, b) => b.length - a.length);
  if (!names.length) return <>{marked(text, q, 't')}</>;
  const pattern = new RegExp(
    `(${names.map((n) => n.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|')})`,
  );
  const parts = text.split(pattern);
  return (
    <>
      {parts.map((part, i) =>
        names.includes(part) ? (
          <ToolName key={String(i)} name={part} />
        ) : (
          <Fragment key={String(i)}>{marked(part, q, String(i))}</Fragment>
        ),
      )}
    </>
  );
}
