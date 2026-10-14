import { TriangleAlert, Workflow } from 'lucide-react';

import type { Node } from '@/api/types';
import { Button } from '@/components/ui/button';
import { useCopy } from '@/lib/copy';
import type { Issue } from './model/checks';
import { groupOf, ordered, type Catalog } from './model/steps';
import { StepIcon } from './StepIcon';

/**
 * Issues grouped by step in canvas order, then the workflow's own issues. Each row says what
 * is wrong and where, with one action that goes there.
 */
export function IssueList({
  issues,
  nodes,
  catalog,
  onShow,
}: {
  issues: readonly Issue[];
  nodes: readonly Node[];
  catalog: Catalog;
  onShow: (issue: Issue) => void;
}) {
  const copy = useCopy();
  const t = copy.editor;
  const groups: { node: Node | null; rows: Issue[] }[] = [];
  for (const n of ordered(nodes)) {
    const rows = issues.filter((i) => i.node === n.id);
    if (rows.length) groups.push({ node: n, rows });
  }
  const own = issues.filter((i) => !i.node || !nodes.some((n) => n.id === i.node));
  if (own.length) groups.push({ node: null, rows: own });

  const where = (i: Issue): string => {
    if (!i.section) return '';
    const section =
      i.section === 'step' ? '' : ((t.panel.sections as Record<string, string>)[i.section] ?? '');
    if (!section) return i.label ?? '';
    return i.label && i.label !== section ? t.checks.where(section, i.label) : section;
  };

  return (
    <ul className="m-0 flex list-none flex-col gap-2 p-0">
      {groups.map((g) => (
        <li key={g.node?.id ?? '__workflow'}>
          <div className="flex min-h-control-xs items-center gap-2 px-2 text-sm font-medium text-fg">
            <span className="inline-flex shrink-0 text-fg-muted">
              {g.node ? (
                <StepIcon
                  type={g.node.type}
                  group={groupOf(catalog.get(g.node.type), g.node.type)}
                />
              ) : (
                <Workflow aria-hidden="true" className="size-icon-sm" />
              )}
            </span>
            <span className="min-w-0 flex-1 wrap-anywhere">
              {g.node ? g.node.name : t.checks.workflow}
            </span>
            <span
              aria-label={t.checks.count(g.rows.length)}
              className="inline-flex h-5 min-w-5 items-center justify-center rounded-sm bg-caution-bg px-1 text-xs text-fg tabular-nums"
            >
              {g.rows.length}
            </span>
          </div>
          <ul className="m-0 flex list-none flex-col p-0">
            {g.rows.map((i, k) => {
              const w = where(i);
              return (
                <li
                  key={`${i.field ?? ''}-${String(k)}`}
                  className="flex items-start gap-2 rounded-md px-2 py-1"
                >
                  <TriangleAlert
                    aria-hidden="true"
                    className="mt-0.5 size-icon-sm shrink-0 text-caution-fg"
                  />
                  <span className="flex min-w-0 flex-1 flex-col">
                    <span className="text-sm wrap-anywhere text-fg">{i.text}</span>
                    {w ? <span className="text-sm wrap-anywhere text-fg-muted">{w}</span> : null}
                  </span>
                  {i.node || i.text === t.checks.noOutput ? (
                    <Button
                      variant="ghost"
                      size="sm"
                      aria-label={i.node ? t.checks.showLabel(i.text) : undefined}
                      onClick={() => onShow(i)}
                      className="shrink-0"
                    >
                      {i.node ? t.checks.show : t.checks.addOutput}
                    </Button>
                  ) : null}
                </li>
              );
            })}
          </ul>
        </li>
      ))}
    </ul>
  );
}
