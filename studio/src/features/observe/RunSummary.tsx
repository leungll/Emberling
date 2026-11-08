import { ChevronRight } from 'lucide-react';
import type { ReactNode } from 'react';

import type { JsonValue } from '@/api/types';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import { imagesOf, asData } from '@/components/media/value-media';
import { ReportResult } from '@/components/values/ReportResult';
import { ShapedValues } from '@/components/values/ShapedValues';
import { TaskInputValues } from '@/components/values/TaskValues';
import { hasReport, lastReport, reportOf, type LastReport } from '@/lib/agent-report';
import { useCopy, type Copy } from '@/lib/copy';
import { shapeSummary } from '@/lib/shape-summary';
import { taskValues } from '@/lib/task-values';
import { duration } from '@/lib/time';
import {
  deepEqual,
  fieldTitle,
  humanize,
  isTechnical,
  nestedSummary,
  readableKeys,
} from '@/lib/value-reading';
import { CompletionCard } from './CompletionCard';
import { FailCard, WaitCard } from './Diagnosis';
import { ElementTime } from './RunFacts';
import type { DetailContext } from './detail-context';
import type { RunModel, Step } from './run-model';
import { RunMediaProvider } from './RunMediaProvider';
import { runImagesOf } from './run-value-media';

/**
 * The run page's reading area, in the order a reader asks: did it work and what came out,
 * where it stopped and why, how it got there. Generic: it reads the run snapshot, the bound
 * definition and the agent traces only.
 */

type Obj = Record<string, JsonValue>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

/** The report a finished run handed back: from a step nothing follows, fed by an Agent. */
function runResult(model: RunModel | null | undefined): { step: Step; report: LastReport } | null {
  if (!model?.terminal) return null;
  const { nodes, edges } = model.definition;
  for (const step of model.steps) {
    if (edges.some((e) => e.source === step.node.id)) continue;
    const report = lastReport(nodes, edges, step.node, step.nodeRun ?? undefined);
    if (hasReport(report)) return { step, report };
  }
  return null;
}

function Block({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex min-w-0 flex-col gap-2">
      <h3 className="m-0 text-sm font-semibold text-fg">{title}</h3>
      {children}
    </section>
  );
}

/** The goal-driven verdict of the Agent that produced the run's result, if any. */
function completionOf(ctx: DetailContext) {
  const res = runResult(ctx.model);
  const agents = ctx.model.steps.filter((s) => s.trace?.agentRun.completion);
  const feeding = res
    ? agents.find((a) =>
        ctx.model.definition.edges.some(
          (e) => e.source === a.node.id && e.target === res.step.node.id,
        ),
      )
    : undefined;
  return (feeding ?? agents[0])?.trace?.agentRun.completion ?? null;
}

/** One status card: why it stopped, what it waits on, the Agent's verdict, or the run status. */
function RunStatusCard({ ctx }: { ctx: DetailContext }) {
  const { model } = ctx;
  const run = model.snapshot.run;
  if (run.status === 'FAILED' && ctx.failure)
    return <FailCard failure={ctx.failure} workflowId={ctx.workflowId} />;
  if (run.status === 'PAUSED' && ctx.wait)
    return <WaitCard wait={ctx.wait} model={model} tools={ctx.tools} onSelect={ctx.onSelect} />;
  const completion = completionOf(ctx);
  if (completion) return <CompletionCard completion={completion} />;
  return (
    <div className="flex items-center gap-2 rounded-lg bg-surface-inset px-4 py-3 text-sm">
      <StatusIndicator status={{ kind: 'runLong', value: run.status }} />
      <span className="text-fg-muted">
        <ElementTime model={model} el={model.root} />
      </span>
    </div>
  );
}

/** The one task step a run fills in, when the workflow has exactly one. */
function taskStep(ctx: DetailContext) {
  const tasks = ctx.model.steps.filter((s) => s.node.type === 'task_input');
  return tasks.length === 1 && tasks[0] ? tasks[0].node : null;
}

/** The values the Request shows: the task step's fields, else the run input. */
function requestValues(ctx: DetailContext): Obj | null {
  const input = ctx.model.snapshot.run.input;
  if (!isObj(input)) return null;
  const task = taskStep(ctx);
  return (task ? taskValues(task, input) : null) ?? input;
}

/** Result fields that only echo the request unchanged; the Request already shows them. */
function echoed(data: Obj | null, request: Obj | null): string[] {
  if (!data || !request) return [];
  return Object.keys(data).filter((k) => k in request && deepEqual(data[k], request[k]));
}

