import { Activity, ChevronRight, X } from 'lucide-react';
import { useEffect, useRef, type ReactNode } from 'react';

import { ElementOverview } from './ElementOverview';
import { RunOffset } from './RunFacts';
import { TurnsTab } from './TurnsTab';
import { ValueView } from './ValueView';
import { imageLabel, mediaItems, savedImageOf } from './run-media';
import { Words } from './Words';
import type { DetailContext } from './detail-context';
import { crumbLabel, kindOf, tabsFor, type DetailTab } from './detail-tabs';
import { elementOfEvent, eventLabel, eventSummary, eventsFor } from './run-events';
import { ancestors, attemptWord, type TraceElement } from './run-model';
import type { JsonValue } from '@/api/types';
import { IdChip } from '@/components/id/IdChip';
import { JsonViewer } from '@/components/json/JsonViewer';
import { ThumbSet } from '@/components/media/Thumbs';
import { StateBlock } from '@/components/state/StateBlock';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import { IconButton } from '@/components/ui/button';
import { Tabs } from '@/components/ui/tabs';
import { Tooltip } from '@/components/ui/tooltip';
import { copy as words, useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';

/**
 * The details pane: where the selected element sits in the run, what it is, and its tabs.
 * Every tab reads returned fields only; Raw shows the records as the Backend sent them.
 */

function Title({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  if (el.kind === 'run') return <>{ctx.model.definition.name}</>;
  if (el.kind === 'attempt') {
    if (el.toolAttempt) {
      return (
        <Words
          text={words.runs.details.toolAttemptTitle(
            el.toolAttempt.toolName,
            el.toolAttempt.attemptNo,
          )}
          tools={[el.toolAttempt.toolName]}
        />
      );
    }
    if (el.nodeAttempt) return <>{attemptWord(el.nodeAttempt.attemptNo, el.step?.node ?? null)}</>;
  }
  return <Words text={el.label} tools={el.tools} />;
}

/** A status with its own word, for waits and repeated turns, else the status table's. */
export function ElementStatus({ el, size }: { el: TraceElement; size?: 'icon' }) {
  if (size === 'icon' || !el.statusLabel) {
    return <StatusIndicator status={el.status} size={size} endedWait={el.endedWait} />;
  }
  return (
    <span className="inline-flex items-center gap-1.5 text-sm text-fg">
      <StatusIndicator status={el.status} size="icon" endedWait={el.endedWait} />
      <span className={cn(el.endedWait && 'text-fg-muted')}>{el.statusLabel}</span>
    </span>
  );
}

function Path({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  const copy = useCopy();
  if (el.kind === 'run') return null;
  const chain = ancestors(el).reverse();
  const sep = (
    <span aria-hidden="true" className="inline-flex text-fg-muted">
      <ChevronRight className="size-icon-xs" />
    </span>
  );
  return (
    <nav
      aria-label={copy.runs.details.path}
      className="-ml-1 mb-2 flex flex-wrap items-center gap-0.5 pr-8"
    >
      {chain.map((a) => (
        <span key={a.id} className="contents">
          <button
            type="button"
            onClick={() => {
              ctx.onSelect(a.id);
            }}
            className={cn(
              'inline-flex min-h-control-xs items-center gap-1 rounded-sm px-1 text-sm text-fg-muted hover:bg-surface-hover hover:text-fg active:bg-surface-pressed',
              a.kind === 'run' && 'font-medium text-fg-secondary',
            )}
          >
            {a.kind === 'run' ? <Activity aria-hidden="true" className="size-icon-xs" /> : null}
            {crumbLabel(a)}
          </button>
          {sep}
        </span>
      ))}
      <span aria-current="location" className="inline-flex items-center px-1 text-sm text-fg">
        {crumbLabel(el)}
      </span>
    </nav>
  );
}

function Pad({ children }: { children: ReactNode }) {
  return <div className="flex flex-col gap-4 px-4 pt-4 pb-16">{children}</div>;
}

function isEmpty(v: JsonValue | null | undefined): boolean {
  return (
    v === null ||
    v === undefined ||
    (typeof v === 'object' && !Array.isArray(v) && Object.keys(v).length === 0)
  );
}

function IoTab({
  el,
  ctx,
  which,
}: {
  el: TraceElement;
  ctx: DetailContext;
  which: 'input' | 'output';
}) {
  const copy = useCopy();
  const io = copy.runs.details.io;
  const input = which === 'input';
  let value: JsonValue | null | undefined = null;
  let root = input ? 'input' : 'output';
  let gap = false;
  const run = ctx.model.snapshot.run;
  switch (el.kind) {
    case 'run':
      value = input ? run.input : run.output;
      break;
    case 'step':
      value = el.step?.nodeRun ? (input ? el.step.nodeRun.input : el.step.nodeRun.output) : null;
      break;
    case 'attempt':
      if (el.nodeAttempt) {
        value = input ? el.nodeAttempt.input : el.nodeAttempt.result;
        root = input ? 'input' : 'result';
      } else gap = true;
      break;
    case 'action':
      if (el.turn?.decision?.kind === 'FINAL') value = el.step?.nodeRun?.output ?? null;
      else gap = true;
      break;
    default:
      break;
  }
  if (gap) {
    return (
      <Pad>
        <StateBlock kind="empty" title={input ? io.argsGap : io.resultGap} body={io.gapBody} />
      </Pad>
    );
  }
  if (isEmpty(value)) {
    const body =
      el.kind === 'run' && !input
        ? !ctx.model.terminal
          ? io.outputLater
          : run.status === 'FAILED'
            ? io.outputFailed
            : undefined
        : undefined;
    return (
      <Pad>
        <StateBlock kind="empty" title={input ? io.noInput : io.noOutput} body={body} />
      </Pad>
    );
  }
  return (
    <Pad>
      <ValueView ctx={ctx} value={value ?? null} root={root} storageKey={`${el.kind}-${which}`} />
    </Pad>
  );
}

function rawOf(el: TraceElement, ctx: DetailContext): [string, unknown][] {
  const r = words.runs.details.raw;
  const turnNo = el.turn?.turnNo ?? 0;
  switch (el.kind) {
    case 'run':
      return [[r.run, ctx.model.snapshot.run]];
    case 'step': {
      const step = el.step;
      if (!step) return [];
      if (!step.nodeRun) return [[r.definitionNode, step.node]];
      const out: [string, unknown][] = [[r.nodeDetail, step.detail ?? { nodeRun: step.nodeRun }]];
      if (step.trace) {
        const t = step.trace;
        out.push([
          r.agent,
          { agentRun: t.agentRun, facts: t.facts, generationBudget: t.generationBudget },
        ]);
      }
      return out;
    }
    case 'turn':
      return el.turn ? [[r.turn(turnNo), el.turn]] : [];
    case 'decision':
      return el.turn
        ? [
            [
              r.decision(turnNo),
              {
                decision: el.turn.decision,
                usage: el.turn.usage,
                startedAt: el.turn.startedAt,
                completedAt: el.turn.completedAt,
              },
            ],
          ]
        : [];
    case 'action':
      return el.turn
        ? [[r.action(turnNo), { action: el.turn.action, toolAttempts: el.turn.toolAttempts }]]
        : [];
    case 'attempt':
      if (el.nodeAttempt) return [[r.attempt(el.nodeAttempt.attemptNo), el.nodeAttempt]];
      return el.toolAttempt ? [[r.toolAttempt(el.toolAttempt.attemptNo), el.toolAttempt]] : [];
    case 'wait':
      return el.toolAttempt ? [[r.toolAttempt(el.toolAttempt.attemptNo), el.toolAttempt]] : [];
    default:
      return [];
  }
}

function RawTab({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  return (
    <Pad>
      {rawOf(el, ctx).map(([title, value]) => (
        <section key={title} className="flex min-w-0 flex-col gap-2">
          <h3 className="m-0 text-sm font-semibold text-fg">{title}</h3>
          <JsonViewer value={value} root="record" initialMode="raw" />
        </section>
      ))}
    </Pad>
  );
}

function EventsTab({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  const copy = useCopy();
  const list = eventsFor(ctx.model, el);
  return (
    <ul
      aria-label={copy.runs.events.forElement}
      className="m-0 flex min-w-fit list-none flex-col p-0 pt-1 pb-16"
    >
      {list.map((e) => {
        const target = elementOfEvent(ctx.model, e);
        const summary = eventSummary(ctx.model, e);
        return (
          <li key={e.seq}>
            <button
              type="button"
              onClick={() => {
                ctx.onSelect(target.id);
              }}
              style={{ gridTemplateColumns: '32px 72px minmax(7rem, 1fr) minmax(7rem, 1fr)' }}
              className="grid min-h-row-dense w-full items-start gap-3 px-4 py-1.5 text-left text-sm hover:bg-surface-hover"
            >
              <span className="text-right font-mono text-fg-muted tabular-nums">{e.seq}</span>
              <span className="text-fg-secondary tabular-nums">
                <RunOffset model={ctx.model} iso={e.timestamp} />
              </span>
              <Tooltip content={e.type}>
                <span className="min-w-0 wrap-anywhere text-fg">
                  <Words {...eventLabel(e)} />
                </span>
              </Tooltip>
              <span className="min-w-0 wrap-anywhere text-fg-muted">{summary}</span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

function FactsTab({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  const copy = useCopy();
  const f = copy.runs.details.facts2;
  const trace = el.step?.trace;
  if (!trace) return null;
  const turnOf = new Map<string, { turnNo: number; actionId: string | null }>();
  for (const t of trace.turns) {
    for (const a of t.toolAttempts)
      turnOf.set(a.id, { turnNo: t.turnNo, actionId: t.action?.id ?? null });
  }
  return (
    <Pad>
      <ul className="m-0 flex list-none flex-col p-0">
        {trace.facts.items.map((fact) => {
          const t = turnOf.get(fact.toolAttemptId);
          const label =
            (copy.factLabel as Record<string, string | undefined>)[fact.factType] ??
            fact.factType.replace(/_/g, ' ');
          const img = savedImageOf(ctx.model, fact);
          return (
            <li
              key={fact.id}
              className="flex min-h-row-dense flex-wrap items-center gap-2 px-2 py-1 text-sm text-fg"
            >
              {img ? <ThumbSet items={mediaItems(ctx.model, [img])} /> : null}
              <span className="flex min-w-0 flex-col">
                <span>{label}</span>
                {img ? <span className="text-fg-muted">{imageLabel(ctx.model, img)}</span> : null}
              </span>
              {fact.verdict === true ? (
                <span className="text-st-succeeded-fg">{f.passed}</span>
              ) : fact.verdict === false ? (
                <span className="text-st-failed-fg">{f.failed}</span>
              ) : (
                <span className="text-fg-muted">{f.noVerdict}</span>
              )}
              <span className="flex-1" />
              {img ? null : (
                <IdChip id={fact.subject} kind={f.subject} label={f.subject.toLowerCase()} />
              )}
              {t?.actionId ? (
                <button
                  type="button"
                  data-variant="link"
                  onClick={() => {
                    if (t.actionId) ctx.onSelect(t.actionId);
                  }}
                  className="min-h-control-xs text-sm text-accent-fg underline-offset-3 hover:underline"
                >
                  {f.turnLink(t.turnNo)}
                </button>
              ) : null}
            </li>
          );
        })}
      </ul>
      {trace.facts.truncated ? <p className="m-0 text-sm text-fg-muted">{f.truncated}</p> : null}
    </Pad>
  );
}

interface DetailsPaneProps {
  el: TraceElement;
  ctx: DetailContext;
  tab: DetailTab;
  onTab: (tab: DetailTab) => void;
  /** Shown as a drawer: it has a close button. */
  drawer: boolean;
  onClose: () => void;
  /** Bumped to move focus to the title, after a drawer opens or the path is used. */
  focusKey: number;
}

export function DetailsPane({ el, ctx, tab, onTab, drawer, onClose, focusKey }: DetailsPaneProps) {
  const copy = useCopy();
  const tabs = tabsFor(el, ctx);
  const current = tabs.some((t) => t.value === tab) ? tab : 'overview';
  const kind = kindOf(el);
  const titleRef = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    if (focusKey > 0) titleRef.current?.focus({ preventScroll: true });
  }, [focusKey]);

  let body: ReactNode;
  if (current === 'input' || current === 'output')
    body = <IoTab el={el} ctx={ctx} which={current} />;
  else if (current === 'events') body = <EventsTab el={el} ctx={ctx} />;
  else if (current === 'raw') body = <RawTab el={el} ctx={ctx} />;
  else if (current === 'facts') body = <FactsTab el={el} ctx={ctx} />;
  else if (current === 'turns') body = <TurnsTab el={el} ctx={ctx} />;
  else body = <ElementOverview el={el} ctx={ctx} />;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="relative shrink-0 px-4 pt-3 pb-2">
        {drawer ? (
          <span className="absolute top-2 right-3">
            <IconButton
              label={copy.runs.details.close}
              tooltip={copy.runs.details.closeTip}
              onClick={onClose}
            >
              <X aria-hidden="true" className="size-icon-md" />
            </IconButton>
          </span>
        ) : null}
        <Path el={el} ctx={ctx} />
        <div className="flex items-start gap-3 pr-8">
          <span className="inline-flex size-control-md shrink-0 items-center justify-center rounded-md bg-surface-hover text-fg-secondary">
            <kind.icon aria-hidden="true" className="size-icon-md" />
          </span>
          <div className="flex min-w-0 flex-1 flex-col gap-0.5">
            <span className="text-xs font-medium text-fg-muted">{kind.word}</span>
            <h2
              ref={titleRef}
              tabIndex={-1}
              className="m-0 min-w-0 text-md font-semibold wrap-anywhere text-fg focus-visible:outline-offset-2"
            >
              <Title el={el} ctx={ctx} />
            </h2>
            <span>
              {el.kind === 'run' ? (
                <StatusIndicator
                  status={{ kind: 'runLong', value: ctx.model.snapshot.run.status }}
                />
              ) : (
                <ElementStatus el={el} />
              )}
            </span>
          </div>
        </div>
      </div>
      {tabs.length > 1 ? (
        <Tabs<DetailTab>
          label={copy.runs.details.label}
          value={current}
          items={tabs}
          onValueChange={onTab}
          listClassName="shrink-0 gap-3 overflow-x-auto px-4"
          className="flex min-h-0 flex-1 flex-col"
          contentClassName="min-h-0 flex-1 overflow-y-auto"
        >
          {body}
        </Tabs>
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto">{body}</div>
      )}
    </div>
  );
}
