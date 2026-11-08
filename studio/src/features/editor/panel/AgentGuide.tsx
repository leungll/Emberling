import { CircleAlert, Clock, Lock, RefreshCw } from 'lucide-react';
import type { ReactNode } from 'react';

import type { JsonObject } from '@/api/types';
import { retriesText, type ToolStep } from '@/lib/agent-tools';
import { useCopy } from '@/lib/copy';

/**
 * The Agent side panel read top-down: how its tools work together, then the rules the Runtime
 * enforces. Everything is derived from Registry tool metadata and the node's configuration, so
 * it reads the same way for any Agent.
 */

const joinWords = (xs: string[]) =>
  xs.length <= 1 ? (xs[0] ?? '') : `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`;

export function HowItWorks({ steps }: { steps: ToolStep[] }) {
  const copy = useCopy();
  const later = steps.filter((s) => s.later).map((s) => s.label);
  return (
    <div className="flex flex-col gap-3">
      <ol className="m-0 flex list-none flex-col gap-3 p-0">
        {steps.map((s, i) => (
          <li
            key={s.tool.name}
            style={{ gridTemplateColumns: '20px minmax(0, 1fr)' }}
            className="grid gap-x-2"
          >
            <span className="mt-px flex size-5 items-center justify-center rounded-full bg-surface-inset text-sm text-fg-secondary tabular-nums">
              {i + 1}
            </span>
            <div className="flex flex-col gap-0.5">
              <span className="text-sm font-medium text-fg" title={s.tool.name}>
                {s.label}
              </span>
              <span className="text-sm text-fg-secondary">{s.tool.description}</span>
            </div>
          </li>
        ))}
      </ol>
      {later.length ? (
        <p className="m-0 text-sm text-fg-muted">
          {copy.editor.guide.reportsBackLater(joinWords(later), later.length === 1)}
        </p>
      ) : null}
    </div>
  );
}

export function AgentRules({ steps, config }: { steps: ToolStep[]; config: JsonObject }) {
  const copy = useCopy();
  const cls = 'mt-0.5 size-icon-sm shrink-0 text-fg-muted';
  const rules: { icon: ReactNode; text: string }[] = [];
  for (const s of steps.filter((x) => x.after.length))
    rules.push({
      icon: <Lock aria-hidden="true" className={cls} />,
      text: copy.editor.guide.onlyAfter(s.label, joinWords(s.after)),
    });
  for (const s of steps.filter((x) => x.fixes !== null))
    rules.push({
      icon: <RefreshCw aria-hidden="true" className={cls} />,
      text: copy.editor.guide.retriesAfterFailure(s.label, retriesText(s.fixes ?? 0)),
    });
  const turns =
    typeof config.maxTurns === 'number' ? copy.editor.guide.turns(config.maxTurns) : null;
  const mins =
    typeof config.timeoutMs === 'number'
      ? copy.editor.guide.minutes(Math.round(config.timeoutMs / 60000))
      : null;
  if (turns || mins)
    rules.push({
      icon: <Clock aria-hidden="true" className={cls} />,
      text: copy.editor.guide.stopsAfter([turns, mins].filter(Boolean).join(' or ')),
    });
  rules.push({
    icon: <CircleAlert aria-hidden="true" className={cls} />,
    text: copy.editor.guide.failedStops,
  });
  return (
    <ul className="m-0 flex list-none flex-col gap-2 p-0">
      {rules.map((r) => (
        <li key={r.text} className="flex gap-2 text-sm text-fg-secondary">
          {r.icon}
          {r.text}
        </li>
      ))}
    </ul>
  );
}
