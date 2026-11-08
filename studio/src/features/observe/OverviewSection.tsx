import { type ReactNode } from 'react';

import { ElementTime, RunOffset } from './RunFacts';
import type { DetailContext } from './detail-context';
import { type TraceElement } from './run-model';
import type { ExecutionError, JsonValue } from '@/api/types';
import { FactList, TechnicalDetails, type Fact } from '@/components/facts/FactList';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import { useCopy } from '@/lib/copy';

/** The pieces every Overview layout is built from: titled sections, lead lines and IDs. */

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

export function Lead({ children }: { children: ReactNode }) {
  return <p className="m-0 text-md text-fg">{children}</p>;
}

export function Muted({ children }: { children: ReactNode }) {
  return <span className="text-fg-muted">{children}</span>;
}

export function Ids({ rows, storageKey }: { rows: (Fact | null)[]; storageKey: string }) {
  return (
    <TechnicalDetails storageKey={storageKey}>
      <FactList compact facts={rows} />
    </TechnicalDetails>
  );
}

function valueText(v: JsonValue | undefined): string {
  return typeof v === 'object' && v !== null ? JSON.stringify(v) : String(v);
}

export function ErrorDetails({
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
