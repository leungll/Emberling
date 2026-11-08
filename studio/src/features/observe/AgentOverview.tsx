import { Link } from 'react-router';

import { FailCard, SelectLink, WaitCard } from './Diagnosis';
import { ElementTime, RunOffset, Tokens } from './RunFacts';
import { ToolName, Words } from './Words';
import { modelName, type DetailContext } from './detail-context';
import { notRetried } from './run-diagnosis';
import { attemptWord, type TraceElement } from './run-model';
import { Money } from '@/components/cost/Cost';
import { FactList, type Fact } from '@/components/facts/FactList';
import { IdChip } from '@/components/id/IdChip';
import { Timestamp } from '@/components/time/Time';
import { useCopy } from '@/lib/copy';
import { ErrorDetails, Ids, Lead, Muted, Section, ElementList } from './OverviewSection';

/** The Overviews of an Agent's turns, decisions, actions, waits and repeated groups. */

export function TurnOverview({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
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
          : status === 'SUCCEEDED' ||
              status === 'WAITING_CALLBACK' ||
              status === 'WAITING_APPROVAL' ||
              status === 'WAITING_HUMAN' ||
              status === 'RUNNING'
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
                      {final ? (
                        copy.runs.trace.finalAnswer
                      ) : (
                        <ToolName name={tool} tools={ctx.tools} />
                      )}
                    </SelectLink>
                  ),
                }
              : null,
            turn.usage ? { label: f.tokens, value: <Tokens usage={turn.usage} /> } : null,
            turn.cost ? { label: f.cost, value: <Money cost={turn.cost} /> } : null,
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

export function DecisionOverview({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
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
            turn.cost ? { label: f.cost, value: <Money cost={turn.cost} /> } : null,
            { label: f.agentState, value: decision.hasStatePatch ? f.updated : f.unchanged },
          ]}
        />
      </Section>
      <p className="m-0 text-sm text-fg-muted">{d.notRecordedPrompt}</p>
    </>
  );
}

export function ActionOverview({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
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
    (action.status === 'WAITING_CALLBACK' ||
      action.status === 'WAITING_APPROVAL' ||
      action.status === 'WAITING_HUMAN') &&
    ctx.model.waitingStep === step
      ? ctx.wait
      : null;
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
  const approval = action.approval;
  const approvalWords = approval ? copy.status.approval[approval.status] : null;
  const release = action.release;
  const lastOperation = release?.operations.at(-1) ?? null;
  const authorizationWords = action.authorization
    ? copy.status.authorization[action.authorization]
    : null;
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
      {!attempts.length && action.status === 'FAILED' && !focus ? (
        <Lead>
          {action.authorization === 'DENY' && action.authorizationReason
            ? d.denied(action.authorizationReason)
            : d.blocked}
        </Lead>
      ) : null}
      <Section>
        <FactList
          compact
          facts={[
            {
              label: f.tool,
              value: (
                <span className="flex flex-col">
                  <ToolName name={tool} tools={ctx.tools} />
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
            authorizationWords ? { label: f.authorization, value: authorizationWords } : null,
            approval && approvalWords
              ? {
                  label: f.approval,
                  value: (
                    <span className="flex flex-col">
                      <span>
                        <Link
                          to={`/approvals/${encodeURIComponent(approval.id)}`}
                          className="text-accent-fg underline-offset-3 hover:underline"
                        >
                          {approvalWords}
                        </Link>
                      </span>
                      {approval.decidedBy ? (
                        <span className="text-fg-muted">
                          {f.decidedBy}: {approval.decidedBy}
                        </span>
                      ) : null}
                      {approval.reason ? (
                        <span className="text-fg-muted">
                          {f.reason}: {approval.reason}
                        </span>
                      ) : null}
                    </span>
                  ),
                }
              : null,
            attempts.length > 1 ? { label: f.attempts, value: String(attempts.length) } : null,
            release?.operations.length
              ? {
                  label: f.operations,
                  value: (
                    <span className="flex flex-col">
                      {release.operations.map((op) => (
                        <span key={op.operationKey}>
                          {f.operation(op.operationKey, copy.status.releaseOutcome[op.outcome])}
                        </span>
                      ))}
                    </span>
                  ),
                }
              : null,
            release?.occupation
              ? {
                  label: f.releaseTarget,
                  value:
                    release.occupation.status === 'ACTIVE' || !release.occupation.releaseReason
                      ? f.occupationHeld(release.occupation.targetKey)
                      : f.occupationReleased(
                          release.occupation.targetKey,
                          copy.status.releaseReason[release.occupation.releaseReason],
                        ),
                }
              : null,
            release?.occupation?.status === 'ACTIVE' && release.occupation.nextQueryAt
              ? {
                  label: f.postTaskQuery,
                  value: <Timestamp iso={release.occupation.nextQueryAt} />,
                }
              : null,
            release?.occupation?.deployedUncheckedVersion
              ? {
                  label: f.uncheckedVersion,
                  value: f.uncheckedVersionValue(release.occupation.deployedUncheckedVersion),
                }
              : null,
            lastOperation?.confirmDeadline &&
            action.status !== 'SUCCEEDED' &&
            action.status !== 'FAILED'
              ? {
                  label: f.confirmDeadline,
                  value: <Timestamp iso={lastOperation.confirmDeadline} />,
                }
              : null,
            release?.humanWait
              ? {
                  label: f.humanWait,
                  value: (
                    <span className="flex flex-col">
                      <span>
                        {release.humanWait.outcome
                          ? f.humanWaitOutcome(
                              copy.status.humanWait[release.humanWait.status],
                              release.humanWait.outcome,
                            )
                          : copy.status.humanWait[release.humanWait.status]}
                      </span>
                      {release.humanWait.closedBy ? (
                        <span className="text-fg-muted">
                          {f.decidedBy}: {release.humanWait.closedBy}
                        </span>
                      ) : null}
                    </span>
                  ),
                }
              : null,
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

export function WaitOverview({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
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

export function GroupOverview({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
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
