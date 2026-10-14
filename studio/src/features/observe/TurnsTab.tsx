import {
  ArrowRightFromLine,
  ChevronRight,
  CircleMinus,
  FileCheck,
  Hourglass,
  RotateCw,
  Sparkle,
  TriangleAlert,
  Wrench,
  type LucideIcon,
} from 'lucide-react';
import { useState, type ReactNode } from 'react';

import { ElementTime, RunOffset, Tokens } from './RunFacts';
import { ToolName, Words } from './Words';
import type { DetailContext } from './detail-context';
import { notRetried } from './run-diagnosis';
import { factImage, imageLabel, mediaItems, savedImageOf } from './run-media';
import { attemptWord, toolSequence, type RunModel, type TraceElement } from './run-model';
import type { ExecutionFactTrace } from '@/api/types';
import { ThumbSet } from '@/components/media/Thumbs';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import { copy as words, useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';

/**
 * The Turns tab of an Agent step: one block per turn in order, the decision in plain
 * words, then the tool call with its status, duration, waits, attempts and the results it
 * saved. Repeats folded in the trace stay folded here.
 */

type ByAttempt = ReadonlyMap<string, readonly ExecutionFactTrace[]>;

function factLabel(type: string): string {
  return (words.factLabel as Record<string, string | undefined>)[type] ?? type.replace(/_/g, ' ');
}

function Item({
  id,
  ctx,
  icon: Icon,
  children,
  meta,
  wait = false,
}: {
  id: string;
  ctx: DetailContext;
  icon: LucideIcon;
  children: ReactNode;
  meta?: ReactNode;
  wait?: boolean;
}) {
  return (
    <button
      type="button"
      aria-current={ctx.selectedId === id ? 'true' : undefined}
      onClick={() => {
        ctx.onSelect(id);
      }}
      className="flex min-h-control-md w-full items-center gap-2 rounded-md px-2 py-1 text-left text-sm text-fg hover:bg-surface-hover aria-[current=true]:bg-surface-selected"
    >
      <Icon aria-hidden="true" className="size-icon-sm shrink-0 text-fg-muted" />
      <span
        className={cn(
          'flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1',
          wait && 'text-fg-secondary',
        )}
      >
        {children}
      </span>
      {meta ? (
        <span className="flex shrink-0 gap-3 whitespace-nowrap text-fg-secondary tabular-nums">
          {meta}
        </span>
      ) : null}
    </button>
  );
}

function Line({
  icon: Icon,
  children,
  caution = false,
}: {
  icon: LucideIcon;
  children: ReactNode;
  caution?: boolean;
}) {
  return (
    <span
      className={cn(
        'flex min-h-control-sm gap-2 px-2 text-sm text-fg-secondary',
        caution ? 'items-start py-1' : 'items-center',
      )}
    >
      <Icon
        aria-hidden="true"
        className={cn(
          'size-icon-sm shrink-0',
          caution ? 'mt-0.5 text-caution-fg' : 'text-fg-muted',
        )}
      />
      <span>{children}</span>
    </span>
  );
}

function Saved({
  facts,
  model,
}: {
  facts: readonly ExecutionFactTrace[] | undefined;
  model: RunModel;
}) {
  const copy = useCopy();
  const t = copy.runs.details.turnsTab;
  if (!facts?.length) return null;
  return (
    <>
      {facts.map((f) => {
        const img = savedImageOf(model, f);
        return (
          <Line key={f.id} icon={FileCheck}>
            {t.saved(`${factLabel(f.factType)}${t.verdict(f.verdict)}`)}
            {img ? (
              <>
                {' '}
                <span className="text-fg-muted">{imageLabel(model, img)}</span>
                <ThumbSet
                  items={mediaItems(model, [img])}
                  size="sm"
                  className="ml-2 inline-flex align-middle"
                />
              </>
            ) : null}
          </Line>
        );
      })}
    </>
  );
}

function Head({
  id,
  ctx,
  no,
  status,
  what,
  start,
}: {
  id: string;
  ctx: DetailContext;
  no: string;
  status: ReactNode;
  what?: string;
  start: string | null;
}) {
  return (
    <button
      type="button"
      aria-current={ctx.selectedId === id ? 'true' : undefined}
      onClick={() => {
        ctx.onSelect(id);
      }}
      className="flex min-h-row-dense w-full items-center gap-2 rounded-t-lg px-3 text-left text-sm text-fg hover:bg-surface-hover aria-[current=true]:bg-surface-selected"
    >
      <span className="font-semibold whitespace-nowrap">{no}</span>
      {status}
      {what ? <span className="min-w-0 text-fg-secondary">{what}</span> : null}
      <span className="flex-1" />
      <span className="text-fg-muted tabular-nums">
        <RunOffset model={ctx.model} iso={start} />
      </span>
    </button>
  );
}

/** A failed or waiting turn carries a status edge on its left. */
const TURN_EDGE: Partial<Record<string, string>> = {
  failed: 'bg-st-failed-dot',
  waiting: 'bg-st-waiting-dot',
};

function TurnBlock({
  tel,
  ctx,
  byAttempt,
}: {
  tel: TraceElement;
  ctx: DetailContext;
  byAttempt: ByAttempt;
}) {
  const copy = useCopy();
  const t = copy.runs.details.turnsTab;
  const turn = tel.turn;
  const decision = turn?.decision;
  const action = turn?.action;
  if (!turn) return null;
  const final = decision?.kind === 'FINAL';
  const tool = decision?.tool ?? '';
  const dec = ctx.model.byId.get(`dec:${turn.id}`);
  const act = action ? ctx.model.byId.get(action.id) : undefined;
  const attempts = turn.toolAttempts;
  const errorCode = action?.error?.code;
  const blockedWhy =
    (errorCode
      ? (copy.errors as Record<string, { short: string } | undefined>)[errorCode]?.short
      : undefined) ?? t.notAllowed;
  return (
    <li className="relative rounded-lg border border-border bg-surface">
      {TURN_EDGE[tel.tone] ? (
        <span
          aria-hidden="true"
          className={cn('absolute inset-y-0 left-0 w-0.5 rounded-l-lg', TURN_EDGE[tel.tone])}
        />
      ) : null}
      <Head
        id={turn.id}
        ctx={ctx}
        no={copy.runs.details.crumb.turn(turn.turnNo)}
        status={<StatusIndicator size="icon" status={tel.status} />}
        start={turn.startedAt}
      />
      <div className="flex flex-col px-1 pb-1">
        {dec ? (
          <Item
            id={dec.id}
            ctx={ctx}
            icon={Sparkle}
            meta={
              <>
                <ElementTime model={ctx.model} el={dec} />
                <Tokens usage={turn.usage} />
              </>
            }
          >
            {final ? t.chooseFinish : <Words text={t.choose(tool)} tools={[tool]} />}
          </Item>
        ) : null}
        {act && action ? (
          <Item
            id={action.id}
            ctx={ctx}
            icon={final ? ArrowRightFromLine : Wrench}
            meta={<ElementTime model={ctx.model} el={act} />}
          >
            {final ? copy.runs.trace.finalAnswer : <ToolName name={tool} />}
            <StatusIndicator status={act.status} />
          </Item>
        ) : null}
        <div className="flex flex-col pl-6">
          {!final && !attempts.length && action?.status === 'FAILED' ? (
            <Line icon={CircleMinus}>{t.blocked(blockedWhy.toLowerCase())}</Line>
          ) : null}
          {attempts.length > 1
            ? attempts.map((x) => {
                const xe = ctx.model.byId.get(x.id);
                return xe ? (
                  <Item
                    key={x.id}
                    id={x.id}
                    ctx={ctx}
                    icon={RotateCw}
                    meta={<ElementTime model={ctx.model} el={xe} />}
                  >
                    {attemptWord(x.attemptNo, null)}
                    <StatusIndicator status={xe.status} />
                  </Item>
                ) : null;
              })
            : null}
          {attempts.map((x) => {
            const w = ctx.model.byId.get(`wait:${attempts.length > 1 ? x.id : (action?.id ?? '')}`);
            return (
              <div key={x.id} className="contents">
                {w ? (
                  <Item
                    id={w.id}
                    ctx={ctx}
                    icon={Hourglass}
                    wait
                    meta={<ElementTime model={ctx.model} el={w} />}
                  >
                    {w.label}
                  </Item>
                ) : null}
                <Saved facts={byAttempt.get(x.id)} model={ctx.model} />
              </div>
            );
          })}
          {!final && action?.status === 'FAILED' && notRetried(tool, attempts, ctx.tools) ? (
            <Line icon={TriangleAlert} caution>
              <Words text={copy.runs.diagnosis.notRetried(tool)} tools={[tool]} />
            </Line>
          ) : null}
          {final && action?.status === 'SUCCEEDED' && tel.step ? (
            <Line icon={ArrowRightFromLine}>{t.became(tel.step.node.name)}</Line>
          ) : null}
        </div>
      </div>
    </li>
  );
}

function GroupBlock({
  g,
  ctx,
  byAttempt,
}: {
  g: TraceElement;
  ctx: DetailContext;
  byAttempt: ByAttempt;
}) {
  const copy = useCopy();
  const [open, setOpen] = useState(false);
  const t = copy.runs.details.turnsTab;
  const turns = g.turns ?? [];
  const unit = g.unit ?? [];
  const first = turns[0];
  const last = turns[turns.length - 1];
  if (!first || !last) return null;
  return (
    <li className="rounded-lg border border-border bg-surface">
      <Head
        id={g.id}
        ctx={ctx}
        no={copy.runs.details.crumb.turns(first.turnNo, last.turnNo)}
        status={<StatusIndicator size="icon" status={g.status} />}
        what={t.repeated(g.reps ?? 0)}
        start={first.startedAt}
      />
      <div className="flex flex-col px-1 pb-1">
        {unit.map((tool, i) => {
          const mine = turns.filter((_, k) => k % unit.length === i);
          const saved = new Map<string, number>();
          for (const turn of mine) {
            for (const x of turn.toolAttempts) {
              for (const f of byAttempt.get(x.id) ?? []) {
                const k = `${factLabel(f.factType)}${t.verdict(f.verdict)}`;
                saved.set(k, (saved.get(k) ?? 0) + 1);
              }
            }
          }
          const images = mine.flatMap((turn) =>
            turn.toolAttempts.flatMap((x) =>
              (byAttempt.get(x.id) ?? []).flatMap((f) => {
                const img = factImage(f);
                return img ? [img] : [];
              }),
            ),
          );
          const target = mine[0]?.action?.id;
          return (
            <div key={`${tool}-${String(i)}`} className="contents">
              {target ? (
                <Item id={target} ctx={ctx} icon={Wrench}>
                  <ToolName name={tool} />
                  <span className="text-fg-muted tabular-nums">{t.times(mine.length)}</span>
                  <span className="text-sm text-st-succeeded-fg">
                    {mine.length > 1 ? t.allSucceeded : t.succeeded}
                  </span>
                </Item>
              ) : null}
              {saved.size || images.length ? (
                <div className="flex flex-col pl-6">
                  {[...saved].map(([k, n]) => (
                    <Line key={k} icon={FileCheck}>
                      {t.saved(k)}
                      {n > 1 ? (
                        <span className="text-fg-muted tabular-nums"> {t.times(n)}</span>
                      ) : null}
                    </Line>
                  ))}
                  {images.length ? (
                    <ThumbSet
                      items={mediaItems(ctx.model, images)}
                      size="sm"
                      className="px-2 py-1"
                    />
                  ) : null}
                </div>
              ) : null}
            </div>
          );
        })}
        <button
          type="button"
          aria-expanded={open}
          onClick={() => {
            setOpen((o) => !o);
          }}
          className="mt-1 flex min-h-control-sm items-center gap-1 rounded-md px-2 text-left text-sm text-fg-secondary hover:bg-surface-hover hover:text-fg"
        >
          <ChevronRight
            aria-hidden="true"
            className={cn('size-icon-sm transition-transform duration-2', open && 'rotate-90')}
          />
          {t.showEach(turns.length)}
        </button>
        {open ? (
          <ol className="m-0 flex list-none flex-col gap-2 py-2 pr-0 pl-2">
            {g.children.map((c) => (
              <TurnBlock key={c.id} tel={c} ctx={ctx} byAttempt={byAttempt} />
            ))}
          </ol>
        ) : null}
      </div>
    </li>
  );
}

export function TurnsTab({ el, ctx }: { el: TraceElement; ctx: DetailContext }) {
  const copy = useCopy();
  const trace = el.step?.trace;
  if (!trace) return null;
  const byAttempt = new Map<string, ExecutionFactTrace[]>();
  for (const f of trace.facts.items) {
    const list = byAttempt.get(f.toolAttemptId) ?? [];
    list.push(f);
    byAttempt.set(f.toolAttemptId, list);
  }
  const sequence = toolSequence(trace);
  const tools = [...new Set(trace.turns.map((x) => x.decision?.tool ?? '').filter(Boolean))];
  return (
    <div className="flex flex-col gap-4 px-4 pt-4 pb-16">
      {sequence ? (
        <p className="m-0 text-md text-fg">
          <Words text={sequence} tools={tools} />
        </p>
      ) : null}
      <ol className="m-0 flex list-none flex-col gap-2 p-0">
        {el.children.map((c) =>
          c.kind === 'group' ? (
            <GroupBlock key={c.id} g={c} ctx={ctx} byAttempt={byAttempt} />
          ) : (
            <TurnBlock key={c.id} tel={c} ctx={ctx} byAttempt={byAttempt} />
          ),
        )}
      </ol>
      <p className="m-0 text-sm text-fg-muted">{copy.runs.details.turnsTab.gap}</p>
    </div>
  );
}
