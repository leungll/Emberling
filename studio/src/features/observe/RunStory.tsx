import { ChevronRight, CircleCheck, CircleX, Dot } from 'lucide-react';

import type { AgentTrace, AgentTurnTrace, ToolMetadata } from '@/api/types';
import { toolTitle } from '@/lib/agent-tools';
import { useCopy, type Copy } from '@/lib/copy';
import { humanize } from '@/lib/value-reading';

/**
 * How an Agent worked, for the run page's details pane: one row per turn, read from the Agent
 * Trace alone. A tool shows by its registered title; a row's verdict comes from the execution
 * facts its Tool Attempts recorded. Works for any Agent and any tools.
 */

type Outcome = 'pass' | 'fail' | 'done';

interface Row {
  turn: AgentTurnTrace;
  sentence: string;
  detail: string | null;
  outcome: Outcome;
}

/** A tool call worth timing: one that took longer than this shows how long. */
const SLOW_MS = 10_000;

function clock(ms: number): string {
  const s = Math.round(ms / 1000);
  return `${String(Math.floor(s / 60))}:${String(s % 60).padStart(2, '0')}`;
}

/**
 * A failure in plain words. The Runtime's error code picks the wording; the raw code and
 * message stay in the Data tab.
 */
function plainError(copy: Copy, err: { code: string } | null | undefined): string {
  const known = err ? (copy.errors as Partial<Record<string, { short: string }>>)[err.code] : null;
  return known?.short ?? copy.runs.story.failedPlain;
}

function rowOf(
  copy: Copy,
  trace: AgentTrace,
  turn: AgentTurnTrace,
  tools: readonly ToolMetadata[],
): Row | null {
  const s = copy.runs.story;
  const d = turn.decision;
  const a = turn.action;
  if (!d) {
    return turn.status === 'FAILED'
      ? { turn, sentence: s.turnFailed, detail: plainError(copy, turn.error), outcome: 'fail' }
      : null;
  }
  if (d.kind === 'FINAL') {
    const failed = a?.status === 'FAILED';
    return {
      turn,
      sentence: s.finalReport,
      detail: failed ? plainError(copy, a.error) : null,
      outcome: failed ? 'fail' : 'pass',
    };
  }
  const name = d.tool ?? '';
  const meta = tools.find((t) => t.name === name);
  const title = meta ? toolTitle(meta) : humanize(name);
  if (a?.authorization === 'DENY')
    return { turn, sentence: s.refused(title), detail: a.authorizationReason, outcome: 'fail' };
  if (a?.status === 'FAILED')
    return { turn, sentence: s.failed(title), detail: plainError(copy, a.error), outcome: 'fail' };
  const attempts = new Set(turn.toolAttempts.map((t) => t.id));
  const verdicts = trace.facts.items.filter(
    (f) => attempts.has(f.toolAttemptId) && f.verdict !== null,
  );
  const outcome: Outcome = verdicts.some((f) => f.verdict === false)
    ? 'fail'
    : verdicts.length
      ? 'pass'
      : 'done';
  const ms =
    a?.startedAt && a.completedAt ? Date.parse(a.completedAt) - Date.parse(a.startedAt) : null;
  return {
    turn,
    sentence:
      outcome === 'pass' ? s.passed(title) : outcome === 'fail' ? s.didNotPass(title) : title,
    detail: ms !== null && ms >= SLOW_MS ? s.took(clock(ms)) : null,
    outcome,
  };
}

function OutcomeIcon({ outcome }: { outcome: Outcome }) {
  const copy = useCopy();
  if (outcome === 'pass')
    return (
      <CircleCheck
        aria-label={copy.runs.story.passedLabel}
        className="size-icon-sm shrink-0 text-st-succeeded-fg"
      />
    );
  if (outcome === 'fail')
    return (
      <CircleX
        aria-label={copy.runs.story.didNotPassLabel}
        className="size-icon-sm shrink-0 text-st-failed-fg"
      />
    );
  return <Dot aria-hidden="true" className="size-icon-sm shrink-0 text-fg-muted" />;
}

export function WhatHappened({
  trace,
  tools,
  title,
  onSelect,
}: {
  trace: AgentTrace;
  tools: readonly ToolMetadata[];
  title?: string;
  /** Opens a turn, by its id. */
  onSelect: (turnId: string) => void;
}) {
  const copy = useCopy();
  const t0 = Date.parse(trace.turns[0]?.startedAt ?? '');
  const rows = trace.turns.flatMap((turn) => {
    const r = rowOf(copy, trace, turn, tools);
    return r ? [r] : [];
  });
  if (!rows.length) return null;
  return (
    <section className="flex flex-col gap-1">
      <h3 className="m-0 text-sm font-semibold text-fg">{title ?? copy.runs.story.whatHappened}</h3>
      <ol className="m-0 flex list-none flex-col p-0">
        {rows.map((r) => (
          <li key={r.turn.id}>
            <button
              type="button"
              onClick={() => {
                onSelect(r.turn.id);
              }}
              title={copy.runs.story.openTurn(r.turn.turnNo)}
              style={{ gridTemplateColumns: '44px 16px minmax(0, 1fr) 14px' }}
              className="group/row grid w-full cursor-pointer items-start gap-2 rounded-sm px-1 py-1 text-left hover:bg-surface-hover"
            >
              <span className="pt-px text-sm text-fg-muted tabular-nums">
                {r.turn.startedAt && !Number.isNaN(t0)
                  ? `+${clock(Date.parse(r.turn.startedAt) - t0)}`
                  : ''}
              </span>
              <span className="pt-0.5">
                <OutcomeIcon outcome={r.outcome} />
              </span>
              <span className="flex min-w-0 flex-col">
                <span className="text-sm text-fg">{r.sentence}</span>
                {r.detail ? <span className="text-sm text-fg-muted">{r.detail}</span> : null}
              </span>
              <ChevronRight
                aria-hidden="true"
                className="mt-0.5 size-icon-sm text-fg-muted opacity-0 group-hover/row:opacity-100"
              />
            </button>
          </li>
        ))}
      </ol>
    </section>
  );
}
