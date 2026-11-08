import { Clapperboard, FileText, ListChecks, Lock, ScanEye, Wrench } from 'lucide-react';
import type { ReactNode } from 'react';

import type { ToolMetadata } from '@/api/types';
import { Tooltip } from '@/components/ui/tooltip';
import { retriesText, type ToolStep } from '@/lib/agent-tools';
import { useCopy } from '@/lib/copy';

/**
 * An Agent's tools on its card as a small ordered list with their gates and retry allowance.
 * The icon follows what the registered tool does (reads material, reviews with a model,
 * produces a verdict, needs prior facts), never its name.
 */

function iconOf(t: ToolMetadata): ReactNode {
  const cls = 'size-icon-sm';
  if (t.materialPointer) return <FileText aria-hidden="true" className={cls} />;
  if (t.usesReviewModel) return <ScanEye aria-hidden="true" className={cls} />;
  if (t.produces?.verdictPointer) return <ListChecks aria-hidden="true" className={cls} />;
  if (t.requires.length) return <Clapperboard aria-hidden="true" className={cls} />;
  return <Wrench aria-hidden="true" className={cls} />;
}

export function AgentToolFlow({ steps }: { steps: ToolStep[] }) {
  const copy = useCopy();
  return (
    <ol className="m-0 flex list-none flex-col gap-1.5 p-0">
      {steps.map((s) => (
        <li key={s.tool.name} className="flex items-start gap-2">
          <Tooltip content={copy.editor.guide.toolTip(s.tool.name, s.tool.description)}>
            <span className="flex size-6 shrink-0 items-center justify-center rounded-sm bg-surface-hover text-fg-secondary">
              {iconOf(s.tool)}
            </span>
          </Tooltip>
          <span className="flex min-w-0 flex-col">
            <span className="text-sm text-fg">{s.label}</span>
            {s.after.length ? (
              <span className="flex items-center gap-1 text-xs text-fg-muted">
                <Lock aria-hidden="true" className="size-3 shrink-0 text-caution-fg" />
                {copy.editor.guide.onlyAfterShort(s.after.join(' and '))}
              </span>
            ) : null}
            {s.fixes !== null ? (
              <span className="text-xs text-fg-muted">
                {copy.editor.guide.retriesShort(retriesText(s.fixes))}
              </span>
            ) : null}
          </span>
        </li>
      ))}
    </ol>
  );
}
