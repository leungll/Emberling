import { useEffect, useState, type ReactNode } from 'react';

import { SectionLabel } from './Panel';
import { StatusDot } from './StatusGlyph';
import type { NodeRun, RunEvent, RunSnapshot } from '@/api/types';
import { formatClock, formatRunClock } from '@/lib/format';
import { runStatusLabel, runStatusTone, type StatusTone } from '@/lib/status';
import { cn } from '@/lib/utils';

const BANNER_TONE: Record<StatusTone, string> = {
  neutral: 'border-[var(--border)] bg-[var(--muted)]',
  running: 'border-[var(--status-running-dot)] bg-[var(--status-running-bg)]',
  waiting: 'border-[var(--status-waiting-dot)] bg-[var(--status-waiting-bg)]',
  succeeded: 'border-[var(--status-succeeded-dot)] bg-[var(--status-succeeded-bg)]',
  failed: 'border-[var(--status-failed-dot)] bg-[var(--status-failed-bg)]',
};

/** Re-renders once a second while `active`, so a stopwatch over server timestamps ticks. */
function useNow(active: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [active]);
  return now;
}

function nodeRunName(nodeRun: NodeRun, nodeNames?: ReadonlyMap<string, string>): string {
  return nodeNames?.get(nodeRun.nodeId) ?? nodeRun.nodeId;
}

/** Earliest `waitingAt` among the WAITING_CALLBACK NodeRuns the Backend reported. */
function earliestWaitingAt(waiting: NodeRun[]): string | null {
  return waiting.reduce<string | null>(
    (earliest, nodeRun) =>
      nodeRun.waitingAt && (!earliest || nodeRun.waitingAt < earliest)
        ? nodeRun.waitingAt
        : earliest,
    null,
  );
}

interface Banner {
  title: string;
  detail: string;
  clock: string;
  clockLabel: string;
}

/**
 * Headline for the server's own Run status. Which banner shows is keyed only on
 * `run.status`; the waiting NodeRun names are a display filter over reported NodeRun
 * statuses and never feed back into the Run status.
 */
function banner(
  snapshot: RunSnapshot,
  nodeNames: ReadonlyMap<string, string> | undefined,
  now: number,
): Banner {
  const { run } = snapshot;
  const nowIso = new Date(now).toISOString();
  switch (run.status) {
    case 'PAUSED': {
      const waiting = snapshot.nodeRuns.filter((n) => n.status === 'WAITING_CALLBACK');
      const names = waiting.map((n) => nodeRunName(n, nodeNames)).join(', ');
      const since = earliestWaitingAt(waiting);
      return {
        title: names ? `Waiting for ${names} callback` : runStatusLabel(run.status),
        detail: since
          ? `${waiting.length} NodeRun${waiting.length === 1 ? '' : 's'} WAITING_CALLBACK since ${formatClock(since)}.`
          : `Started ${formatClock(run.startedAt)}.`,
        clock: formatRunClock(since, nowIso),
        clockLabel: 'waiting',
      };
    }
    case 'RUNNING':
      return {
        title: runStatusLabel(run.status),
        detail: `Started ${formatClock(run.startedAt)}.`,
        clock: formatRunClock(run.startedAt, nowIso),
        clockLabel: 'elapsed',
      };
    case 'COMPLETED':
      return {
        title: runStatusLabel(run.status),
        detail: `Finished at ${formatClock(run.completedAt)}.`,
        clock: formatRunClock(run.startedAt, run.completedAt ?? nowIso),
        clockLabel: 'duration',
      };
    case 'FAILED':
      return {
        title: runStatusLabel(run.status),
        detail: run.error ? `${run.error.code}: ${run.error.message}` : 'The Run failed.',
        clock: formatRunClock(run.startedAt, run.completedAt ?? nowIso),
        clockLabel: 'duration',
      };
  }
}

interface RunSummaryCardProps {
  snapshot: RunSnapshot;
  events: RunEvent[];
  followLive: boolean;
  definitionName?: string;
  nodeNames?: ReadonlyMap<string, string>;
  /** The Run Rail, placed under the summary facts. */
  children?: ReactNode;
}

