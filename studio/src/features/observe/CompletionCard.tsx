import { CircleAlert, CircleCheck, CircleHelp, TriangleAlert } from 'lucide-react';

import type { AgentCompletion, CompletionOutcome } from '@/api/types';
import { FactList } from '@/components/facts/FactList';
import { Timestamp } from '@/components/time/Time';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';

/**
 * The task result of an agent with a goal, as the Backend decided it: one card per outcome
 * with what it means, how many checks passed, and for an incomplete task what is still
 * missing. Studio renders the verdict; it never recomputes it from the facts it can see.
 */

const TONE: Record<CompletionOutcome, { icon: typeof CircleCheck; bg: string; fg: string }> = {
  READY_FOR_APPROVAL: { icon: CircleCheck, bg: 'bg-st-succeeded-bg', fg: 'text-st-succeeded-fg' },
  DEPLOYED: { icon: CircleCheck, bg: 'bg-st-succeeded-bg', fg: 'text-st-succeeded-fg' },
  DEPLOYED_CHECK_FAILED: { icon: TriangleAlert, bg: 'bg-st-waiting-bg', fg: 'text-st-waiting-fg' },
  RELEASE_UNKNOWN: { icon: CircleHelp, bg: 'bg-st-waiting-bg', fg: 'text-st-waiting-fg' },
  INCOMPLETE: { icon: CircleAlert, bg: 'bg-st-failed-bg', fg: 'text-st-failed-fg' },
  CLIP_DELIVERED: { icon: CircleCheck, bg: 'bg-st-succeeded-bg', fg: 'text-st-succeeded-fg' },
  CANNOT_SATISFY: { icon: CircleHelp, bg: 'bg-st-waiting-bg', fg: 'text-st-waiting-fg' },
};

export function CompletionCard({ completion }: { completion: AgentCompletion }) {
  const copy = useCopy();
  const c = copy.status.completion;
  const outcome = c.outcome[completion.outcome];
  const tone = TONE[completion.outcome];
  const Icon = tone.icon;
  const passed = completion.checks.filter((check) => check.passed).length;
  return (
    <div
      role="group"
      aria-label={c.title}
      data-outcome={completion.outcome}
      className={cn('flex gap-3 rounded-lg px-4 py-3 text-md text-fg', tone.bg)}
    >
      <Icon aria-hidden="true" className={cn('mt-0.5 size-icon-md shrink-0', tone.fg)} />
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <p className="m-0 font-semibold">{outcome.label}</p>
        <p className="m-0">{outcome.description}</p>
        <FactList
          compact
          facts={[
            { label: c.checks, value: c.checksPassed(passed, completion.checks.length) },
            { label: c.evaluated, value: <Timestamp iso={completion.evaluatedAt} /> },
          ]}
        />
        {completion.missing.length > 0 ? (
          <div className="flex flex-col gap-1">
            <p className="m-0 text-sm font-semibold">{c.stillMissing}</p>
            <ul className="m-0 flex list-disc flex-col gap-1 pl-5">
              {completion.missing.map((item) => (
                <li key={item.code}>
                  {copy.status.completionMissing[item.code] ?? item.description}
                </li>
              ))}
            </ul>
          </div>
        ) : null}
      </div>
    </div>
  );
}
