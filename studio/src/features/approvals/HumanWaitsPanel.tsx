import { useEffect, useState } from 'react';

import { ApiRequestError, listHumanWaits, stopHumanWait } from '@/api/client';
import type { HumanWaitView } from '@/api/types';
import { FactList } from '@/components/facts/FactList';
import { IdChip } from '@/components/id/IdChip';
import { Deadline, Timestamp } from '@/components/time/Time';
import { Callout } from '@/components/ui/banner';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { useCopy } from '@/lib/copy';
import { useOperatorStore } from '@/stores/operator-store';

type LoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; items: HumanWaitView[] }
  | { kind: 'error' }
  | { kind: 'hidden' };

type StopState =
  | { kind: 'idle' }
  | { kind: 'saving'; id: string }
  | { kind: 'done'; repeated: boolean }
  | { kind: 'resolved' }
  | { kind: 'closed' }
  | { kind: 'failed'; message: string };

/**
 * Releases production has not answered by their confirm deadline, each with what a person
 * needs to decide whether to keep waiting, and the control that stops the task. The Backend
 * decides whether a stop takes effect; this panel sends it after a confirmation and shows
 * the answer, including a production answer that got there first.
 */
export function HumanWaitsPanel() {
  const copy = useCopy();
  const h = copy.humanWaits;
  const token = useOperatorStore((s) => s.token);
  const [state, setState] = useState<LoadState>({ kind: 'loading' });
  const [reloads, setReloads] = useState(0);
  const [confirming, setConfirming] = useState<HumanWaitView | null>(null);
  const [stop, setStop] = useState<StopState>({ kind: 'idle' });

  useEffect(() => {
    if (!token) return;
    const controller = new AbortController();
    listHumanWaits(token, controller.signal)
      .then((items) => {
        setState({ kind: 'loaded', items });
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        // A rejected credential or a server without operator routes is reported by the
        // approvals list on the same page; the panel then stays out of the way.
        const operatorProblem =
          error instanceof ApiRequestError && (error.status === 401 || error.status === 404);
        setState({ kind: operatorProblem ? 'hidden' : 'error' });
      });
    return () => {
      controller.abort();
    };
  }, [token, reloads]);

  if (!token || state.kind === 'hidden') return null;

  const reload = () => {
    setReloads((n) => n + 1);
  };

  const confirmStop = () => {
    const wait = confirming;
    if (!wait) return;
    setConfirming(null);
    setStop({ kind: 'saving', id: wait.id });
    stopHumanWait(wait.id, token)
      .then((res) => {
        setStop({ kind: 'done', repeated: res.repeated });
        reload();
      })
      .catch((error: unknown) => {
        if (error instanceof ApiRequestError && error.status === 409) {
          setStop(error.code === 'HUMAN_WAIT_RESOLVED' ? { kind: 'resolved' } : { kind: 'closed' });
          reload();
          return;
        }
        if (error instanceof ApiRequestError && error.status === 401) {
          setStop({ kind: 'failed', message: copy.approvals.unauthorized });
          return;
        }
        setStop({ kind: 'failed', message: h.stopFailed });
      });
  };

  let body;
  if (state.kind === 'loading') {
    body = (
      <p role="status" className="m-0 text-md text-fg-muted">
        {copy.loading}
      </p>
    );
  } else if (state.kind === 'error') {
    body = <Callout tone="danger">{h.loadFailed}</Callout>;
  } else if (!state.items.length) {
    body = <p className="m-0 text-md text-fg-muted">{h.empty}</p>;
  } else {
    body = (
      <ul className="m-0 flex list-none flex-col gap-3 p-0">
        {state.items.map((wait) => (
          <HumanWaitCard
            key={wait.id}
            wait={wait}
            saving={stop.kind === 'saving' && stop.id === wait.id}
            onStop={() => {
              setStop({ kind: 'idle' });
              setConfirming(wait);
            }}
          />
        ))}
      </ul>
    );
  }

  return (
    <section
      aria-labelledby="human-waits-title"
      className="mb-6 flex flex-col gap-3 rounded-lg border border-border bg-surface p-4"
    >
      <h2 id="human-waits-title" className="m-0 text-lg font-semibold">
        {h.title}
      </h2>
      <p className="m-0 text-md text-fg-secondary">{h.intro}</p>
      {stop.kind === 'done' ? (
        <Callout tone="neutral">
          {h.stopped}
          {stop.repeated ? ` ${h.repeated}` : ''}
        </Callout>
      ) : null}
      {stop.kind === 'resolved' ? <Callout tone="caution">{h.resolved}</Callout> : null}
      {stop.kind === 'closed' ? <Callout tone="caution">{h.closed}</Callout> : null}
      {stop.kind === 'failed' ? <Callout tone="danger">{stop.message}</Callout> : null}
      {body}
      <Dialog
        open={confirming !== null}
        onClose={() => {
          setConfirming(null);
        }}
        size="sm"
        title={h.confirmTitle}
        onSubmit={confirmStop}
        footer={
          <>
            <Button
              variant="secondary"
              onClick={() => {
                setConfirming(null);
              }}
            >
              {copy.cancel}
            </Button>
            <Button variant="danger" data-dialog-primary onClick={confirmStop}>
              {h.confirmStop}
            </Button>
          </>
        }
      >
        <p className="m-0 text-md">{confirming ? h.confirmBody(confirming.targetKey) : null}</p>
      </Dialog>
    </section>
  );
}

function HumanWaitCard({
  wait,
  saving,
  onStop,
}: {
  wait: HumanWaitView;
  saving: boolean;
  onStop: () => void;
}) {
  const copy = useCopy();
  const h = copy.humanWaits;
  return (
    <li
      aria-label={`${wait.toolName} ${wait.targetKey}`}
      className="flex flex-col gap-3 rounded-md border border-border-subtle p-3"
    >
      <div className="flex items-center gap-3">
        <span className="font-mono text-md font-medium">{wait.toolName}</span>
        <IdChip id={wait.runId} label={h.run.toLowerCase()} to={`/runs/${wait.runId}`} />
      </div>
      <FactList
        compact
        facts={[
          { label: h.target, value: <span className="font-mono">{wait.targetKey}</span> },
          {
            label: h.operation,
            value: <IdChip id={wait.operationKey} label={h.operation.toLowerCase()} />,
          },
          {
            label: h.confirmDeadline,
            value: wait.confirmDeadline ? <Timestamp iso={wait.confirmDeadline} /> : null,
          },
          {
            label: h.waitDeadline,
            value: (
              <span className="flex flex-col">
                <Timestamp iso={wait.deadline} />
                <span className="text-fg-muted">
                  <Deadline iso={wait.deadline} />
                </span>
              </span>
            ),
          },
          { label: h.reasonLabel, value: h.reason[wait.reason] },
        ]}
      />
      <div className="flex items-center gap-3">
        <Button variant="danger" size="sm" onClick={onStop} disabled={saving}>
          {h.stop}
        </Button>
        {saving ? (
          <span role="status" className="text-md text-fg-muted">
            {h.stopping}
          </span>
        ) : null}
      </div>
    </li>
  );
}
