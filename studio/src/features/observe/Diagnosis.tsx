import { CircleAlert, Hourglass, PencilLine, TriangleAlert } from 'lucide-react';
import type { ReactNode } from 'react';
import { Link, useLocation } from 'react-router';

import { Elapsed, RunOffset } from './RunFacts';
import { Words } from './Words';
import { editHref, type Failure } from './run-diagnosis';
import { eventLabel } from './run-events';
import type { RunModel, WaitInfo } from './run-model';
import type { ToolMetadata } from '@/api/types';
import { FactList } from '@/components/facts/FactList';
import { IdChip } from '@/components/id/IdChip';
import { buttonVariants } from '@/components/ui/button-variants';
import { Deadline, Timestamp } from '@/components/time/Time';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';

/** A link-styled button that selects another element of the run. */
export function SelectLink({
  id,
  onSelect,
  children,
}: {
  id: string;
  onSelect: (id: string) => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      data-variant="link"
      onClick={() => {
        onSelect(id);
      }}
      className={cn(buttonVariants({ variant: 'link' }), 'min-h-control-xs text-sm')}
    >
      {children}
    </button>
  );
}

/**
 * The failure: what happened, why, what to do next, from returned fields only. Its one
 * action is the fix in the Editor; Run again lives in the header.
 */
export function FailCard({
  failure,
  workflowId,
  withEffect = false,
}: {
  failure: Failure;
  workflowId: string;
  withEffect?: boolean;
}) {
  const copy = useCopy();
  const { search } = useLocation();
  const d = copy.runs.diagnosis;
  const tools = failure.tool ? [failure.tool] : [];
  return (
    <div
      role="group"
      aria-label={d.failure}
      className="flex gap-3 rounded-lg bg-st-failed-bg px-4 py-3 text-md text-fg"
    >
      <CircleAlert aria-hidden="true" className="mt-0.5 size-icon-md shrink-0 text-st-failed-fg" />
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <p className="m-0 font-semibold">{failure.title}</p>
        <p className="m-0">
          <Words text={failure.cause} tools={tools} />
          {withEffect && failure.effect ? ` ${failure.effect}` : null}
        </p>
        {failure.next ? <p className="m-0 text-fg-secondary">{failure.next}</p> : null}
        {failure.notRetriedTool ? (
          <p className="m-0 flex items-start gap-2 text-fg-secondary">
            <TriangleAlert
              aria-hidden="true"
              className="mt-0.5 size-icon-sm shrink-0 text-caution-fg"
            />
            <span>
              <Words text={d.notRetried(failure.notRetriedTool)} tools={[failure.notRetriedTool]} />{' '}
              {d.checkTarget}
            </span>
          </p>
        ) : null}
        <div className="mt-1 flex flex-wrap gap-2">
          <Link
            to={editHref(workflowId, failure.step.node.id, failure.field, search)}
            className={cn(buttonVariants({ variant: 'primary', size: 'sm' }), 'no-underline')}
          >
            <PencilLine aria-hidden="true" className="size-icon-sm" />
            {failure.nextLabel}
          </Link>
        </div>
      </div>
    </div>
  );
}

/**
 * The wait: for what, since when, until when and what happens then. Stored values only:
 * the binding's service and external task, the attempt's dispatch time and the Agent
 * run's deadline. Never the binding's routing, tokens or addresses.
 */
export function WaitCard({
  wait,
  model,
  tools,
  onSelect,
}: {
  wait: WaitInfo;
  model: RunModel;
  tools: readonly ToolMetadata[];
  onSelect: (id: string) => void;
}) {
  const copy = useCopy();
  const d = copy.runs.diagnosis;
  const f = copy.runs.details.facts;
  const who = copy.runs.provider(wait.provider);
  const tool = wait.turn?.decision?.tool ?? null;
  const meta = tool ? tools.find((t) => t.name === tool) : undefined;
  const last = model.events[model.events.length - 1];
  const attemptEl = wait.attempt ? (model.byId.get(wait.attempt.id) ?? null) : null;
  const actionId = wait.turn?.action?.id ?? null;
  const attemptTarget = attemptEl?.id ?? actionId;
  return (
    <div
      role="group"
      aria-label={d.waiting}
      className="flex gap-3 rounded-lg bg-st-waiting-bg px-4 py-3 text-md text-fg"
    >
      <Hourglass aria-hidden="true" className="mt-0.5 size-icon-md shrink-0 text-st-waiting-fg" />
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <p className="m-0 font-semibold">{d.waitingTitle(wait.step.node.name, who)}</p>
        <p className="m-0">
          {wait.turn && tool ? (
            <Words text={d.turnCalled(wait.turn.turnNo, tool)} tools={[tool]} />
          ) : (
            d.continues
          )}
        </p>
        <FactList
          compact
          facts={[
            {
              label: f.waitingFor,
              value: tool ? (
                <span className="flex flex-col">
                  <span>
                    <Words
                      text={d.toReport(copy.runs.providerCap(wait.provider), tool)}
                      tools={[tool]}
                    />
                  </span>
                  {meta?.description ? (
                    <span className="text-fg-muted">{meta.description}</span>
                  ) : null}
                </span>
              ) : (
                copy.runs.providerCap(wait.provider)
              ),
            },
            wait.since
              ? {
                  label: f.since,
                  value: (
                    <>
                      <Timestamp iso={wait.since} />
                      <span className="text-fg-muted">
                        {d.sinceFor('')}
                        <Elapsed since={wait.since} />
                      </span>
                    </>
                  ),
                }
              : null,
            {
              label: f.deadline,
              value: wait.deadline ? (
                <>
                  <Timestamp iso={wait.deadline} />
                  <span className="text-fg-muted">
                    <Deadline iso={wait.deadline} />
                  </span>
                </>
              ) : (
                <span className="text-fg-muted">{f.noneSet}</span>
              ),
            },
            wait.task
              ? {
                  label: f.externalTask,
                  value: <IdChip id={wait.task} label={copy.runs.view.taskIdLabel(who)} />,
                }
              : null,
            wait.attempt && attemptTarget
              ? {
                  label: f.attempt,
                  value: (
                    <>
                      <SelectLink id={attemptTarget} onSelect={onSelect}>
                        {copy.runs.details.crumb.attempt(wait.attempt.attemptNo)}
                      </SelectLink>
                      <span className="text-fg-muted">
                        {d.sentAt('')}
                        <RunOffset
                          model={model}
                          iso={wait.attempt.dispatchedAt ?? wait.attempt.startedAt}
                        />
                      </span>
                    </>
                  ),
                }
              : null,
            { label: f.progress, value: d.saved },
            last
              ? {
                  label: f.latestEvent,
                  value: (
                    <>
                      <Words {...eventLabel(last)} />
                      <span className="text-fg-muted">
                        <RunOffset model={model} iso={last.timestamp} />
                      </span>
                    </>
                  ),
                }
              : null,
          ]}
        />
        {wait.deadline ? (
          <p className="m-0 text-fg-secondary">{d.timesOut(wait.step.node.name)}</p>
        ) : null}
      </div>
    </div>
  );
}
