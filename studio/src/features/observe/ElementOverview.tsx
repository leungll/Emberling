import { ChevronRight } from 'lucide-react';
import { useMemo } from 'react';
import { Link, useLocation } from 'react-router';

import { CompletionCard } from './CompletionCard';
import { WhatHappened } from './RunStory';
import { openTurn } from './open-turn';
import { RunSummary, StepOutput } from './RunSummary';
import { FailCard, SelectLink, WaitCard } from './Diagnosis';
import { ElementTime, RunOffset, Tokens } from './RunFacts';
import { ToolName, Words } from './Words';
import { modelName, type DetailContext } from './detail-context';
import { editHref } from './run-diagnosis';
import { blockedBy, toolSequence, type Step, type TraceElement } from './run-model';
import { FactList, type Fact } from '@/components/facts/FactList';
import { IdChip } from '@/components/id/IdChip';
import { Timestamp } from '@/components/time/Time';
import { WorkflowSummary } from '@/components/workflow/WorkflowSummary';
import { useCopy } from '@/lib/copy';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import { cardInfo } from './card-info';
import { limitText } from '@/lib/time';
import { cn } from '@/lib/utils';
import { ErrorDetails, Ids, Lead, Muted, Section, ElementList } from './OverviewSection';
import { AttemptOverview } from './AttemptOverview';
import {
  ActionOverview,
  DecisionOverview,
  GroupOverview,
  TurnOverview,
  WaitOverview,
} from './AgentOverview';

/**
 * The Overview tab of the details pane, one layout per kind of element: what it did, its
 * facts from returned fields, and its IDs under Technical details.
 */

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

/** The run's facts and IDs, for its Data tab; the run view reads the rest. */
export function RunIds({ ctx }: { ctx: DetailContext }) {
  const copy = useCopy();
  const { search } = useLocation();
  const d = copy.runs.details;
  const f = d.facts;
  const { model } = ctx;
  const run = model.snapshot.run;
  const latest = ctx.latestVersion;
  return (
    <>
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
    const agentRun = trace.agentRun;
    if (agentRun.goal) facts.push({ label: f.agentGoal, value: agentRun.goal });
    // A stopped clock is a stored fact (pausedAt and its reason); the deadline is only a
    // countdown while the clock runs, so a paused Agent shows the pause instead.
    if (agentRun.pausedAt && agentRun.pauseReason) {
      facts.push({
        label: f.agentClock,
        value: (
          <span className="flex flex-col">
            <span>{copy.status.pause[agentRun.pauseReason]}</span>
            <Muted>
              {copy.runs.details.pausedSince} <Timestamp iso={agentRun.pausedAt} />
            </Muted>
          </span>
        ),
      });
    }
    const term = agentRun.termination;
    if (term) facts.push({ label: f.ended, value: copy.status.termination[term] });
    const ws = trace.workspace;
    if (ws) {
      facts.push({ label: f.repository, value: ws.repositoryId });
      facts.push({
        label: f.baseCommit,
        value: <span className="font-mono">{ws.baseCommit.slice(0, 12)}</span>,
      });
      facts.push({ label: f.allowedPaths, value: ws.allowedPaths.join(', ') });
      if (ws.deployTarget) {
        facts.push({
          label: f.deployTarget,
          value: `${ws.deployTarget.service} (${ws.deployTarget.environment})`,
        });
      }
      const latest = ws.patches.at(-1);
      facts.push({
        label: f.patches,
        value: (
          <>
            <span className="tabular-nums">{ws.patches.length}</span>
            {latest ? <Muted> {f.latestPatch(latest.patchDigest.slice(0, 12))}</Muted> : null}
            {ws.patchesTruncated ? <Muted> ({f.morePatches})</Muted> : null}
          </>
        ),
      });
    }
    const requests = trace.changeRequests;
    if (requests && requests.items.length > 0) {
      const latestRequest = requests.items.at(-1);
      facts.push({
        label: f.changeRequests,
        value: (
          <>
            <span className="tabular-nums">{requests.items.length}</span>
            {latestRequest ? (
              <Muted>
                {' '}
                <span className="font-mono">{f.latestChangeRequest(latestRequest.id)}</span>
              </Muted>
            ) : null}
            {requests.truncated ? <Muted> ({f.moreChangeRequests})</Muted> : null}
          </>
        ),
      });
    }
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
  const wait = step.detail?.capacityWait;
  if (nr.status === 'READY' && wait) {
    facts.push({ label: f.queue, value: f.queuePlace(wait.position, wait.held, wait.limit) });
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
              <ToolName name={n} tools={ctx.tools} className="text-fg" />
              <p className="m-0 text-sm text-fg-muted">{meta ? meta.description : d.notOnServer}</p>
            </div>
          );
        })}
      </div>
    </details>
  );
}