/** The run input by shape: a task step's fields with its schema, else the input as it is. */
function Request({ ctx, heading }: { ctx: DetailContext; heading: string }) {
  const input = ctx.model.snapshot.run.input;
  if (!isObj(input) || !Object.keys(input).length) return null;
  const task = taskStep(ctx);
  const data = task ? taskValues(task, input) : null;
  if (task && data && Object.keys(input).length === 1)
    return <TaskInputValues node={task} data={data} />;
  return <ShapedValues schema={null} data={input} heading={heading} />;
}

function StepRows({ ctx }: { ctx: DetailContext }) {
  return (
    <ol className="m-0 flex list-none flex-col p-0">
      {ctx.model.steps.map((s) => {
        const el = ctx.model.byId.get(s.node.id);
        const ms = s.nodeRun?.latencyMs;
        return (
          <li key={s.node.id}>
            <button
              type="button"
              onClick={() => {
                ctx.onSelect(s.node.id);
              }}
              className="group/row flex min-h-row-dense w-full cursor-pointer items-center gap-2 rounded-sm px-1 text-left text-sm hover:bg-surface-hover"
            >
              {el ? <StatusIndicator status={el.status} size="icon" /> : null}
              <span className="min-w-0 flex-1 wrap-anywhere text-fg">{s.node.name}</span>
              <span className="text-fg-muted tabular-nums">
                {typeof ms === 'number' ? duration(ms) : ''}
              </span>
              <ChevronRight
                aria-hidden="true"
                className="size-icon-xs text-fg-muted opacity-0 group-hover/row:opacity-100"
              />
            </button>
          </li>
        );
      })}
    </ol>
  );
}

/** The run view, read as asked, got, how: status, request, result and the steps. */
export function RunSummary({ ctx }: { ctx: DetailContext }) {
  const copy = useCopy();
  const s = copy.runs.summary;
  const { model } = ctx;
  const run = model.snapshot.run;
  const res = runResult(model);
  const completion = completionOf(ctx);
  const showsOutcome =
    !(run.status === 'FAILED' && ctx.failure) &&
    !(run.status === 'PAUSED' && ctx.wait) &&
    !!completion;
  return (
    <RunMediaProvider model={model}>
      <RunStatusCard ctx={ctx} />
      <Block title={s.request}>
        <Request ctx={ctx} heading={s.request} />
      </Block>
      {res ? (
        <Block title={s.result}>
          <ReportResult
            report={res.report}
            status={!showsOutcome}
            omit={echoed(res.report.data, requestValues(ctx))}
          />
        </Block>
      ) : run.status === 'COMPLETED' && isObj(run.output) && Object.keys(run.output).length ? (
        <Block title={s.result}>
          <ShapedValues
            schema={null}
            data={run.output}
            heading={s.result}
            omit={echoed(run.output, requestValues(ctx))}
          />
        </Block>
      ) : null}
      <Block title={s.steps}>
        <StepRows ctx={ctx} />
      </Block>
    </RunMediaProvider>
  );
}

/** What a step handed on, by shape: an Agent's report, a report it shows, or its output. */
export function StepOutput({
  step,
  ctx,
  status,
}: {
  step: Step;
  ctx: DetailContext;
  status: boolean;
}) {
  return (
    <RunMediaProvider model={ctx.model}>
      <StepOutputBody step={step} ctx={ctx} status={status} />
    </RunMediaProvider>
  );
}

