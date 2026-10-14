import { ChevronRight, CircleAlert } from 'lucide-react';
import { useMemo, type ReactNode } from 'react';
import { Link, useLocation } from 'react-router';

import { FailCard, SelectLink, WaitCard } from './Diagnosis';
import { ElementTime, RunOffset, Tokens } from './RunFacts';
import { StepImages, ValueView } from './ValueView';
import { ToolName, Words } from './Words';
import type { DetailContext } from './detail-context';
import { editHref, notRetried } from './run-diagnosis';
import {
  attemptWord,
  between,
  blockedBy,
  toolSequence,
  type Step,
  type TraceElement,
} from './run-model';
import type { ExecutionError, JsonValue } from '@/api/types';
import { FactList, TechnicalDetails, type Fact } from '@/components/facts/FactList';
import { IdChip } from '@/components/id/IdChip';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import { Timestamp } from '@/components/time/Time';
import { WorkflowSummary } from '@/components/workflow/WorkflowSummary';
import { copy as words, useCopy } from '@/lib/copy';
import { duration, limitText } from '@/lib/time';
import { cn } from '@/lib/utils';

/**
 * The Overview tab of the details pane, one layout per kind of element: what it did, its
 * facts from returned fields, and its IDs under Technical details.
 */

export function Section({
  title,
  count,
  children,
}: {
  title?: string;
  count?: number;
  children: ReactNode;
}) {
  return (
    <section className="flex min-w-0 flex-col gap-2">
      {title ? (
        <h3 className="m-0 flex items-center gap-1.5 text-sm font-semibold text-fg">
          {title}
          {count !== undefined ? (
            <span className="font-normal text-fg-muted tabular-nums">{count}</span>
          ) : null}
        </h3>
      ) : null}
      {children}
    </section>
  );
}

function Lead({ children }: { children: ReactNode }) {
  return <p className="m-0 text-md text-fg">{children}</p>;
}

function Muted({ children }: { children: ReactNode }) {
  return <span className="text-fg-muted">{children}</span>;
}

function Ids({ rows, storageKey }: { rows: (Fact | null)[]; storageKey: string }) {
  return (
    <TechnicalDetails storageKey={storageKey}>
      <FactList compact facts={rows} />
    </TechnicalDetails>
  );
}

function valueText(v: JsonValue | undefined): string {
  return typeof v === 'object' && v !== null ? JSON.stringify(v) : String(v);
}

function ErrorDetails({
  error,
  rows,
  storageKey,
}: {
  error: ExecutionError | null;
  rows: (Fact | null)[];
  storageKey: string;
}) {
  const copy = useCopy();
  const f = copy.runs.details.facts;
  const details =
    error?.details && typeof error.details === 'object' && !Array.isArray(error.details)
      ? Object.entries(error.details)
      : [];
  return (
    <TechnicalDetails storageKey={storageKey}>
      {error ? (
        <FactList
          compact
          facts={[
            { label: f.errorCode, value: <code className="font-mono text-xs">{error.code}</code> },
            ...details.map(([k, v]) => ({
              label: k,
              value: <code className="font-mono text-xs wrap-anywhere">{valueText(v)}</code>,
            })),
          ]}
        />
      ) : null}
      {error?.message ? (
        <pre className="m-0 rounded-md bg-surface-inset p-3 font-mono text-xs whitespace-pre-wrap wrap-anywhere text-fg">
          {error.message}
        </pre>
      ) : null}
      <FactList compact facts={rows} />
    </TechnicalDetails>
  );
}

/** A list of attempts (or turns) that each select themselves. */
export function ElementList({
  items,
  ctx,
  label,
}: {
  items: readonly TraceElement[];
  ctx: DetailContext;
  label: (el: TraceElement) => ReactNode;
}) {
  return (
    <ul className="m-0 flex list-none flex-col p-0">
      {items.map((el) => (
        <li key={el.id}>
          <button
            type="button"
            aria-current={ctx.selectedId === el.id ? 'true' : undefined}
            onClick={() => {
              ctx.onSelect(el.id);
            }}
            className="flex min-h-row-dense w-full items-center gap-2 rounded-md px-2 text-left text-sm text-fg hover:bg-surface-hover aria-[current=true]:bg-surface-selected"
          >
            <StatusIndicator size="icon" status={el.status} endedWait={el.endedWait} />
            <span className="min-w-0">{label(el)}</span>
            {el.statusLabel ? <Muted>{el.statusLabel}</Muted> : null}
            <span className="flex-1" />
            <span className="text-fg-muted tabular-nums">
              <RunOffset model={ctx.model} iso={el.start} />
            </span>
            <span className="text-right text-fg-secondary tabular-nums" style={{ minWidth: '7ch' }}>
              <ElementTime model={ctx.model} el={el} />
            </span>
          </button>
        </li>
      ))}
    </ul>
  );
}

