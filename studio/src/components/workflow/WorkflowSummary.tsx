import type { ReactNode } from 'react';

import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';
import {
  stepSentence,
  summaryInputs,
  summaryOrder,
  summaryOutputs,
  type SummaryDefinition,
  type SummaryRegistry,
} from '@/lib/workflow-summary';

export interface WorkflowSummaryProps {
  definition: SummaryDefinition;
  registry: SummaryRegistry;
  /** Shown above the description, for example which version the summary describes. */
  note?: ReactNode;
  /** Hides the description, for a page that shows it elsewhere. */
  hideDescription?: boolean;
  /** Makes each step a button, for example to select it on a canvas. */
  onStepClick?: (nodeId: string) => void;
  /** Extra content after a step's name, for example its issue count. */
  stepExtra?: (nodeId: string) => ReactNode;
  /** Makes each input and output name a button. */
  onIoClick?: (nodeId: string) => void;
  /** Shows "No output step yet." instead of leaving Produces out when there is none. */
  showEmptyOutputs?: boolean;
  className?: string;
}

function Part({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex min-w-0 flex-col gap-1">
      <h4 className="m-0 text-xs font-medium text-fg-muted">{title}</h4>
      {children}
    </section>
  );
}

/**
 * The read-only overview of a workflow: its description, the numbered steps with one sentence
 * each, what a run needs to start and what it produces. The Editor's overview and the run
 * pages use the same component so the words match.
 */
export function WorkflowSummary({
  definition,
  registry,
  note,
  hideDescription = false,
  onStepClick,
  stepExtra,
  onIoClick,
  showEmptyOutputs = false,
  className,
}: WorkflowSummaryProps) {
  const copy = useCopy();
  const s = copy.editor.summary;
  const steps = summaryOrder(definition.nodes);
  const inputs = summaryInputs(definition, registry);
  const outputs = summaryOutputs(definition, registry);

  const ioName = (id: string, name: string) =>
    onIoClick ? (
      <button
        type="button"
        onClick={() => onIoClick(id)}
        className="min-h-control-xs min-w-0 rounded-sm text-left font-medium wrap-anywhere text-fg hover:text-accent-fg"
      >
        {name}
      </button>
    ) : (
      <span className="min-w-0 font-medium wrap-anywhere text-fg">{name}</span>
    );

  const ioList = (rows: typeof inputs) => (
    <ul className="m-0 flex list-none flex-col gap-2 p-0">
      {rows.map((r) => (
        <li key={r.id} className="flex items-baseline justify-between gap-3">
          {ioName(r.id, r.name)}
          <span className="shrink-0 text-sm text-fg-muted">{r.meta}</span>
        </li>
      ))}
    </ul>
  );

  return (
    <div className={cn('flex min-w-0 flex-col gap-3', className)}>
      {note}
      {!hideDescription && definition.description ? (
        <p className="m-0 text-md text-fg">{definition.description}</p>
      ) : null}
      <Part title={s.steps}>
        {steps.length ? (
          <ol className="-mx-2 my-0 flex list-none flex-col gap-0.5 p-0">
            {steps.map((n, i) => {
              const inner = (
                <>
                  <span className="flex size-5 shrink-0 items-center justify-center rounded-full border border-border-control text-xs text-fg-muted tabular-nums">
                    {i + 1}
                  </span>
                  <span className="flex min-w-0 flex-col gap-0.5">
                    <span className="flex flex-wrap items-center gap-2 font-medium text-fg">
                      {n.name}
                      {stepExtra?.(n.id)}
                    </span>
                    <span className="text-sm text-fg-muted">
                      {stepSentence(definition, n, registry)}
                    </span>
                  </span>
                </>
              );
              return (
                <li key={n.id}>
                  {onStepClick ? (
                    <button
                      type="button"
                      onClick={() => onStepClick(n.id)}
                      className="flex w-full gap-2 rounded-md p-2 text-left hover:bg-surface-hover"
                    >
                      {inner}
                    </button>
                  ) : (
                    <div className="flex w-full gap-2 p-2">{inner}</div>
                  )}
                </li>
              );
            })}
          </ol>
        ) : (
          <p className="m-0 text-sm text-fg-muted">{s.noSteps}</p>
        )}
      </Part>
      <Part title={s.needs}>
        {inputs.length ? (
          ioList(inputs)
        ) : (
          <p className="m-0 text-sm text-fg-muted">{s.nothingNeeded}</p>
        )}
      </Part>
      {outputs.length ? (
        <Part title={s.produces}>{ioList(outputs)}</Part>
      ) : showEmptyOutputs ? (
        <Part title={s.produces}>
          <p className="m-0 text-sm text-fg-muted">{s.noOutput}</p>
        </Part>
      ) : null}
    </div>
  );
}