function StepOutputBody({
  step,
  ctx,
  status,
}: {
  step: Step;
  ctx: DetailContext;
  status: boolean;
}) {
  const copy = useCopy();
  const s = copy.runs.summary;
  const nr = step.nodeRun ?? undefined;
  if (!nr) return null;
  const { nodes, edges } = ctx.model.definition;
  const report =
    step.node.type === 'agent' ? reportOf(step.node, nr) : lastReport(nodes, edges, step.node, nr);
  if (hasReport(report)) return <ReportResult report={report} status={status} />;
  if (step.node.type === 'task_input') {
    const data = taskValues(step.node, ctx.model.snapshot.run.input);
    if (data) return <TaskInputValues node={step.node} data={data} />;
  }
  const out = nr.output;
  if (!isObj(out) || !Object.keys(out).length) return null;
  const meta = ctx.nodeTypes.find((m) => m.type === step.node.type);
  // Unused outputs are left out; without registered ports every output key counts.
  const ports: { name: string; title?: string }[] = meta?.outputs.length
    ? meta.outputs.filter(
        (p) =>
          edges.some((e) => e.source === step.node.id && e.sourceHandle === p.name) &&
          p.name in out,
      )
    : Object.keys(out).map((name) => ({ name, title: undefined }));
  const inputs = inputValues(nr.input);
  const images = runImagesOf(ctx.model);
  const rows = ports.map((p) => {
    const v = asData(out[p.name]);
    return { p, v, rel: relation(copy, v, inputs) };
  });
  if (!rows.length) return null;
  if (rows.every((r) => r.rel === s.sameAsInput))
    return <p className="m-0 text-sm text-fg">{s.sameAsInput}</p>;
  if (rows.every((r) => !r.rel)) {
    const only = rows.length === 1 ? rows[0] : undefined;
    if (only && isObj(only.v) && !imagesOf(images, only.v))
      return <ShapedValues schema={null} data={only.v} />;
    if (only && typeof only.v === 'string')
      return isTechnical(only.v) ? null : (
        <p className="m-0 text-sm whitespace-pre-wrap text-fg">{only.v}</p>
      );
    // Each output by its registered title, laid out as one value set.
    const schema = {
      properties: Object.fromEntries(
        rows.map((r) => [r.p.name, { title: r.p.title ?? humanize(r.p.name) }]),
      ),
    };
    return (
      <ShapedValues
        schema={schema}
        data={Object.fromEntries(rows.map((r) => [r.p.name, r.v ?? null]))}
      />
    );
  }
  return (
    <ul className="m-0 flex list-none flex-col gap-3 p-0">
      {rows.map(({ p, v, rel }) => (
        <li key={p.name} className="flex flex-col gap-2 text-sm">
          <span className="flex flex-wrap gap-x-2">
            <span className="font-medium text-fg">{p.title ?? humanize(p.name)}</span>
            <span className="text-fg-muted">{rel ?? summaryOf(copy, v)}</span>
          </span>
          <Highlights value={v} inputs={inputs} model={ctx.model} />
        </li>
      ))}
    </ul>
  );
}

/** The values a step received: each input port's value, and the input as a whole. */
function inputValues(input: JsonValue | undefined): JsonValue[] {
  if (!isObj(input)) return input === undefined || input === null ? [] : [input];
  return [input, ...Object.values(input).map((v) => asData(v) ?? null)];
}

/** "Same as the input", or what a value adds to an input it contains; null when unrelated. */
function relation(copy: Copy, v: JsonValue | undefined, inputs: JsonValue[]): string | null {
  const s = copy.runs.summary;
  if (inputs.some((i) => deepEqual(v, i))) return s.sameAsInput;
  if (!isObj(v)) return null;
  for (const i of inputs) {
    if (!isObj(i) || !Object.keys(i).length) continue;
    if (!Object.keys(i).every((k) => deepEqual(v[k], i[k]))) continue;
    const added = readableKeys(
      v,
      Object.keys(v).filter((k) => !(k in i)),
    );
    if (!added.length) return s.sameAsInput;
    const names = s.inputPlus(copy.listNames(added.map((k) => fieldTitle(k, v[k]).toLowerCase())));
    const one = added.length === 1 && added[0] ? v[added[0]] : undefined;
    return isObj(one) ? `${names}: ${nestedSummary(one)}` : names;
  }
  return null;
}

/** One line for a value, by its shape, counting only fields an Overview would show. */
function summaryOf(copy: Copy, v: JsonValue | undefined): string {
  if (v === undefined || v === null) return copy.runs.summary.nothing;
  if (isTechnical(v)) return copy.runs.summary.aReference;
  if (isObj(v) || Array.isArray(v)) return nestedSummary(v);
  return shapeSummary(v) ?? '';
}

/** What a port adds that a summary line cannot say: its images and its long text. */
function Highlights({
  value,
  inputs,
  model,
}: {
  value: JsonValue | undefined;
  inputs: JsonValue[];
  model: RunModel;
}) {
  if (typeof value === 'string' && value.length > 80 && !inputs.some((i) => deepEqual(value, i)))
    return <p className="m-0 whitespace-pre-wrap text-fg">{value}</p>;
  if (!isObj(value)) return null;
  const known = new Set(
    inputs.flatMap((i) =>
      isObj(i) ? Object.keys(i).filter((k) => deepEqual(value[k], i[k])) : [],
    ),
  );
  const images = runImagesOf(model);
  const keys = Object.keys(value).filter((k) => {
    if (known.has(k)) return false;
    const v = value[k];
    return !!imagesOf(images, v) || (typeof v === 'string' && v.length > 80);
  });
  if (!keys.length) return null;
  return (
    <ShapedValues schema={null} data={Object.fromEntries(keys.map((k) => [k, value[k] ?? null]))} />
  );
}