function modelName(ctx: DetailContext, id: unknown): string {
  const m = typeof id === 'string' ? ctx.models.find((x) => x.id === id) : undefined;
  return m ? m.displayName : typeof id === 'string' ? id : '';
}

function numberOf(config: Record<string, unknown>, key: string): number | null {
  const v = config[key];
  return typeof v === 'number' ? v : null;
}

function allowedTools(step: Step): string[] {
  const t = step.node.config.allowedTools;
  return Array.isArray(t) ? t.filter((x): x is string => typeof x === 'string') : [];
}

/* ---------- Run ------------------------------------------------------------------ */

/**
 * What the workflow does, from the Definition version this run is bound to (never the latest,
 * which may have changed since). Read-only; the Editor is where it changes.
 */
export function WorkflowAbout({ ctx }: { ctx: DetailContext }) {
  const copy = useCopy();
  const { search } = useLocation();
  const d = copy.runs.details;
  const def = ctx.model.definition;
  const used = ctx.model.snapshot.run.definitionVersion;
  const older = ctx.latestVersion !== null && ctx.latestVersion > used;
  const registry = useMemo(
    () => ({
      nodeTypes: new Map(ctx.nodeTypes.map((m) => [m.type, m])),
      models: new Map(ctx.models.map((m) => [m.id, m])),
    }),
    [ctx.nodeTypes, ctx.models],
  );
  return (
    <Section title={d.about}>
      <WorkflowSummary
        definition={def}
        registry={registry}
        note={
          older ? (
            <p className="m-0 text-sm text-fg-secondary">
              {d.olderNote(used)}{' '}
              <Link
                to={editHref(ctx.workflowId, null, null, search)}
                className="text-accent-fg underline-offset-3 hover:underline"
              >
                {d.openLatest}
              </Link>
            </p>
          ) : undefined
        }
      />
    </Section>
  );
}

function RunOverview({ ctx }: { ctx: DetailContext }) {
  const copy = useCopy();
  const { search } = useLocation();
  const d = copy.runs.details;
  const f = d.facts;
  const { model } = ctx;
  const run = model.snapshot.run;
  const latest = ctx.latestVersion;
  return (
    <>
      <WorkflowAbout ctx={ctx} />
      {run.status === 'FAILED' && ctx.failure ? (
        <FailCard failure={ctx.failure} workflowId={ctx.workflowId} />
      ) : null}
      {run.status === 'PAUSED' && ctx.wait ? (
        <WaitCard wait={ctx.wait} model={model} tools={ctx.tools} onSelect={ctx.onSelect} />
      ) : null}
      {run.status === 'COMPLETED' ? <Lead>{d.completed}</Lead> : null}
      {run.status === 'COMPLETED' && run.output !== null ? (
        <Section title={d.result}>
          <ValueView ctx={ctx} value={run.output} root="output" storageKey="run-result" />
        </Section>
      ) : null}
      <Section>
        <FactList
          compact
          facts={[
            { label: f.started, value: <Timestamp iso={run.startedAt} /> },
            { label: f.duration, value: <ElementTime model={model} el={model.root} /> },
            run.tokenUsage ? { label: f.tokens, value: <Tokens usage={run.tokenUsage} /> } : null,
            {
              label: f.version,
              value:
                latest !== null && latest > run.definitionVersion
                  ? d.versionOlder(run.definitionVersion, latest)
                  : d.versionLatest(run.definitionVersion),
            },
            { label: f.runId, value: <IdChip id={run.id} label={copy.runs.page.runIdLabel} /> },
            {
              label: f.workflowId,
              value: (
                <IdChip
                  id={run.workflowId}
                  label={copy.runs.view.workflowId.toLowerCase()}
                  to={editHref(ctx.workflowId, null, null, search)}
                />
              ),
            },
          ]}
        />
      </Section>
    </>
  );
}

/* ---------- Step ----------------------------------------------------------------- */