function stepIds(
  step: Step,
  f: ReturnType<typeof useCopy>['runs']['details']['facts'],
): (Fact | null)[] {
  const nr = step.nodeRun;
  return [
    nr
      ? { label: f.stepRunId, value: <IdChip id={nr.id} label={f.stepRunId.toLowerCase()} /> }
      : null,
    { label: f.nodeId, value: <code className="font-mono text-xs">{step.node.id}</code> },
    { label: f.nodeType, value: <code className="font-mono text-xs">{step.node.type}</code> },
    step.trace
      ? {
          label: f.agentRunId,
          value: <IdChip id={step.trace.agentRun.id} label={f.agentRunId.toLowerCase()} />,
        }
      : null,
  ];
}

/** A step's times and IDs, for its Data tab. */
export function StepData({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  const copy = useCopy();
  if (!el.step) return null;
  return (
    <>
      {el.step.trace ? null : <StepFacts el={el} ctx={ctx} />}
      <Ids rows={stepIds(el.step, copy.runs.details.facts)} storageKey="step-ids" />
    </>
  );
}

/**
 * An Agent's status without a goal verdict, from its own step: what it answered
 * with and the turn it finished in.
 */
function AgentStatusCard({ el, step, ctx }: { el: TraceElement; step: Step; ctx: DetailContext }) {
  const info = cardInfo(ctx.model, step);
  return (
    <div className="flex items-start gap-3 rounded-lg bg-surface-inset px-4 py-3 text-sm">
      <StatusIndicator size="icon" status={el.status} />
      <span className="flex min-w-0 flex-col">
        <span className="font-semibold text-fg">
          <Words text={info.line.text} tools={info.line.tools} />
        </span>
        {info.turn ? <span className="text-fg-muted">{info.turn}</span> : null}
      </span>
    </div>
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
  const ids = stepIds(step, f);
  const completion = step.trace?.agentRun.completion ?? null;
  return (
    <>
      {failure ? <FailCard failure={failure} workflowId={ctx.workflowId} /> : null}
      {wait ? (
        <WaitCard wait={wait} model={ctx.model} tools={ctx.tools} onSelect={ctx.onSelect} />
      ) : null}
      {completion ? <CompletionCard completion={completion} /> : null}
      {step.trace && !completion && !failure && !wait ? (
        <AgentStatusCard el={el} step={step} ctx={ctx} />
      ) : null}
      {/* What the step handed on, then, for an Agent, how it got there. Times and IDs are in
          the Data tab. */}
      {step.trace ? (
        <StepOutput step={step} ctx={ctx} status={!completion} />
      ) : (
        <Section title={d.tabs.output}>
          <StepOutput step={step} ctx={ctx} status={!completion} />
        </Section>
      )}
      {step.trace?.turns.length ? (
        <WhatHappened
          trace={step.trace}
          tools={ctx.tools}
          title={copy.runs.story.howItGotThere}
          onSelect={(turnId) => {
            ctx.onSelect(step.node.id, 'turns');
            openTurn(turnId);
          }}
        />
      ) : sequence ? (
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
      {step.trace ? (
        <Section>
          <StepFacts el={el} ctx={ctx} />
        </Section>
      ) : null}
      {!step.trace && attemptEls.length > 1 ? (
        <Section title={f.attempts} count={attemptEls.length}>
          <ElementList items={attemptEls} ctx={ctx} label={(a) => a.label} />
        </Section>
      ) : null}
      {step.trace ? <ToolsSection step={step} ctx={ctx} /> : null}
      {failure?.focus === step.node.id ? (
        <ErrorDetails error={nr.error} rows={ids} storageKey="step-error" />
      ) : null}
    </>
  );
}

/* ---------- Attempts --------------------------------------------------------------- */

export function ElementOverview({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  const body = (() => {
    switch (el.kind) {
      case 'run':
        return <RunSummary ctx={ctx} />;
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
