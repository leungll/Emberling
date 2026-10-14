import { History, Play, RotateCw } from 'lucide-react';

import { Words } from './Words';
import type { Failure } from './run-diagnosis';
import { externalTasks, type RunModel, type WaitInfo } from './run-model';
import type { StreamConnection } from './runObserver';
import { IdChip } from '@/components/id/IdChip';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import { Duration, LiveDuration, Timestamp } from '@/components/time/Time';
import { Banner } from '@/components/ui/banner';
import { Button } from '@/components/ui/button';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import { absoluteTime, duration } from '@/lib/time';
import { useNow } from '@/lib/use-now';
import { useTimeZone } from '@/stores/prefs-store';

/**
 * The run header: the start time as the title, the status and how long, the run's IDs
 * and a note when the workflow has moved on, then the live controls.
 */

interface RunHeaderProps {
  model: RunModel;
  connection: StreamConnection;
  latestVersion: number | null;
  /** The selection follows the run's newest activity. */
  following: boolean;
  onResumeLive: () => void;
}

export function RunHeader({
  model,
  connection,
  latestVersion,
  following,
  onResumeLive,
}: RunHeaderProps) {
  const copy = useCopy();
  const v = copy.runs.view;
  const run = model.snapshot.run;
  const live = !model.terminal;
  const used = run.definitionVersion;
  const newer = latestVersion !== null && latestVersion > used ? latestVersion - used : 0;
  const ended =
    run.startedAt && run.completedAt
      ? new Date(run.completedAt).getTime() - new Date(run.startedAt).getTime()
      : null;
  return (
    <div className="flex min-w-0 shrink-0 items-center gap-2 border-b border-border bg-surface py-2 pr-3 pl-4">
      <div className="flex min-w-0 flex-col gap-0.5">
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-3">
          <h2 className="m-0 text-lg font-semibold whitespace-nowrap text-fg tabular-nums">
            <Timestamp iso={run.startedAt} />
          </h2>
          <span className="inline-flex items-center gap-1.5 text-sm whitespace-nowrap">
            <span data-testid="run-status" className="inline-flex">
              <StatusIndicator status={{ kind: 'run', value: run.status }} colourLabel focusable />
            </span>
            <span className="text-fg-secondary tabular-nums">
              {ended !== null ? (
                <>
                  {copy.runs.inDuration('')}
                  <Duration ms={ended} />
                </>
              ) : run.startedAt ? (
                <>
                  {copy.runs.forDuration('')}
                  <LiveDuration start={run.startedAt} />
                </>
              ) : null}
            </span>
          </span>
        </div>
        <div className="flex min-h-control-xs min-w-0 flex-wrap items-center gap-x-4 gap-y-1">
          <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
            <span className="text-sm text-fg-muted">{v.runId}</span>
            <IdChip id={run.id} label={copy.runs.page.runIdLabel} size="md" />
          </span>
          {externalTasks(model).map((t) => {
            const name = copy.runs.providerCap(t.provider);
            return (
              <span key={t.id} className="inline-flex items-center gap-1.5 whitespace-nowrap">
                <span className="text-sm text-fg-muted">{v.taskOf(name)}</span>
                <IdChip id={t.id} label={v.taskIdLabel(name)} size="md" />
              </span>
            );
          })}
          {newer ? (
            <Tooltip content={v.olderVersionTip(newer)}>
              <span
                tabIndex={0}
                className="inline-flex min-h-control-xs min-w-0 cursor-default items-center gap-1.5 rounded-sm text-sm text-fg-muted"
              >
                <History aria-hidden="true" className="size-icon-sm shrink-0" />
                <span>{v.olderVersion(used)}</span>
              </span>
            </Tooltip>
          ) : null}
        </div>
      </div>
      <span className="flex-1" />
      {live && !following && connection === 'live' ? (
        <Button variant="ghost" size="sm" tooltip={v.followLatestTip} onClick={onResumeLive}>
          <Play aria-hidden="true" className="size-icon-sm" />
          {v.followLatest}
        </Button>
      ) : null}
      {live ? (
        <StatusIndicator status={{ kind: 'connection', value: connection }} focusable />
      ) : null}
    </div>
  );
}

function WaitedSoFar({ since }: { since: string }) {
  const copy = useCopy();
  const now = useNow(1000);
  return <>{copy.runs.view.waitedSoFar(duration(now.getTime() - new Date(since).getTime()))}</>;
}

interface RunBannerProps {
  model: RunModel;
  connection: StreamConnection;
  failure: Failure | null;
  wait: WaitInfo | null;
  /** The element the details pane shows, and whether it is visible beside the trace. */
  selectedId: string;
  detailsVisible: boolean;
  onShow: (id: string) => void;
  onReload: () => void;
}

/** One line above the graph: a dropped connection, the failure or the wait. */
export function RunBanner({
  model,
  connection,
  failure,
  wait,
  selectedId,
  detailsVisible,
  onShow,
  onReload,
}: RunBannerProps) {
  const copy = useCopy();
  const v = copy.runs.view;
  const zone = useTimeZone();
  const live = !model.terminal;
  const run = model.snapshot.run;
  if (live && connection === 'reconnecting') {
    const last = model.events[model.events.length - 1];
    return (
      <Banner tone="caution">
        {v.reconnecting(absoluteTime(last?.timestamp ?? run.startedAt, zone))}
      </Banner>
    );
  }
  if (live && connection === 'stopped') {
    return (
      <Banner
        tone="caution"
        action={
          <Button variant="secondary" size="sm" onClick={onReload}>
            <RotateCw aria-hidden="true" className="size-icon-sm" />
            {v.reload}
          </Button>
        }
      >
        {v.stopped}
      </Banner>
    );
  }
  if (run.status === 'FAILED' && failure) {
    const shown = detailsVisible && selectedId === failure.focus;
    return (
      <Banner
        tone="failed"
        action={
          shown ? null : (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => {
                onShow(failure.focus);
              }}
            >
              {v.showFailure}
            </Button>
          )
        }
      >
        <span className="font-semibold text-fg">{failure.title}.</span>{' '}
        <span className="text-fg-secondary">
          <Words
            text={failure.effect || failure.cause}
            tools={failure.tool ? [failure.tool] : []}
          />
        </span>
      </Banner>
    );
  }
  if (run.status === 'PAUSED' && wait) {
    const shown = detailsVisible && selectedId === wait.focus;
    return (
      <Banner
        tone="waiting"
        action={
          shown ? null : (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => {
                onShow(wait.focus);
              }}
            >
              {v.showWait}
            </Button>
          )
        }
      >
        <span className="font-semibold text-fg">
          {v.waitingBanner(wait.step.node.name, copy.runs.provider(wait.provider))}
        </span>{' '}
        {wait.since ? (
          <span className="text-fg-secondary">
            <WaitedSoFar since={wait.since} />
          </span>
        ) : null}
      </Banner>
    );
  }
  return null;
}