function StepFacts({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  const copy = useCopy();
  const f = copy.runs.details.facts;
  const step = el.step;
  const nr = step?.nodeRun;
  if (!step || !nr) return null;
  const config = step.node.config as Record<string, unknown>;
  const policy = step.node.executionPolicy;
  const attempts = step.detail?.attempts ?? [];
  const trace = step.trace;
  const facts: (Fact | null)[] = [
    { label: f.started, value: <RunOffset model={ctx.model} iso={nr.startedAt} /> },
    { label: f.duration, value: <ElementTime model={ctx.model} el={el} /> },
  ];
  if (trace) {
    const maxTurns = numberOf(config, 'maxTurns');
    facts.push({ label: f.model, value: modelName(ctx, config.modelId) });
    facts.push({
      label: f.turns,
      value: (
        <>
          <span className="tabular-nums">{trace.turns.length}</span>
          {maxTurns !== null ? <Muted>{f.ofUpTo(maxTurns)}</Muted> : null}
        </>
      ),
    });
    const budget = trace.generationBudget;
    const counted = allowedTools(step).some(
      (n) => ctx.tools.find((t) => t.name === n)?.countsTowardGenerationLimit,
    );
    if (budget.maxGenerationCalls !== null && counted) {
      facts.push({
        label: f.generationCalls,
        value: (
          <>
            <span className="tabular-nums">{budget.generationCallsUsed}</span>
            <Muted>{f.of(budget.maxGenerationCalls)}</Muted>
          </>
        ),
      });
    }
    facts.push({ label: f.timeLimit, value: limitText(numberOf(config, 'timeoutMs')) });
    const term = trace.agentRun.termination;
    if (term) facts.push({ label: f.ended, value: copy.status.termination[term] });
  } else if (policy && (policy.maxAttempts ?? 1) > 1) {
    facts.push({
      label: f.attempts,
      value: (
        <>
          <span className="tabular-nums">{attempts.length}</span>
          <Muted>{f.ofUpTo(policy.maxAttempts ?? 1)}</Muted>
        </>
      ),
    });
    if (policy.timeoutMs) {
      facts.push({
        label: f.timeLimit,
        value: (
          <>
            {limitText(policy.timeoutMs)}
            <Muted>{f.perAttempt}</Muted>
          </>
        ),
      });
    }
  }
  if (nr.tokenUsage) facts.push({ label: f.tokens, value: <Tokens usage={nr.tokenUsage} /> });
  return <FactList compact facts={facts} />;
}

function ToolsSection({ step, ctx }: { step: Step; ctx: DetailContext }) {
  const copy = useCopy();
  const d = copy.runs.details;
  const names = allowedTools(step);
  if (!names.length) return null;
  return (
    <details className="group">
      <summary className="flex h-control-md cursor-pointer list-none items-center gap-1.5 rounded-sm text-sm font-medium text-fg-muted hover:text-fg [&::-webkit-details-marker]:hidden">
        <ChevronRight
          aria-hidden="true"
          className="size-icon-sm transition-transform duration-2 group-open:rotate-90"
        />
        {d.toolsItCouldUse}
        <span className="font-normal tabular-nums">{names.length}</span>
      </summary>
      <div className="flex flex-col">
        {names.map((n) => {
          const meta = ctx.tools.find((t) => t.name === n);
          return (
            <div key={n} className="flex flex-col gap-1 border-t border-border-subtle py-2">
              <ToolName name={n} className="text-fg" />
              <p className="m-0 text-sm text-fg-muted">{meta ? meta.description : d.notOnServer}</p>
            </div>
          );
        })}
      </div>
    </details>
  );
}

function StepOverview({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  const copy = useCopy();
  const d = copy.runs.details;
  const f = d.facts;
  const step = el.step;
  if (!step) return null;
  const nr = step.nodeRun;
  if (!nr) {
    const by = step.statusKey === 'NONE_RUN_FAILED' ? blockedBy(ctx.model, step.node.id) : null;
    return (
      <>
        <Lead>
          {step.statusKey === 'NONE_RUN_FAILED' ? (
            by ? (
              <>
                {d.didNotRunBecause}{' '}
                <SelectLink id={by.node.id} onSelect={ctx.onSelect}>
                  {by.node.name}
                </SelectLink>{' '}
                {d.failedSuffix}
              </>
            ) : (
              d.stoppedEarlier
            )
          ) : (
            d.notStartedYet
          )}
        </Lead>
        <Ids
          storageKey="step-ids"
          rows={[
            { label: f.nodeId, value: <code className="font-mono text-xs">{step.node.id}</code> },
            {
              label: f.nodeType,
              value: <code className="font-mono text-xs">{step.node.type}</code>,
            },
          ]}
        />
      </>
    );
  }
  const failure = ctx.model.failedStep === step ? ctx.failure : null;
  const wait = ctx.model.waitingStep === step ? ctx.wait : null;
  const sequence = step.trace ? toolSequence(step.trace) : '';
  const attemptEls = el.children.filter((c) => c.kind === 'attempt');
  const ids: (Fact | null)[] = [
    { label: f.stepRunId, value: <IdChip id={nr.id} label={f.stepRunId.toLowerCase()} /> },
    { label: f.nodeId, value: <code className="font-mono text-xs">{step.node.id}</code> },
    { label: f.nodeType, value: <code className="font-mono text-xs">{step.node.type}</code> },
    step.trace
      ? {
          label: f.agentRunId,
          value: <IdChip id={step.trace.agentRun.id} label={f.agentRunId.toLowerCase()} />,
        }
      : null,
  ];
  return (
    <>
      {failure ? <FailCard failure={failure} workflowId={ctx.workflowId} /> : null}
      {wait ? (
        <WaitCard wait={wait} model={ctx.model} tools={ctx.tools} onSelect={ctx.onSelect} />
      ) : null}
      {sequence ? (
        <Lead>
          <Words text={sequence} tools={allowedTools(step)} />{' '}
          <button
            type="button"
            data-variant="link"
            onClick={() => {
              ctx.onSelect(step.node.id, 'turns');
            }}
            className="min-h-control-xs text-md text-accent-fg underline-offset-3 hover:underline"
          >
            {d.showTurns}
          </button>
        </Lead>
      ) : null}
      <Section>
        <StepFacts el={el} ctx={ctx} />
      </Section>
      <StepImages ctx={ctx} step={step} />
      {!step.trace && attemptEls.length > 1 ? (
        <Section title={f.attempts} count={attemptEls.length}>
          <ElementList items={attemptEls} ctx={ctx} label={(a) => a.label} />
        </Section>
      ) : null}
      {step.trace ? <ToolsSection step={step} ctx={ctx} /> : null}
      {failure?.focus === step.node.id ? (
        <ErrorDetails error={nr.error} rows={ids} storageKey="step-error" />
      ) : (
        <Ids rows={ids} storageKey="step-ids" />
      )}
    </>
  );
}

/* ---------- Attempts --------------------------------------------------------------- */

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

function AttemptOverview({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
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
            { label: f.tool, value: <ToolName name={a.toolName} /> },
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

function TurnOverview({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  const copy = useCopy();
  const d = copy.runs.details;
  const f = d.facts;
  const turn = el.turn;
  if (!turn) return null;
  const decision = turn.decision;
  const action = turn.action;
  const tool = decision?.tool ?? '';
  const final = decision?.kind === 'FINAL';
  let lead = '';
  if (decision) {
    if (final) lead = d.turnLead.final;
    else {
      const status = action?.status;
      const tail =
        status === 'FAILED'
          ? turn.toolAttempts.length
            ? d.turnLead.FAILED
            : d.turnLead.BLOCKED
          : status === 'SUCCEEDED' || status === 'WAITING_CALLBACK' || status === 'RUNNING'
            ? d.turnLead[status]
            : d.turnLead.other;
      lead = `${d.turnLead.call(tool)}${tail}`;
    }
  }
  return (
    <>
      {lead ? (
        <Lead>
          <Words text={lead} tools={tool ? [tool] : []} />
        </Lead>
      ) : null}
      <Section>
        <FactList
          compact
          facts={[
            { label: f.started, value: <RunOffset model={ctx.model} iso={turn.startedAt} /> },
            decision
              ? {
                  label: f.decision,
                  value: (
                    <SelectLink id={`dec:${turn.id}`} onSelect={ctx.onSelect}>
                      {final ? d.finish : <Words text={d.call(tool)} tools={[tool]} />}
                    </SelectLink>
                  ),
                }
              : null,
            action
              ? {
                  label: f.action,
                  value: (
                    <SelectLink id={action.id} onSelect={ctx.onSelect}>
                      {final ? copy.runs.trace.finalAnswer : <ToolName name={tool} />}
                    </SelectLink>
                  ),
                }
              : null,
            turn.usage ? { label: f.tokens, value: <Tokens usage={turn.usage} /> } : null,
          ]}
        />
      </Section>
      <Ids
        storageKey="turn-ids"
        rows={[{ label: f.turnId, value: <IdChip id={turn.id} label={f.turnId.toLowerCase()} /> }]}
      />
    </>
  );
}

function DecisionOverview({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  const copy = useCopy();
  const d = copy.runs.details;
  const f = d.facts;
  const turn = el.turn;
  const decision = turn?.decision;
  if (!turn || !decision) return null;
  const tool = decision.tool ?? '';
  return (
    <>
      <Section>
        <FactList
          compact
          facts={[
            {
              label: f.decided,
              value:
                decision.kind === 'FINAL' ? (
                  d.finishWithAnswer
                ) : (
                  <Words text={d.call(tool)} tools={[tool]} />
                ),
            },
            { label: f.model, value: modelName(ctx, el.step?.node.config.modelId) },
            { label: f.started, value: <RunOffset model={ctx.model} iso={turn.startedAt} /> },
            { label: f.duration, value: <ElementTime model={ctx.model} el={el} /> },
            turn.usage ? { label: f.tokens, value: <Tokens usage={turn.usage} /> } : null,
            { label: f.agentState, value: decision.hasStatePatch ? f.updated : f.unchanged },
          ]}
        />
      </Section>
      <p className="m-0 text-sm text-fg-muted">{d.notRecordedPrompt}</p>
    </>
  );
}

function ActionOverview({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  const copy = useCopy();
  const d = copy.runs.details;
  const f = d.facts;
  const turn = el.turn;
  const action = el.action;
  const decision = turn?.decision;
  const step = el.step;
  if (!turn || !action || !decision || !step) return null;
  const focus = ctx.failure?.focus === el.id;
  const wait =
    action.status === 'WAITING_CALLBACK' && ctx.model.waitingStep === step ? ctx.wait : null;
  const ids: (Fact | null)[] = [
    { label: f.actionId, value: <IdChip id={action.id} label={f.actionId.toLowerCase()} /> },
  ];
  if (decision.kind === 'FINAL') {
    return (
      <>
        {focus && ctx.failure ? (
          <FailCard failure={ctx.failure} workflowId={ctx.workflowId} />
        ) : null}
        <Lead>{d.finalLead(step.node.name)}</Lead>
        <Section>
          <FactList
            compact
            facts={[
              { label: f.started, value: <RunOffset model={ctx.model} iso={action.startedAt} /> },
              { label: f.duration, value: <ElementTime model={ctx.model} el={el} /> },
            ]}
          />
        </Section>
        {action.error ? (
          <ErrorDetails error={action.error} rows={ids} storageKey="action-error" />
        ) : (
          <Ids rows={ids} storageKey="action-ids" />
        )}
      </>
    );
  }
  const tool = decision.tool ?? '';
  const meta = ctx.tools.find((t) => t.name === tool);
  const attempts = turn.toolAttempts;
  const last = attempts[attempts.length - 1];
  const binding = last?.callbackBinding ?? null;
  const source = action.completionSource;
  const claim = action.claimSource;
  const claimWords = claim ? copy.status.claimSource[claim] : null;
  const attemptEls = attempts
    .map((x) => ctx.model.byId.get(x.id))
    .filter((x): x is TraceElement => Boolean(x));
  ids.push({ label: f.turnId, value: <IdChip id={turn.id} label={f.turnId.toLowerCase()} /> });
  if (last)
    ids.push({
      label: f.toolAttemptId,
      value: <IdChip id={last.id} label={f.toolAttemptId.toLowerCase()} />,
    });
  return (
    <>
      {focus && ctx.failure ? <FailCard failure={ctx.failure} workflowId={ctx.workflowId} /> : null}
      {wait ? (
        <WaitCard wait={wait} model={ctx.model} tools={ctx.tools} onSelect={ctx.onSelect} />
      ) : null}
      {!attempts.length && action.status === 'FAILED' && !focus ? <Lead>{d.blocked}</Lead> : null}
      <Section>
        <FactList
          compact
          facts={[
            {
              label: f.tool,
              value: (
                <span className="flex flex-col">
                  <ToolName name={tool} />
                  {meta ? <span className="text-fg-muted">{meta.description}</span> : null}
                </span>
              ),
            },
            { label: f.started, value: <RunOffset model={ctx.model} iso={action.startedAt} /> },
            { label: f.duration, value: <ElementTime model={ctx.model} el={el} /> },
            binding ? { label: f.service, value: copy.runs.providerCap(binding.providerId) } : null,
            binding
              ? {
                  label: f.externalTask,
                  value: (
                    <IdChip id={binding.externalTaskId} label={f.externalTask.toLowerCase()} />
                  ),
                }
              : null,
            source && source !== 'SYNC_EXECUTION'
              ? { label: f.result, value: d.receivedVia[source] ?? d.received }
              : null,
            claimWords ? { label: f.pickedUp, value: claimWords } : null,
            attempts.length > 1 ? { label: f.attempts, value: String(attempts.length) } : null,
          ]}
        />
      </Section>
      {!focus && action.status === 'FAILED' && notRetried(tool, attempts, ctx.tools) ? (
        <p className="m-0 text-md text-fg-secondary">
          <Words text={copy.runs.diagnosis.notRetried(tool)} tools={[tool]} />
        </p>
      ) : null}
      {attemptEls.length > 1 ? (
        <Section title={f.attempts} count={attemptEls.length}>
          <ElementList
            items={attemptEls}
            ctx={ctx}
            label={(a) => (a.toolAttempt ? attemptWord(a.toolAttempt.attemptNo, null) : a.label)}
          />
        </Section>
      ) : null}
      {action.error ? (
        <ErrorDetails error={action.error} rows={ids} storageKey="action-error" />
      ) : (
        <Ids rows={ids} storageKey="action-ids" />
      )}
    </>
  );
}

function WaitOverview({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  const copy = useCopy();
  const d = copy.runs.details;
  const f = d.facts;
  const at = el.toolAttempt;
  if (!at) return null;
  const ids = [
    { label: f.toolAttemptId, value: <IdChip id={at.id} label={f.toolAttemptId.toLowerCase()} /> },
  ];
  if (el.live && ctx.wait) {
    return (
      <>
        <WaitCard wait={ctx.wait} model={ctx.model} tools={ctx.tools} onSelect={ctx.onSelect} />
        <Ids rows={ids} storageKey="wait-ids" />
      </>
    );
  }
  const source = el.actionId ? (el.turn?.action?.completionSource ?? null) : null;
  return (
    <>
      <Section>
        <FactList
          compact
          facts={[
            {
              label: f.waitedFor,
              value: el.provider ? copy.runs.providerCap(el.provider) : d.anOutsideService,
            },
            el.task
              ? {
                  label: f.externalTask,
                  value: <IdChip id={el.task} label={f.externalTask.toLowerCase()} />,
                }
              : null,
            { label: f.sent, value: <RunOffset model={ctx.model} iso={at.dispatchedAt} /> },
            {
              label: f.result,
              value:
                at.status === 'FAILED' ? (
                  d.noResult
                ) : (
                  <>
                    {source ? (d.receivedVia[source] ?? d.received) : d.received}
                    <Muted>
                      <RunOffset model={ctx.model} iso={at.completedAt} />
                    </Muted>
                  </>
                ),
            },
            { label: f.waited, value: <ElementTime model={ctx.model} el={el} /> },
          ]}
        />
      </Section>
      <Ids rows={ids} storageKey="wait-ids" />
    </>
  );
}

function GroupOverview({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  const copy = useCopy();
  const d = copy.runs.details;
  const unit = el.unit ?? [];
  return (
    <>
      <Lead>
        <Words text={d.groupLead(unit.join(' → '), el.reps ?? 0)} tools={unit} />
      </Lead>
      <Section title={d.turnsSection} count={el.children.length}>
        <ElementList
          items={el.children}
          ctx={ctx}
          label={(c) => <Words text={c.label} tools={c.tools} />}
        />
      </Section>
    </>
  );
}

export function ElementOverview({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  const body = (() => {
    switch (el.kind) {
      case 'run':
        return <RunOverview ctx={ctx} />;
      case 'step':
        return <StepOverview el={el} ctx={ctx} />;
      case 'attempt':
        return <AttemptOverview el={el} ctx={ctx} />;
      case 'turn':
        return <TurnOverview el={el} ctx={ctx} />;
      case 'decision':
        return <DecisionOverview el={el} ctx={ctx} />;
      case 'action':
        return <ActionOverview el={el} ctx={ctx} />;
      case 'wait':
        return <WaitOverview el={el} ctx={ctx} />;
      case 'group':
        return <GroupOverview el={el} ctx={ctx} />;
    }
  })();
  return <div className={cn('flex flex-col gap-4 px-4 pt-4 pb-16')}>{body}</div>;
}
