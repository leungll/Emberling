import { CircleAlert } from 'lucide-react';
import { type ReactNode } from 'react';

import { FailCard, SelectLink } from './Diagnosis';
import { ElementTime, RunOffset } from './RunFacts';
import { ToolName, Words } from './Words';
import type { DetailContext } from './detail-context';
import { notRetried } from './run-diagnosis';
import { attemptWord, between, type TraceElement } from './run-model';
import { Money } from '@/components/cost/Cost';
import { FactList } from '@/components/facts/FactList';
import { IdChip } from '@/components/id/IdChip';
import { copy as words, useCopy } from '@/lib/copy';
import { duration } from '@/lib/time';
import { ErrorDetails, Ids, Muted, Section, ElementList } from './OverviewSection';

/** The Overview of one attempt of a step or a tool call. */

function FailedLine({ text, children }: { text?: string; children?: ReactNode }) {
  return (
    <p className="m-0 flex items-center gap-1.5 text-md text-fg">
      <CircleAlert aria-hidden="true" className="size-icon-sm shrink-0 text-st-failed-fg" />
      <span>{children ?? text}</span>
    </p>
  );
}

function errorShort(code: string | undefined): string {
  const entry = code
    ? (words.errors as Record<string, { short: string } | undefined>)[code]
    : undefined;
  return entry?.short ?? words.runs.events.summary.failed;
}

export function AttemptOverview({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  const copy = useCopy();
  const d = copy.runs.details;
  const f = d.facts;
  const focus = ctx.failure?.focus === el.id;
  const siblings = el.parent?.children.filter((c) => c.kind === 'attempt') ?? [];
  const all =
    siblings.length > 1 ? (
      <Section title={d.allAttempts} count={siblings.length}>
        <ElementList
          items={siblings}
          ctx={ctx}
          label={(a) => (a.toolAttempt ? attemptWord(a.toolAttempt.attemptNo, null) : a.label)}
        />
      </Section>
    ) : null;

  if (el.nodeAttempt) {
    const a = el.nodeAttempt;
    const max = el.step?.node.executionPolicy?.maxAttempts ?? null;
    const focusEl = ctx.failure ? ctx.model.byId.get(ctx.failure.focus) : undefined;
    const focusError = focusEl?.nodeAttempt?.error ?? null;
    const same =
      !focus &&
      a.error &&
      focusError &&
      focusEl?.nodeAttempt &&
      focusError.message === a.error.message;
    const retry = ctx.model.retryEvent.get(
      `${a.nodeRunId ?? el.step?.nodeRun?.id ?? ''}:${String(a.attemptNo)}`,
    );
    const nextAt =
      retry && typeof retry.payload.nextAttemptAt === 'string' ? retry.payload.nextAttemptAt : null;
    const ids = [
      { label: f.attemptId, value: <IdChip id={a.id} label={f.attemptId.toLowerCase()} /> },
    ];
    return (
      <>
        {focus && ctx.failure ? (
          <FailCard failure={ctx.failure} workflowId={ctx.workflowId} />
        ) : null}
        {!focus && a.error ? (
          same && focusEl?.nodeAttempt ? (
            <FailedLine>
              {d.sameError}{' '}
              <SelectLink id={focusEl.id} onSelect={ctx.onSelect}>
                {d.attemptLower(focusEl.nodeAttempt.attemptNo)}
              </SelectLink>
              .
            </FailedLine>
          ) : (
            <FailedLine text={`${errorShort(a.error.code)}.`} />
          )
        ) : null}
        <Section>
          <FactList
            compact
            facts={[
              {
                label: f.attempt,
                value: (
                  <>
                    <span className="tabular-nums">{a.attemptNo}</span>
                    {max ? <Muted>{f.ofUpTo(max)}</Muted> : null}
                  </>
                ),
              },
              { label: f.started, value: <RunOffset model={ctx.model} iso={a.startedAt} /> },
              {
                label: f.finished,
                value: a.completedAt ? (
                  <RunOffset model={ctx.model} iso={a.completedAt} />
                ) : (
                  <Muted>{f.notYet}</Muted>
                ),
              },
              { label: f.duration, value: <ElementTime model={ctx.model} el={el} /> },
              a.cost ? { label: f.cost, value: <Money cost={a.cost} /> } : null,
              a.deadlineAt
                ? { label: f.timeLimit, value: duration(between(a.startedAt, a.deadlineAt)) }
                : null,
              nextAt
                ? {
                    label: f.nextAttempt,
                    value: (
                      <>
                        <RunOffset model={ctx.model} iso={nextAt} />
                        <Muted>{f.scheduled}</Muted>
                      </>
                    ),
                  }
                : null,
            ]}
          />
        </Section>
        {all}
        {focus || (a.error && !same) ? (
          <ErrorDetails error={a.error} rows={ids} storageKey="attempt-error" />
        ) : (
          <Ids rows={ids} storageKey="attempt-ids" />
        )}
      </>
    );
  }

  const a = el.toolAttempt;
  if (!a) return null;
  const isLast = siblings[siblings.length - 1] === el;
  const turnAttempts = el.turn?.toolAttempts ?? [];
  const showNotRetried = !focus && isLast && notRetried(a.toolName, turnAttempts, ctx.tools);
  const tids = [
    { label: f.toolAttemptId, value: <IdChip id={a.id} label={f.toolAttemptId.toLowerCase()} /> },
  ];
  return (
    <>
      {focus && ctx.failure ? <FailCard failure={ctx.failure} workflowId={ctx.workflowId} /> : null}
      {!focus && a.error ? <FailedLine text={`${errorShort(a.error.code)}.`} /> : null}
      <Section>
        <FactList
          compact
          facts={[
            { label: f.tool, value: <ToolName name={a.toolName} tools={ctx.tools} /> },
            { label: f.attempt, value: <span className="tabular-nums">{a.attemptNo}</span> },
            { label: f.started, value: <RunOffset model={ctx.model} iso={a.startedAt} /> },
            a.dispatchedAt
              ? { label: f.sent, value: <RunOffset model={ctx.model} iso={a.dispatchedAt} /> }
              : null,
            {
              label: f.finished,
              value: a.completedAt ? (
                <RunOffset model={ctx.model} iso={a.completedAt} />
              ) : (
                <Muted>{f.notYet}</Muted>
              ),
            },
            { label: f.duration, value: <ElementTime model={ctx.model} el={el} /> },
            a.cost ? { label: f.cost, value: <Money cost={a.cost} /> } : null,
            a.callbackBinding
              ? {
                  label: f.externalTask,
                  value: (
                    <IdChip
                      id={a.callbackBinding.externalTaskId}
                      label={f.externalTask.toLowerCase()}
                    />
                  ),
                }
              : null,
          ]}
        />
      </Section>
      {showNotRetried ? (
        <p className="m-0 text-md text-fg-secondary">
          <Words text={copy.runs.diagnosis.notRetried(a.toolName)} tools={[a.toolName]} />
        </p>
      ) : null}
      {all}
      {focus || a.error ? (
        <ErrorDetails error={a.error} rows={tids} storageKey="attempt-error" />
      ) : (
        <Ids rows={tids} storageKey="attempt-ids" />
      )}
    </>
  );
}

/* ---------- Agent turns, decisions, calls and waits --------------------------------- */