/** RUN SUMMARY card of the Run view mock. Every value is a server fact. */
export function RunSummaryCard({
  snapshot,
  events,
  followLive,
  definitionName,
  nodeNames,
  children,
}: RunSummaryCardProps) {
  const { run } = snapshot;
  const tone = runStatusTone(run.status);
  const live = run.status === 'RUNNING' || run.status === 'PAUSED';
  const now = useNow(live);
  const head = banner(snapshot, nodeNames, now);
  const latestSeq = events.reduce((max, event) => Math.max(max, event.seq), snapshot.lastSeq);

  return (
    <section
      data-testid="run-summary"
      className="flex min-w-0 flex-1 flex-col gap-4 rounded-xl border border-[var(--border)] bg-[var(--card)] px-5 pt-4 pb-5"
    >
      <SectionLabel>Run Summary</SectionLabel>

      <div
        data-testid="run-summary-banner"
        data-tone={tone}
        className={cn('flex items-start gap-4 rounded-xl border px-5 py-4', BANNER_TONE[tone])}
      >
        <StatusDot tone={tone} className="mt-1.5 h-4 w-4" />
        <div className="min-w-0 flex-1">
          <p className="text-[19px] leading-tight font-bold">{head.title}</p>
          <p className="mt-1.5 text-[14px] text-[var(--muted-foreground)]">{head.detail}</p>
        </div>
        <div className="shrink-0 text-center">
          <p className="text-[26px] leading-none font-bold tabular-nums">{head.clock}</p>
          <p className="mt-1.5 text-xs text-[var(--muted-foreground)]">{head.clockLabel}</p>
        </div>
      </div>

      {/* The Run ID column is the widest so a full Run ID fits on one line; if it still
          has to wrap it breaks anywhere rather than leaving a one- or two-character tail. */}
      <dl className="grid grid-cols-[minmax(0,2fr)_minmax(0,1.6fr)_minmax(0,0.7fr)_minmax(0,0.7fr)] gap-4">
        <Fact label="Run ID" value={run.id} wrap />
        <Fact
          label="Definition"
          value={
            definitionName
              ? `${definitionName} · v${run.definitionVersion}`
              : `v${run.definitionVersion}`
          }
          helper={`${run.workflowId} · v${run.definitionVersion}`}
        />
        <Fact label="Started" value={formatClock(run.startedAt)} />
        <Fact label="Events" value={`${events.length} committed`} />
      </dl>

      <div className="flex items-center justify-between rounded-lg bg-[var(--muted)] px-5 py-2.5 text-[14px]">
        <span className="flex items-center gap-3 text-[var(--muted-foreground)]">
          {live ? (followLive ? 'Following live' : 'Viewing history') : 'Run finished'}
          <StatusDot tone={live && followLive ? 'succeeded' : 'neutral'} className="h-2 w-2" />
        </span>
        <span className="text-[13px] text-[var(--muted-foreground)] tabular-nums">
          latest seq {latestSeq}
        </span>
      </div>

      {children}
    </section>
  );
}

function Fact({
  label,
  value,
  helper,
  wrap,
}: {
  label: string;
  value: string;
  helper?: string;
  wrap?: boolean;
}) {
  return (
    <div className="min-w-0">
      <dt className="text-xs font-bold tracking-[0.08em] text-[var(--muted-foreground)] uppercase">
        {label}
      </dt>
      <dd
        className={cn(
          'mt-1.5 text-[15px] font-semibold',
          wrap ? '[overflow-wrap:anywhere]' : 'truncate',
        )}
        title={value}
      >
        {value}
      </dd>
      {helper ? (
        <dd className="truncate text-xs text-[var(--muted-foreground)]">{helper}</dd>
      ) : null}
    </div>
  );
}

/**
 * Compact RUN SUMMARY for the Agent view's left column (as in the Agent view mock): raw server status,
 * the waiting NodeRuns and how long the earliest has waited, the Agent deadline when one
 * was committed, and the bound version.
 */
export function RunSummaryCompact({
  snapshot,
  nodeNames,
  deadline,
}: {
  snapshot: RunSnapshot;
  nodeNames?: ReadonlyMap<string, string>;
  /** Absolute Agent deadline committed by the Backend; the row is omitted without one. */
  deadline?: string;
}) {
  const { run } = snapshot;
  const waiting = snapshot.nodeRuns.filter((n) => n.status === 'WAITING_CALLBACK');
  const now = useNow(waiting.length > 0);
  const waitingSince = earliestWaitingAt(waiting);

  return (
    <section
      data-testid="run-summary"
      className="rounded-xl border border-[var(--border)] bg-[var(--card)] px-5 pt-4 pb-4"
    >
      <SectionLabel>Run Summary</SectionLabel>
      <dl className="mt-3 space-y-2.5 text-[14px]">
        <Row label="Status" value={run.status} />
        <Row
          label="Waiting NodeRuns"
          value={
            waiting.length > 0
              ? `${waiting.map((n) => nodeRunName(n, nodeNames)).join(', ')} · ${waiting.length}`
              : '—'
          }
        />
        <Row
          label="Waiting since"
          value={waitingSince ? formatRunClock(waitingSince, new Date(now).toISOString()) : '—'}
        />
        {deadline ? <Row label="Deadline" value={formatClock(deadline)} /> : null}
        <Row label="Definition" value={`v${run.definitionVersion}`} />
      </dl>
    </section>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-3">
      <dt className="text-[var(--muted-foreground)]">{label}</dt>
      <dd className="truncate text-right font-semibold">{value}</dd>
    </div>
  );
}
